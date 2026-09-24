package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPGStoreQARC11RouteResponses exercises the QA rc11 fixes against a real
// migrated PostgreSQL (opt-in, like the other integration tests):
//   - B2: POST/PUT/PATCH responses carry servers[] exactly like GET.
//   - B1: PATCH {enabled} keeps every stored field.
//   - L2: an identical PUT keeps the version and creates no revision.
//   - H1: kernel shaper is rejected for an Agent older than 1.1.0 both on
//     save and on publish after an Agent downgrade.
func TestPGStoreQARC11RouteResponses(t *testing.T) {
	databaseURL := os.Getenv("NODEFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NODEFLOW_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()
	store := NewPGStore(pool)
	suffix := time.Now().UnixNano()
	node, err := store.CreateNode(ctx, fmt.Sprintf("qa-rc11-%d", suffix), fmt.Sprintf("198.18.%d.%d", (suffix/256)%256, suffix%256), map[string]any{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DeleteNode(context.Background(), node.ID) })
	setAgent := func(version string) {
		_, err := pool.Exec(ctx, `
			INSERT INTO node_heartbeats(node_id,agent_version,status,metrics,received_at) VALUES($1,$2,'online','{}',now())
			ON CONFLICT(node_id) DO UPDATE SET agent_version=EXCLUDED.agent_version`, node.ID, version)
		require.NoError(t, err)
	}
	revisions := func() int {
		var count int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM config_revisions WHERE node_id=$1`, node.ID).Scan(&count))
		return count
	}
	h := NewHandler(store, Config{AdminToken: testAdminToken})
	base := "/api/v1/nodes/" + node.ID + "/routes"
	decodeRoute := func(body []byte) Route {
		var route Route
		require.NoError(t, json.Unmarshal(body, &route))
		return route
	}

	// B2: POST response has the servers with their backup flags.
	create := `{"name":"qa-fo","listener_port":19450,"match_mode":"fallback","balance_mode":"failover",
		"accept_proxy_from":["10.0.0.0/8"],"client_download_mbps":5,
		"servers":[{"name":"p","host":"127.0.0.1","port":9903},{"name":"r","target_type":"unix","unix_socket_path":"/dev/shm/nf-test.sock"}]}`
	w := request(t, h, http.MethodPost, base, create, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	created := decodeRoute(w.Body.Bytes())
	require.Len(t, created.Servers, 2)
	assert.False(t, created.Servers[0].Backup)
	assert.True(t, created.Servers[1].Backup)
	get := request(t, h, http.MethodGet, base+"/"+created.ID, "", testAdminToken)
	require.Equal(t, http.StatusOK, get.Code)
	assert.Equal(t, decodeRoute(get.Body.Bytes()).Servers, created.Servers)

	// B2: PUT response has the servers too.
	update := fmt.Sprintf(`{"expected_version":%d,"name":"qa-fo","listener_port":19450,"match_mode":"fallback","balance_mode":"failover",
		"accept_proxy_from":["10.0.0.0/8"],"client_download_mbps":5,"enabled":true,
		"servers":[{"name":"p","host":"127.0.0.1","port":9903},{"name":"r","target_type":"unix","unix_socket_path":"/dev/shm/nf-test.sock"}]}`, created.Version)
	w = request(t, h, http.MethodPut, base+"/"+created.ID, update, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	updated := decodeRoute(w.Body.Bytes())
	require.Len(t, updated.Servers, 2)
	assert.True(t, updated.Servers[1].Backup)
	assert.Equal(t, "pending", updated.DeploymentState)
	require.Equal(t, 1, revisions())

	// L2: identical PUT on a settled active route is a no-op.
	_, err = pool.Exec(ctx, `UPDATE routes SET deployed=true,deployment_state='active',deployed_fingerprint=desired_fingerprint,applied_revision=desired_revision WHERE id=$1`, updated.ID)
	require.NoError(t, err)
	update = fmt.Sprintf(`{"expected_version":%d,"name":"qa-fo","listener_port":19450,"match_mode":"fallback","balance_mode":"failover",
		"accept_proxy_from":["10.0.0.0/8"],"client_download_mbps":5,"enabled":true,
		"servers":[{"name":"p","host":"127.0.0.1","port":9903},{"name":"r","target_type":"unix","unix_socket_path":"/dev/shm/nf-test.sock"}]}`, updated.Version)
	w = request(t, h, http.MethodPut, base+"/"+created.ID, update, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	noop := decodeRoute(w.Body.Bytes())
	assert.Equal(t, updated.Version, noop.Version)
	assert.Len(t, noop.Servers, 2)
	assert.Equal(t, 1, revisions(), "identical PUT must not publish a revision")

	// B1: PATCH toggles only enabled; response is the full route.
	w = request(t, h, http.MethodPatch, base+"/"+created.ID, fmt.Sprintf(`{"enabled":false,"expected_version":%d}`, noop.Version), testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	off := decodeRoute(w.Body.Bytes())
	assert.False(t, off.Enabled)
	require.Len(t, off.Servers, 2)
	assert.True(t, off.Servers[1].Backup)
	assert.Equal(t, []string{"10.0.0.0/8"}, off.AcceptProxyFrom)
	assert.Equal(t, "failover", off.BalanceMode)
	require.NotNil(t, off.ClientDownloadMbps)
	assert.Equal(t, 2, revisions())
	w = request(t, h, http.MethodPatch, base+"/"+created.ID, fmt.Sprintf(`{"enabled":true,"expected_version":%d}`, noop.Version), testAdminToken)
	assert.Equal(t, http.StatusConflict, w.Code, "stale expected_version")

	// B1: legacy full PUT body without the 1.1.0 fields keeps them.
	legacy := fmt.Sprintf(`{"expected_version":%d,"name":"qa-fo","match_mode":"fallback","listener_ip":"*","listener_port":19450,
		"snis":[],"fallback":true,"target_type":"tcp","target_host":"127.0.0.1","target_port":9903,"health_check":true,
		"proxy_protocol":"none","quota_action":"observe","quota_period":"calendar_month","client_download_mbps":5,"enabled":true,"custom_fragment":""}`, off.Version)
	w = request(t, h, http.MethodPut, base+"/"+created.ID, legacy, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	on := decodeRoute(w.Body.Bytes())
	require.Len(t, on.Servers, 2)
	assert.True(t, on.Servers[1].Backup)
	assert.Equal(t, []string{"10.0.0.0/8"}, on.AcceptProxyFrom)

	// H1: kernel shaper on Agent 1.0.5 is rejected on save.
	setAgent("1.0.5")
	kernel := fmt.Sprintf(`{"expected_version":%d,"name":"qa-fo","listener_port":19450,"match_mode":"fallback","client_download_mbps":5,
		"shaper_mode":"kernel","enabled":true}`, on.Version)
	w = request(t, h, http.MethodPut, base+"/"+created.ID, kernel, testAdminToken)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "kernel_shaper_requires_agent_1_1")
	w = request(t, h, http.MethodPost, base, `{"name":"qa-k","listener_port":19451,"match_mode":"fallback","target_host":"127.0.0.1","target_port":80,"client_upload_mbps":1,"shaper_mode":"kernel"}`, testAdminToken)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())

	// Accepted on Agent 1.1.0.
	setAgent("1.1.0")
	w = request(t, h, http.MethodPut, base+"/"+created.ID, kernel, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	kernelRoute := decodeRoute(w.Body.Bytes())
	assert.Equal(t, ShaperModeKernel, kernelRoute.ShaperMode)
	require.Len(t, kernelRoute.Servers, 2)

	// Agent downgrade: the next publish (any other route change) is rejected
	// instead of silently dropping the limit; render-config too.
	setAgent("1.0.5")
	before := revisions()
	w = request(t, h, http.MethodPost, base, `{"name":"qa-other","listener_port":19452,"match_mode":"fallback","target_host":"127.0.0.1","target_port":81}`, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	other := decodeRoute(w.Body.Bytes())
	w = request(t, h, http.MethodPatch, base+"/"+other.ID, fmt.Sprintf(`{"enabled":true,"expected_version":%d}`, other.Version), testAdminToken)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "kernel_shaper_requires_agent_1_1")
	assert.Equal(t, before, revisions())
	w = request(t, h, http.MethodPost, "/api/v1/nodes/"+node.ID+"/render-config", `{}`, testAdminToken)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	// Disabling the kernel-shaped route is still possible on the old Agent.
	w = request(t, h, http.MethodPatch, base+"/"+created.ID, fmt.Sprintf(`{"enabled":false,"expected_version":%d}`, kernelRoute.Version), testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}
