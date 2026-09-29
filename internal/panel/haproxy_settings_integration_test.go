package panel

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPGStoreHAProxySettingsAndAdvancedMode(t *testing.T) {
	databaseURL := os.Getenv("NODEFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NODEFLOW_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()
	store := NewPGStore(pool)
	seed := time.Now().UnixNano()
	node, err := store.CreateNode(ctx, fmt.Sprintf("configuration-%d", seed), fmt.Sprintf("2001:db8:%x:%x::1", uint16(seed), uint16(seed>>16)), map[string]any{})
	require.NoError(t, err)
	defer func() { _ = store.DeleteNode(context.Background(), node.ID) }()
	spec, err := validatePayload(t, `{"name":"example","match_mode":"fallback","listener_port":16443,"target_host":"192.0.2.10","target_port":443,"enabled":true}`)
	require.NoError(t, err)
	created, err := store.CreateRoute(ctx, node.ID, spec)
	require.NoError(t, err)
	spec.ExpectedVersion = revision(created.Version)
	spec.Enabled = true
	created, err = store.UpdateRoute(ctx, node.ID, created.ID, spec)
	require.NoError(t, err)
	spec.ExpectedVersion = revision(created.Version)
	before, err := store.GetConfigState(ctx, node.ID)
	require.NoError(t, err)
	_, err = store.UpdateNode(ctx, node.ID, node.Name, node.Address, map[string]any{nodeHAProxySettingsKey: map[string]any{"max_connections": 40000, "timeout_client": "1h"}})
	require.NoError(t, err)
	after, err := store.GetConfigState(ctx, node.ID)
	require.NoError(t, err)
	require.Greater(t, *after.DesiredRevision, *before.DesiredRevision)
	generated, err := store.GetConfigRevision(ctx, node.ID, *after.DesiredRevision)
	require.NoError(t, err)
	assert.Contains(t, generated.Config, "maxconn 40000")
	assert.Contains(t, generated.Config, "timeout client 1h")
	// Omission by an older client must not reset tuning or publish another revision.
	_, err = store.UpdateNode(ctx, node.ID, node.Name, node.Address, map[string]any{})
	require.NoError(t, err)
	same, err := store.GetConfigState(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, after.DesiredRevision, same.DesiredRevision)
	manual, err := store.CreateConfigRevision(ctx, node.ID, generated.Config+"\n# manual\n", "manual", map[string]any{"source": "advanced_editor"})
	require.NoError(t, err)
	_, err = store.AssignDesiredRevision(ctx, node.ID, manual.Revision)
	require.NoError(t, err)
	// A publishing route mutation fails atomically while advanced mode is active.
	spec.Name = "blocked"
	spec.ListenerPort = 17443
	_, err = store.UpdateRoute(ctx, node.ID, created.ID, spec)
	require.ErrorIs(t, err, ErrAdvancedConfig)
	routes, err := store.ListRoutes(ctx, node.ID)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	assert.Equal(t, "example", routes[0].Name)
	state, err := store.GetConfigState(ctx, node.ID)
	require.NoError(t, err)
	require.Equal(t, manual.Revision, *state.DesiredRevision)
	// Tuning is stored for later generated mode and never overwrites manual text.
	_, err = store.UpdateNode(ctx, node.ID, node.Name, node.Address, map[string]any{nodeHAProxySettingsKey: map[string]any{"max_connections": 50000}})
	require.NoError(t, err)
	state, err = store.GetConfigState(ctx, node.ID)
	require.NoError(t, err)
	require.Equal(t, manual.Revision, *state.DesiredRevision)
	// Resuming builds current intent and restores route lifecycle tracking atomically.
	resumed, err := store.ResumeGeneratedConfig(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, "route_lifecycle", resumed.CreatedBy)
	assert.Contains(t, resumed.Config, "maxconn 50000")
	_, err = store.UpdateRoute(ctx, node.ID, created.ID, spec)
	require.NoError(t, err)
	state, err = store.GetConfigState(ctx, node.ID)
	require.NoError(t, err)
	latest, err := store.GetConfigRevision(ctx, node.ID, *state.DesiredRevision)
	require.NoError(t, err)
	assert.Contains(t, latest.Config, "maxconn 50000")
}
