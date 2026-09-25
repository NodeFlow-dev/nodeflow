package panel

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// singleServerEditorPayload is what the 2.0.0 route editor sends for a new
// fallback route with one plain IP server (no DNS pool, no weight, health
// check on, PROXY off): the legacy single-target shape (servers: []) with
// sticky_mode "none". 2.0.0 rejected it with "sticky_mode must be source,
// source_table, leastconn or roundrobin".
func singleServerEditorPayload(stickyMode string) string {
	return `{"name":"srv1-route","hostname":"","listener_ip":"217.16.26.34","listener_port":443,
		"match_mode":"fallback","snis":[],"fallback":true,
		"target_type":"tcp","target_host":"192.168.1.1","target_port":443,"dns_pool":false,"unix_socket_path":"",
		"health_check":true,"proxy_protocol":"none","quota_bytes":null,"quota_action":"observe","quota_period":"calendar_month",
		"client_upload_mbps":null,"client_download_mbps":null,"enabled":true,"custom_fragment":"",
		"accept_proxy_from":[],"servers":[],
		"sticky_enabled":false,"sticky_mode":"` + stickyMode + `","sticky_hash":"","sticky_hash_balance_factor":0,
		"sticky_table_entries":"","sticky_ipv6_prefix":0,"client_ipv6":true,"balance_algorithm":""}`
}

func TestSingleServerStickyModeNoneAccepted(t *testing.T) {
	spec, got := renderPayload(t, singleServerEditorPayload(StickyModeNone))
	assert.Empty(t, spec.Servers)
	assert.Equal(t, StickyModeRoundRobin, spec.StickyMode, "flat none is stored as the legacy roundrobin")
	assert.False(t, spec.StickyEnabled)
	assert.Equal(t, "", spec.BalanceAlgorithm)
	assert.Equal(t, "", spec.StickyTTL)

	// Byte-identical to the same route saved without sticky_mode (the
	// pre-existing derivation) and with the legacy explicit roundrobin.
	for _, legacy := range []string{"", StickyModeRoundRobin, "  NONE "} {
		_, want := renderPayload(t, singleServerEditorPayload(legacy))
		assert.Equal(t, want.Config, got.Config, "sticky_mode %q", legacy)
	}

	backend := backendSection(got.Config)
	require.NotEmpty(t, backend)
	assert.Contains(t, backend, "192.168.1.1:443")
	assert.NotContains(t, backend, "balance ")
	assert.NotContains(t, backend, "stick")
	assert.True(t, strings.Contains(got.Config, "217.16.26.34:443"))
}

// The same single server sent via servers[] (older/other API clients) with
// sticky_mode none is folded into the flat route and must render the same.
func TestSingleServerViaServersStickyModeNone(t *testing.T) {
	_, flat := renderPayload(t, singleServerEditorPayload(StickyModeNone))
	payload := strings.Replace(singleServerEditorPayload(StickyModeNone), `"servers":[]`,
		`"servers":[{"name":"srv1","target_type":"tcp","host":"192.168.1.1","port":443}]`, 1)
	spec, got := renderPayload(t, payload)
	assert.Empty(t, spec.Servers)
	assert.Equal(t, flat.Config, got.Config)
}

func TestSingleServerStickyModeInvalidStillRejected(t *testing.T) {
	_, err := validatePayload(t, singleServerEditorPayload("hash"))
	require.ErrorContains(t, err, "sticky_mode must be none, source, source_table, leastconn or roundrobin")
}
