package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestPGStoreRouteTemplates(t *testing.T) {
	url := os.Getenv("NODEFLOW_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("NODEFLOW_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	store := NewPGStore(pool)
	spec, err := validatePayload(t, `{"name":"shared settings","match_mode":"sni","snis":["template.example.com"],"target_host":"192.0.2.10","target_port":443}`)
	require.NoError(t, err)
	in, err := portableRouteInput(specToRoute(spec))
	require.NoError(t, err)
	config, err := validatedTemplateConfig(in)
	require.NoError(t, err)
	template, err := store.SaveRouteTemplate(ctx, "", "Reusable", "Description", config, 0)
	require.NoError(t, err)
	defer pool.Exec(context.Background(), `DELETE FROM route_templates WHERE id=$1`, template.ID)
	require.Equal(t, int64(1), template.Version)
	listed, err := store.ListRouteTemplates(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, listed)
	loaded, err := store.GetRouteTemplate(ctx, template.ID)
	require.NoError(t, err)
	require.JSONEq(t, string(config), string(loaded.Config))
	var intent routeInput
	require.NoError(t, json.Unmarshal(loaded.Config, &intent))
	validated, err := validateRoute(intent, false)
	require.NoError(t, err)
	nodes := []Node{}
	for _, name := range []string{"template-a", "template-b"} {
		stamp := uint64(time.Now().UnixNano())
		node, err := store.CreateNode(ctx, name, fmt.Sprintf("2001:db8::%x:%x:%x:%x", stamp>>48, (stamp>>32)&0xffff, (stamp>>16)&0xffff, stamp&0xffff), nil)
		require.NoError(t, err)
		nodes = append(nodes, node)
		defer store.DeleteNode(context.Background(), node.ID)
		created, err := store.CreateRoute(ctx, node.ID, validated)
		require.NoError(t, err)
		require.False(t, created.Enabled)
		require.Equal(t, "draft", created.DeploymentState)
		state, err := store.GetConfigState(ctx, node.ID)
		if err != nil {
			require.ErrorIs(t, err, ErrNotFound)
		} else {
			require.Nil(t, state.DesiredRevision)
		}
	}
	updated, err := store.SaveRouteTemplate(ctx, template.ID, "Updated", "Description", config, 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), updated.Version)
	_, err = store.SaveRouteTemplate(ctx, template.ID, "Stale", "", config, 1)
	require.ErrorIs(t, err, ErrRouteVersionConflict)
	require.ErrorIs(t, store.DeleteRouteTemplate(ctx, template.ID, 1), ErrRouteVersionConflict)
	require.NoError(t, store.DeleteRouteTemplate(ctx, template.ID, 2))
	_, err = store.GetRouteTemplate(ctx, template.ID)
	require.ErrorIs(t, err, ErrNotFound)
	for _, node := range nodes {
		routes, err := store.ListRoutes(ctx, node.ID)
		require.NoError(t, err)
		require.Len(t, routes, 1)
		require.Equal(t, "shared settings", routes[0].Name)
	}
}
