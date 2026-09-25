package panel

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Toggling the node's HAProxy connection-log setting publishes a new route
// revision (graceful reload on the Agent); unrelated node edits and old
// clients that omit the key do not.
func TestPGStoreNodeHAProxyLogsToggleRepublishes(t *testing.T) {
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
	name := fmt.Sprintf("haproxy-logs-%d", seed)
	address := fmt.Sprintf("198.19.%d.%d", 1+seed%250, 1+(seed/250)%250)
	node, err := store.CreateNode(ctx, name, address, map[string]any{"agent_port": float64(4200)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DeleteNode(context.Background(), node.ID) })
	assert.True(t, nodeMetadataHAProxyLogs(node.Metadata))

	// No route revision yet: toggling only stores the setting.
	updated, err := store.UpdateNode(ctx, node.ID, name, address, map[string]any{"agent_port": float64(4200), "haproxy_logs": false})
	require.NoError(t, err)
	assert.False(t, nodeMetadataHAProxyLogs(updated.Metadata))
	updated, err = store.UpdateNode(ctx, node.ID, name, address, map[string]any{"agent_port": float64(4200), "haproxy_logs": true})
	require.NoError(t, err)
	assert.True(t, nodeMetadataHAProxyLogs(updated.Metadata))

	desired := func() (int64, string, string, map[string]any) {
		t.Helper()
		var revision int64
		var config, note string
		var metadata map[string]any
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT r.revision,r.config,r.note,r.metadata FROM node_config_state s
			JOIN config_revisions r ON r.node_id=s.node_id AND r.revision=s.desired_revision
			WHERE s.node_id=$1`, node.ID).Scan(&revision, &config, &note, &metadata))
		return revision, config, note, metadata
	}

	spec, err := validatePayload(t, `{"name":"logs","match_mode":"fallback","listener_port":16443,"target_host":"192.0.2.10","target_port":443,"enabled":true}`)
	require.NoError(t, err)
	created, err := store.CreateRoute(ctx, node.ID, spec)
	require.NoError(t, err)
	if !created.Enabled || created.DesiredRevision == nil {
		spec.ExpectedVersion = revision(created.Version)
		spec.Enabled = true
		_, err = store.UpdateRoute(ctx, node.ID, created.ID, spec)
		require.NoError(t, err)
	}
	first, config, _, _ := desired()
	assert.Contains(t, config, "    log /dev/log local0\n")
	assert.Contains(t, config, "    option tcplog\n")

	// Unrelated edit: no new revision.
	_, err = store.UpdateNode(ctx, node.ID, name+"-renamed", address, map[string]any{"agent_port": float64(4200)})
	require.NoError(t, err)
	again, _, _, _ := desired()
	assert.Equal(t, first, again)

	// Off: new revision without any log directive.
	updated, err = store.UpdateNode(ctx, node.ID, name, address, map[string]any{"agent_port": float64(4200), "haproxy_logs": false})
	require.NoError(t, err)
	assert.False(t, nodeMetadataHAProxyLogs(updated.Metadata))
	second, config, note, metadata := desired()
	assert.Greater(t, second, first)
	assert.NotContains(t, config, "log")
	assert.True(t, strings.HasPrefix(note, "node setting changed"), note)
	assert.Equal(t, false, metadata["haproxy_logs"])
	var state string
	require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM node_config_state WHERE node_id=$1`, node.ID).Scan(&state))
	assert.Equal(t, "pending", state)

	// Old client omits the key: stored off survives, no revision.
	updated, err = store.UpdateNode(ctx, node.ID, name, address, map[string]any{"agent_port": float64(4200)})
	require.NoError(t, err)
	assert.False(t, nodeMetadataHAProxyLogs(updated.Metadata))
	again, _, _, _ = desired()
	assert.Equal(t, second, again)

	// Route changes keep honouring the setting.
	facts, err := store.GetNodeRenderFacts(ctx, node.ID)
	require.NoError(t, err)
	assert.True(t, facts.HAProxyLogsDisabled)

	// On again: new revision, byte-identical to the original render.
	_, err = store.UpdateNode(ctx, node.ID, name, address, map[string]any{"agent_port": float64(4200), "haproxy_logs": true})
	require.NoError(t, err)
	third, config3, _, _ := desired()
	assert.Greater(t, third, second)
	var firstConfig string
	require.NoError(t, pool.QueryRow(ctx, `SELECT config FROM config_revisions WHERE node_id=$1 AND revision=$2`, node.ID, first).Scan(&firstConfig))
	assert.Equal(t, firstConfig, config3)
}
