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

// TestPGStoreBalanceAlgorithmIntegration round-trips the migration 000052
// fields through PostgreSQL and checks the leastping/ip_weights Agent gates
// (opt-in, like the other integration tests).
func TestPGStoreBalanceAlgorithmIntegration(t *testing.T) {
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
	node, err := store.CreateNode(ctx, fmt.Sprintf("balance-%d", suffix), fmt.Sprintf("198.19.%d.%d", (suffix/256)%256, suffix%256), map[string]any{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DeleteNode(context.Background(), node.ID) })
	setAgent := func(version string) {
		_, err := pool.Exec(ctx, `
			INSERT INTO node_heartbeats(node_id,agent_version,status,metrics,received_at) VALUES($1,$2,'online','{}',now())
			ON CONFLICT(node_id) DO UPDATE SET agent_version=EXCLUDED.agent_version`, node.ID, version)
		require.NoError(t, err)
	}
	h := NewHandler(store, Config{AdminToken: testAdminToken})
	base := "/api/v1/nodes/" + node.ID + "/routes"
	decode := func(body []byte) Route {
		var route Route
		require.NoError(t, json.Unmarshal(body, &route))
		return route
	}

	table := `{"name":"tbl","listener_port":19460,"match_mode":"fallback","balance_algorithm":"random","balance_random_draws":1,
		"sticky_mode":"source_table","sticky_ttl":"15m","sticky_table_entries":"1m","sticky_ipv6_prefix":56,"slowstart":"60s",
		"servers":[{"name":"a","host":"192.0.2.1","port":443,"weight":3},{"name":"b","host":"192.0.2.2","port":443}]}`
	w := request(t, h, http.MethodPost, base, table, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	created := decode(w.Body.Bytes())
	get := request(t, h, http.MethodGet, base+"/"+created.ID, "", testAdminToken)
	require.Equal(t, http.StatusOK, get.Code)
	got := decode(get.Body.Bytes())
	assert.Equal(t, "random", got.BalanceAlgorithm)
	assert.Equal(t, 1, got.BalanceRandomDraws)
	assert.Equal(t, StickyModeSourceTable, got.StickyMode)
	assert.Equal(t, "15m", got.StickyTTL)
	assert.Equal(t, "1m", got.StickyTableEntries)
	assert.Equal(t, 56, got.StickyIPv6Prefix)
	assert.Equal(t, "60s", got.Slowstart)
	assert.True(t, got.StickyEnabled)
	require.Len(t, got.Servers, 2)
	assert.Equal(t, 3, got.Servers[0].Weight)

	// Partial PUT without the 000052 keys keeps them.
	legacy := fmt.Sprintf(`{"expected_version":%d,"name":"tbl","listener_port":19460,"match_mode":"fallback","enabled":false}`, got.Version)
	w = request(t, h, http.MethodPut, base+"/"+created.ID, legacy, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	kept := decode(w.Body.Bytes())
	assert.Equal(t, "random", kept.BalanceAlgorithm)
	assert.Equal(t, "1m", kept.StickyTableEntries)
	assert.Equal(t, "60s", kept.Slowstart)
	assert.Equal(t, 3, kept.Servers[0].Weight)
	require.NotNil(t, kept.ClientIPv6)
	assert.True(t, *kept.ClientIPv6, "client_ipv6 defaults to true")

	// client_ipv6=false round-trips, clears the prefix and survives a
	// partial PUT without the key.
	v4only := fmt.Sprintf(`{"expected_version":%d,"name":"tbl","listener_port":19460,"match_mode":"fallback","enabled":false,
		"sticky_mode":"source_table","sticky_ipv6_prefix":56,"client_ipv6":false,
		"servers":[{"name":"a","host":"192.0.2.1","port":443,"weight":3},{"name":"b","host":"192.0.2.2","port":443}]}`, kept.Version)
	w = request(t, h, http.MethodPut, base+"/"+created.ID, v4only, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	off := decode(w.Body.Bytes())
	require.NotNil(t, off.ClientIPv6)
	assert.False(t, *off.ClientIPv6)
	assert.Zero(t, off.StickyIPv6Prefix)
	w = request(t, h, http.MethodPut, base+"/"+created.ID, fmt.Sprintf(`{"expected_version":%d,"name":"tbl","listener_port":19460,"match_mode":"fallback","enabled":false}`, off.Version), testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	offKept := decode(w.Body.Bytes())
	require.NotNil(t, offKept.ClientIPv6)
	assert.False(t, *offKept.ClientIPv6)

	// leastping and ip_weights need Agent 1.1.0 on save.
	setAgent("1.0.8")
	leastping := `{"name":"lp","listener_port":19461,"match_mode":"fallback","balance_algorithm":"leastping","leastping_tolerance":0.3,
		"servers":[{"name":"a","host":"192.0.2.1","port":443,"cost":2.5},{"name":"b","host":"192.0.2.2","port":443}]}`
	w = request(t, h, http.MethodPost, base, leastping, testAdminToken)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "leastping_requires_agent_1_1")
	ipWeights := `{"name":"ipw","listener_port":19462,"match_mode":"fallback",
		"servers":[{"name":"dp","host":"pool.example.com","port":443,"dns_pool":true,"ip_weights":[{"ip":"192.0.2.9","weight":40}]},{"name":"b","host":"192.0.2.2","port":443}]}`
	w = request(t, h, http.MethodPost, base, ipWeights, testAdminToken)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "ip_weights_requires_agent_1_1")

	setAgent("1.1.0")
	w = request(t, h, http.MethodPost, base, leastping, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	lp := decode(w.Body.Bytes())
	assert.Equal(t, "leastping", lp.BalanceAlgorithm)
	assert.InDelta(t, 0.3, lp.LeastPingTolerance, 1e-9)
	assert.InDelta(t, 2.5, lp.Servers[0].Cost, 1e-9)
	assert.True(t, lp.HealthCheck)
	w = request(t, h, http.MethodPost, base, ipWeights, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	ipw := decode(w.Body.Bytes())
	require.Len(t, ipw.Servers, 2)
	assert.Equal(t, []RouteServerIPWeight{{IP: "192.0.2.9", Weight: 40}}, ipw.Servers[0].IPWeights)
	assert.Equal(t, StickyModeSource, ipw.StickyMode, "DNS pool defaults to the source hash")

	// Publish gate after an Agent downgrade.
	w = request(t, h, http.MethodPatch, base+"/"+lp.ID, fmt.Sprintf(`{"enabled":true,"expected_version":%d}`, lp.Version), testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	setAgent("1.0.8")
	w = request(t, h, http.MethodPost, "/api/v1/nodes/"+node.ID+"/render-config", `{}`, testAdminToken)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "leastping_requires_agent_1_1")

	// sni is gone.
	w = request(t, h, http.MethodPost, base, `{"name":"sni","listener_port":19463,"match_mode":"sni","snis":["a.example.com"],"sticky_mode":"sni",
		"servers":[{"name":"a","host":"192.0.2.1","port":443},{"name":"b","host":"192.0.2.2","port":443}]}`, testAdminToken)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "sticky_mode sni was removed")

	// leastconn: cost alone needs no Agent; balance_tolerance > 0 needs 1.1.1
	// and round-trips through the leastping_tolerance column under both keys.
	setAgent("1.1.0")
	leastconn := `{"name":"lc","listener_port":19464,"match_mode":"fallback","balance_mode":"pool","balance_algorithm":"leastconn","balance_tolerance":0.25,
		"servers":[{"name":"a","host":"192.0.2.1","port":443,"cost":2},{"name":"b","host":"192.0.2.2","port":443}]}`
	w = request(t, h, http.MethodPost, base, leastconn, testAdminToken)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "leastconn_tolerance_requires_agent_1_1_1")
	costOnly := `{"name":"lc0","listener_port":19465,"match_mode":"fallback","balance_mode":"pool","balance_algorithm":"leastconn",
		"servers":[{"name":"a","host":"192.0.2.1","port":443,"cost":2},{"name":"b","host":"192.0.2.2","port":443}]}`
	w = request(t, h, http.MethodPost, base, costOnly, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	setAgent("1.1.1")
	w = request(t, h, http.MethodPost, base, leastconn, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"balance_tolerance":0.25`)
	lc := decode(w.Body.Bytes())
	get = request(t, h, http.MethodGet, base+"/"+lc.ID, "", testAdminToken)
	require.Equal(t, http.StatusOK, get.Code)
	lcGot := decode(get.Body.Bytes())
	assert.InDelta(t, 0.25, lcGot.LeastPingTolerance, 1e-9)
	assert.InDelta(t, 0.25, lcGot.BalanceTolerance, 1e-9)
	assert.InDelta(t, 2, lcGot.Servers[0].Cost, 1e-9)
	// A partial PUT without either tolerance key keeps it.
	w = request(t, h, http.MethodPut, base+"/"+lc.ID, fmt.Sprintf(`{"expected_version":%d,"name":"lc","listener_port":19464,"match_mode":"fallback","enabled":false}`, lcGot.Version), testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.InDelta(t, 0.25, decode(w.Body.Bytes()).BalanceTolerance, 1e-9)
}
