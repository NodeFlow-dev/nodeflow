package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound                     = errors.New("not found")
	ErrInvalidObservedConfig        = errors.New("invalid observed configuration state")
	ErrInvalidHeartbeatOrder        = errors.New("invalid heartbeat traffic ordering fields")
	ErrInvalidUpdateMetrics         = errors.New("invalid Agent update metrics")
	ErrAgentUpdateStateChanged      = errors.New("Agent update state changed; reload and retry")
	ErrReleaseNotNewer              = errors.New("Agent release sequence is not newer than installed or already assigned sequence")
	ErrReleasePlatform              = errors.New("Agent release platform does not match node platform")
	ErrReleasePlatformUnknown       = errors.New("node platform is not known yet; wait for an Agent heartbeat")
	ErrRouteVersionConflict         = errors.New("route version conflict")
	ErrCredentialRenewalNotDue      = errors.New("credential renewal is not due")
	ErrCredentialRenewalInProgress  = errors.New("credential renewal is already in progress")
	ErrCredentialRenewalRateLimited = errors.New("credential renewal rate limit exceeded")
	ErrCredentialRenewalIdempotency = errors.New("credential renewal idempotency conflict")
	ErrCredentialRenewalExpired     = errors.New("credential renewal confirmation expired")
	ErrOrderConflict                = errors.New("ordered IDs do not match current resources")
	ErrControlGenerationConflict    = errors.New("HAProxy control state changed; reload and retry")
	ErrControlUnsupported           = errors.New("installed Node Agent does not support HAProxy service control")
	ErrHAProxyDisabled              = errors.New("HAProxy is disabled on this node")
)

type Store interface {
	Health(context.Context) error
	GetPanelSettings(context.Context) (PanelSettings, error)
	UpdatePanelSettings(context.Context, PanelSettings) (PanelSettings, error)
	ListNodes(context.Context) ([]Node, error)
	ReorderNodes(context.Context, []string) ([]Node, error)
	GetDashboardOverview(context.Context, string, string) (DashboardOverview, error)
	CreateNode(context.Context, string, string, map[string]any) (Node, error)
	GetNode(context.Context, string) (Node, error)
	GetNodeOperationalDetail(context.Context, string) (NodeOperationalDetail, error)
	GetTraffic(context.Context, string, time.Time) (NodeTrafficReport, error)
	GetTrafficHistory(context.Context, string, string) (TrafficHistory, error)
	GetRouteTrafficHistory(context.Context, string, string, string) (TrafficHistory, error)
	GetFirewallPolicy(context.Context, string) (NodeFirewallPolicy, error)
	UpdateFirewallPolicy(context.Context, string, string) (NodeFirewallPolicy, error)
	ReserveAgentReleaseSequence(context.Context) (int64, error)
	CreateAgentRelease(context.Context, AgentRelease) (AgentRelease, error)
	ListAgentReleases(context.Context) ([]AgentRelease, error)
	DeleteAgentRelease(context.Context, string) (AgentRelease, error)
	PrepareBootstrapAgentRelease(context.Context, string, string) error
	AssignAgentRelease(context.Context, string, string, int64, int64) (NodeAgentUpdateState, error)
	GetAgentUpdateState(context.Context, string) (NodeAgentUpdateState, error)
	GetAssignedAgentRelease(context.Context, string, AgentCredentialIdentity, int64) (AgentRelease, error)
	UpdateNode(context.Context, string, string, string, map[string]any) (Node, error)
	DeleteNode(context.Context, string) error
	GetHAProxyControl(context.Context, string) (NodeHAProxyControl, error)
	UpdateHAProxyControl(context.Context, string, bool, int64) (NodeHAProxyControl, error)
	RestartHAProxy(context.Context, string, int64) (NodeHAProxyControl, error)
	ListRoutes(context.Context, string) ([]Route, error)
	ReorderRoutes(context.Context, string, []string) ([]Route, error)
	CreateRoute(context.Context, string, RouteSpec) (Route, error)
	GetRoute(context.Context, string, string) (Route, error)
	UpdateRoute(context.Context, string, string, RouteSpec) (Route, error)
	SetRouteEnabled(context.Context, string, string, bool, *int64) (Route, error)
	GetNodeAgentVersion(context.Context, string) (string, error)
	GetNodeRenderFacts(context.Context, string) (NodeRenderFacts, error)
	DeleteRoute(context.Context, string, string, *int64) (RouteDeleteResult, error)
	CreateEnrollmentToken(context.Context, string, string, string, time.Time) (EnrollmentToken, error)
	EnrollmentTokenUsed(context.Context, string) (bool, error)
	RevokeEnrollmentToken(context.Context, string) error
	RevokeOtherEnrollmentTokens(context.Context, string, string) error
	AuthorizeCredentialRenewal(context.Context, string, AgentCredentialIdentity, CredentialRenewalRequest) (*CredentialRenewalRecord, error)
	CreateCredentialRenewal(context.Context, string, AgentCredentialIdentity, CredentialRenewalCandidate) (CredentialRenewalRecord, bool, error)
	ConfirmCredentialRenewal(context.Context, string, AgentCredentialIdentity, string) (CredentialRenewalRecord, error)
	AppendAudit(context.Context, AuditEvent) error
	CleanupAudit(context.Context) error
	ListAudit(context.Context, string, int) ([]AuditEntry, error)
	IngestHeartbeat(context.Context, string, Heartbeat) (HeartbeatResult, error)
	CreateConfigRevision(context.Context, string, string, string, map[string]any) (ConfigRevision, error)
	ListConfigRevisions(context.Context, string) ([]ConfigRevision, error)
	GetConfigRevision(context.Context, string, int64) (ConfigRevision, error)
	AssignDesiredRevision(context.Context, string, int64) (NodeConfigState, error)
	ResumeGeneratedConfig(context.Context, string) (ConfigRevision, error)
	GetConfigState(context.Context, string) (NodeConfigState, error)
	IngestApplyReport(context.Context, string, ApplyReport) (NodeConfigState, error)
}

type PGStore struct{ pool *pgxpool.Pool }

func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func (s *PGStore) Health(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *PGStore) GetPanelSettings(ctx context.Context) (PanelSettings, error) {
	var settings PanelSettings
	err := s.pool.QueryRow(ctx, `
		SELECT theme,accent,session_timeout_minutes,max_sessions,audit_retention_days,updated_at
		FROM panel_settings WHERE singleton=true`).Scan(
		&settings.Theme,
		&settings.Accent,
		&settings.InactivityTimeoutMinutes,
		&settings.MaxSessions,
		&settings.AuditRetentionDays,
		&settings.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PanelSettings{}, ErrNotFound
	}
	return settings, err
}

func (s *PGStore) UpdatePanelSettings(ctx context.Context, settings PanelSettings) (PanelSettings, error) {
	var updated PanelSettings
	err := s.pool.QueryRow(ctx, `
		UPDATE panel_settings
		SET theme=$1,
		    accent=$2,
		    session_timeout_minutes=$3,
		    max_sessions=$4,
		    next_audit_cleanup_at=CASE
		      WHEN audit_retention_days<>$5 THEN LEAST(next_audit_cleanup_at,clock_timestamp())
		      ELSE next_audit_cleanup_at
		    END,
		    audit_retention_days=$5,
		    updated_at=clock_timestamp()
		WHERE singleton=true
		RETURNING theme,accent,session_timeout_minutes,max_sessions,audit_retention_days,updated_at`,
		settings.Theme,
		settings.Accent,
		settings.InactivityTimeoutMinutes,
		settings.MaxSessions,
		settings.AuditRetentionDays,
	).Scan(
		&updated.Theme,
		&updated.Accent,
		&updated.InactivityTimeoutMinutes,
		&updated.MaxSessions,
		&updated.AuditRetentionDays,
		&updated.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PanelSettings{}, ErrNotFound
	}
	return updated, err
}

const nodeColumns = `id::text,name,host(address),status,metadata,last_seen_at,created_at,updated_at,sort_order`

func scanNode(row pgx.Row) (Node, error) {
	var n Node
	var metadata []byte
	err := row.Scan(&n.ID, &n.Name, &n.Address, &n.Status, &metadata, &n.LastSeen, &n.CreatedAt, &n.UpdatedAt, &n.SortOrder)
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(metadata, &n.Metadata)
		if n.Status == "online" && n.LastSeen != nil && time.Since(*n.LastSeen) > 45*time.Second {
			n.Status = "offline"
		}
	}
	return n, err
}

func (s *PGStore) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+nodeColumns+` FROM nodes ORDER BY sort_order,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Node, 0)
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *PGStore) CreateNode(ctx context.Context, name, address string, metadata map[string]any) (Node, error) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	b, _ := json.Marshal(metadata)
	firewallMode := initialFirewallMode(metadata)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Node{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `LOCK TABLE nodes IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return Node{}, err
	}
	node, err := scanNode(tx.QueryRow(ctx, `INSERT INTO nodes(name,address,metadata,sort_order) VALUES($1,$2,$3,(SELECT COALESCE(max(sort_order),0)+1 FROM nodes)) RETURNING `+nodeColumns, name, address, b))
	if err != nil {
		return Node{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO node_firewall_policies(node_id,mode) VALUES($1,$2)`, node.ID, firewallMode); err != nil {
		return Node{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO node_haproxy_control(node_id) VALUES($1)`, node.ID); err != nil {
		return Node{}, err
	}
	return node, tx.Commit(ctx)
}

func initialFirewallMode(metadata map[string]any) string {
	if allowed, ok := metadata["firewall_apply_allowed"].(bool); ok && allowed {
		return "apply"
	}
	return "observe"
}

func (s *PGStore) GetNode(ctx context.Context, id string) (Node, error) {
	return scanNode(s.pool.QueryRow(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE id=$1`, id))
}

func (s *PGStore) GetFirewallPolicy(ctx context.Context, nodeID string) (NodeFirewallPolicy, error) {
	var policy NodeFirewallPolicy
	err := s.pool.QueryRow(ctx, `
		SELECT node_id::text,mode,updated_at
		FROM node_firewall_policies WHERE node_id=$1`, nodeID).
		Scan(&policy.NodeID, &policy.Mode, &policy.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return NodeFirewallPolicy{}, ErrNotFound
	}
	if err != nil {
		return policy, err
	}
	policy.TCPPorts = []int{}
	var raw []byte
	var complete bool
	err = s.pool.QueryRow(ctx, `
		SELECT revision.metadata->'listener_tcp_ports',revision.metadata ? 'listener_tcp_ports'
		FROM node_config_state AS state
		JOIN config_revisions AS revision
		  ON revision.node_id=state.node_id AND revision.revision=state.actual_revision
		WHERE state.node_id=$1 AND state.actual_revision IS NOT NULL
		  AND revision.metadata->>'renderer' = ANY($2::text[])`, nodeID, supportedHAProxyRenderers()).Scan(&raw, &complete)
	if errors.Is(err, pgx.ErrNoRows) {
		return policy, nil
	}
	if err != nil {
		return NodeFirewallPolicy{}, err
	}
	if !complete {
		return policy, nil
	}
	policy.TCPPorts, err = parseFirewallPorts(raw)
	policy.PlanComplete = err == nil
	return policy, err
}

func (s *PGStore) UpdateFirewallPolicy(ctx context.Context, nodeID, mode string) (NodeFirewallPolicy, error) {
	var policy NodeFirewallPolicy
	err := s.pool.QueryRow(ctx, `
		INSERT INTO node_firewall_policies(node_id,mode) VALUES($1,$2)
		ON CONFLICT(node_id) DO UPDATE SET mode=EXCLUDED.mode,updated_at=now()
		RETURNING node_id::text,mode,updated_at`, nodeID, mode).
		Scan(&policy.NodeID, &policy.Mode, &policy.UpdatedAt)
	policy.TCPPorts = []int{}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return NodeFirewallPolicy{}, ErrNotFound
		}
	}
	return policy, err
}

// nodeOperationalDetailSQL gathers every per-node operational read model in a
// single statement (node, route counts, month/day traffic, 30-day average,
// active credential, HAProxy control, latest heartbeat and its rate sample).
// The metric summary is loaded concurrently on a second pool connection.
const nodeOperationalDetailSQL = `
	WITH clock AS (
		SELECT date_trunc('month',clock_timestamp() AT TIME ZONE 'UTC')::date AS month,
		       (clock_timestamp() AT TIME ZONE 'UTC')::date AS day
	)
	SELECT n.id::text,n.name,host(n.address),n.status,n.metadata,n.last_seen_at,n.created_at,n.updated_at,n.sort_order,
	       route_count.total,route_count.enabled,
	       clock.month,
	       COALESCE(month_traffic.bytes_in,0),COALESCE(month_traffic.bytes_out,0),
	       COALESCE(month_traffic.bytes_in,0)+COALESCE(month_traffic.bytes_out,0),
	       month_traffic.node_id IS NOT NULL,
	       clock.day,
	       COALESCE(day_traffic.bytes_in,0),COALESCE(day_traffic.bytes_out,0),
	       COALESCE(day_traffic.bytes_in,0)+COALESCE(day_traffic.bytes_out,0),
	       day_traffic.node_id IS NOT NULL,
	       daily_average.days,daily_average.avg_in,daily_average.avg_out,daily_average.avg_used,
	       credential.token_prefix,credential.expires_at,credential.last_used_at,
	       hc.node_id IS NOT NULL,
	       COALESCE(hc.supported,false),COALESCE(hc.desired_enabled,false),COALESCE(hc.generation,0),hc.actual_enabled,
	       COALESCE(hc.active_state,''),COALESCE(hc.report_generation,0),COALESCE(hc.restart_generation,0),
	       COALESCE(hc.restart_report_generation,0),COALESCE(hc.last_error,''),hc.reported_at,
	       COALESCE(hc.updated_at,'epoch'::timestamptz),
	       hb.node_id IS NOT NULL,
	       COALESCE(hb.agent_version,''),COALESCE(hb.status,''),hb.metrics,hb.routes_ok,
	       COALESCE(hb.traffic_instance_id::text,''),hb.traffic_instance_started_at,hb.traffic_sample_seq,
	       COALESCE(hb.received_at,'epoch'::timestamptz),
	       rate.rx_bytes_per_second*8,rate.tx_bytes_per_second*8,rate.sampled_at
	FROM nodes AS n
	CROSS JOIN clock
	CROSS JOIN LATERAL (
		SELECT count(*) AS total,count(*) FILTER (WHERE enabled) AS enabled
		FROM routes WHERE node_id=n.id
	) AS route_count
	LEFT JOIN traffic_monthly AS month_traffic
	  ON month_traffic.node_id=n.id AND month_traffic.month=clock.month
	 AND month_traffic.scope='node' AND month_traffic.proxy_name=''
	LEFT JOIN traffic_daily AS day_traffic
	  ON day_traffic.node_id=n.id AND day_traffic.day=clock.day
	CROSS JOIN LATERAL (
		SELECT count(*) AS days,
		       avg(recent.bytes_in)::double precision AS avg_in,
		       avg(recent.bytes_out)::double precision AS avg_out,
		       avg(recent.bytes_in::numeric+recent.bytes_out::numeric)::double precision AS avg_used
		FROM (
			SELECT bytes_in,bytes_out FROM traffic_daily
			WHERE node_id=n.id ORDER BY day DESC LIMIT 30
		) AS recent
	) AS daily_average
	LEFT JOIN LATERAL (
		SELECT token_prefix,expires_at,last_used_at
		FROM enrollment_tokens
		WHERE node_id=n.id AND activated_at IS NOT NULL AND revoked_at IS NULL
		  AND expires_at>clock_timestamp()
		ORDER BY activated_at DESC,created_at DESC LIMIT 1
	) AS credential ON true
	LEFT JOIN node_haproxy_control AS hc ON hc.node_id=n.id
	LEFT JOIN node_heartbeats AS hb ON hb.node_id=n.id
	LEFT JOIN node_traffic_rate_samples AS rate
	  ON rate.node_id=n.id AND rate.sampled_at=hb.received_at
	WHERE n.id=$1`

func (s *PGStore) GetNodeOperationalDetail(ctx context.Context, id string) (NodeOperationalDetail, error) {
	type summaryResult struct {
		summary *NodeMetricSummary
		err     error
	}
	summaryCtx, cancelSummary := context.WithCancel(ctx)
	defer cancelSummary()
	summaryDone := make(chan summaryResult, 1)
	go func() {
		summary, err := s.getNodeMetricSummary(summaryCtx, id, "24h")
		summaryDone <- summaryResult{summary, err}
	}()

	var detail NodeOperationalDetail
	var n Node
	var nodeMetadata []byte
	var trafficMonth, trafficDay time.Time
	var credentialPrefix *string
	var controlFound, heartbeatFound bool
	var control NodeHAProxyControl
	var heartbeat NodeHeartbeat
	var heartbeatMetrics []byte
	var rxBitsPerSecond, txBitsPerSecond *float64
	var rateSampledAt *time.Time
	err := s.pool.QueryRow(ctx, nodeOperationalDetailSQL, id).Scan(
		&n.ID, &n.Name, &n.Address, &n.Status, &nodeMetadata, &n.LastSeen, &n.CreatedAt, &n.UpdatedAt, &n.SortOrder,
		&detail.RoutesTotal, &detail.RoutesEnabled,
		&trafficMonth, &detail.TrafficBytesIn, &detail.TrafficBytesOut, &detail.TrafficUsed, &detail.TrafficObserved,
		&trafficDay, &detail.TrafficDayBytesIn, &detail.TrafficDayBytesOut, &detail.TrafficDayUsed, &detail.TrafficDayObserved,
		&detail.TrafficDailyObservedDays, &detail.TrafficDailyAverageBytesIn, &detail.TrafficDailyAverageBytesOut, &detail.TrafficDailyAverageUsed,
		&credentialPrefix, &detail.CredentialExpiresAt, &detail.CredentialLastUsed,
		&controlFound,
		&control.Supported, &control.DesiredEnabled, &control.Generation, &control.ActualEnabled,
		&control.ActiveState, &control.ReportGeneration, &control.RestartGeneration,
		&control.RestartReportGeneration, &control.LastError, &control.ReportedAt, &control.UpdatedAt,
		&heartbeatFound,
		&heartbeat.AgentVersion, &heartbeat.Status, &heartbeatMetrics, &heartbeat.RoutesOK,
		&heartbeat.TrafficInstanceID, &heartbeat.TrafficInstanceStartedAt, &heartbeat.TrafficSampleSeq,
		&heartbeat.ReceivedAt,
		&rxBitsPerSecond, &txBitsPerSecond, &rateSampledAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return NodeOperationalDetail{}, ErrNotFound
	}
	if err != nil {
		return NodeOperationalDetail{}, err
	}
	if err = json.Unmarshal(nodeMetadata, &n.Metadata); err != nil {
		return NodeOperationalDetail{}, err
	}
	if n.Status == "online" && n.LastSeen != nil && time.Since(*n.LastSeen) > 45*time.Second {
		n.Status = "offline"
	}
	detail.Node = n
	detail.TrafficMonth = trafficMonth.UTC().Format("2006-01")
	detail.TrafficDay = trafficDay.UTC().Format("2006-01-02")
	if credentialPrefix != nil {
		detail.CredentialPrefix = *credentialPrefix
	} else {
		detail.CredentialExpiresAt = nil
		detail.CredentialLastUsed = nil
	}
	if !controlFound {
		// Preserve the previous contract: a node without a control row is
		// reported as not found, exactly like GetHAProxyControl.
		return NodeOperationalDetail{}, ErrNotFound
	}
	control.NodeID = n.ID
	detail.HAProxyControl = &control

	summary := <-summaryDone
	if summary.err != nil {
		return NodeOperationalDetail{}, summary.err
	}
	detail.MetricsSummary = summary.summary

	if !heartbeatFound {
		return detail, nil
	}
	if err = json.Unmarshal(heartbeatMetrics, &heartbeat.Metrics); err != nil {
		return NodeOperationalDetail{}, err
	}
	detail.LatestHeartbeat = &heartbeat
	if rateSampledAt != nil && rxBitsPerSecond != nil && txBitsPerSecond != nil {
		detail.RXBitsPerSecond = rxBitsPerSecond
		detail.TXBitsPerSecond = txBitsPerSecond
		detail.RateSampledAt = rateSampledAt
	}
	return detail, nil
}

// UpdateNode replaces name, address and metadata. Settings owned by the
// Panel (haproxy_logs and haproxy_settings) survive a metadata object that
// omits them, so older clients cannot silently reset them. When effective
// HAProxy settings change, a new route revision is published in the same
// transaction; the Agent applies it with a graceful reload.
func (s *PGStore) UpdateNode(ctx context.Context, id, name, address string, metadata map[string]any) (Node, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Node{}, err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT metadata FROM nodes WHERE id=$1 FOR UPDATE`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	if err != nil {
		return Node{}, err
	}
	var previous map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &previous)
	}
	merged := mergeNodeMetadata(previous, metadata)
	previousSettings, err := settingsFromMetadata(previous)
	if err != nil {
		return Node{}, err
	}
	nextSettings, err := settingsFromMetadata(merged)
	if err != nil {
		return Node{}, err
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return Node{}, err
	}
	node, err := scanNode(tx.QueryRow(ctx, `UPDATE nodes SET name=$2,address=$3,metadata=$4,updated_at=now() WHERE id=$1 RETURNING `+nodeColumns, id, name, address, b))
	if err != nil {
		return Node{}, err
	}
	if haproxyLogsEnabled(previous) != haproxyLogsEnabled(merged) || previousSettings != nextSettings {
		if err = republishOnNodeSettingChangeTx(ctx, tx, id); err != nil {
			return Node{}, err
		}
	}
	return node, tx.Commit(ctx)
}

// panelOwnedNodeMetadataKeys are node metadata keys a client may omit on
// update without resetting them.
var panelOwnedNodeMetadataKeys = []string{nodeMetadataHAProxyLogsKey, nodeHAProxySettingsKey}

// mergeNodeMetadata returns next with panel-owned keys carried over from
// previous when next omits them. next is not modified.
func mergeNodeMetadata(previous, next map[string]any) map[string]any {
	out := make(map[string]any, len(next)+len(panelOwnedNodeMetadataKeys))
	for key, value := range next {
		out[key] = value
	}
	for _, key := range panelOwnedNodeMetadataKeys {
		if _, ok := out[key]; ok {
			continue
		}
		if value, ok := previous[key]; ok {
			out[key] = value
		}
	}
	return out
}

// republishOnNodeSettingChangeTx publishes a new route revision after a
// node setting that feeds the renderer changed. Nodes without a published
// route revision (none yet, or an admin-authored manual config) are left
// alone; their next route render picks the setting up.
func republishOnNodeSettingChangeTx(ctx context.Context, tx pgx.Tx, nodeID string) error {
	var createdBy string
	err := tx.QueryRow(ctx, `
		SELECT r.created_by
		FROM node_config_state s
		JOIN config_revisions r ON r.node_id=s.node_id AND r.revision=s.desired_revision
		WHERE s.node_id=$1`, nodeID).Scan(&createdBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if createdBy != "route_lifecycle" {
		return nil
	}
	_, err = createAndAssignRouteRevisionTx(ctx, tx, nodeID, "", "node setting changed (HAProxy global/defaults and logs)")
	return err
}
func (s *PGStore) DeleteNode(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM nodes WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// routeColumns reads one route including its ordered SNI list. It is used by
// single-row lookups where the correlated SNI subquery runs exactly once.
const routeColumns = routeColumnsHead + `
	ARRAY(SELECT rs.sni FROM route_snis rs WHERE rs.route_id=r.id ORDER BY rs.position),` + routeColumnsTail

// routeListColumns is routeColumns without the per-row correlated SNI
// subquery. Multi-row reads scan a NULL placeholder and fill SNIs with one
// batched query via loadRouteChildrenTx.
const routeListColumns = routeColumnsHead + `
	NULL::text[],` + routeColumnsTail

const routeColumnsHead = `r.id::text,r.node_id::text,r.name,r.version,r.listener_ip,r.listener_port,r.match_mode,`

const routeColumnsTail = `
	r.fallback,r.hostname,r.target_type,r.target_host,r.target_port,r.dns_pool,r.unix_socket_path,
	r.health_check,r.proxy_protocol,r.accept_proxy_from,r.quota_bytes,r.quota_action,r.quota_period,r.client_upload_mbps,r.client_download_mbps,r.enabled,r.deployed,r.deployment_state,
	r.deployment_error,r.desired_revision,r.applied_revision,r.desired_fingerprint,
	r.deployed_fingerprint,r.delete_pending,
	r.custom_fragment,r.sticky_enabled,r.balance_mode,r.shaper_mode,r.sticky_mode,r.sticky_ttl,
	r.balance_algorithm,r.balance_random_draws,r.leastping_tolerance,r.leastping_tolerance_ms,
	r.sticky_hash,r.sticky_hash_balance_factor,r.sticky_table_entries,r.sticky_ipv6_prefix,r.slowstart,
	r.client_ipv6,r.created_at,r.updated_at,r.sort_order`

func scanRoute(row pgx.Row) (Route, error) {
	var r Route
	err := row.Scan(
		&r.ID, &r.NodeID, &r.Name, &r.Version, &r.ListenerIP, &r.ListenerPort, &r.MatchMode, &r.SNIs, &r.Fallback,
		&r.Hostname, &r.TargetType, &r.TargetHost, &r.TargetPort, &r.DNSPool, &r.UnixSocketPath,
		&r.HealthCheck, &r.ProxyProtocol, &r.AcceptProxyFrom, &r.QuotaBytes, &r.QuotaAction, &r.QuotaPeriod, &r.ClientUploadMbps, &r.ClientDownloadMbps, &r.Enabled, &r.Deployed, &r.DeploymentState,
		&r.DeploymentError, &r.DesiredRevision, &r.AppliedRevision,
		&r.DesiredFingerprint, &r.DeployedFingerprint, &r.DeletePending,
		&r.CustomFragment, &r.StickyEnabled, &r.BalanceMode, &r.ShaperMode, &r.StickyMode, &r.StickyTTL,
		&r.BalanceAlgorithm, &r.BalanceRandomDraws, &r.LeastPingTolerance, &r.LeastPingToleranceMS,
		&r.StickyHash, &r.StickyHashBalanceFactor, &r.StickyTableEntries, &r.StickyIPv6Prefix, &r.Slowstart,
		&r.ClientIPv6, &r.CreatedAt, &r.UpdatedAt, &r.SortOrder,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Route{}, ErrNotFound
	}
	if err == nil && r.SNIs == nil {
		r.SNIs = []string{}
	}
	r.BalanceTolerance = r.LeastPingTolerance
	if err == nil && r.AcceptProxyFrom == nil {
		r.AcceptProxyFrom = []string{}
	}
	// F6: normalize legacy match_mode values on read so the API always
	// returns the canonical "fallback" mode regardless of stored value.
	if err == nil && (r.MatchMode == "any_tcp" || r.MatchMode == "destination_ip") {
		r.MatchMode = "fallback"
	}
	return r, err
}

type routeQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// routeBatcher is satisfied by *pgxpool.Pool and pgx.Tx.
type routeBatcher interface {
	routeQuerier
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

const routeSNIsBatchSQL = `
	SELECT route_id::text,sni
	FROM route_snis
	WHERE route_id = ANY($1::uuid[])
	ORDER BY route_id,position`

const routeServersBatchSQL = `
	SELECT id::text,route_id::text,position,name,target_type,
	       COALESCE(host,''),COALESCE(port,0),COALESCE(unix_socket_path,''),backup,
	       COALESCE(dns_pool,false),COALESCE(preferred_ip,''),created_at,updated_at,
	       weight,cost,ip_weights
	FROM route_servers
	WHERE route_id = ANY($1::uuid[])
	ORDER BY route_id,position`

func routeIndex(routes []Route) ([]string, map[string]int) {
	ids := make([]string, len(routes))
	index := make(map[string]int, len(routes))
	for i, r := range routes {
		ids[i] = r.ID
		index[r.ID] = i
	}
	return ids, index
}

func scanRouteSNIRows(rows pgx.Rows, routes []Route, index map[string]int) error {
	defer rows.Close()
	for i := range routes {
		routes[i].SNIs = []string{}
	}
	for rows.Next() {
		var routeID, sni string
		if err := rows.Scan(&routeID, &sni); err != nil {
			return err
		}
		if i, ok := index[routeID]; ok {
			routes[i].SNIs = append(routes[i].SNIs, sni)
		}
	}
	return rows.Err()
}

func scanRouteServerRows(rows pgx.Rows, routes []Route, index map[string]int) error {
	defer rows.Close()
	for rows.Next() {
		var srv RouteServer
		var ipWeights []byte
		if err := rows.Scan(&srv.ID, &srv.RouteID, &srv.Position, &srv.Name,
			&srv.TargetType, &srv.Host, &srv.Port, &srv.UnixSocketPath,
			&srv.Backup, &srv.DNSPool, &srv.PreferredIP, &srv.CreatedAt, &srv.UpdatedAt,
			&srv.Weight, &srv.Cost, &ipWeights); err != nil {
			return err
		}
		if len(ipWeights) > 0 {
			if err := json.Unmarshal(ipWeights, &srv.IPWeights); err != nil {
				return err
			}
		}
		if i, ok := index[srv.RouteID]; ok {
			routes[i].Servers = append(routes[i].Servers, srv)
		}
	}
	return rows.Err()
}

// loadRouteChildrenTx loads SNIs and servers for list reads. Both child
// queries are pipelined in one pgx batch, so a route list costs two network
// round-trips in total regardless of the number of routes.
func loadRouteChildrenTx(ctx context.Context, q routeBatcher, routes []Route) ([]Route, error) {
	if len(routes) == 0 {
		return routes, nil
	}
	ids, index := routeIndex(routes)
	batch := &pgx.Batch{}
	batch.Queue(routeSNIsBatchSQL, ids)
	batch.Queue(routeServersBatchSQL, ids)
	results := q.SendBatch(ctx, batch)
	rows, err := results.Query()
	if err == nil {
		err = scanRouteSNIRows(rows, routes, index)
	}
	if err == nil {
		rows, err = results.Query()
		if err == nil {
			err = scanRouteServerRows(rows, routes, index)
		}
	}
	if closeErr := results.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	return routes, nil
}

// loadRouteServersTx loads all server rows for a set of route IDs and
// populates each Route.Servers slice. Safe to call with an empty routes slice.
func loadRouteServersTx(ctx context.Context, q routeQuerier, routes []Route) ([]Route, error) {
	if len(routes) == 0 {
		return routes, nil
	}
	ids, index := routeIndex(routes)
	rows, err := q.Query(ctx, routeServersBatchSQL, ids)
	if err != nil {
		return nil, err
	}
	if err = scanRouteServerRows(rows, routes, index); err != nil {
		return nil, err
	}
	return routes, nil
}

// insertRouteServersTx replaces all server rows for a route.
func insertRouteServersTx(ctx context.Context, tx pgx.Tx, routeID, nodeID string, servers []RouteServerSpec) error {
	if _, err := tx.Exec(ctx, `DELETE FROM route_servers WHERE route_id=$1`, routeID); err != nil {
		return err
	}
	if len(servers) == 0 {
		return nil
	}
	positions := make([]int32, len(servers))
	names := make([]string, len(servers))
	targetTypes := make([]string, len(servers))
	hosts := make([]string, len(servers))
	ports := make([]int32, len(servers))
	socketPaths := make([]string, len(servers))
	backups := make([]bool, len(servers))
	dnsPools := make([]bool, len(servers))
	preferredIPs := make([]string, len(servers))
	weights := make([]int32, len(servers))
	costs := make([]float64, len(servers))
	ipWeights := make([]string, len(servers))
	for i, srv := range servers {
		positions[i] = int32(srv.Position)
		names[i] = srv.Name
		targetTypes[i] = srv.TargetType
		hosts[i] = srv.Host
		ports[i] = int32(srv.Port)
		socketPaths[i] = srv.UnixSocketPath
		backups[i] = srv.Backup
		dnsPools[i] = srv.DNSPool
		preferredIPs[i] = srv.PreferredIP
		weights[i] = int32(srv.Weight)
		costs[i] = srv.Cost
		values := srv.IPWeights
		if values == nil {
			values = []RouteServerIPWeight{}
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			return err
		}
		ipWeights[i] = string(encoded)
	}
	// One round-trip for all servers; unnest keeps array order so rows are
	// inserted exactly as supplied (constraint errors stay identical).
	_, err := tx.Exec(ctx, `
		INSERT INTO route_servers(route_id,node_id,position,name,target_type,host,port,unix_socket_path,backup,dns_pool,preferred_ip,weight,cost,ip_weights)
		SELECT $1,$2,srv.position,srv.name,srv.target_type,srv.host,srv.port,srv.unix_socket_path,srv.backup,srv.dns_pool,srv.preferred_ip,srv.weight,srv.cost,srv.ip_weights::jsonb
		FROM unnest($3::integer[],$4::text[],$5::text[],$6::text[],$7::integer[],$8::text[],$9::boolean[],$10::boolean[],$11::text[],$12::integer[],$13::numeric[],$14::text[])
		  AS srv(position,name,target_type,host,port,unix_socket_path,backup,dns_pool,preferred_ip,weight,cost,ip_weights)`,
		routeID, nodeID, positions, names, targetTypes, hosts, ports, socketPaths, backups, dnsPools, preferredIPs, weights, costs, ipWeights)
	return err
}

func (s *PGStore) ListRoutes(ctx context.Context, nodeID string) ([]Route, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+routeListColumns+` FROM routes r WHERE r.node_id=$1 ORDER BY r.sort_order,r.id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Route, 0)
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return loadRouteChildrenTx(ctx, s.pool, out)
}
func (s *PGStore) CreateRoute(ctx context.Context, nodeID string, spec RouteSpec) (Route, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Route{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockRouteNode(ctx, tx, nodeID); err != nil {
		return Route{}, err
	}
	// Creating a route is always a control-plane-only draft. Activation is an
	// explicit PUT with enabled=true so a partially completed builder can never
	// alter the running HAProxy configuration.
	spec.Enabled = false
	normalizeRouteSpecDefaults(&spec)
	if spec.QuotaPeriod == "" {
		spec.QuotaPeriod = defaultQuotaPeriod
	}
	if specNeedsKernelShaper(spec) {
		if err = requireKernelShaperAgentTx(ctx, tx, nodeID, ""); err != nil {
			return Route{}, err
		}
	}
	if specNeedsLeastPingAgent(spec) {
		if err = requireLeastPingAgentTx(ctx, tx, nodeID, ""); err != nil {
			return Route{}, err
		}
	}
	if specNeedsIPWeightsAgent(spec) {
		if err = requireIPWeightsAgentTx(ctx, tx, nodeID, ""); err != nil {
			return Route{}, err
		}
	}
	if specNeedsLeastConnAgent(spec) {
		if err = requireLeastConnAgentTx(ctx, tx, nodeID, ""); err != nil {
			return Route{}, err
		}
	}
	if specNeedsPPTrustedDomainsAgent(spec) {
		if err = requirePPTrustedDomainsAgentTx(ctx, tx, nodeID, ""); err != nil {
			return Route{}, err
		}
	}
	desiredFingerprint := routeSpecFingerprint(spec)
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO routes(
			node_id,name,listener_ip,listener_port,match_mode,fallback,hostname,target_type,target_host,target_port,dns_pool,
			unix_socket_path,health_check,proxy_protocol,accept_proxy_from,quota_bytes,quota_action,quota_period,client_upload_mbps,client_download_mbps,enabled,desired_fingerprint,custom_fragment,sticky_enabled,balance_mode,shaper_mode,sticky_mode,sticky_ttl,
			balance_algorithm,balance_random_draws,leastping_tolerance,leastping_tolerance_ms,sticky_hash,sticky_hash_balance_factor,sticky_table_entries,sticky_ipv6_prefix,slowstart,client_ipv6,sort_order
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,
			$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,
			(SELECT COALESCE(max(sort_order),0)+1 FROM routes WHERE node_id=$1))
		RETURNING id::text`,
		nodeID, spec.Name, spec.ListenerIP, spec.ListenerPort, spec.MatchMode, spec.Fallback, spec.Hostname, spec.TargetType,
		spec.TargetHost, spec.TargetPort, spec.DNSPool, spec.UnixSocketPath, spec.HealthCheck, spec.ProxyProtocol, nonNilStrings(spec.AcceptProxyFrom), spec.QuotaBytes,
		spec.QuotaAction, spec.QuotaPeriod, spec.ClientUploadMbps, spec.ClientDownloadMbps, spec.Enabled, desiredFingerprint, spec.CustomFragment,
		spec.StickyEnabled, spec.BalanceMode, storedShaperMode(spec.ShaperMode), spec.StickyMode, spec.StickyTTL,
		spec.BalanceAlgorithm, spec.BalanceRandomDraws, spec.LeastPingTolerance, spec.LeastPingToleranceMS,
		spec.StickyHash, spec.StickyHashBalanceFactor, spec.StickyTableEntries, spec.StickyIPv6Prefix, spec.Slowstart,
		!spec.ClientIPv4Only,
	).Scan(&id)
	if err != nil {
		return Route{}, err
	}
	if err = insertRouteSNIs(ctx, tx, id, nodeID, spec); err != nil {
		return Route{}, err
	}
	if err = insertRouteServersTx(ctx, tx, id, nodeID, spec.Servers); err != nil {
		return Route{}, err
	}
	route, err := readRouteTx(ctx, tx, nodeID, id)
	if err != nil {
		return Route{}, err
	}
	return route, tx.Commit(ctx)
}

// readRouteTx reads one route with every child collection (SNIs and
// servers), i.e. exactly the shape GET /routes/{id} returns. Mutation
// responses must use it: the editor rebuilds its draft from them.
func readRouteTx(ctx context.Context, tx pgx.Tx, nodeID, id string) (Route, error) {
	route, err := scanRoute(tx.QueryRow(ctx, `SELECT `+routeColumns+` FROM routes r WHERE r.node_id=$1 AND r.id=$2`, nodeID, id))
	if err != nil {
		return Route{}, err
	}
	routes, err := loadRouteServersTx(ctx, tx, []Route{route})
	if err != nil {
		return Route{}, err
	}
	return routes[0], nil
}

func (s *PGStore) GetRoute(ctx context.Context, nodeID, id string) (Route, error) {
	route, err := scanRoute(s.pool.QueryRow(ctx, `SELECT `+routeColumns+` FROM routes r WHERE r.node_id=$1 AND r.id=$2`, nodeID, id))
	if err != nil {
		return Route{}, err
	}
	routes, err := loadRouteServersTx(ctx, s.pool, []Route{route})
	if err != nil {
		return Route{}, err
	}
	return routes[0], nil
}
func (s *PGStore) UpdateRoute(ctx context.Context, nodeID, id string, spec RouteSpec) (Route, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Route{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockRouteNode(ctx, tx, nodeID); err != nil {
		return Route{}, err
	}
	current, err := lockRouteForUpdateTx(ctx, tx, nodeID, id)
	if err != nil {
		return Route{}, err
	}
	route, err := updateRouteTx(ctx, tx, nodeID, id, current, spec)
	if err != nil {
		return Route{}, err
	}
	return route, tx.Commit(ctx)
}

// SetRouteEnabled toggles only the enabled flag of a stored route. Every
// other field, including servers[] and the 1.1.0 options, is taken from the
// stored row, so the node-page toggle can never drop them.
func (s *PGStore) SetRouteEnabled(ctx context.Context, nodeID, id string, enabled bool, expectedVersion *int64) (Route, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Route{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockRouteNode(ctx, tx, nodeID); err != nil {
		return Route{}, err
	}
	current, err := lockRouteForUpdateTx(ctx, tx, nodeID, id)
	if err != nil {
		return Route{}, err
	}
	spec := routeAsSpec(current, enabled)
	spec.ExpectedVersion = expectedVersion
	route, err := updateRouteTx(ctx, tx, nodeID, id, current, spec)
	if err != nil {
		return Route{}, err
	}
	return route, tx.Commit(ctx)
}

// lockRouteForUpdateTx reads and row-locks one route with its servers.
func lockRouteForUpdateTx(ctx context.Context, tx pgx.Tx, nodeID, id string) (Route, error) {
	current, err := scanRoute(tx.QueryRow(ctx, `SELECT `+routeColumns+` FROM routes r WHERE r.node_id=$1 AND r.id=$2 FOR UPDATE`, nodeID, id))
	if err != nil {
		return Route{}, err
	}
	routes, err := loadRouteServersTx(ctx, tx, []Route{current})
	if err != nil {
		return Route{}, err
	}
	return routes[0], nil
}

func updateRouteTx(ctx context.Context, tx pgx.Tx, nodeID, id string, current Route, spec RouteSpec) (Route, error) {
	if spec.ExpectedVersion != nil && current.Version != *spec.ExpectedVersion {
		return Route{}, ErrRouteVersionConflict
	}
	normalizeRouteSpecDefaults(&spec)
	if spec.QuotaPeriod == "" {
		spec.QuotaPeriod = defaultQuotaPeriod
	}
	if routeUpdateIsNoop(current, spec) {
		// An identical save must not bump the version, publish a revision or
		// reload HAProxy.
		return current, nil
	}
	if specNeedsKernelShaper(spec) && (spec.Enabled || !routeNeedsKernelShaper(current)) {
		if err := requireKernelShaperAgentTx(ctx, tx, nodeID, id); err != nil {
			return Route{}, err
		}
	}
	if specNeedsLeastPingAgent(spec) && (spec.Enabled || !routeNeedsLeastPingAgent(current)) {
		if err := requireLeastPingAgentTx(ctx, tx, nodeID, id); err != nil {
			return Route{}, err
		}
	}
	if specNeedsIPWeightsAgent(spec) && (spec.Enabled || !routeNeedsIPWeightsAgent(current)) {
		if err := requireIPWeightsAgentTx(ctx, tx, nodeID, id); err != nil {
			return Route{}, err
		}
	}
	if specNeedsLeastConnAgent(spec) && (spec.Enabled || !routeNeedsLeastConnAgent(current)) {
		if err := requireLeastConnAgentTx(ctx, tx, nodeID, id); err != nil {
			return Route{}, err
		}
	}
	if specNeedsPPTrustedDomainsAgent(spec) && (spec.Enabled || !routeNeedsPPTrustedDomainsAgent(current)) {
		if err := requirePPTrustedDomainsAgentTx(ctx, tx, nodeID, id); err != nil {
			return Route{}, err
		}
	}
	desiredFingerprint := routeSpecFingerprint(spec)
	if _, err := tx.Exec(ctx, `DELETE FROM route_snis WHERE node_id=$1 AND route_id=$2`, nodeID, id); err != nil {
		return Route{}, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE routes SET
			name=$3,listener_ip=$4,listener_port=$5,match_mode=$6,fallback=$7,hostname=$8,target_type=$9,target_host=$10,
			target_port=$11,dns_pool=$12,unix_socket_path=$13,health_check=$14,proxy_protocol=$15,accept_proxy_from=$16,quota_bytes=$17,quota_action=$18,
			quota_period=$19,client_upload_mbps=$20,client_download_mbps=$21,enabled=$22,desired_fingerprint=$23,delete_pending=false,custom_fragment=$24,
			sticky_enabled=$25,balance_mode=$26,shaper_mode=$27,sticky_mode=$28,sticky_ttl=$29,
			balance_algorithm=$30,balance_random_draws=$31,leastping_tolerance=$32,leastping_tolerance_ms=$33,
			sticky_hash=$34,sticky_hash_balance_factor=$35,sticky_table_entries=$36,sticky_ipv6_prefix=$37,slowstart=$38,client_ipv6=$39,
			version=version+1,updated_at=now()
		WHERE node_id=$1 AND id=$2`,
		nodeID, id, spec.Name, spec.ListenerIP, spec.ListenerPort, spec.MatchMode, spec.Fallback, spec.Hostname, spec.TargetType,
		spec.TargetHost, spec.TargetPort, spec.DNSPool, spec.UnixSocketPath, spec.HealthCheck, spec.ProxyProtocol, nonNilStrings(spec.AcceptProxyFrom), spec.QuotaBytes,
		spec.QuotaAction, spec.QuotaPeriod, spec.ClientUploadMbps, spec.ClientDownloadMbps, spec.Enabled, desiredFingerprint, spec.CustomFragment,
		spec.StickyEnabled, spec.BalanceMode, storedShaperMode(spec.ShaperMode), spec.StickyMode, spec.StickyTTL,
		spec.BalanceAlgorithm, spec.BalanceRandomDraws, spec.LeastPingTolerance, spec.LeastPingToleranceMS,
		spec.StickyHash, spec.StickyHashBalanceFactor, spec.StickyTableEntries, spec.StickyIPv6Prefix, spec.Slowstart,
		!spec.ClientIPv4Only,
	)
	if err != nil {
		return Route{}, err
	}
	if tag.RowsAffected() == 0 {
		return Route{}, ErrNotFound
	}
	if err = insertRouteSNIs(ctx, tx, id, nodeID, spec); err != nil {
		return Route{}, err
	}
	if err = insertRouteServersTx(ctx, tx, id, nodeID, spec.Servers); err != nil {
		return Route{}, err
	}
	needsApply := routeUpdateNeedsApply(current, spec)
	if needsApply {
		if _, err = createAndAssignRouteRevisionTx(ctx, tx, nodeID, id, "route "+id+" updated"); err != nil {
			return Route{}, err
		}
	} else {
		state := "draft"
		if current.AppliedRevision != nil {
			state = "disabled"
		}
		if _, err = tx.Exec(ctx, `
			UPDATE routes SET deployment_state=$3,deployment_error='',delete_pending=false
			WHERE node_id=$1 AND id=$2`, nodeID, id, state); err != nil {
			return Route{}, err
		}
	}
	return readRouteTx(ctx, tx, nodeID, id)
}

// requireKernelShaperAgentTx rejects kernel shaping for a node whose latest
// heartbeat reports an Agent older than KernelShaperMinAgentVersion.
func requireKernelShaperAgentTx(ctx context.Context, tx pgx.Tx, nodeID, routeID string) error {
	version, err := nodeAgentVersionTx(ctx, tx, nodeID)
	if err != nil {
		return err
	}
	if !agentSupportsKernelShaper(version) {
		return &KernelShaperUnsupportedError{RouteID: routeID, AgentVersion: version}
	}
	return nil
}

// requireLeastPingAgentTx rejects effective leastping for a node whose latest
// heartbeat reports an Agent older than NodeAgent110.
func requireLeastPingAgentTx(ctx context.Context, tx pgx.Tx, nodeID, routeID string) error {
	version, err := nodeAgentVersionTx(ctx, tx, nodeID)
	if err != nil {
		return err
	}
	if !agentSupportsRoute110Features(version) {
		return &LeastPingUnsupportedError{RouteID: routeID, AgentVersion: version}
	}
	return nil
}

// requireLeastConnAgentTx rejects a leastconn tolerance for a node whose
// latest heartbeat reports an Agent older than NodeAgent111.
func requireLeastConnAgentTx(ctx context.Context, tx pgx.Tx, nodeID, routeID string) error {
	version, err := nodeAgentVersionTx(ctx, tx, nodeID)
	if err != nil {
		return err
	}
	if !agentVersionAtLeast(version, NodeAgent111) {
		return &LeastConnToleranceUnsupportedError{RouteID: routeID, AgentVersion: version}
	}
	return nil
}

// requirePPTrustedDomainsAgentTx rejects hostnames in accept_proxy_from for
// a node whose latest heartbeat reports an Agent older than NodeAgent113.
func requirePPTrustedDomainsAgentTx(ctx context.Context, tx pgx.Tx, nodeID, routeID string) error {
	version, err := nodeAgentVersionTx(ctx, tx, nodeID)
	if err != nil {
		return err
	}
	if !agentVersionAtLeast(version, NodeAgent113) {
		return &PPTrustedDomainsUnsupportedError{RouteID: routeID, AgentVersion: version}
	}
	return nil
}

// requireIPWeightsAgentTx rejects ip_weights for a node whose latest
// heartbeat reports an Agent older than NodeAgent110.
func requireIPWeightsAgentTx(ctx context.Context, tx pgx.Tx, nodeID, routeID string) error {
	version, err := nodeAgentVersionTx(ctx, tx, nodeID)
	if err != nil {
		return err
	}
	if !agentSupportsRoute110Features(version) {
		return &IPWeightsUnsupportedError{RouteID: routeID, AgentVersion: version}
	}
	return nil
}

// routeUpdateIsNoop reports whether spec is exactly the stored route and the
// route is settled, so the update can be skipped entirely. A failed, pending
// or deleting route is never a no-op: a repeated save is the retry.
func routeUpdateIsNoop(current Route, spec RouteSpec) bool {
	if current.DeletePending || current.Enabled != spec.Enabled {
		return false
	}
	if spec.Enabled {
		if current.DeploymentState != "active" || !current.Deployed || current.DesiredFingerprint != current.DeployedFingerprint {
			return false
		}
	} else if current.Deployed || (current.DeploymentState != "draft" && current.DeploymentState != "disabled") {
		return false
	}
	stored := routeAsSpec(current, current.Enabled)
	normalizeRouteSpecDefaults(&stored)
	if stored.BalanceMode != spec.BalanceMode || stored.StickyMode != spec.StickyMode || stored.StickyTTL != spec.StickyTTL ||
		storedShaperMode(stored.ShaperMode) != storedShaperMode(spec.ShaperMode) || stored.Hostname != spec.Hostname ||
		stored.BalanceAlgorithm != spec.BalanceAlgorithm {
		return false
	}
	return routeSpecFingerprint(canonicalRouteSpec(stored)) == routeSpecFingerprint(canonicalRouteSpec(spec))
}

// canonicalRouteSpec maps empty and nil child slices to the same value so a
// stored route and a freshly validated spec hash identically.
func canonicalRouteSpec(spec RouteSpec) RouteSpec {
	if len(spec.SNIs) == 0 {
		spec.SNIs = []string{}
	}
	if len(spec.AcceptProxyFrom) == 0 {
		spec.AcceptProxyFrom = []string{}
	}
	if len(spec.Servers) == 0 {
		spec.Servers = nil
	}
	return spec
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (s *PGStore) DeleteRoute(ctx context.Context, nodeID, id string, expectedVersion *int64) (RouteDeleteResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RouteDeleteResult{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockRouteNode(ctx, tx, nodeID); err != nil {
		return RouteDeleteResult{}, err
	}
	current, err := scanRoute(tx.QueryRow(ctx, `SELECT `+routeColumns+` FROM routes r WHERE r.node_id=$1 AND r.id=$2 FOR UPDATE`, nodeID, id))
	if err != nil {
		return RouteDeleteResult{}, err
	}
	if expectedVersion != nil && current.Version != *expectedVersion {
		return RouteDeleteResult{}, ErrRouteVersionConflict
	}
	// A route already known to be absent from the running configuration can be
	// removed immediately. Every other state first publishes an exclusion
	// revision; this also supersedes a failed/pending enable operation.
	if routeCanDeleteImmediately(current) {
		if _, err = tx.Exec(ctx, `DELETE FROM routes WHERE node_id=$1 AND id=$2`, nodeID, id); err != nil {
			return RouteDeleteResult{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return RouteDeleteResult{}, err
		}
		return RouteDeleteResult{}, nil
	}
	if _, err = tx.Exec(ctx, `
		UPDATE routes SET enabled=false,delete_pending=true,deployment_state='deleting',
			deployment_error='',desired_fingerprint=$3,version=version+1,updated_at=now()
		WHERE node_id=$1 AND id=$2`, nodeID, id, routeSpecFingerprint(routeAsSpec(current, false))); err != nil {
		return RouteDeleteResult{}, err
	}
	if _, err = createAndAssignRouteRevisionTx(ctx, tx, nodeID, id, "route "+id+" deletion"); err != nil {
		return RouteDeleteResult{}, err
	}
	route, err := readRouteTx(ctx, tx, nodeID, id)
	if err != nil {
		return RouteDeleteResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RouteDeleteResult{}, err
	}
	return RouteDeleteResult{Route: route, Pending: true}, nil
}

func routeUpdateNeedsApply(current Route, desired RouteSpec) bool {
	// Any enabled edit is published, including a retry after a failed enable.
	// A disabled edit is local-only only when both desired and actual state are
	// already off and no previous operation still needs superseding.
	return desired.Enabled || current.Enabled || current.Deployed || current.DeletePending
}

func routeCanDeleteImmediately(route Route) bool {
	return !route.Enabled && !route.Deployed && !route.DeletePending &&
		(route.DeploymentState == "draft" || route.DeploymentState == "disabled")
}

func listRoutesTx(ctx context.Context, tx pgx.Tx, nodeID string) ([]Route, error) {
	rows, err := tx.Query(ctx, `SELECT `+routeListColumns+` FROM routes r WHERE r.node_id=$1 ORDER BY r.sort_order,r.id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	routes := make([]Route, 0)
	for rows.Next() {
		route, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	// The publish path must see the same multi-server backends as the API
	// read path; otherwise revisions silently drop extra/backup servers.
	return loadRouteChildrenTx(ctx, tx, routes)
}

// createAndAssignRouteRevisionTx is the atomic route->config transition. The
// node row is already locked by the caller, so concurrent mutations serialize
// into monotonically increasing immutable revisions without a stale render.
func createAndAssignRouteRevisionTx(ctx context.Context, tx pgx.Tx, nodeID, affectedRouteID, note string) (ConfigRevision, error) {
	return createAndAssignGeneratedRevisionTx(ctx, tx, nodeID, affectedRouteID, note, false)
}

// Only an explicit resume operation may replace an advanced configuration.
func createAndAssignGeneratedRevisionTx(ctx context.Context, tx pgx.Tx, nodeID, affectedRouteID, note string, resume bool) (ConfigRevision, error) {
	var manual bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM node_config_state s JOIN config_revisions r ON r.node_id=s.node_id AND r.revision=s.desired_revision WHERE s.node_id=$1 AND r.metadata->>'source'='advanced_editor')`, nodeID).Scan(&manual); err != nil {
		return ConfigRevision{}, err
	}
	if manual && !resume {
		return ConfigRevision{}, ErrAdvancedConfig
	}
	routes, err := listRoutesTx(ctx, tx, nodeID)
	if err != nil {
		return ConfigRevision{}, err
	}
	agentVersion, err := nodeAgentVersionTx(ctx, tx, nodeID)
	if err != nil {
		return ConfigRevision{}, err
	}
	// An Agent downgrade after a kernel-shaped/leastping/ip_weights route was
	// enabled must not let the next revision silently drop it.
	if err = checkKernelShaperRoutes(routes, agentVersion); err != nil {
		return ConfigRevision{}, err
	}
	if err = checkLeastPingAgentRoutes(routes, agentVersion); err != nil {
		return ConfigRevision{}, err
	}
	if err = checkIPWeightsAgentRoutes(routes, agentVersion); err != nil {
		return ConfigRevision{}, err
	}
	if err = checkLeastConnAgentRoutes(routes, agentVersion); err != nil {
		return ConfigRevision{}, err
	}
	if err = checkPPTrustedDomainsAgentRoutes(routes, agentVersion); err != nil {
		return ConfigRevision{}, err
	}
	facts, err := nodeRenderFactsTx(ctx, tx, nodeID)
	if err != nil {
		return ConfigRevision{}, err
	}
	result, err := renderHAProxyConfigForLifecycle(routes, facts)
	if err != nil {
		return ConfigRevision{}, err
	}
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(max(revision),0)+1 FROM config_revisions WHERE node_id=$1`, nodeID).Scan(&revision); err != nil {
		return ConfigRevision{}, err
	}
	metadataJSON, err := json.Marshal(renderMetadata(result))
	if err != nil {
		return ConfigRevision{}, err
	}
	created, err := scanConfigRevision(tx.QueryRow(ctx, `
		INSERT INTO config_revisions(node_id,revision,config,sha256,note,metadata,created_by)
		VALUES($1,$2,$3,$4,$5,$6,'route_lifecycle') RETURNING `+configRevisionColumns,
		nodeID, revision, result.Config, result.SHA256, note, metadataJSON))
	if err != nil {
		return ConfigRevision{}, err
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO node_config_state(node_id,desired_revision,state,last_error,updated_at)
		VALUES($1,$2,'pending','',clock_timestamp())
		ON CONFLICT(node_id) DO UPDATE SET desired_revision=EXCLUDED.desired_revision,
			state='pending',last_error='',updated_at=EXCLUDED.updated_at`, nodeID, revision); err != nil {
		return ConfigRevision{}, err
	}
	// A node revision republishes every enabled route, not only the route that
	// triggered it. Project the immutable ledger fingerprints back to every
	// member so renderer fingerprint-schema changes cannot leave stale intent.
	if _, err = tx.Exec(ctx, `
		UPDATE routes SET desired_revision=$2,
			desired_fingerprint=COALESCE(
				NULLIF(($4::jsonb->'route_fingerprints'->>id::text),''),
				desired_fingerprint
			),
			deployment_state=CASE WHEN delete_pending THEN 'deleting' ELSE 'pending' END,
			deployment_error=''
		WHERE node_id=$1 AND (enabled OR deployed OR delete_pending OR id=$3)`,
		nodeID, revision, nullableUUID(affectedRouteID), string(metadataJSON)); err != nil {
		return ConfigRevision{}, err
	}
	return created, nil
}

type routeTx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func lockRouteNode(ctx context.Context, tx pgx.Tx, nodeID string) error {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT true FROM nodes WHERE id=$1 FOR UPDATE`, nodeID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func insertRouteSNIs(ctx context.Context, tx routeTx, routeID, nodeID string, spec RouteSpec) error {
	if len(spec.SNIs) == 0 {
		return nil
	}
	// Positions are the zero-based array order, matching the previous
	// per-row inserts; WITH ORDINALITY is one-based.
	_, err := tx.Exec(ctx, `
		INSERT INTO route_snis(route_id,node_id,listener_ip,listener_port,position,sni)
		SELECT $1,$2,$3,$4,(item.ordinality-1)::smallint,item.sni
		FROM unnest($5::text[]) WITH ORDINALITY AS item(sni,ordinality)`,
		routeID, nodeID, spec.ListenerIP, spec.ListenerPort, spec.SNIs)
	return err
}

func (s *PGStore) CreateEnrollmentToken(ctx context.Context, nodeID, hash, prefix string, expires time.Time) (EnrollmentToken, error) {
	var t EnrollmentToken
	err := s.pool.QueryRow(ctx, `INSERT INTO enrollment_tokens(node_id,token_hash,token_prefix,expires_at) VALUES($1,$2,$3,$4) RETURNING id::text,node_id::text,token_prefix,expires_at,created_at`, nodeID, hash, prefix, expires).Scan(&t.ID, &t.NodeID, &t.Prefix, &t.ExpiresAt, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func (s *PGStore) EnrollmentTokenUsed(ctx context.Context, tokenID string) (bool, error) {
	var used bool
	err := s.pool.QueryRow(ctx, `SELECT last_used_at IS NOT NULL FROM enrollment_tokens WHERE id=$1 AND activated_at IS NOT NULL AND revoked_at IS NULL`, tokenID).Scan(&used)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	return used, err
}

func (s *PGStore) RevokeEnrollmentToken(ctx context.Context, tokenID string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE enrollment_tokens SET revoked_at=clock_timestamp() WHERE id=$1 AND revoked_at IS NULL`, tokenID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *PGStore) RevokeOtherEnrollmentTokens(ctx context.Context, nodeID, keepTokenID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE enrollment_tokens SET revoked_at=clock_timestamp()
		WHERE node_id=$1 AND id<>$2 AND revoked_at IS NULL`, nodeID, keepTokenID)
	return err
}

func (s *PGStore) AppendAudit(ctx context.Context, event AuditEvent) error {
	details, err := json.Marshal(event.Details)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO audit_log(actor_type,actor_id,action,resource_type,resource_id,details,source_ip)
		VALUES($1,NULLIF($2,''),$3,$4,NULLIF($5,''),$6,NULLIF($7,'')::inet)`,
		event.ActorType, event.ActorID, event.Action, event.ResourceType, event.ResourceID, details, event.SourceIP)
	return err
}

// CleanupAudit is independently gated in PostgreSQL. Multiple Panel replicas
// may run their own periodic cleaner, but at most one retention batch is due in
// each gate window. Each transaction deletes a bounded number of rows; a full
// batch reopens the gate shortly so a backlog drains without one huge delete.
func (s *PGStore) CleanupAudit(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	var retentionDays int
	err = tx.QueryRow(ctx, `
		UPDATE panel_settings
		SET next_audit_cleanup_at=clock_timestamp()+interval '6 hours'
		WHERE singleton=true AND next_audit_cleanup_at<=clock_timestamp()
		RETURNING audit_retention_days`).Scan(&retentionDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}

	tag, err := tx.Exec(ctx, `
		DELETE FROM audit_log AS audit
		USING (
		  SELECT id FROM audit_log
		  WHERE created_at < clock_timestamp()-make_interval(days => $1)
		  ORDER BY created_at,id
		  LIMIT $2
		) AS expired
		WHERE audit.id=expired.id`, retentionDays, auditCleanupBatchSize)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == auditCleanupBatchSize {
		_, err = tx.Exec(ctx, `
			UPDATE panel_settings
			SET next_audit_cleanup_at=LEAST(next_audit_cleanup_at,clock_timestamp()+($1 * interval '1 second'))
			WHERE singleton=true`, int64(auditCleanupBacklogRetry/time.Second))
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PGStore) ListAudit(ctx context.Context, nodeID string, limit int) ([]AuditEntry, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	const auditColumns = `id,actor_type,COALESCE(actor_id,''),action,resource_type,COALESCE(resource_id,''),
		       details,COALESCE(host(source_ip),''),created_at`
	var rows pgx.Rows
	var err error
	if nodeID == "" {
		rows, err = s.pool.Query(ctx, `SELECT `+auditColumns+` FROM audit_log ORDER BY id DESC LIMIT $1`, limit)
	} else {
		// Each branch is an ordered range scan on its own partial index
		// (audit_log_resource_id_idx / audit_log_node_detail_idx); the outer
		// merge reads at most 2*limit ids instead of filtering the whole log.
		rows, err = s.pool.Query(ctx, `
			SELECT `+auditColumns+`
			FROM audit_log
			WHERE id IN (
				(SELECT id FROM audit_log WHERE resource_id=$1 ORDER BY id DESC LIMIT $2)
				UNION
				(SELECT id FROM audit_log WHERE details->>'node_id'=$1 ORDER BY id DESC LIMIT $2)
			)
			ORDER BY id DESC LIMIT $2`, nodeID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]AuditEntry, 0)
	for rows.Next() {
		var entry AuditEntry
		var details []byte
		if err = rows.Scan(&entry.ID, &entry.ActorType, &entry.ActorID, &entry.Action, &entry.ResourceType, &entry.ResourceID, &details, &entry.SourceIP, &entry.CreatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(details, &entry.Details); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}
func (s *PGStore) IngestHeartbeat(ctx context.Context, token string, h Heartbeat) (HeartbeatResult, error) {
	if err := validateObservedConfig(h); err != nil {
		return HeartbeatResult{}, err
	}
	if err := validateHeartbeatTrafficOrder(h); err != nil {
		return HeartbeatResult{}, err
	}
	if err := validateHAProxyServiceReport(h); err != nil {
		return HeartbeatResult{}, err
	}
	updateReport, hasUpdateReport, err := parseReportedAgentUpdate(h.Metrics)
	if err != nil {
		return HeartbeatResult{}, err
	}
	if _, _, err := parseQuotaRuntime(h.Metrics); err != nil {
		return HeartbeatResult{}, err
	}
	metrics, _ := json.Marshal(h.Metrics)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return HeartbeatResult{}, err
	}
	defer tx.Rollback(ctx)
	identity := h.Credential
	if identity.NodeID == "" {
		identity.NodeID = h.MTLSNodeID
	}
	credential, err := authenticateActiveAgentCredential(ctx, tx, token, identity, true)
	if err != nil {
		return HeartbeatResult{}, err
	}
	nodeID := credential.NodeID
	traffic, hasTraffic, err := parseTrafficSnapshot(h.Metrics)
	if err != nil {
		return HeartbeatResult{}, err
	}
	// Different valid enrollment tokens may exist for one node. Serialize all
	// of them on the node row before timestamping and comparing raw counters.
	var firewallMode string
	if err = tx.QueryRow(ctx, `
		SELECT COALESCE((SELECT mode FROM node_firewall_policies WHERE node_id=nodes.id),'observe')
		FROM nodes WHERE id=$1 FOR UPDATE`, nodeID).Scan(&firewallMode); err != nil {
		return HeartbeatResult{}, err
	}
	var observedAt time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&observedAt); err != nil {
		return HeartbeatResult{}, err
	}
	if h.HAProxyControlGeneration != nil {
		var actualEnabled *bool
		if h.HAProxyServiceState == "active" || h.HAProxyServiceState == "reloading" {
			value := true
			actualEnabled = &value
		} else if h.HAProxyServiceState == "inactive" {
			value := false
			actualEnabled = &value
		}
		_, err = tx.Exec(ctx, `
			UPDATE node_haproxy_control
			SET supported=true,actual_enabled=$2,active_state=$3,report_generation=$4,last_error=$5,reported_at=$6
			WHERE node_id=$1 AND $4<=generation AND $4>=report_generation`,
			nodeID, actualEnabled, h.HAProxyServiceState, *h.HAProxyControlGeneration,
			h.HAProxyControlError, observedAt)
		if err != nil {
			return HeartbeatResult{}, err
		}
	}
	if h.HAProxyRestartGeneration != nil {
		_, err = tx.Exec(ctx, `
			UPDATE node_haproxy_control
			SET restart_report_generation=$2,reported_at=$3
			WHERE node_id=$1 AND $2<=restart_generation AND $2>=restart_report_generation`,
			nodeID, *h.HAProxyRestartGeneration, observedAt)
		if err != nil {
			return HeartbeatResult{}, err
		}
	}
	sampleAccepted, err := acceptHeartbeatTrafficSample(ctx, tx, nodeID, observedAt, h)
	if err != nil {
		return HeartbeatResult{}, err
	}
	status := h.Status
	if status == "" {
		status = "online"
	}
	if sampleAccepted {
		_, err = tx.Exec(ctx, `UPDATE nodes SET status=$2,last_seen_at=$3,updated_at=$3 WHERE id=$1`, nodeID, status, observedAt)
		if err != nil {
			return HeartbeatResult{}, err
		}
	}
	// The reported actual revision and HAProxy counters describe the same Agent
	// snapshot. Reconcile first so a newly activated backend's initial counter is
	// attributed to its immutable actual-revision quota windows.
	snapshotCurrent := false
	if sampleAccepted {
		var reconcileErr error
		snapshotCurrent, reconcileErr = reconcileObservedConfig(ctx, tx, nodeID, observedAt, h)
		if reconcileErr != nil {
			return HeartbeatResult{}, reconcileErr
		}
	}
	if snapshotCurrent {
		if err = upsertLatestHeartbeat(ctx, tx, nodeID, h.Version, status, metrics, h.RoutesOK, observedAt, h); err != nil {
			return HeartbeatResult{}, err
		}
		// Node facts (kernel pipe limits, RAM bucket) feed the renderer;
		// when they no longer match the published route revision, publish a
		// new one so the assignment below already carries it.
		// h.Metrics carries live Go values (uint64, structs), not the
		// float64/map shapes json.Unmarshal produces, so normalise through JSON
		// exactly as the stored heartbeat would be read back.
		if err = republishOnNodeFactsChangeTx(ctx, tx, nodeID, nodeRenderFactsFromMetrics(jsonNormalizedMetrics(h.Metrics))); err != nil {
			return HeartbeatResult{}, err
		}
	}
	if hasTraffic && snapshotCurrent {
		if err = s.recordTrafficSnapshot(ctx, tx, nodeID, observedAt, traffic, h.Metrics, h.ActualRevision); err != nil {
			return HeartbeatResult{}, err
		}
	}
	if sampleAccepted {
		if err = reconcileReportedAgentUpdate(ctx, tx, nodeID, observedAt, updateReport, hasUpdateReport); err != nil {
			return HeartbeatResult{}, err
		}
	}
	result := HeartbeatResult{Status: "accepted", NodeID: nodeID}
	var assignment ConfigAssignment
	err = tx.QueryRow(ctx, `
		SELECT r.revision,r.config,r.sha256
		FROM node_config_state s
		JOIN config_revisions r ON r.node_id=s.node_id AND r.revision=s.desired_revision
		JOIN node_haproxy_control hc ON hc.node_id=s.node_id AND hc.desired_enabled
		WHERE s.node_id=$1
		  AND s.state IN ('pending','applying','drifted')
		  AND (s.desired_revision IS DISTINCT FROM s.actual_revision OR s.state='drifted')
		  AND octet_length(r.config) <= 524288`, nodeID).
		Scan(&assignment.Revision, &assignment.Config, &assignment.SHA256)
	if err == nil {
		if len(assignment.Config) > MaxManagedConfigBytes {
			return HeartbeatResult{}, errors.New("assigned configuration exceeds size limit")
		}
		result.Assignment = &assignment
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return HeartbeatResult{}, err
	}
	quotaAssignment, err := buildQuotaAssignment(ctx, tx, nodeID, observedAt)
	if err != nil {
		return HeartbeatResult{}, err
	}
	result.QuotaAssignment = quotaAssignment
	firewallAssignment, err := buildFirewallAssignment(ctx, tx, nodeID, firewallMode, result.Assignment != nil)
	if err != nil {
		return HeartbeatResult{}, err
	}
	result.FirewallAssignment = firewallAssignment
	updateAssignment, err := buildAgentUpdateAssignment(ctx, tx, nodeID)
	if err != nil {
		return HeartbeatResult{}, err
	}
	result.UpdateAssignment = updateAssignment
	var serviceAssignment HAProxyServiceAssignment
	err = tx.QueryRow(ctx, `
		SELECT generation,desired_enabled,
		       restart_generation,restart_generation>restart_report_generation
		FROM node_haproxy_control
		WHERE node_id=$1 AND desired_enabled
		  AND (generation>report_generation OR restart_generation>restart_report_generation)`, nodeID).
		Scan(&serviceAssignment.Generation, &serviceAssignment.Enabled, &serviceAssignment.RestartGeneration, &serviceAssignment.Restart)
	if err == nil {
		result.ServiceAssignment = &serviceAssignment
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return HeartbeatResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return HeartbeatResult{}, err
	}
	return result, nil
}

func validateHAProxyServiceReport(h Heartbeat) error {
	if h.HAProxyControlGeneration == nil && h.HAProxyRestartGeneration == nil && h.HAProxyServiceState == "" && h.HAProxyControlError == "" {
		return nil
	}
	if h.HAProxyControlGeneration == nil || *h.HAProxyControlGeneration < 0 ||
		(h.HAProxyRestartGeneration != nil && *h.HAProxyRestartGeneration < 0) || len(h.HAProxyControlError) > 200 {
		return ErrInvalidObservedConfig
	}
	switch h.HAProxyServiceState {
	case "active", "reloading", "inactive", "failed", "activating", "deactivating", "unknown":
		return nil
	default:
		return ErrInvalidObservedConfig
	}
}

const upsertLatestHeartbeatSQL = `
	INSERT INTO node_heartbeats(
		node_id,agent_version,status,metrics,routes_ok,received_at,
		traffic_instance_id,traffic_instance_started_at,traffic_sample_seq
	)
	VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
	ON CONFLICT(node_id) DO UPDATE SET
		agent_version=EXCLUDED.agent_version,
		status=EXCLUDED.status,
		metrics=EXCLUDED.metrics,
		routes_ok=EXCLUDED.routes_ok,
		received_at=EXCLUDED.received_at,
		traffic_instance_id=EXCLUDED.traffic_instance_id,
		traffic_instance_started_at=EXCLUDED.traffic_instance_started_at,
		traffic_sample_seq=EXCLUDED.traffic_sample_seq`

func upsertLatestHeartbeat(ctx context.Context, tx pgx.Tx, nodeID, version, status string, metrics []byte, routesOK *bool, receivedAt time.Time, heartbeat Heartbeat) error {
	var instanceID *string
	if heartbeat.TrafficInstanceID != "" {
		value := heartbeat.TrafficInstanceID
		instanceID = &value
	}
	_, err := tx.Exec(
		ctx, upsertLatestHeartbeatSQL,
		nodeID, version, status, metrics, routesOK, receivedAt,
		instanceID, heartbeat.TrafficInstanceStartedAt, heartbeat.TrafficSampleSeq,
	)
	return err
}

type heartbeatTrafficOrderState struct {
	InstanceID *string
	StartedAt  *time.Time
	SampleSeq  *int64
}

func validateHeartbeatTrafficOrder(heartbeat Heartbeat) error {
	legacy := heartbeat.TrafficInstanceID == "" && heartbeat.TrafficInstanceStartedAt == nil && heartbeat.TrafficSampleSeq == nil
	if legacy {
		return nil
	}
	if heartbeat.TrafficInstanceID == "" || heartbeat.TrafficInstanceStartedAt == nil || heartbeat.TrafficSampleSeq == nil ||
		!validID(heartbeat.TrafficInstanceID) || heartbeat.TrafficInstanceID != strings.ToLower(heartbeat.TrafficInstanceID) ||
		*heartbeat.TrafficSampleSeq <= 0 || heartbeat.TrafficInstanceStartedAt.IsZero() {
		return ErrInvalidHeartbeatOrder
	}
	_, offset := heartbeat.TrafficInstanceStartedAt.Zone()
	if offset != 0 {
		return ErrInvalidHeartbeatOrder
	}
	return nil
}

func acceptHeartbeatTrafficSample(ctx context.Context, tx pgx.Tx, nodeID string, observedAt time.Time, heartbeat Heartbeat) (bool, error) {
	if heartbeat.TrafficInstanceStartedAt != nil && heartbeat.TrafficInstanceStartedAt.After(observedAt.UTC().Add(5*time.Minute)) {
		return false, ErrInvalidHeartbeatOrder
	}
	var stored heartbeatTrafficOrderState
	err := tx.QueryRow(ctx, `
		SELECT traffic_instance_id::text,traffic_instance_started_at,traffic_sample_seq
		FROM node_heartbeats WHERE node_id=$1 FOR UPDATE`, nodeID).
		Scan(&stored.InstanceID, &stored.StartedAt, &stored.SampleSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return heartbeatTrafficSampleNewer(stored, heartbeat), nil
}

func heartbeatTrafficSampleNewer(stored heartbeatTrafficOrderState, heartbeat Heartbeat) bool {
	incomingLegacy := heartbeat.TrafficInstanceID == ""
	storedLegacy := stored.InstanceID == nil
	if incomingLegacy {
		return storedLegacy
	}
	if storedLegacy {
		return true
	}
	if heartbeat.TrafficInstanceID == *stored.InstanceID {
		return stored.StartedAt != nil && stored.SampleSeq != nil &&
			heartbeat.TrafficInstanceStartedAt.Equal(stored.StartedAt.UTC()) &&
			*heartbeat.TrafficSampleSeq > *stored.SampleSeq
	}
	return stored.StartedAt != nil && heartbeat.TrafficInstanceStartedAt.After(stored.StartedAt.UTC())
}

func validateObservedConfig(h Heartbeat) error {
	if h.ActualRevision != nil && *h.ActualRevision < 1 {
		return ErrInvalidObservedConfig
	}
	if h.ConfigSHA256 == "" {
		return nil
	}
	if len(h.ConfigSHA256) != sha256.Size*2 {
		return ErrInvalidObservedConfig
	}
	if _, err := hex.DecodeString(h.ConfigSHA256); err != nil {
		return ErrInvalidObservedConfig
	}
	return nil
}

func reconcileObservedConfig(ctx context.Context, tx pgx.Tx, nodeID string, observedAt time.Time, h Heartbeat) (bool, error) {
	if h.ActualRevision == nil && h.ConfigSHA256 == "" {
		return true, nil // compatibility with Agents that predate observed-state heartbeats
	}
	var desired, storedActual *int64
	err := tx.QueryRow(ctx, `
		SELECT desired_revision,actual_revision
		FROM node_config_state
		WHERE node_id=$1
		FOR UPDATE`, nodeID).Scan(&desired, &storedActual)
	if errors.Is(err, pgx.ErrNoRows) || desired == nil {
		return true, nil
	}
	if err != nil {
		return false, err
	}

	verified := false
	if h.ActualRevision != nil && h.ConfigSHA256 != "" {
		err = tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM config_revisions
				WHERE node_id=$1 AND revision=$2 AND lower(sha256)=lower($3)
			)`, nodeID, *h.ActualRevision, h.ConfigSHA256).Scan(&verified)
		if err != nil {
			return false, err
		}
	}

	if verified && *h.ActualRevision == *desired {
		_, err = tx.Exec(ctx, `
			UPDATE node_config_state SET actual_revision=$2,
				state=CASE WHEN `+kernelShaperFailureHold+` THEN state ELSE 'in_sync' END,
				last_error=CASE WHEN `+kernelShaperFailureHold+` THEN last_error ELSE '' END,
				updated_at=$3
			WHERE node_id=$1`, nodeID, *h.ActualRevision, observedAt)
		if err != nil {
			return false, err
		}
		return true, reconcileRoutesToActualRevision(ctx, tx, nodeID, *h.ActualRevision)
	}
	// A delayed heartbeat must not move the immutable actual-revision pointer
	// backwards. An intentional rollback remains possible by first assigning the
	// older revision as desired, which is handled by the branch above.
	if verified && staleObservedRevision(storedActual, *h.ActualRevision) {
		return false, nil
	}
	if verified && storedActual != nil && *storedActual == *h.ActualRevision {
		return true, reconcileRoutesToActualRevision(ctx, tx, nodeID, *h.ActualRevision) // preserve pending/apply failure detail
	}

	if verified {
		_, err = tx.Exec(ctx, `
			UPDATE node_config_state SET actual_revision=$2,state='drifted',last_error='observed_config_drift',updated_at=$3
			WHERE node_id=$1`, nodeID, *h.ActualRevision, observedAt)
		if err == nil {
			err = reconcileRoutesToActualRevision(ctx, tx, nodeID, *h.ActualRevision)
		}
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE node_config_state SET state='drifted',last_error='observed_config_drift',updated_at=$2
			WHERE node_id=$1`, nodeID, observedAt)
	}
	return verified, err
}

func staleObservedRevision(stored *int64, observed int64) bool {
	return stored != nil && observed < *stored
}

func revisionRouteFingerprints(ctx context.Context, tx pgx.Tx, nodeID string, revision int64) (map[string]string, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT metadata FROM config_revisions WHERE node_id=$1 AND revision=$2`, nodeID, revision).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var metadata struct {
		RouteBackends     map[string]json.RawMessage `json:"route_backends"`
		RouteFingerprints map[string]string          `json:"route_fingerprints"`
		RuntimeNames      []struct {
			RouteID string `json:"route_id"`
		} `json:"runtime_names"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, err
	}
	ids := make(map[string]string, len(metadata.RouteBackends)+len(metadata.RuntimeNames))
	for id := range metadata.RouteBackends {
		if validID(id) {
			ids[id] = normalizedRouteFingerprint(metadata.RouteFingerprints[id])
		}
	}
	for _, runtime := range metadata.RuntimeNames {
		if validID(runtime.RouteID) {
			if _, exists := ids[runtime.RouteID]; !exists {
				ids[runtime.RouteID] = normalizedRouteFingerprint(metadata.RouteFingerprints[runtime.RouteID])
			}
		}
	}
	return ids, nil
}

func normalizedRouteFingerprint(value string) string {
	if len(value) != sha256.Size*2 {
		return ""
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(decoded)
}

// reconcileRoutesToActualRevision projects the immutable config ledger back
// into per-route actual state. It always refreshes deployed flags. Desired
// state is completed only when the applied revision is the route's revision or
// a later revision, so superseded revisions cannot finalize newer mutations.
func reconcileRoutesToActualRevision(ctx context.Context, tx pgx.Tx, nodeID string, revision int64) error {
	actualFingerprints, err := revisionRouteFingerprints(ctx, tx, nodeID, revision)
	if err != nil {
		return err
	}
	routes, err := listRoutesTx(ctx, tx, nodeID)
	if err != nil {
		return err
	}
	for _, route := range routes {
		deployedFingerprint, included := actualFingerprints[route.ID]
		decision := routeActualDecisionFor(route, revision, included)
		if decision.Delete {
			if _, err = tx.Exec(ctx, `DELETE FROM routes WHERE node_id=$1 AND id=$2`, nodeID, route.ID); err != nil {
				return err
			}
			continue
		}
		if decision.ResolveIntent {
			if _, err = tx.Exec(ctx, `
				UPDATE routes SET deployed=$3,applied_revision=$4,deployment_state=$5,deployment_error=$6,
					deployed_fingerprint=$7
				WHERE node_id=$1 AND id=$2`, nodeID, route.ID, included, revision, decision.State, decision.Error, deployedFingerprint); err != nil {
				return err
			}
			continue
		}
		if _, err = tx.Exec(ctx, `
			UPDATE routes SET deployed=$3,applied_revision=$4,deployed_fingerprint=$5
			WHERE node_id=$1 AND id=$2`, nodeID, route.ID, included, revision, deployedFingerprint); err != nil {
			return err
		}
	}
	return nil
}

type routeActualDecision struct {
	Delete        bool
	ResolveIntent bool
	State         string
	Error         string
}

func routeActualDecisionFor(route Route, revision int64, included bool) routeActualDecision {
	if route.DesiredRevision == nil || revision < *route.DesiredRevision {
		return routeActualDecision{}
	}
	if route.DeletePending && !included {
		return routeActualDecision{Delete: true}
	}
	decision := routeActualDecision{ResolveIntent: true, State: "failed", Error: "observed_route_state_mismatch"}
	switch {
	case route.Enabled && included && !route.DeletePending:
		decision.State, decision.Error = "active", ""
	case !route.Enabled && !included && !route.DeletePending:
		decision.State, decision.Error = "disabled", ""
	}
	return decision
}

func markRouteRevisionFailed(ctx context.Context, tx pgx.Tx, nodeID string, revision int64, code string) error {
	if code == "" {
		code = "apply_failed"
	}
	_, err := tx.Exec(ctx, `
		UPDATE routes SET deployment_state='failed',deployment_error=$3
		WHERE node_id=$1 AND desired_revision=$2`, nodeID, revision, code)
	return err
}

const configRevisionColumns = `id::text,node_id::text,revision,config,sha256,note,metadata,created_by,created_at`
const configRevisionListColumns = `id::text,node_id::text,revision,''::text AS config,sha256,note,metadata,created_by,created_at`

func scanConfigRevision(row pgx.Row) (ConfigRevision, error) {
	var v ConfigRevision
	var metadata []byte
	err := row.Scan(&v.ID, &v.NodeID, &v.Revision, &v.Config, &v.SHA256, &v.Note, &metadata, &v.CreatedBy, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(metadata, &v.Metadata)
	}
	return v, err
}

func (s *PGStore) CreateConfigRevision(ctx context.Context, nodeID, config, note string, metadata map[string]any) (ConfigRevision, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ConfigRevision{}, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT true FROM nodes WHERE id=$1 FOR UPDATE`, nodeID).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
		return ConfigRevision{}, ErrNotFound
	} else if err != nil {
		return ConfigRevision{}, err
	}
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(max(revision),0)+1 FROM config_revisions WHERE node_id=$1`, nodeID).Scan(&revision); err != nil {
		return ConfigRevision{}, err
	}
	sum := sha256.Sum256([]byte(config))
	metadataJSON, _ := json.Marshal(metadata)
	v, err := scanConfigRevision(tx.QueryRow(ctx, `INSERT INTO config_revisions(node_id,revision,config,sha256,note,metadata,created_by) VALUES($1,$2,$3,$4,$5,$6,'admin') RETURNING `+configRevisionColumns, nodeID, revision, config, hex.EncodeToString(sum[:]), note, metadataJSON))
	if err != nil {
		return ConfigRevision{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO node_config_state(node_id) VALUES($1) ON CONFLICT (node_id) DO NOTHING`, nodeID); err != nil {
		return ConfigRevision{}, err
	}
	return v, tx.Commit(ctx)
}

func (s *PGStore) ListConfigRevisions(ctx context.Context, nodeID string) ([]ConfigRevision, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+configRevisionListColumns+` FROM config_revisions WHERE node_id=$1 ORDER BY revision DESC LIMIT 100`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ConfigRevision, 0)
	for rows.Next() {
		v, err := scanConfigRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *PGStore) GetConfigRevision(ctx context.Context, nodeID string, revision int64) (ConfigRevision, error) {
	return scanConfigRevision(s.pool.QueryRow(ctx, `SELECT `+configRevisionColumns+` FROM config_revisions WHERE node_id=$1 AND revision=$2`, nodeID, revision))
}

func scanConfigState(row pgx.Row) (NodeConfigState, error) {
	var v NodeConfigState
	err := row.Scan(&v.NodeID, &v.DesiredRevision, &v.ActualRevision, &v.State, &v.LastError, &v.LastReportAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

func (s *PGStore) AssignDesiredRevision(ctx context.Context, nodeID string, revision int64) (NodeConfigState, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return NodeConfigState{}, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM nodes WHERE id=$1 FOR UPDATE`, nodeID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return NodeConfigState{}, ErrNotFound
		}
		return NodeConfigState{}, err
	}
	state, err := scanConfigState(tx.QueryRow(ctx, `
		INSERT INTO node_config_state(node_id,desired_revision,state,updated_at)
		SELECT $1,$2,CASE WHEN s.actual_revision=$2 THEN 'in_sync' ELSE 'pending' END,now()
		FROM config_revisions r LEFT JOIN node_config_state s ON s.node_id=r.node_id
		WHERE r.node_id=$1 AND r.revision=$2
		ON CONFLICT (node_id) DO UPDATE SET desired_revision=EXCLUDED.desired_revision,
		state=CASE WHEN node_config_state.actual_revision=EXCLUDED.desired_revision THEN 'in_sync' ELSE 'pending' END,
		last_error='',updated_at=now()
		RETURNING node_id::text,desired_revision,actual_revision,state,last_error,last_report_at,updated_at`, nodeID, revision))
	if err != nil {
		return NodeConfigState{}, err
	}
	return state, tx.Commit(ctx)
}

func (s *PGStore) GetConfigState(ctx context.Context, nodeID string) (NodeConfigState, error) {
	return scanConfigState(s.pool.QueryRow(ctx, `SELECT node_id::text,desired_revision,actual_revision,state,last_error,last_report_at,updated_at FROM node_config_state WHERE node_id=$1`, nodeID))
}

// ListConfigStates returns config state rows for many nodes in one query,
// keyed by node ID. Nodes without a state row are absent from the map.
func (s *PGStore) ListConfigStates(ctx context.Context, nodeIDs []string) (map[string]NodeConfigState, error) {
	out := make(map[string]NodeConfigState, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT node_id::text,desired_revision,actual_revision,state,last_error,last_report_at,updated_at
		FROM node_config_state WHERE node_id = ANY($1::uuid[])`, nodeIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		state, err := scanConfigState(rows)
		if err != nil {
			return nil, err
		}
		out[state.NodeID] = state
	}
	return out, rows.Err()
}

func (s *PGStore) IngestApplyReport(ctx context.Context, token string, report ApplyReport) (NodeConfigState, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return NodeConfigState{}, err
	}
	defer tx.Rollback(ctx)
	identity := report.Credential
	if identity.NodeID == "" {
		identity.NodeID = report.MTLSNodeID
	}
	credential, err := authenticateActiveAgentCredential(ctx, tx, token, identity, true)
	if err != nil {
		return NodeConfigState{}, err
	}
	nodeID := credential.NodeID
	// Serialize Agent reports with route mutations and with each other. Without
	// the same node lock, a late report could overwrite a newer desired/actual
	// pair after the route transaction had already published it.
	if err = lockRouteNode(ctx, tx, nodeID); err != nil {
		return NodeConfigState{}, err
	}
	var currentDesired, currentActual *int64
	var currentState, currentError string
	err = tx.QueryRow(ctx, `
		SELECT desired_revision,actual_revision,state,last_error
		FROM node_config_state WHERE node_id=$1 FOR UPDATE`, nodeID).
		Scan(&currentDesired, &currentActual, &currentState, &currentError)
	if errors.Is(err, pgx.ErrNoRows) {
		return NodeConfigState{}, ErrNotFound
	}
	if err != nil {
		return NodeConfigState{}, err
	}
	var storedActual *int64
	if report.State == "applied" {
		storedActual = &report.Revision
	} else {
		storedActual = report.ActualRevision
	}
	addedObservedRevision := false
	if storedActual != nil && report.State != "applied" {
		var known bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM config_revisions WHERE node_id=$1 AND revision=$2)`, nodeID, *storedActual).Scan(&known); err != nil {
			return NodeConfigState{}, err
		}
		if !known {
			details := make(map[string]any, len(report.Details)+1)
			for key, value := range report.Details {
				details[key] = value
			}
			details["unrecognized_actual_revision"] = *storedActual
			report.Details = details
			addedObservedRevision = true
			storedActual = nil
		}
	}
	if report.Details == nil {
		report.Details = map[string]any{}
	}
	details, _ := json.Marshal(report.Details)
	if addedObservedRevision && len(details) > MaxReportDetailsBytes {
		delete(report.Details, "unrecognized_actual_revision")
		details, _ = json.Marshal(report.Details)
	}
	_, err = tx.Exec(ctx, `INSERT INTO config_apply_reports(node_id,revision,state,actual_revision,error,rollback_attempted,rollback_succeeded,details) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, nodeID, report.Revision, report.State, storedActual, report.Error, report.RollbackAttempted, report.RollbackSucceeded, details)
	if err != nil {
		return NodeConfigState{}, err
	}
	reportIsCurrent := currentDesired != nil && report.Revision == *currentDesired
	effectiveActual := currentActual
	acceptedObservedActual := false
	if storedActual != nil && (currentActual == nil || *storedActual >= *currentActual) {
		actual := *storedActual
		effectiveActual = &actual
		acceptedObservedActual = currentActual == nil || actual != *currentActual
	}
	state, stateError := currentState, currentError
	if reportIsCurrent {
		switch report.State {
		case "applied":
			if effectiveActual != nil && *effectiveActual == report.Revision {
				state, stateError = "in_sync", ""
			} else {
				state, stateError = "drifted", "observed_config_drift"
			}
		case "applying":
			state, stateError = "applying", ""
		case "failed", "rolled_back":
			state, stateError = report.State, report.Error
		}
	}
	v, err := scanConfigState(tx.QueryRow(ctx, `
		UPDATE node_config_state SET
			actual_revision=$2,state=$3,last_error=$4,
			last_report_at=CASE WHEN $5 THEN now() ELSE last_report_at END,
			updated_at=CASE WHEN $5 OR actual_revision IS DISTINCT FROM $2 THEN now() ELSE updated_at END
		WHERE node_id=$1
		RETURNING node_id::text,desired_revision,actual_revision,state,last_error,last_report_at,updated_at`,
		nodeID, effectiveActual, state, stateError, reportIsCurrent))
	if err != nil {
		return NodeConfigState{}, err
	}
	if effectiveActual != nil && (acceptedObservedActual || reportIsCurrent) {
		if err = reconcileRoutesToActualRevision(ctx, tx, nodeID, *effectiveActual); err != nil {
			return NodeConfigState{}, err
		}
	}
	if reportIsCurrent && (report.State == "failed" || report.State == "rolled_back") {
		if err = markRouteRevisionFailed(ctx, tx, nodeID, report.Revision, report.Error); err != nil {
			return NodeConfigState{}, err
		}
	}
	return v, tx.Commit(ctx)
}
