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

func TestPGStoreFirewallAssignmentCarriesActiveAndDesiredListenerPlans(t *testing.T) {
	databaseURL := os.Getenv("NODEFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NODEFLOW_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()
	var migrated bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version='000015')`).Scan(&migrated))
	require.True(t, migrated, "database must include migration 000015")

	store := NewPGStore(pool)
	node, err := store.CreateNode(ctx, fmt.Sprintf("firewall-transition-%d", time.Now().UnixNano()), "198.18.0.31", map[string]any{"firewall_apply_allowed": true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DeleteNode(context.Background(), node.ID) })
	token := fmt.Sprintf("firewall-transition-token-%d", time.Now().UnixNano())
	sum := sha256.Sum256([]byte(token))
	_, err = store.CreateEnrollmentToken(ctx, node.ID, hex.EncodeToString(sum[:]), "firewall-test", time.Now().Add(time.Hour))
	require.NoError(t, err)

	route, err := store.CreateRoute(ctx, node.ID, RouteSpec{
		ListenerIP: "*", ListenerPort: 443, SNIs: []string{"firewall.example"}, Hostname: "firewall.example",
		TargetType: "tcp", TargetHost: "192.0.2.31", TargetPort: 443,
		ProxyProtocol: "none", QuotaAction: "observe", QuotaPeriod: "calendar_month",
	})
	require.NoError(t, err)
	enabled := routeAsSpec(route, true)
	enabled.ExpectedVersion = revision(route.Version)
	route, err = store.UpdateRoute(ctx, node.ID, route.ID, enabled)
	require.NoError(t, err)
	require.NotNil(t, route.DesiredRevision)
	_, err = store.IngestApplyReport(ctx, token, ApplyReport{Revision: *route.DesiredRevision, State: "applied"})
	require.NoError(t, err)

	updated := routeAsSpec(route, true)
	updated.ListenerPort = 10065
	updated.ExpectedVersion = revision(route.Version)
	_, err = store.UpdateRoute(ctx, node.ID, route.ID, updated)
	require.NoError(t, err)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	assignment, err := buildFirewallAssignment(ctx, tx, node.ID, "apply", true)
	require.NoError(t, err)
	require.NotNil(t, assignment)
	assert.Equal(t, []int{443}, assignment.TCPPorts)
	assert.Equal(t, []int{10065}, assignment.DesiredTCPPorts)
	assert.True(t, assignment.Transition)
	assert.True(t, assignment.ActivePlanComplete)

	activeOnly, err := buildFirewallAssignment(ctx, tx, node.ID, "apply", false)
	require.NoError(t, err)
	require.NotNil(t, activeOnly)
	assert.Equal(t, []int{443}, activeOnly.TCPPorts)
	assert.Empty(t, activeOnly.DesiredTCPPorts)
	assert.False(t, activeOnly.Transition)
}
