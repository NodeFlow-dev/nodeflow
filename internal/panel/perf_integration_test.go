package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func perfIntegrationStore(t *testing.T) (context.Context, *pgxpool.Pool, *PGStore) {
	t.Helper()
	databaseURL := os.Getenv("NODEFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NODEFLOW_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	var migrated bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version='000040')`).Scan(&migrated))
	require.True(t, migrated, "database must include migration 000040")
	return ctx, pool, NewPGStore(pool)
}

func TestPGStoreBatchedRouteChildren(t *testing.T) {
	ctx, _, store := perfIntegrationStore(t)
	node, err := store.CreateNode(ctx, fmt.Sprintf("perf-routes-%d", time.Now().UnixNano()), "198.18.0.40", map[string]any{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DeleteNode(context.Background(), node.ID) })

	multi := RouteSpec{
		Name: "multi", ListenerIP: "*", ListenerPort: 443, MatchMode: "sni",
		SNIs: []string{"c.example", "a.example", "b.example"}, Hostname: "c.example",
		TargetType: "tcp", TargetHost: "192.0.2.10", TargetPort: 443, HealthCheck: true,
		ProxyProtocol: "none", QuotaAction: "observe", AcceptProxyFrom: []string{}, BalanceMode: "pool",
		Servers: []RouteServerSpec{
			{Position: 1, Name: "s1", TargetType: "tcp", Host: "192.0.2.11", Port: 443},
			{Position: 2, Name: "s2", TargetType: "tcp", Host: "192.0.2.12", Port: 443, Backup: true},
			{Position: 3, Name: "s3", TargetType: "tcp", Host: "backend.example", Port: 8443, DNSPool: true, PreferredIP: "192.0.2.13"},
		},
	}
	created, err := store.CreateRoute(ctx, node.ID, multi)
	require.NoError(t, err)
	assert.Equal(t, []string{"c.example", "a.example", "b.example"}, created.SNIs)

	fallback := RouteSpec{
		Name: "fallback", ListenerIP: "*", ListenerPort: 8443, MatchMode: "fallback", Fallback: true,
		TargetType: "tcp", TargetHost: "192.0.2.20", TargetPort: 443, HealthCheck: true,
		ProxyProtocol: "none", QuotaAction: "observe", AcceptProxyFrom: []string{}, BalanceMode: "pool",
	}
	_, err = store.CreateRoute(ctx, node.ID, fallback)
	require.NoError(t, err)

	routes, err := store.ListRoutes(ctx, node.ID)
	require.NoError(t, err)
	require.Len(t, routes, 2)
	assert.Equal(t, []string{"c.example", "a.example", "b.example"}, routes[0].SNIs, "batched SNIs keep position order")
	require.Len(t, routes[0].Servers, 3)
	assert.Equal(t, "s1", routes[0].Servers[0].Name)
	assert.True(t, routes[0].Servers[1].Backup)
	assert.True(t, routes[0].Servers[2].DNSPool)
	assert.Equal(t, "192.0.2.13", routes[0].Servers[2].PreferredIP)
	assert.NotNil(t, routes[1].SNIs)
	assert.Empty(t, routes[1].SNIs, "routes without SNIs return an empty list, not null")

	single, err := store.GetRoute(ctx, node.ID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, routes[0].SNIs, single.SNIs)

	// Update rewrites both child sets through the batched inserts.
	update := routeAsSpec(single, false)
	update.AcceptProxyFrom = []string{}
	update.SNIs = []string{"z.example"}
	update.Hostname = "z.example"
	update.Servers = update.Servers[:1]
	update.ExpectedVersion = revision(single.Version)
	updated, err := store.UpdateRoute(ctx, node.ID, created.ID, update)
	require.NoError(t, err)
	assert.Equal(t, []string{"z.example"}, updated.SNIs)
	routes, err = store.ListRoutes(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"z.example"}, routes[0].SNIs)
	assert.Len(t, routes[0].Servers, 1)

	// Duplicate server names must still fail atomically.
	bad := routeAsSpec(routes[0], false)
	bad.AcceptProxyFrom = []string{}
	bad.Servers = []RouteServerSpec{
		{Position: 1, Name: "dup", TargetType: "tcp", Host: "192.0.2.11", Port: 443},
		{Position: 2, Name: "dup", TargetType: "tcp", Host: "192.0.2.12", Port: 443},
	}
	bad.ExpectedVersion = revision(routes[0].Version)
	_, err = store.UpdateRoute(ctx, node.ID, created.ID, bad)
	require.Error(t, err)
	after, err := store.GetRoute(ctx, node.ID, created.ID)
	require.NoError(t, err)
	assert.Len(t, after.Servers, 1)
}

func TestPGStoreNodeOperationalDetailSingleQuery(t *testing.T) {
	ctx, pool, store := perfIntegrationStore(t)
	_, err := store.GetNodeOperationalDetail(ctx, "00000000-0000-4000-8000-00000000dead")
	assert.ErrorIs(t, err, ErrNotFound)

	node, err := store.CreateNode(ctx, fmt.Sprintf("perf-detail-%d", time.Now().UnixNano()), "198.18.0.41", map[string]any{"k": "v"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DeleteNode(context.Background(), node.ID) })

	detail, err := store.GetNodeOperationalDetail(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, node.ID, detail.Node.ID)
	assert.Equal(t, map[string]any{"k": "v"}, detail.Node.Metadata)
	assert.Zero(t, detail.RoutesTotal)
	assert.False(t, detail.TrafficObserved)
	assert.Zero(t, detail.TrafficDailyObservedDays)
	assert.Nil(t, detail.TrafficDailyAverageUsed)
	assert.Empty(t, detail.CredentialPrefix)
	assert.Nil(t, detail.CredentialExpiresAt)
	require.NotNil(t, detail.HAProxyControl)
	assert.Equal(t, node.ID, detail.HAProxyControl.NodeID)
	assert.Nil(t, detail.LatestHeartbeat)
	assert.Nil(t, detail.MetricsSummary)
	assert.Equal(t, time.Now().UTC().Format("2006-01"), detail.TrafficMonth)

	sum := sha256.Sum256([]byte(node.ID))
	_, err = store.CreateEnrollmentToken(ctx, node.ID, hex.EncodeToString(sum[:]), "perfpref", time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO traffic_monthly(node_id,month,scope,proxy_name,bytes_in,bytes_out,updated_at)
		VALUES($1,date_trunc('month',clock_timestamp() AT TIME ZONE 'UTC')::date,'node','',100,50,now())`, node.ID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO traffic_daily(node_id,day,bytes_in,bytes_out,updated_at)
		SELECT $1,(clock_timestamp() AT TIME ZONE 'UTC')::date - d,10*(d+1),5,now() FROM generate_series(0,1) d`, node.ID)
	require.NoError(t, err)
	var receivedAt time.Time
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO node_heartbeats(node_id,agent_version,status,metrics,routes_ok,received_at)
		VALUES($1,'1.2.3','online','{"cpu_percent":5}',true,date_trunc('milliseconds',clock_timestamp()))
		RETURNING received_at`, node.ID).Scan(&receivedAt))
	_, err = pool.Exec(ctx, `
		INSERT INTO node_traffic_rate_samples(node_id,sampled_at,rx_bytes_per_second,tx_bytes_per_second,cpu_percent)
		VALUES($1,$2::timestamptz - interval '1 minute',50,25,10),($1,$2::timestamptz,100,50,30)`, node.ID, receivedAt)
	require.NoError(t, err)

	detail, err = store.GetNodeOperationalDetail(ctx, node.ID)
	require.NoError(t, err)
	assert.True(t, detail.TrafficObserved)
	assert.Equal(t, int64(150), detail.TrafficUsed)
	assert.True(t, detail.TrafficDayObserved)
	assert.Equal(t, int64(15), detail.TrafficDayUsed)
	assert.Equal(t, 2, detail.TrafficDailyObservedDays)
	require.NotNil(t, detail.TrafficDailyAverageUsed)
	assert.InDelta(t, 20.0, *detail.TrafficDailyAverageUsed, 0.001)
	assert.Equal(t, "perfpref", detail.CredentialPrefix)
	assert.NotNil(t, detail.CredentialExpiresAt)
	require.NotNil(t, detail.LatestHeartbeat)
	assert.Equal(t, "1.2.3", detail.LatestHeartbeat.AgentVersion)
	assert.EqualValues(t, 5, detail.LatestHeartbeat.Metrics["cpu_percent"])
	require.NotNil(t, detail.RXBitsPerSecond)
	assert.Equal(t, 800.0, *detail.RXBitsPerSecond)
	assert.Equal(t, 400.0, *detail.TXBitsPerSecond)
	require.NotNil(t, detail.MetricsSummary)
	assert.Equal(t, int64(2), detail.MetricsSummary.SampleCount)
	require.NotNil(t, detail.MetricsSummary.CPUPercent)
	assert.Equal(t, 20.0, *detail.MetricsSummary.CPUPercent.Average)
	require.NotNil(t, detail.MetricsSummary.CPUPercent.Delta)
	assert.Equal(t, 20.0, *detail.MetricsSummary.CPUPercent.Delta, "first/last via LIMIT 1 lookups")
	require.NotNil(t, detail.MetricsSummary.RXBPS)
	assert.Equal(t, 400.0, *detail.MetricsSummary.RXBPS.Delta)
	assert.Nil(t, detail.MetricsSummary.MemoryPercent)

	states, err := store.ListConfigStates(ctx, []string{node.ID})
	require.NoError(t, err)
	assert.Empty(t, states)
}

func TestPGStoreListAuditNodeFilterAndRetention(t *testing.T) {
	ctx, pool, store := perfIntegrationStore(t)
	node, err := store.CreateNode(ctx, fmt.Sprintf("perf-audit-%d", time.Now().UnixNano()), "198.18.0.42", map[string]any{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DeleteNode(context.Background(), node.ID) })

	require.NoError(t, store.AppendAudit(ctx, AuditEvent{ActorType: "admin", Action: "node.update", ResourceType: "node", ResourceID: node.ID, Details: map[string]any{}}))
	require.NoError(t, store.AppendAudit(ctx, AuditEvent{ActorType: "admin", Action: "route.update", ResourceType: "route", ResourceID: "r1", Details: map[string]any{"node_id": node.ID}}))
	require.NoError(t, store.AppendAudit(ctx, AuditEvent{ActorType: "admin", Action: "both", ResourceType: "node", ResourceID: node.ID, Details: map[string]any{"node_id": node.ID}}))
	require.NoError(t, store.AppendAudit(ctx, AuditEvent{ActorType: "admin", Action: "other", ResourceType: "route", ResourceID: "r2", Details: map[string]any{"node_id": "x"}}))
	entries, err := store.ListAudit(ctx, node.ID, 20)
	require.NoError(t, err)
	require.Len(t, entries, 3, "matches by resource_id or details.node_id without duplicates")
	assert.Equal(t, "both", entries[0].Action)
	assert.Equal(t, "node.update", entries[2].Action)
	entries, err = store.ListAudit(ctx, node.ID, 2)
	require.NoError(t, err)
	assert.Len(t, entries, 2)
	all, err := store.ListAudit(ctx, "", 1)
	require.NoError(t, err)
	assert.Len(t, all, 1)

	// Retention: old rows go, current windows and recent months stay.
	_, err = pool.Exec(ctx, `
		INSERT INTO traffic_quota_usage(node_id,period,window_start,window_end,proxy_name,bytes_in,bytes_out,updated_at)
		VALUES($1,'hourly',now()-interval '200 days',now()-interval '200 days'+interval '1 hour','nf_be_old',1,1,now()),
		      ($1,'calendar_month',date_trunc('month',now()),date_trunc('month',now())+interval '1 month','nf_be_cur',1,1,now())`, node.ID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO traffic_monthly(node_id,month,scope,proxy_name,bytes_in,bytes_out,updated_at)
		VALUES($1,(date_trunc('month',now())-interval '30 months')::date,'node','',1,1,now()),
		      ($1,(date_trunc('month',now())-interval '1 month')::date,'node','',1,1,now())`, node.ID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE traffic_rate_retention_state SET last_cleanup_at='-infinity'`)
	require.NoError(t, err)
	result, err := store.CleanupRetention(ctx, DefaultDataRetentionPolicy())
	require.NoError(t, err)
	require.True(t, result.Ran)
	var quotaRows, monthlyRows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM traffic_quota_usage WHERE node_id=$1`, node.ID).Scan(&quotaRows))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM traffic_monthly WHERE node_id=$1`, node.ID).Scan(&monthlyRows))
	assert.Equal(t, 1, quotaRows)
	assert.Equal(t, 1, monthlyRows)
}
