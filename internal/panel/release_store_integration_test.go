package panel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This test is intentionally opt-in because unit-test hosts do not always
// provide PostgreSQL. It proves that assignments sharing one observed state
// are serialized and that both actual and desired sequences participate in CAS.
func TestPGStoreAgentReleaseAssignmentCASIntegration(t *testing.T) {
	databaseURL := os.Getenv("NODEFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NODEFLOW_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, pool.Ping(ctx))
	var migrated bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version='000009')`).Scan(&migrated))
	require.True(t, migrated, "database must include migration 000009")

	store := NewPGStore(pool)
	now := uint64(time.Now().UnixNano())
	name := fmt.Sprintf("agent-release-cas-%d", now)
	address := fmt.Sprintf("2001:db8:%x:%x:%x:%x::1", uint16(now>>48), uint16(now>>32), uint16(now>>16), uint16(now))
	node, err := store.CreateNode(ctx, name, address, map[string]any{})
	require.NoError(t, err)
	releaseIDs := make([]string, 0, 3)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = store.DeleteNode(cleanupCtx, node.ID)
		for _, releaseID := range releaseIDs {
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM agent_releases WHERE id=$1`, releaseID)
		}
	})
	_, err = pool.Exec(ctx, `
		INSERT INTO node_heartbeats(node_id,agent_version,status,metrics,routes_ok)
		VALUES($1,'integration','online','{"os":"linux","arch":"amd64"}'::jsonb,true)`, node.ID)
	require.NoError(t, err)

	releases := make([]AgentRelease, 0, 3)
	for index := 0; index < 3; index++ {
		sequence, reserveErr := store.ReserveAgentReleaseSequence(ctx)
		require.NoError(t, reserveErr)
		release, createErr := store.CreateAgentRelease(ctx, AgentRelease{
			Version:      fmt.Sprintf("cas-integration-%d", sequence),
			OS:           "linux",
			Arch:         "amd64",
			SHA256:       strings.Repeat(fmt.Sprintf("%x", index+1), 64),
			SizeBytes:    1,
			Sequence:     sequence,
			Signature:    "integration-signature",
			ArtifactPath: fmt.Sprintf("%020d-linux-amd64-cas-integration.bin", sequence),
		})
		require.NoError(t, createErr)
		releases = append(releases, release)
		releaseIDs = append(releaseIDs, release.ID)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, release := range releases[:2] {
		release := release
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, assignErr := store.AssignAgentRelease(ctx, node.ID, release.ID, 0, 0)
			results <- assignErr
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	successes, staleConflicts := 0, 0
	for result := range results {
		switch {
		case result == nil:
			successes++
		case errors.Is(result, ErrAgentUpdateStateChanged):
			staleConflicts++
		default:
			require.NoError(t, result)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, staleConflicts)

	state, err := store.GetAgentUpdateState(ctx, node.ID)
	require.NoError(t, err)
	require.NotNil(t, state.DesiredRelease)
	winnerSequence := state.DesiredRelease.Sequence
	assert.Zero(t, state.ActualSequence)

	_, err = store.AssignAgentRelease(ctx, node.ID, releases[2].ID, 0, 0)
	assert.ErrorIs(t, err, ErrAgentUpdateStateChanged, "stale desired sequence must lose CAS")
	unchanged, err := store.GetAgentUpdateState(ctx, node.ID)
	require.NoError(t, err)
	require.NotNil(t, unchanged.DesiredRelease)
	assert.Equal(t, winnerSequence, unchanged.DesiredRelease.Sequence)

	_, err = pool.Exec(ctx, `
		UPDATE node_agent_updates
		SET actual_sequence=$2,state='installed',updated_at=clock_timestamp()
		WHERE node_id=$1`, node.ID, winnerSequence)
	require.NoError(t, err)
	_, err = store.AssignAgentRelease(ctx, node.ID, releases[2].ID, 0, winnerSequence)
	assert.ErrorIs(t, err, ErrAgentUpdateStateChanged, "stale actual sequence must lose CAS")
	unchanged, err = store.GetAgentUpdateState(ctx, node.ID)
	require.NoError(t, err)
	require.NotNil(t, unchanged.DesiredRelease)
	assert.Equal(t, winnerSequence, unchanged.ActualSequence)
	assert.Equal(t, winnerSequence, unchanged.DesiredRelease.Sequence)

	_, err = pool.Exec(ctx, `
		UPDATE node_agent_updates
		SET desired_release_id=$2,actual_sequence=$3,state='installed',updated_at=clock_timestamp()
		WHERE node_id=$1`, node.ID, releases[2].ID, releases[2].Sequence)
	require.NoError(t, err)
	deleted, err := store.DeleteAgentRelease(ctx, releases[0].ID)
	require.NoError(t, err, "an older compatible release is only a potential rollback target and must remain deletable")
	assert.Equal(t, releases[0].ID, deleted.ID)
	_, err = store.DeleteAgentRelease(ctx, releases[2].ID)
	assert.ErrorIs(t, err, ErrReleaseInUse, "the installed and desired release must remain protected")
}
