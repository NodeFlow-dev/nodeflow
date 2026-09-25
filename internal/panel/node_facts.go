package panel

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// Splice pipe sizes. 256 KiB fits the default kernel pipe budget; 1 MiB is
// rendered only when the node's Agent reports the kernel limits as tuned.
const (
	defaultPipeSize = 262144
	tunedPipeSize   = 1048576
)

// Adaptive («Авто») stick-table sizing for sticky_mode=source_table with
// sticky_table_entries ”. 5 % of node RAM is shared by every auto table on
// the node; one entry costs ~228 B (measured, HAProxy 3.4.2).
const (
	autoStickyTableRAMDivisor  = 20 // 5 %
	stickyTableEntryBytes      = 228
	autoStickyTableMinEntries  = 100 * 1024
	autoStickyTableMaxEntries  = 10 * 1024 * 1024
	autoStickyTableUnknownSize = "1m"
	// Memory is bucketed so small MemTotal changes never re-publish.
	nodeMemoryBucketBytes = 256 << 20
)

// NodeRenderFacts are the per-node facts from the latest Agent heartbeat that
// change the rendered configuration. The zero value (nothing known) renders
// 256 KiB pipes and 1m auto stick-tables.
type NodeRenderFacts struct {
	// KernelPipesTuned: the Agent reported pipe-max-size >= 1 MiB and
	// pipe-user-pages-soft == 0 (1 MiB splice pipes are safe).
	KernelPipesTuned bool `json:"kernel_pipes_tuned"`
	// MemoryBytes is memory_total_bytes rounded down to 256 MiB (0 = unknown).
	MemoryBytes int64 `json:"memory_bytes"`
	// HAProxyLogsDisabled comes from the node setting (nodes.metadata
	// haproxy_logs=false), not from the heartbeat. The zero value keeps the
	// historical syslog connection logging.
	HAProxyLogsDisabled bool `json:"haproxy_logs_disabled,omitempty"`
}

// nodeMetadataHAProxyLogsKey is the nodes.metadata key of the per-node
// «Логи соединений HAProxy» setting. Absent or true = logging on (default).
const nodeMetadataHAProxyLogsKey = "haproxy_logs"

// haproxyLogsEnabled reports the effective setting stored in node metadata.
// Anything but an explicit JSON false means on.
func haproxyLogsEnabled(metadata map[string]any) bool {
	value, ok := metadata[nodeMetadataHAProxyLogsKey].(bool)
	return !ok || value
}

// nodeMetadataHAProxyLogs is haproxyLogsEnabled for the decoded Node.Metadata.
func nodeMetadataHAProxyLogs(metadata any) bool {
	m, _ := metadata.(map[string]any)
	return haproxyLogsEnabled(m)
}

// PipeSize is the tune.pipesize for this node.
func (f NodeRenderFacts) PipeSize() int {
	if f.KernelPipesTuned {
		return tunedPipeSize
	}
	return defaultPipeSize
}

// AutoStickyTableEntries is the stick-table size of each «Авто» table when
// autoTables tables share the node: clamp(5 % RAM / 228 B / N, 100k, 10m),
// rounded down to whole k. Unknown memory renders 1m.
func (f NodeRenderFacts) AutoStickyTableEntries(autoTables int) string {
	if f.MemoryBytes <= 0 {
		return autoStickyTableUnknownSize
	}
	if autoTables < 1 {
		autoTables = 1
	}
	entries := f.MemoryBytes / autoStickyTableRAMDivisor / stickyTableEntryBytes / int64(autoTables)
	if entries < autoStickyTableMinEntries {
		entries = autoStickyTableMinEntries
	}
	if entries >= autoStickyTableMaxEntries {
		return "10m"
	}
	return strconv.FormatInt(entries/1024, 10) + "k"
}

func bucketNodeMemory(total float64) int64 {
	if math.IsNaN(total) || math.IsInf(total, 0) || total <= 0 || total > math.MaxInt64/2 {
		return 0
	}
	return int64(total) / nodeMemoryBucketBytes * nodeMemoryBucketBytes
}

// jsonNormalizedMetrics round-trips metrics through JSON so typed values
// (uint64, int, nested structs) become the float64/map[string]any shapes that
// nodeRenderFactsFromMetrics expects. On failure it returns nil (facts unknown).
func jsonNormalizedMetrics(metrics map[string]any) map[string]any {
	raw, err := json.Marshal(metrics)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// nodeRenderFactsFromMetrics derives the facts from heartbeat metrics as
// stored in node_heartbeats.metrics. Malformed values are treated as unknown.
func nodeRenderFactsFromMetrics(metrics map[string]any) NodeRenderFacts {
	var facts NodeRenderFacts
	if total, ok := metrics["memory_total_bytes"].(float64); ok {
		facts.MemoryBytes = bucketNodeMemory(total)
	}
	if pipes, ok := metrics["kernel_pipes"].(map[string]any); ok {
		tuned, _ := pipes["tuned"].(bool)
		maxSize, maxOK := pipes["pipe_max_size"].(float64)
		soft, softOK := pipes["pipe_user_pages_soft"].(float64)
		facts.KernelPipesTuned = tuned && maxOK && softOK && maxSize >= tunedPipeSize && soft == 0
	}
	return facts
}

// nodeRenderFactsTx reads the facts from the node's latest heartbeat.
func nodeRenderFactsTx(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, nodeID string) (NodeRenderFacts, error) {
	logsDisabled, err := nodeHAProxyLogsDisabledTx(ctx, q, nodeID)
	if err != nil {
		return NodeRenderFacts{}, err
	}
	var raw []byte
	err = q.QueryRow(ctx, `SELECT metrics FROM node_heartbeats WHERE node_id=$1`, nodeID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return NodeRenderFacts{HAProxyLogsDisabled: logsDisabled}, nil
	}
	if err != nil {
		return NodeRenderFacts{}, err
	}
	var metrics map[string]any
	if json.Unmarshal(raw, &metrics) != nil {
		return NodeRenderFacts{HAProxyLogsDisabled: logsDisabled}, nil
	}
	facts := nodeRenderFactsFromMetrics(metrics)
	facts.HAProxyLogsDisabled = logsDisabled
	return facts, nil
}

// nodeHAProxyLogsDisabledTx reads the per-node logging setting. A missing
// node renders with the default (logging on).
func nodeHAProxyLogsDisabledTx(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, nodeID string) (bool, error) {
	var disabled bool
	err := q.QueryRow(ctx, `SELECT COALESCE(metadata->'haproxy_logs' = 'false'::jsonb, false) FROM nodes WHERE id=$1`, nodeID).Scan(&disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return disabled, err
}

// GetNodeRenderFacts returns the render facts of the node's latest heartbeat.
func (s *PGStore) GetNodeRenderFacts(ctx context.Context, nodeID string) (NodeRenderFacts, error) {
	return nodeRenderFactsTx(ctx, s.pool, nodeID)
}

// routeUsesAutoStickyTable reports whether an enabled route renders an
// adaptive («Авто») source_table stick-table.
func routeUsesAutoStickyTable(route Route) bool {
	if route.StickyTableEntries != "" {
		return false
	}
	r := renderRoute{StickyMode: route.StickyMode, StickyEnabled: route.StickyEnabled, BalanceMode: route.BalanceMode, DNSPool: route.DNSPool}
	return effectiveStickyMode(r, false) == StickyModeSourceTable
}

// annotateStickyTableEffective fills sticky_table_entries_effective for every
// source_table route: the explicit size, or the adaptive size the renderer
// uses for «Авто». A disabled auto route is counted as if it were enabled
// (the value it would get once enabled).
func annotateStickyTableEffective(routes []Route, facts NodeRenderFacts) {
	enabledAuto := 0
	for _, route := range routes {
		if route.Enabled && routeUsesAutoStickyTable(route) {
			enabledAuto++
		}
	}
	for i := range routes {
		route := &routes[i]
		r := renderRoute{StickyMode: route.StickyMode, StickyEnabled: route.StickyEnabled, BalanceMode: route.BalanceMode, DNSPool: route.DNSPool}
		if effectiveStickyMode(r, false) != StickyModeSourceTable {
			route.StickyTableEntriesEffective = ""
			continue
		}
		if route.StickyTableEntries != "" {
			route.StickyTableEntriesEffective = route.StickyTableEntries
			continue
		}
		n := enabledAuto
		if !route.Enabled {
			n++
		}
		route.StickyTableEntriesEffective = facts.AutoStickyTableEntries(n)
	}
}

func nullableUUID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// republishOnNodeFactsChangeTx publishes a new route revision when the node
// facts behind the desired route-lifecycle revision changed (the Agent
// started reporting tuned kernel pipes, or the node RAM bucket moved while
// «Авто» stick-tables exist). Admin-authored revisions are left alone. The
// comparison uses revision metadata only, so an unchanged node costs two
// small queries per heartbeat. A render failure (for example an Agent
// downgrade below a route's requirement) never fails the heartbeat: the
// savepoint is rolled back and the next route change reports it.
func republishOnNodeFactsChangeTx(ctx context.Context, tx pgx.Tx, nodeID string, facts NodeRenderFacts) error {
	var createdBy string
	var pipeSize *int64
	var autoSize, renderer string
	err := tx.QueryRow(ctx, `
		SELECT r.created_by,
		       CASE WHEN jsonb_typeof(r.metadata->'pipe_size')='number' THEN (r.metadata->>'pipe_size')::bigint END,
		       COALESCE(r.metadata->>'auto_sticky_table_entries',''),
		       COALESCE(r.metadata->>'renderer','')
		FROM node_config_state s
		JOIN config_revisions r ON r.node_id=s.node_id AND r.revision=s.desired_revision
		WHERE s.node_id=$1`, nodeID).Scan(&createdBy, &pipeSize, &autoSize, &renderer)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if createdBy != "route_lifecycle" {
		return nil
	}
	var autoTables int
	if err = tx.QueryRow(ctx, `
		SELECT count(*) FROM routes
		WHERE node_id=$1 AND enabled AND sticky_mode=$2 AND sticky_table_entries=''
		  AND COALESCE(balance_mode,'')<>'failover'`, nodeID, StickyModeSourceTable).Scan(&autoTables); err != nil {
		return err
	}
	publishedPipe := int64(defaultPipeSize)
	if pipeSize != nil {
		publishedPipe = *pipeSize
	}
	wantAuto := ""
	if autoTables > 0 {
		wantAuto = facts.AutoStickyTableEntries(autoTables)
		if autoSize == "" && renderer != HAProxyRendererVersion {
			autoSize = "100k" // v20 and older rendered «Авто» as 100k
		}
	} else {
		autoSize = ""
	}
	if publishedPipe == int64(facts.PipeSize()) && autoSize == wantAuto {
		return nil
	}
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	if _, err = createAndAssignRouteRevisionTx(ctx, savepoint, nodeID, "", "node facts changed (kernel pipes / RAM)"); err != nil {
		return savepoint.Rollback(ctx)
	}
	return savepoint.Commit(ctx)
}
