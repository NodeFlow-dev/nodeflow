package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	trafficScopeNode       = "node"
	trafficScopeBackend    = "backend"
	maxTrafficBackends     = 1024
	maxQuotaPolicies       = 1024
	maxTrafficProxyNameLen = 128
	maxExactFloatInteger   = 1<<53 - 1
	stableBackendPrefix    = "nf_be_"
	legacyBackendPrefix    = "bc_be_"
	legacyServerPrefix     = "bc_srv_"
)

var (
	ErrInvalidTrafficMetrics  = errors.New("invalid HAProxy traffic metrics")
	errTrafficCounterOverflow = errors.New("traffic counter overflow")
)

type trafficCounter struct {
	BytesIn  int64
	BytesOut int64
}

type trafficRate struct {
	RXBytesPerSecond float64
	TXBytesPerSecond float64
	Valid            bool
}

type actualTrafficBinding struct {
	RouteID string
	Anchor  time.Time
}

type trafficSnapshot struct {
	Generation string
	Node       trafficCounter
	Backends   map[string]trafficCounter
}

type TrafficBackend struct {
	BackendKey string  `json:"backend_key"`
	RouteID    *string `json:"route_id,omitempty"`
	BytesIn    int64   `json:"bytes_in"`
	BytesOut   int64   `json:"bytes_out"`
	UsedBytes  int64   `json:"used_bytes"`
}

type TrafficRoute struct {
	RouteID          string     `json:"route_id"`
	BackendKey       string     `json:"backend_key"`
	BytesIn          int64      `json:"bytes_in"`
	BytesOut         int64      `json:"bytes_out"`
	UsedBytes        int64      `json:"used_bytes"`
	QuotaUsedBytes   int64      `json:"quota_used_bytes"`
	LimitBytes       *int64     `json:"limit_bytes"`
	QuotaPeriod      string     `json:"quota_period"`
	QuotaWindowStart *time.Time `json:"quota_window_start,omitempty"`
	QuotaWindowEnd   *time.Time `json:"quota_window_end,omitempty"`
	Reached          bool       `json:"reached"`
	QuotaAction      string     `json:"quota_action"`
	Enforcement      bool       `json:"enforcement"`
	BlockRequested   bool       `json:"block_requested"`
	Blocked          bool       `json:"blocked"`
	Observed         bool       `json:"observed"`
	Applied          bool       `json:"applied"`
	RXBitsPerSecond  *float64   `json:"rx_bits_per_second,omitempty"`
	TXBitsPerSecond  *float64   `json:"tx_bits_per_second,omitempty"`
	RateSampledAt    *time.Time `json:"rate_sampled_at,omitempty"`
}

type currentRouteRate struct {
	RXBitsPerSecond float64
	TXBitsPerSecond float64
	SampledAt       time.Time
}

type quotaWindow struct {
	Period string
	Start  time.Time
	End    time.Time
}

type quotaUsage struct {
	Window   quotaWindow
	BytesIn  int64
	BytesOut int64
}

type quotaWindowDelta struct {
	Window quotaWindow
	Delta  trafficCounter
}

type NodeTrafficReport struct {
	NodeID      string           `json:"node_id"`
	Month       string           `json:"month"`
	BytesIn     int64            `json:"bytes_in"`
	BytesOut    int64            `json:"bytes_out"`
	UsedBytes   int64            `json:"used_bytes"`
	Enforcement bool             `json:"enforcement"`
	Backends    []TrafficBackend `json:"backends"`
	Routes      []TrafficRoute   `json:"routes"`
}

// RouteBackendKey is the stable HAProxy backend name shared with the renderer.
// UUID hyphens are removed so the name remains short and HAProxy-safe.
func RouteBackendKey(routeID string) string {
	return stableBackendPrefix + routeRuntimeID(routeID)
}

func RouteServerKey(routeID string) string {
	return "nf_srv_" + routeRuntimeID(routeID)
}

func routeRuntimeID(routeID string) string {
	compact := strings.ReplaceAll(strings.ToLower(routeID), "-", "")
	if len(compact) > 12 {
		return compact[:12]
	}
	return compact
}

func legacyRouteBackendKey(routeID string) string {
	return legacyBackendPrefix + strings.ReplaceAll(strings.ToLower(routeID), "-", "")
}

func legacyRouteServerKey(routeID string) string {
	return legacyServerPrefix + strings.ReplaceAll(strings.ToLower(routeID), "-", "")
}

func longRouteBackendKey(routeID string) string {
	return stableBackendPrefix + strings.ReplaceAll(strings.ToLower(routeID), "-", "")
}

func longRouteServerKey(routeID string) string {
	return "nf_srv_" + strings.ReplaceAll(strings.ToLower(routeID), "-", "")
}

func validRouteRuntimeNames(routeID, backend, server string) bool {
	return (backend == RouteBackendKey(routeID) && server == RouteServerKey(routeID)) ||
		(backend == RouteBackendKey(routeID) && server == routeServerTemplatePrefix(routeID)+"1") ||
		(backend == longRouteBackendKey(routeID) && server == longRouteServerKey(routeID)) ||
		(backend == legacyRouteBackendKey(routeID) && server == legacyRouteServerKey(routeID))
}

func quotaRuntimeKey(routeID string) string {
	return RouteBackendKey(routeID) + "/" + RouteServerKey(routeID)
}

func parseQuotaRuntime(metrics map[string]any) (map[string]bool, bool, error) {
	raw, present := metrics["quota_runtime"]
	if !present || raw == nil {
		return map[string]bool{}, false, nil
	}
	values, ok := raw.(map[string]any)
	if !ok || len(values) > maxQuotaPolicies {
		return nil, true, ErrInvalidTrafficMetrics
	}
	result := make(map[string]bool, len(values))
	for key, rawValue := range values {
		blocked, ok := rawValue.(bool)
		if !ok || !validQuotaRuntimeKey(key) {
			return nil, true, ErrInvalidTrafficMetrics
		}
		result[key] = blocked
	}
	return result, true, nil
}

func validQuotaRuntimeKey(key string) bool {
	backend, server, ok := strings.Cut(key, "/")
	if !ok || strings.Contains(server, "/") {
		return false
	}
	backendPrefix, serverPrefix := stableBackendPrefix, "nf_srv_"
	if strings.HasPrefix(backend, legacyBackendPrefix) && strings.HasPrefix(server, legacyServerPrefix) {
		backendPrefix, serverPrefix = legacyBackendPrefix, legacyServerPrefix
	} else if !strings.HasPrefix(backend, backendPrefix) || !strings.HasPrefix(server, serverPrefix) {
		return false
	}
	backendID := strings.TrimPrefix(backend, backendPrefix)
	serverID := strings.TrimPrefix(server, serverPrefix)
	if backendID != serverID || (len(backendID) != 12 && len(backendID) != 32) {
		return false
	}
	for _, value := range backendID {
		if !((value >= '0' && value <= '9') || (value >= 'a' && value <= 'f')) {
			return false
		}
	}
	return true
}

func buildQuotaAssignment(ctx context.Context, tx pgx.Tx, nodeID string, observedAt time.Time) (*QuotaAssignment, error) {
	month := trafficMonth(observedAt)
	rows, err := tx.Query(ctx, `
		SELECT runtime.route_id,runtime.backend,runtime.server,
		       COALESCE(runtime.dns_pool,false) OR COALESCE(runtime.multi_server,false),
		       COALESCE(runtime.quota_action,'observe'),runtime.quota_bytes,
		       COALESCE(runtime.quota_period,'calendar_month'),
		       COALESCE(runtime.quota_anchor_at,revision.created_at),
		       traffic.window_start,traffic.window_end,
		       COALESCE(traffic.bytes_in,0),COALESCE(traffic.bytes_out,0)
		FROM node_config_state AS state
		JOIN config_revisions AS revision
		  ON revision.node_id=state.node_id AND revision.revision=state.actual_revision
		CROSS JOIN LATERAL jsonb_to_recordset(
		  CASE WHEN jsonb_typeof(revision.metadata->'runtime_names')='array'
		       THEN revision.metadata->'runtime_names' ELSE '[]'::jsonb END
		) AS runtime(
		  route_id text,frontend text,backend text,server text,dns_pool boolean,multi_server boolean,
		  quota_bytes bigint,quota_action text,quota_period text,quota_anchor_at timestamptz
		)
		LEFT JOIN traffic_quota_usage AS traffic
		  ON traffic.node_id=state.node_id
		 AND traffic.period=COALESCE(runtime.quota_period,'calendar_month')
		 AND traffic.proxy_name=runtime.backend
		 AND traffic.window_start <= $2 AND traffic.window_end > $2
		WHERE state.node_id=$1 AND state.actual_revision IS NOT NULL
		  AND revision.metadata->>'renderer' = ANY($3::text[])
		ORDER BY runtime.route_id`, nodeID, observedAt.UTC(), supportedHAProxyRenderers())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	assignment := &QuotaAssignment{Month: month.Format("2006-01"), Policies: []QuotaBackendPolicy{}}
	seen := make(map[string]struct{})
	for rows.Next() {
		var routeID, backend, server, action, period string
		var dnsPool bool
		var limit *int64
		var anchor time.Time
		var windowStart, windowEnd *time.Time
		var bytesIn, bytesOut int64
		if err = rows.Scan(
			&routeID, &backend, &server, &dnsPool, &action, &limit, &period, &anchor,
			&windowStart, &windowEnd, &bytesIn, &bytesOut,
		); err != nil {
			return nil, err
		}
		if !validID(routeID) || routeID != strings.ToLower(routeID) || !validRouteRuntimeNames(routeID, backend, server) {
			continue
		}
		if dnsPool {
			continue
		}
		if _, exists := seen[routeID]; exists {
			continue
		}
		seen[routeID] = struct{}{}
		if len(assignment.Policies) >= maxQuotaPolicies {
			return nil, errors.New("quota policy count exceeds limit")
		}
		used := trafficUsedBytes(bytesIn, bytesOut)
		if action != "observe" && action != "block_new" {
			action = "observe"
		}
		if limit != nil && *limit <= 0 {
			limit = nil
			action = "observe"
		}
		enforced := action == "block_new" && limit != nil
		if !enforced {
			action = "observe"
		}
		if !validQuotaPeriod(period) {
			period = defaultQuotaPeriod
		}
		window, windowErr := quotaWindowAt(period, anchor, observedAt)
		if windowErr != nil {
			return nil, windowErr
		}
		if windowStart != nil && windowEnd != nil {
			window.Start, window.End = windowStart.UTC(), windowEnd.UTC()
		}
		assignment.Policies = append(assignment.Policies, QuotaBackendPolicy{
			RouteID: routeID, Backend: backend, Server: server,
			Action: action, Block: enforced && used >= *limit, UsedBytes: used, LimitBytes: limit,
			QuotaPeriod: period, WindowStart: window.Start, WindowEnd: window.End,
		})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(assignment.Policies) == 0 {
		return nil, nil
	}
	return assignment, nil
}

func parseTrafficSnapshot(metrics map[string]any) (trafficSnapshot, bool, error) {
	rawRuntime, present := metrics["haproxy_runtime"]
	if !present || rawRuntime == nil {
		return trafficSnapshot{}, false, nil
	}
	runtimeMetrics, ok := rawRuntime.(map[string]any)
	if !ok {
		return trafficSnapshot{}, true, ErrInvalidTrafficMetrics
	}
	node, err := parseTrafficCounter(runtimeMetrics)
	if err != nil {
		return trafficSnapshot{}, true, err
	}
	generation := ""
	if rawGeneration, exists := runtimeMetrics["counter_generation"]; exists {
		var valid bool
		generation, valid = rawGeneration.(string)
		if !valid || !validTrafficGeneration(generation) {
			return trafficSnapshot{}, true, ErrInvalidTrafficMetrics
		}
	}
	snapshot := trafficSnapshot{Generation: generation, Node: node, Backends: make(map[string]trafficCounter)}
	rawBackends, present := runtimeMetrics["backends"]
	if !present || rawBackends == nil {
		return snapshot, true, nil
	}
	backends, ok := rawBackends.(map[string]any)
	if !ok || len(backends) > maxTrafficBackends {
		return trafficSnapshot{}, true, ErrInvalidTrafficMetrics
	}
	for name, raw := range backends {
		if !validTrafficProxyName(name) {
			return trafficSnapshot{}, true, ErrInvalidTrafficMetrics
		}
		fields, ok := raw.(map[string]any)
		if !ok {
			return trafficSnapshot{}, true, ErrInvalidTrafficMetrics
		}
		counter, err := parseTrafficCounter(fields)
		if err != nil {
			return trafficSnapshot{}, true, err
		}
		snapshot.Backends[name] = counter
	}
	return snapshot, true, nil
}

func parseTrafficCounter(fields map[string]any) (trafficCounter, error) {
	in, ok := trafficInteger(fields["bytes_in"])
	if !ok {
		return trafficCounter{}, ErrInvalidTrafficMetrics
	}
	out, ok := trafficInteger(fields["bytes_out"])
	if !ok {
		return trafficCounter{}, ErrInvalidTrafficMetrics
	}
	return trafficCounter{BytesIn: in, BytesOut: out}, nil
}

func trafficInteger(value any) (int64, bool) {
	switch v := value.(type) {
	case json.Number:
		n, err := v.Int64()
		return n, err == nil && n >= 0
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > maxExactFloatInteger || math.Trunc(v) != v {
			return 0, false
		}
		return int64(v), true
	case int:
		return int64(v), v >= 0
	case int64:
		return v, v >= 0
	case uint64:
		return int64(v), v <= math.MaxInt64
	default:
		return 0, false
	}
}

func validTrafficProxyName(name string) bool {
	return name != "" && len(name) <= maxTrafficProxyNameLen && utf8.ValidString(name) &&
		!strings.ContainsRune(name, '\x00') && !strings.ContainsFunc(name, unicode.IsControl)
}

func validTrafficGeneration(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > maxTrafficProxyNameLen {
		return false
	}
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		if _, err := strconv.ParseUint(part, 10, 64); err != nil {
			return false
		}
	}
	return true
}

func nextTrafficDelta(previous *trafficCounter, current trafficCounter) trafficCounter {
	if previous == nil {
		return current
	}
	return trafficCounter{
		BytesIn:  resetSafeDelta(previous.BytesIn, current.BytesIn),
		BytesOut: resetSafeDelta(previous.BytesOut, current.BytesOut),
	}
}

func resetSafeDelta(previous, current int64) int64 {
	if current < previous {
		return current
	}
	return current - previous
}

func rateBetweenTrafficCounters(previous *trafficCounter, previousAt *time.Time, current trafficCounter, observedAt time.Time) trafficRate {
	if previous == nil || previousAt == nil || current.BytesIn < previous.BytesIn || current.BytesOut < previous.BytesOut {
		return trafficRate{}
	}
	seconds := observedAt.Sub(previousAt.UTC()).Seconds()
	if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return trafficRate{}
	}
	rx := float64(current.BytesIn-previous.BytesIn) / seconds
	tx := float64(current.BytesOut-previous.BytesOut) / seconds
	if math.IsNaN(rx) || math.IsInf(rx, 0) || math.IsNaN(tx) || math.IsInf(tx, 0) {
		return trafficRate{}
	}
	return trafficRate{RXBytesPerSecond: rx, TXBytesPerSecond: tx, Valid: true}
}

func trafficCounterTransition(previous *trafficCounter, previousAt *time.Time, previousGeneration, generation string, current trafficCounter, observedAt time.Time) (trafficCounter, trafficRate) {
	previousHAProxyGeneration := haproxyCounterGeneration(previousGeneration)
	currentHAProxyGeneration := haproxyCounterGeneration(generation)
	generationChanged := previous != nil && currentHAProxyGeneration != "" && previousHAProxyGeneration != "" && currentHAProxyGeneration != previousHAProxyGeneration
	generationEstablished := previous != nil && currentHAProxyGeneration != "" && previousHAProxyGeneration == ""
	rate := trafficRate{}
	if !generationChanged && !generationEstablished {
		rate = rateBetweenTrafficCounters(previous, previousAt, current, observedAt)
	}
	deltaPrevious := previous
	if generationChanged {
		deltaPrevious = nil
	}
	return nextTrafficDelta(deltaPrevious, current), rate
}

// source_generation also records the immutable config revision that mapped a
// HAProxy backend to a route. A revision change alone is not a counter reset:
// seamless reloads can preserve cumulative counters. Only the HAProxy process
// generation prefix may force a new cumulative-counter baseline.
func haproxyCounterGeneration(source string) string {
	if strings.HasPrefix(source, "revision:") {
		return ""
	}
	if index := strings.Index(source, "@revision:"); index >= 0 {
		return source[:index]
	}
	return source
}

func trafficSourceGeneration(counterGeneration string, actualRevision *int64) string {
	if actualRevision == nil {
		return counterGeneration
	}
	revision := strconv.FormatInt(*actualRevision, 10)
	if counterGeneration == "" {
		return "revision:" + revision
	}
	return counterGeneration + "@revision:" + revision
}

func trafficMonth(at time.Time) time.Time {
	at = at.UTC()
	return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func trafficDay(at time.Time) time.Time {
	at = at.UTC()
	return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
}

// quotaWindowAt returns a UTC half-open [start,end) interval. Anniversary
// months always clamp from the original anchor day, never from the previous
// clamped boundary (31 Jan -> 28/29 Feb -> 31 Mar).
func quotaWindowAt(period string, anchor, at time.Time) (quotaWindow, error) {
	at = at.UTC()
	anchor = anchor.UTC()
	switch period {
	case "hourly":
		start := at.Truncate(time.Hour)
		return quotaWindow{Period: period, Start: start, End: start.Add(time.Hour)}, nil
	case "daily":
		start := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
		return quotaWindow{Period: period, Start: start, End: start.AddDate(0, 0, 1)}, nil
	case "calendar_month":
		start := trafficMonth(at)
		return quotaWindow{Period: period, Start: start, End: start.AddDate(0, 1, 0)}, nil
	case "monthly_from_creation":
		if anchor.IsZero() {
			return quotaWindow{}, errors.New("monthly_from_creation quota requires an anchor")
		}
		months := (at.Year()-anchor.Year())*12 + int(at.Month()-anchor.Month())
		start := addMonthsClamped(anchor, months)
		if at.Before(start) {
			months--
			start = addMonthsClamped(anchor, months)
		}
		end := addMonthsClamped(anchor, months+1)
		return quotaWindow{Period: period, Start: start, End: end}, nil
	default:
		return quotaWindow{}, errors.New("invalid quota period")
	}
}

func addMonthsClamped(anchor time.Time, months int) time.Time {
	anchor = anchor.UTC()
	monthStart := time.Date(anchor.Year(), anchor.Month()+time.Month(months), 1,
		anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), time.UTC)
	day := anchor.Day()
	lastDay := time.Date(monthStart.Year(), monthStart.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(monthStart.Year(), monthStart.Month(), day,
		anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), time.UTC)
}

// splitQuotaDelta spreads a monotonic counter delta uniformly over each UTC
// half-open quota window intersecting [start,end). Cumulative integer division
// keeps every part deterministic while preserving the exact byte total.
func splitQuotaDelta(period string, anchor, start, end time.Time, delta trafficCounter) ([]quotaWindowDelta, error) {
	start, end = start.UTC(), end.UTC()
	if !end.After(start) {
		window, err := quotaWindowAt(period, anchor, end)
		if err != nil {
			return nil, err
		}
		return []quotaWindowDelta{{Window: window, Delta: delta}}, nil
	}
	totalDuration := end.Sub(start)
	cursor := start
	elapsed := time.Duration(0)
	assigned := trafficCounter{}
	result := make([]quotaWindowDelta, 0, 2)
	for cursor.Before(end) {
		window, err := quotaWindowAt(period, anchor, cursor)
		if err != nil {
			return nil, err
		}
		segmentEnd := window.End
		if segmentEnd.After(end) {
			segmentEnd = end
		}
		if !segmentEnd.After(cursor) {
			return nil, errors.New("invalid quota window boundary")
		}
		elapsed += segmentEnd.Sub(cursor)
		cumulative := trafficCounter{
			BytesIn:  proportionalTrafficTotal(delta.BytesIn, elapsed, totalDuration),
			BytesOut: proportionalTrafficTotal(delta.BytesOut, elapsed, totalDuration),
		}
		result = append(result, quotaWindowDelta{
			Window: window,
			Delta: trafficCounter{
				BytesIn:  cumulative.BytesIn - assigned.BytesIn,
				BytesOut: cumulative.BytesOut - assigned.BytesOut,
			},
		})
		assigned = cumulative
		cursor = segmentEnd
	}
	return result, nil
}

func proportionalTrafficTotal(value int64, elapsed, total time.Duration) int64 {
	if value <= 0 || elapsed <= 0 {
		return 0
	}
	if elapsed >= total {
		return value
	}
	high, low := bits.Mul64(uint64(value), uint64(elapsed))
	quotient, _ := bits.Div64(high, low, uint64(total))
	return int64(quotient)
}

func quotaDeltas(anchor, observedAt time.Time, intervalStart *time.Time, split bool, delta trafficCounter) ([]quotaWindowDelta, error) {
	periods := []string{"hourly", "daily", "calendar_month"}
	if !anchor.IsZero() {
		periods = append(periods, "monthly_from_creation")
	}
	result := make([]quotaWindowDelta, 0, len(periods))
	for _, period := range periods {
		if split && intervalStart != nil {
			parts, err := splitQuotaDelta(period, anchor, *intervalStart, observedAt, delta)
			if err != nil {
				return nil, err
			}
			result = append(result, parts...)
			continue
		}
		window, err := quotaWindowAt(period, anchor, observedAt)
		if err != nil {
			return nil, err
		}
		result = append(result, quotaWindowDelta{Window: window, Delta: delta})
	}
	return result, nil
}

func canSplitQuotaDelta(previous *trafficCounter, previousAt *time.Time, previousGeneration, generation string, current trafficCounter, observedAt time.Time) bool {
	previousHAProxyGeneration := haproxyCounterGeneration(previousGeneration)
	currentHAProxyGeneration := haproxyCounterGeneration(generation)
	generationChanged := previousHAProxyGeneration != "" && currentHAProxyGeneration != "" && previousHAProxyGeneration != currentHAProxyGeneration
	return previous != nil && previousAt != nil && observedAt.After(previousAt.UTC()) &&
		current.BytesIn >= previous.BytesIn && current.BytesOut >= previous.BytesOut &&
		!generationChanged
}

func (s *PGStore) recordTrafficSnapshot(ctx context.Context, tx pgx.Tx, nodeID string, observedAt time.Time, snapshot trafficSnapshot, metrics map[string]any, actualRevision *int64) error {
	bindings, err := actualTrafficBindings(ctx, tx, nodeID)
	if err != nil {
		return err
	}
	sourceGeneration := trafficSourceGeneration(snapshot.Generation, actualRevision)
	nodeRate, err := s.recordTrafficCounter(ctx, tx, nodeID, trafficScopeNode, "", sourceGeneration, observedAt, snapshot.Node, nil)
	if err != nil {
		return err
	}
	routeRates := make(map[string]trafficRate, len(bindings))
	names := make([]string, 0, len(snapshot.Backends))
	for name := range snapshot.Backends {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		binding, managed := bindings[name]
		var anchorPtr *time.Time
		if managed {
			anchorPtr = &binding.Anchor
		}
		rate, counterErr := s.recordTrafficCounter(ctx, tx, nodeID, trafficScopeBackend, name, sourceGeneration, observedAt, snapshot.Backends[name], anchorPtr)
		if counterErr != nil {
			return counterErr
		}
		if managed && rate.Valid {
			routeRates[binding.RouteID] = rate
		}
	}
	if !nodeRate.Valid {
		// The node aggregate is the generation guard for the snapshot. If it
		// reset, do not publish backend rates even if one backend counter happened
		// to remain monotonic across the reload.
		routeRates = nil
	}
	if err = recordHAProxyRateSamples(ctx, tx, nodeID, observedAt, nodeRate, routeRates, metrics); err != nil {
		return err
	}
	return nil
}

func actualTrafficBindings(ctx context.Context, tx pgx.Tx, nodeID string) (map[string]actualTrafficBinding, error) {
	rows, err := tx.Query(ctx, `
		SELECT runtime.route_id,COALESCE(runtime.backend,''),runtime.quota_anchor_at
		FROM node_config_state AS state
		JOIN config_revisions AS revision
		  ON revision.node_id=state.node_id AND revision.revision=state.actual_revision
		CROSS JOIN LATERAL jsonb_to_recordset(
		  CASE WHEN jsonb_typeof(revision.metadata->'runtime_names')='array'
		       THEN revision.metadata->'runtime_names' ELSE '[]'::jsonb END
		) AS runtime(route_id text,backend text,quota_anchor_at timestamptz)
		WHERE state.node_id=$1 AND state.actual_revision IS NOT NULL
		  AND revision.metadata->>'renderer' = ANY($2::text[])
		ORDER BY runtime.route_id`, nodeID, supportedHAProxyRenderers())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bindings := make(map[string]actualTrafficBinding)
	for rows.Next() {
		var routeID, backend string
		var anchor *time.Time
		if err = rows.Scan(&routeID, &backend, &anchor); err != nil {
			return nil, err
		}
		if !validID(routeID) || routeID != strings.ToLower(routeID) {
			continue
		}
		expected := RouteBackendKey(routeID)
		if backend != "" && backend != expected && backend != longRouteBackendKey(routeID) && backend != legacyRouteBackendKey(routeID) {
			continue
		}
		if backend != "" {
			expected = backend
		}
		if anchor == nil {
			bindings[expected] = actualTrafficBinding{RouteID: routeID}
		} else {
			bindings[expected] = actualTrafficBinding{RouteID: routeID, Anchor: anchor.UTC()}
		}
	}
	return bindings, rows.Err()
}

func (s *PGStore) recordTrafficCounter(ctx context.Context, tx pgx.Tx, nodeID, scope, proxyName, generation string, observedAt time.Time, current trafficCounter, quotaAnchor *time.Time) (trafficRate, error) {
	var previous trafficCounter
	var previousAt time.Time
	var previousGeneration string
	err := tx.QueryRow(ctx, `
		SELECT last_raw_bytes_in,last_raw_bytes_out,updated_at,source_generation
		FROM traffic_counter_state
		WHERE node_id=$1 AND scope=$2 AND proxy_name=$3
		FOR UPDATE`, nodeID, scope, proxyName).Scan(&previous.BytesIn, &previous.BytesOut, &previousAt, &previousGeneration)
	var previousPtr *trafficCounter
	var previousAtPtr *time.Time
	if err == nil {
		previousPtr = &previous
		previousAtPtr = &previousAt
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return trafficRate{}, err
	}
	delta, rate := trafficCounterTransition(previousPtr, previousAtPtr, previousGeneration, generation, current, observedAt)
	splitQuotaDeltaAcrossWindows := canSplitQuotaDelta(previousPtr, previousAtPtr, previousGeneration, generation, current, observedAt)
	if previousPtr == nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO traffic_counter_state(
				node_id,scope,proxy_name,last_raw_bytes_in,last_raw_bytes_out,
				accumulated_bytes_in,accumulated_bytes_out,updated_at,source_generation
			) VALUES($1,$2,$3,$4,$5,$4,$5,$6,$7)`,
			nodeID, scope, proxyName, current.BytesIn, current.BytesOut, observedAt, generation)
	} else {
		tag, updateErr := tx.Exec(ctx, `
			UPDATE traffic_counter_state SET
				last_raw_bytes_in=$4,last_raw_bytes_out=$5,
				accumulated_bytes_in=accumulated_bytes_in+$6,
				accumulated_bytes_out=accumulated_bytes_out+$7,updated_at=$8,
				source_generation=CASE WHEN $9='' THEN source_generation ELSE $9 END
			WHERE node_id=$1 AND scope=$2 AND proxy_name=$3
			  AND accumulated_bytes_in <= $10
			  AND accumulated_bytes_out <= $11`,
			nodeID, scope, proxyName, current.BytesIn, current.BytesOut,
			delta.BytesIn, delta.BytesOut, observedAt, generation,
			int64(math.MaxInt64)-delta.BytesIn, int64(math.MaxInt64)-delta.BytesOut)
		if updateErr != nil {
			return trafficRate{}, updateErr
		}
		if tag.RowsAffected() != 1 {
			return trafficRate{}, errTrafficCounterOverflow
		}
	}
	if err != nil {
		return trafficRate{}, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO traffic_monthly(node_id,month,scope,proxy_name,bytes_in,bytes_out,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (node_id,month,scope,proxy_name) DO UPDATE SET
			bytes_in=traffic_monthly.bytes_in+EXCLUDED.bytes_in,
			bytes_out=traffic_monthly.bytes_out+EXCLUDED.bytes_out,
			updated_at=EXCLUDED.updated_at
		WHERE traffic_monthly.bytes_in <= $8
		  AND traffic_monthly.bytes_out <= $9`,
		nodeID, trafficMonth(observedAt), scope, proxyName, delta.BytesIn, delta.BytesOut, observedAt,
		int64(math.MaxInt64)-delta.BytesIn, int64(math.MaxInt64)-delta.BytesOut)
	if err == nil && tag.RowsAffected() != 1 {
		return trafficRate{}, errTrafficCounterOverflow
	}
	if err != nil {
		return trafficRate{}, err
	}
	if scope == trafficScopeNode {
		tag, err = tx.Exec(ctx, `
			INSERT INTO traffic_daily(node_id,day,bytes_in,bytes_out,updated_at)
			VALUES($1,$2,$3,$4,$5)
			ON CONFLICT (node_id,day) DO UPDATE SET
				bytes_in=traffic_daily.bytes_in+EXCLUDED.bytes_in,
				bytes_out=traffic_daily.bytes_out+EXCLUDED.bytes_out,
				updated_at=EXCLUDED.updated_at
			WHERE traffic_daily.bytes_in <= $6
			  AND traffic_daily.bytes_out <= $7`,
			nodeID, trafficDay(observedAt), delta.BytesIn, delta.BytesOut, observedAt,
			int64(math.MaxInt64)-delta.BytesIn, int64(math.MaxInt64)-delta.BytesOut)
		if err != nil {
			return trafficRate{}, err
		}
		if tag.RowsAffected() != 1 {
			return trafficRate{}, errTrafficCounterOverflow
		}
	}
	if quotaAnchor == nil || scope != trafficScopeBackend {
		return rate, nil
	}
	return rate, recordQuotaUsage(ctx, tx, nodeID, proxyName, *quotaAnchor, previousAtPtr, observedAt, splitQuotaDeltaAcrossWindows, delta)
}

func recordQuotaUsage(ctx context.Context, tx pgx.Tx, nodeID, proxyName string, anchor time.Time, intervalStart *time.Time, observedAt time.Time, split bool, delta trafficCounter) error {
	allocations, err := quotaDeltas(anchor, observedAt, intervalStart, split, delta)
	if err != nil {
		return err
	}
	periods := make([]string, 0, len(allocations))
	starts := make([]time.Time, 0, len(allocations))
	ends := make([]time.Time, 0, len(allocations))
	bytesIn := make([]int64, 0, len(allocations))
	bytesOut := make([]int64, 0, len(allocations))
	for _, allocation := range allocations {
		periods = append(periods, allocation.Window.Period)
		starts = append(starts, allocation.Window.Start)
		ends = append(ends, allocation.Window.End)
		bytesIn = append(bytesIn, allocation.Delta.BytesIn)
		bytesOut = append(bytesOut, allocation.Delta.BytesOut)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO traffic_quota_usage(
			node_id,period,window_start,window_end,proxy_name,bytes_in,bytes_out,updated_at
		)
		SELECT $1,quota_window.period,quota_window.window_start,quota_window.window_end,$2,
		       quota_window.bytes_in,quota_window.bytes_out,$8
		FROM unnest($3::text[],$4::timestamptz[],$5::timestamptz[],$6::bigint[],$7::bigint[])
		  AS quota_window(period,window_start,window_end,bytes_in,bytes_out)
		ON CONFLICT (node_id,period,window_start,proxy_name) DO UPDATE SET
			window_end=EXCLUDED.window_end,
			bytes_in=traffic_quota_usage.bytes_in+EXCLUDED.bytes_in,
			bytes_out=traffic_quota_usage.bytes_out+EXCLUDED.bytes_out,
			updated_at=EXCLUDED.updated_at
		WHERE traffic_quota_usage.bytes_in <= $9-EXCLUDED.bytes_in
		  AND traffic_quota_usage.bytes_out <= $9-EXCLUDED.bytes_out`,
		nodeID, proxyName, periods, starts, ends, bytesIn, bytesOut, observedAt, int64(math.MaxInt64))
	if err == nil && tag.RowsAffected() != int64(len(allocations)) {
		return errTrafficCounterOverflow
	}
	return err
}

func (s *PGStore) actualRoutePolicies(ctx context.Context, nodeID string) (map[string]RouteRuntimeNames, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT runtime.route_id,runtime.frontend,runtime.backend,runtime.server,COALESCE(runtime.dns_pool,false),
		       runtime.quota_bytes,COALESCE(runtime.quota_action,'observe'),
		       COALESCE(runtime.quota_period,'calendar_month'),
		       COALESCE(runtime.quota_anchor_at,revision.created_at)
		FROM node_config_state AS state
		JOIN config_revisions AS revision
		  ON revision.node_id=state.node_id AND revision.revision=state.actual_revision
		CROSS JOIN LATERAL jsonb_to_recordset(
		  CASE WHEN jsonb_typeof(revision.metadata->'runtime_names')='array'
		       THEN revision.metadata->'runtime_names' ELSE '[]'::jsonb END
		) AS runtime(
		  route_id text,frontend text,backend text,server text,dns_pool boolean,quota_bytes bigint,quota_action text,
		  quota_period text,quota_anchor_at timestamptz
		)
		WHERE state.node_id=$1 AND state.actual_revision IS NOT NULL
		  AND revision.metadata->>'renderer' = ANY($2::text[])
		ORDER BY runtime.route_id`, nodeID, supportedHAProxyRenderers())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]RouteRuntimeNames)
	for rows.Next() {
		var runtime RouteRuntimeNames
		if err = rows.Scan(
			&runtime.RouteID, &runtime.Frontend, &runtime.Backend, &runtime.Server, &runtime.DNSPool,
			&runtime.QuotaBytes, &runtime.QuotaAction, &runtime.QuotaPeriod, &runtime.QuotaAnchorAt,
		); err != nil {
			return nil, err
		}
		if !validID(runtime.RouteID) || runtime.RouteID != strings.ToLower(runtime.RouteID) ||
			!validRouteRuntimeNames(runtime.RouteID, runtime.Backend, runtime.Server) {
			continue
		}
		if runtime.QuotaBytes != nil && *runtime.QuotaBytes <= 0 {
			runtime.QuotaBytes = nil
		}
		if runtime.QuotaAction != "block_new" || runtime.QuotaBytes == nil {
			runtime.QuotaAction = "observe"
		}
		if !validQuotaPeriod(runtime.QuotaPeriod) {
			runtime.QuotaPeriod = defaultQuotaPeriod
		}
		result[runtime.RouteID] = runtime
	}
	return result, rows.Err()
}

func quotaUsageKey(period, proxyName string) string {
	return period + "\x00" + proxyName
}

func (s *PGStore) currentQuotaUsage(ctx context.Context, nodeID string, observedAt time.Time) (map[string]quotaUsage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT period,proxy_name,window_start,window_end,bytes_in,bytes_out
		FROM traffic_quota_usage
		WHERE node_id=$1 AND window_start <= $2 AND window_end > $2
		ORDER BY period,proxy_name`, nodeID, observedAt.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]quotaUsage)
	for rows.Next() {
		var period, proxyName string
		var usage quotaUsage
		if err = rows.Scan(
			&period, &proxyName, &usage.Window.Start, &usage.Window.End,
			&usage.BytesIn, &usage.BytesOut,
		); err != nil {
			return nil, err
		}
		if !validQuotaPeriod(period) || !validTrafficProxyName(proxyName) {
			continue
		}
		usage.Window.Period = period
		usage.Window.Start = usage.Window.Start.UTC()
		usage.Window.End = usage.Window.End.UTC()
		result[quotaUsageKey(period, proxyName)] = usage
	}
	return result, rows.Err()
}

func (s *PGStore) currentRouteRates(ctx context.Context, nodeID string) (map[string]currentRouteRate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT rate.route_id::text,rate.rx_bytes_per_second*8,rate.tx_bytes_per_second*8,rate.sampled_at
		FROM route_traffic_rate_samples AS rate
		JOIN node_heartbeats AS heartbeat
		  ON heartbeat.node_id=rate.node_id AND heartbeat.received_at=rate.sampled_at
		WHERE rate.node_id=$1
		ORDER BY rate.route_id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rates := make(map[string]currentRouteRate)
	for rows.Next() {
		var routeID string
		var rate currentRouteRate
		if err = rows.Scan(&routeID, &rate.RXBitsPerSecond, &rate.TXBitsPerSecond, &rate.SampledAt); err != nil {
			return nil, err
		}
		rates[routeID] = rate
	}
	return rates, rows.Err()
}

func (s *PGStore) GetTraffic(ctx context.Context, nodeID string, month time.Time) (NodeTrafficReport, error) {
	if _, err := s.GetNode(ctx, nodeID); err != nil {
		return NodeTrafficReport{}, err
	}
	routes, err := s.ListRoutes(ctx, nodeID)
	if err != nil {
		return NodeTrafficReport{}, err
	}
	actualPolicies, err := s.actualRoutePolicies(ctx, nodeID)
	if err != nil {
		return NodeTrafficReport{}, err
	}
	var observedAt time.Time
	if err = s.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&observedAt); err != nil {
		return NodeTrafficReport{}, err
	}
	quotaUsageByRoute, err := s.currentQuotaUsage(ctx, nodeID, observedAt)
	if err != nil {
		return NodeTrafficReport{}, err
	}
	currentRates, err := s.currentRouteRates(ctx, nodeID)
	if err != nil {
		return NodeTrafficReport{}, err
	}
	month = trafficMonth(month)
	report := NodeTrafficReport{
		NodeID: nodeID, Month: month.Format("2006-01"), Enforcement: false,
		Backends: []TrafficBackend{}, Routes: []TrafficRoute{},
	}
	backendCounters := make(map[string]trafficCounter)
	rows, err := s.pool.Query(ctx, `
		SELECT scope,proxy_name,bytes_in,bytes_out
		FROM traffic_monthly
		WHERE node_id=$1 AND month=$2
		ORDER BY scope,proxy_name`, nodeID, month.Format("2006-01-02"))
	if err != nil {
		return NodeTrafficReport{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var scope, proxyName string
		var counter trafficCounter
		if err = rows.Scan(&scope, &proxyName, &counter.BytesIn, &counter.BytesOut); err != nil {
			return NodeTrafficReport{}, err
		}
		if scope == trafficScopeNode && proxyName == "" {
			report.BytesIn, report.BytesOut = counter.BytesIn, counter.BytesOut
		} else if scope == trafficScopeBackend {
			backendCounters[proxyName] = counter
		}
	}
	if err = rows.Err(); err != nil {
		return NodeTrafficReport{}, err
	}
	report.UsedBytes = trafficUsedBytes(report.BytesIn, report.BytesOut)
	quotaRuntime := make(map[string]bool)
	var quotaRuntimeJSON []byte
	err = s.pool.QueryRow(ctx, `
		SELECT COALESCE(metrics->'quota_runtime','{}'::jsonb)
		FROM node_heartbeats WHERE node_id=$1`, nodeID).Scan(&quotaRuntimeJSON)
	if err == nil {
		// Heartbeat ingestion validates this object. Treat legacy/corrupt rows as
		// unknown rather than claiming that a backend is blocked.
		var decoded map[string]bool
		if json.Unmarshal(quotaRuntimeJSON, &decoded) == nil {
			quotaRuntime = decoded
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return NodeTrafficReport{}, err
	}

	routeByBackend := make(map[string]string, len(routes)+len(actualPolicies))
	seenRoutes := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		seenRoutes[route.ID] = struct{}{}
		policy, applied := actualPolicies[route.ID]
		backendKey := RouteBackendKey(route.ID)
		var limit *int64
		action := "observe"
		period := route.QuotaPeriod
		if !validQuotaPeriod(period) {
			period = defaultQuotaPeriod
		}
		var quotaWindowStart, quotaWindowEnd *time.Time
		var quotaUsed int64
		runtimeKey := quotaRuntimeKey(route.ID)
		if applied {
			backendKey, limit, action = policy.Backend, policy.QuotaBytes, policy.QuotaAction
			period = policy.QuotaPeriod
			runtimeKey = policy.Backend + "/" + policy.Server
			anchor := observedAt
			if policy.QuotaAnchorAt != nil {
				anchor = *policy.QuotaAnchorAt
			}
			window, windowErr := quotaWindowAt(period, anchor, observedAt)
			if windowErr != nil {
				return NodeTrafficReport{}, windowErr
			}
			if usage, exists := quotaUsageByRoute[quotaUsageKey(period, backendKey)]; exists {
				window = usage.Window
				quotaUsed = trafficUsedBytes(usage.BytesIn, usage.BytesOut)
			}
			start, end := window.Start, window.End
			quotaWindowStart, quotaWindowEnd = &start, &end
		}
		routeByBackend[backendKey] = route.ID
		counter, observed := backendCounters[backendKey]
		used := trafficUsedBytes(counter.BytesIn, counter.BytesOut)
		reached := limit != nil && quotaUsed >= *limit
		enforcement := action == "block_new" && limit != nil
		if enforcement {
			report.Enforcement = true
		}
		rate := currentRates[route.ID]
		var rxBitsPerSecond, txBitsPerSecond *float64
		var rateSampledAt *time.Time
		if !rate.SampledAt.IsZero() {
			rx, txRate, sampledAt := rate.RXBitsPerSecond, rate.TXBitsPerSecond, rate.SampledAt
			rxBitsPerSecond, txBitsPerSecond, rateSampledAt = &rx, &txRate, &sampledAt
		}
		report.Routes = append(report.Routes, TrafficRoute{
			RouteID: route.ID, BackendKey: backendKey, BytesIn: counter.BytesIn,
			BytesOut: counter.BytesOut, UsedBytes: used, QuotaUsedBytes: quotaUsed, LimitBytes: limit,
			QuotaPeriod: period, QuotaWindowStart: quotaWindowStart, QuotaWindowEnd: quotaWindowEnd,
			Reached: reached, QuotaAction: action, Enforcement: enforcement,
			BlockRequested: reached && enforcement, Blocked: quotaRuntime[runtimeKey], Observed: observed, Applied: applied,
			RXBitsPerSecond: rxBitsPerSecond, TXBitsPerSecond: txBitsPerSecond, RateSampledAt: rateSampledAt,
		})
	}
	for routeID, policy := range actualPolicies {
		if _, exists := seenRoutes[routeID]; exists {
			continue
		}
		routeByBackend[policy.Backend] = routeID
		counter, observed := backendCounters[policy.Backend]
		used := trafficUsedBytes(counter.BytesIn, counter.BytesOut)
		anchor := observedAt
		if policy.QuotaAnchorAt != nil {
			anchor = *policy.QuotaAnchorAt
		}
		window, windowErr := quotaWindowAt(policy.QuotaPeriod, anchor, observedAt)
		if windowErr != nil {
			return NodeTrafficReport{}, windowErr
		}
		var quotaUsed int64
		if usage, exists := quotaUsageByRoute[quotaUsageKey(policy.QuotaPeriod, policy.Backend)]; exists {
			window = usage.Window
			quotaUsed = trafficUsedBytes(usage.BytesIn, usage.BytesOut)
		}
		reached := policy.QuotaBytes != nil && quotaUsed >= *policy.QuotaBytes
		enforcement := policy.QuotaAction == "block_new" && policy.QuotaBytes != nil
		if enforcement {
			report.Enforcement = true
		}
		rate := currentRates[routeID]
		var rxBitsPerSecond, txBitsPerSecond *float64
		var rateSampledAt *time.Time
		if !rate.SampledAt.IsZero() {
			rx, txRate, sampledAt := rate.RXBitsPerSecond, rate.TXBitsPerSecond, rate.SampledAt
			rxBitsPerSecond, txBitsPerSecond, rateSampledAt = &rx, &txRate, &sampledAt
		}
		report.Routes = append(report.Routes, TrafficRoute{
			RouteID: routeID, BackendKey: policy.Backend, BytesIn: counter.BytesIn, BytesOut: counter.BytesOut,
			UsedBytes: used, QuotaUsedBytes: quotaUsed, LimitBytes: policy.QuotaBytes,
			QuotaPeriod: policy.QuotaPeriod, QuotaWindowStart: &window.Start, QuotaWindowEnd: &window.End,
			Reached: reached, QuotaAction: policy.QuotaAction,
			Enforcement: enforcement, BlockRequested: reached && enforcement,
			Blocked: quotaRuntime[policy.Backend+"/"+policy.Server], Observed: observed, Applied: true,
			RXBitsPerSecond: rxBitsPerSecond, TXBitsPerSecond: txBitsPerSecond, RateSampledAt: rateSampledAt,
		})
	}
	sort.Slice(report.Routes, func(i, j int) bool { return report.Routes[i].RouteID < report.Routes[j].RouteID })

	backendNames := make([]string, 0, len(backendCounters))
	for name := range backendCounters {
		backendNames = append(backendNames, name)
	}
	sort.Strings(backendNames)
	for _, name := range backendNames {
		counter := backendCounters[name]
		backend := TrafficBackend{
			BackendKey: name, BytesIn: counter.BytesIn, BytesOut: counter.BytesOut,
			UsedBytes: trafficUsedBytes(counter.BytesIn, counter.BytesOut),
		}
		if routeID, ok := routeByBackend[name]; ok {
			id := routeID
			backend.RouteID = &id
		}
		report.Backends = append(report.Backends, backend)
	}
	return report, nil
}

func trafficUsedBytes(bytesIn, bytesOut int64) int64 {
	if bytesIn > math.MaxInt64-bytesOut {
		return math.MaxInt64
	}
	return bytesIn + bytesOut
}

func parseTrafficMonth(value string, now time.Time) (time.Time, error) {
	if value == "" {
		return trafficMonth(now), nil
	}
	if len(value) != len("2006-01") {
		return time.Time{}, fmt.Errorf("month must use YYYY-MM")
	}
	month, err := time.Parse("2006-01", value)
	if err != nil || month.Format("2006-01") != value {
		return time.Time{}, fmt.Errorf("month must use YYYY-MM")
	}
	return trafficMonth(month), nil
}
