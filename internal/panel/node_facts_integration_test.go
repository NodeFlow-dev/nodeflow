package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Heartbeat node facts (kernel pipes, RAM bucket) republish the route
// revision only when they change what the renderer emits.
func TestPGStoreHeartbeatNodeFactsRepublish(t *testing.T) {
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
	node, err := store.CreateNode(ctx, fmt.Sprintf("node-facts-%d", seed), fmt.Sprintf("198.18.%d.%d", 1+seed%250, 1+(seed/250)%250), map[string]any{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DeleteNode(context.Background(), node.ID) })
	token := fmt.Sprintf("node-facts-token-%d", seed)
	sum := sha256.Sum256([]byte(token))
	_, err = store.CreateEnrollmentToken(ctx, node.ID, hex.EncodeToString(sum[:]), "facts-test", time.Now().Add(time.Hour))
	require.NoError(t, err)

	instanceID := "44444444-4444-4444-8444-444444444444"
	startedAt := time.Now().UTC().Add(-time.Minute)
	sequence := int64(0)
	send := func(metrics map[string]any) {
		t.Helper()
		sequence++
		hb := orderedHeartbeat(instanceID, startedAt, sequence)
		hb.Version, hb.Status, hb.Metrics = "1.1.2", "online", metrics
		_, err := store.IngestHeartbeat(ctx, token, hb)
		require.NoError(t, err)
	}
	desired := func() (int64, string) {
		t.Helper()
		var revision int64
		var config string
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT r.revision,r.config FROM node_config_state s
			JOIN config_revisions r ON r.node_id=s.node_id AND r.revision=s.desired_revision
			WHERE s.node_id=$1`, node.ID).Scan(&revision, &config))
		return revision, config
	}
	untuned := map[string]any{"memory_total_bytes": float64(4 * gib)}
	send(untuned) // before any route: no revision, nothing to republish

	spec, err := validatePayload(t, `{"name":"facts","match_mode":"fallback","listener_port":15443,"sticky_mode":"source_table","enabled":true,`+poolServers+`}`)
	require.NoError(t, err)
	created, err := store.CreateRoute(ctx, node.ID, spec)
	require.NoError(t, err)
	if !created.Enabled || created.DesiredRevision == nil {
		spec.ExpectedVersion = revision(created.Version)
		spec.Enabled = true
		created, err = store.UpdateRoute(ctx, node.ID, created.ID, spec)
		require.NoError(t, err)
	}
	first, config := desired()
	assert.Contains(t, config, "tune.pipesize 262144\n")
	assert.Contains(t, config, "stick-table type ipv6 size 919k ")

	// Same facts again: no new revision.
	send(untuned)
	again, _ := desired()
	assert.Equal(t, first, again)

	// Tiny RAM change inside the same 256 MiB bucket: no new revision.
	send(map[string]any{"memory_total_bytes": float64(4*gib + 1000)})
	again, _ = desired()
	assert.Equal(t, first, again)

	// Kernel pipes tuned: a new revision with 1 MiB pipes, then stable.
	tuned := map[string]any{
		"memory_total_bytes": float64(4 * gib),
		"kernel_pipes":       map[string]any{"tuned": true, "pipe_max_size": float64(1048576), "pipe_user_pages_soft": float64(0)},
	}
	send(tuned)
	second, config := desired()
	assert.Greater(t, second, first)
	assert.Contains(t, config, "# renderer: "+HAProxyRendererVersion+"\n")
	assert.Contains(t, config, "tune.pipesize 1048576\n")
	var note, createdBy string
	require.NoError(t, pool.QueryRow(ctx, `SELECT note,created_by FROM config_revisions WHERE node_id=$1 AND revision=$2`, node.ID, second).Scan(&note, &createdBy))
	assert.True(t, strings.HasPrefix(note, "node facts changed"), note)
	assert.Equal(t, "route_lifecycle", createdBy)
	send(tuned)
	again, _ = desired()
	assert.Equal(t, second, again)

	// RAM grows to another bucket: new stick-table size.
	tuned["memory_total_bytes"] = float64(8 * gib)
	send(tuned)
	third, config := desired()
	assert.Greater(t, third, second)
	assert.Contains(t, config, "stick-table type ipv6 size 1839k ")

	// The route GET reports the effective size from the same facts.
	facts, err := store.GetNodeRenderFacts(ctx, node.ID)
	require.NoError(t, err)
	routes, err := store.ListRoutes(ctx, node.ID)
	require.NoError(t, err)
	annotateStickyTableEffective(routes, facts)
	require.Len(t, routes, 1)
	assert.Equal(t, "1839k", routes[0].StickyTableEntriesEffective)
}
