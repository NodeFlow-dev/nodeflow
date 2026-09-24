package panel

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Migration 000052: balance_algorithm (server choice) and sticky_mode
// (client stickiness) as two orthogonal pool-mode settings, plus slowstart,
// per-server weight/cost/ip_weights and the Agent weight controller
// annotations.

// --- Validation: field/range/combination errors -----------------------

func TestBalanceAlgorithmFieldValidation(t *testing.T) {
	base := `{"name":"p","match_mode":"fallback",` + poolServers
	cases := []struct {
		name, payload, wantErr string
	}{
		{"invalid algorithm", base + `,"balance_algorithm":"weighted"}`, "balance_algorithm must be"},
		{"random draws too low", base + `,"balance_algorithm":"random","balance_random_draws":0}`, ""}, // 0 = unset, valid
		{"random draws out of range", base + `,"balance_algorithm":"random","balance_random_draws":6}`, "balance_random_draws must be between 1 and 5"},
		{"leastping tolerance negative", base + `,"balance_algorithm":"leastping","leastping_tolerance":-0.1}`, "leastping_tolerance must be"},
		{"leastping tolerance too high", base + `,"balance_algorithm":"leastping","leastping_tolerance":1.5}`, "leastping_tolerance must be"},
		{"leastping tolerance_ms negative", base + `,"balance_algorithm":"leastping","leastping_tolerance_ms":-1}`, "leastping_tolerance_ms must be"},
		{"leastping tolerance_ms too high", base + `,"balance_algorithm":"leastping","leastping_tolerance_ms":1001}`, "leastping_tolerance_ms must be"},
		{"sticky_hash invalid", base + `,"sticky_mode":"source","sticky_hash":"fnv"}`, "sticky_hash must be"},
		{"hash_balance_factor too low", base + `,"sticky_mode":"source","sticky_hash_balance_factor":100}`, "sticky_hash_balance_factor must be"},
		{"hash_balance_factor too high", base + `,"sticky_mode":"source","sticky_hash_balance_factor":1001}`, "sticky_hash_balance_factor must be"},
		{"hash_balance_factor with map-based", base + `,"sticky_mode":"source","sticky_hash":"map-based","sticky_hash_balance_factor":150}`, "requires sticky_hash consistent"},
		{"sticky_table_entries invalid", base + `,"sticky_mode":"source_table","sticky_table_entries":"1g"}`, "sticky_table_entries must be"},
		{"sticky_table_entries too big", base + `,"sticky_mode":"source_table","sticky_table_entries":"11m"}`, "sticky_table_entries must be"},
		{"sticky_table_entries leading zero", base + `,"sticky_mode":"source_table","sticky_table_entries":"050k"}`, "sticky_table_entries must be"},
		{"sticky_table_entries bare number", base + `,"sticky_mode":"source_table","sticky_table_entries":"50000"}`, "sticky_table_entries must be"},
		{"ipv6 prefix too low", base + `,"sticky_mode":"source","sticky_ipv6_prefix":31}`, "sticky_ipv6_prefix must be"},
		{"ipv6 prefix too high", base + `,"sticky_mode":"source","sticky_ipv6_prefix":129}`, "sticky_ipv6_prefix must be"},
		{"slowstart bad format", base + `,"slowstart":"30x"}`, "slowstart must be"},
		{"slowstart too long", base + `,"slowstart":"11m"}`, "slowstart must be"},
		{"slowstart requires health_check", base + `,"slowstart":"30s","health_check":false}`, "slowstart requires health_check"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validatePayload(t, tc.payload)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestBalanceRandomDrawsDefaultAndExplicit(t *testing.T) {
	spec, err := validatePayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"random",`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, 2, spec.BalanceRandomDraws)

	spec, err = validatePayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"random","balance_random_draws":1,`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, 1, spec.BalanceRandomDraws)

	spec, err = validatePayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"random","balance_random_draws":5,`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, 5, spec.BalanceRandomDraws)

	// Stored only for random: dropped for any other algorithm.
	spec, err = validatePayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn","balance_random_draws":2,`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, 0, spec.BalanceRandomDraws)
}

func TestLeastPingToleranceDefaultAndStoredOnlyForLeastPing(t *testing.T) {
	spec, err := validatePayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastping",`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, 0.2, spec.LeastPingTolerance)
	assert.True(t, spec.HealthCheck, "leastping forces health_check")

	spec, err = validatePayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastping","leastping_tolerance":0,"leastping_tolerance_ms":50,`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, 0.0, spec.LeastPingTolerance)
	assert.Equal(t, 50, spec.LeastPingToleranceMS)

	// leastconn keeps the relative tolerance (connection-load tolerance) but
	// never the latency-only millisecond floor.
	spec, err = validatePayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn","leastping_tolerance":0.5,"leastping_tolerance_ms":50,`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, 0.5, spec.LeastPingTolerance)
	assert.Equal(t, 0, spec.LeastPingToleranceMS)

	// leastconn without a tolerance stays plain HAProxy leastconn (no 0.2
	// leastping default).
	spec, err = validatePayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn",`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, 0.0, spec.LeastPingTolerance)

	// balance_tolerance is the algorithm-neutral alias.
	spec, err = validatePayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn","balance_tolerance":0.25,`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, 0.25, spec.LeastPingTolerance)
	_, err = validatePayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn","balance_tolerance":0.25,"leastping_tolerance":0.3,`+poolServers+`}`)
	require.ErrorContains(t, err, "must be equal")

	// Dropped for any other algorithm and for sticky source (the hash
	// ignores the algorithm).
	spec, err = validatePayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"roundrobin","leastping_tolerance":0.5,"leastping_tolerance_ms":50,`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, 0.0, spec.LeastPingTolerance)
	assert.Equal(t, 0, spec.LeastPingToleranceMS)
	spec, err = validatePayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn","sticky_mode":"source","balance_tolerance":0.5,`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, 0.0, spec.LeastPingTolerance)
}

func TestServerWeightCostValidation(t *testing.T) {
	for name, servers := range map[string]string{
		"weight too high": `"servers":[{"name":"a","host":"192.0.2.1","port":443,"weight":257},{"name":"b","host":"192.0.2.2","port":443}]`,
		"weight negative": `"servers":[{"name":"a","host":"192.0.2.1","port":443,"weight":-1},{"name":"b","host":"192.0.2.2","port":443}]`,
		"cost too high":   `"servers":[{"name":"a","host":"192.0.2.1","port":443,"cost":101},{"name":"b","host":"192.0.2.2","port":443}]`,
		"cost negative":   `"servers":[{"name":"a","host":"192.0.2.1","port":443,"cost":-1},{"name":"b","host":"192.0.2.2","port":443}]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validatePayload(t, `{"name":"p","match_mode":"fallback",`+servers+`}`)
			require.Error(t, err)
		})
	}
	spec, err := validatePayload(t, `{"name":"p","match_mode":"fallback","servers":[{"name":"a","host":"192.0.2.1","port":443,"weight":256,"cost":100},{"name":"b","host":"192.0.2.2","port":443}]}`)
	require.NoError(t, err)
	require.Len(t, spec.Servers, 2)
	assert.Equal(t, 256, spec.Servers[0].Weight)
	assert.Equal(t, 100.0, spec.Servers[0].Cost)
}

func TestIPWeightsValidation(t *testing.T) {
	dnsServer := func(ipWeights string) string {
		return `{"name":"dp","host":"pool.example.com","port":443,"dns_pool":true,"ip_weights":[` + ipWeights + `]}`
	}
	cases := []struct {
		name, ipWeights, wantErr string
	}{
		{"weight too high", `{"ip":"192.0.2.1","weight":257}`, "weight must be"},
		{"weight zero without cost", `{"ip":"192.0.2.1","weight":0}`, "must set weight, cost or both"},
		{"weight negative", `{"ip":"192.0.2.1","weight":-1}`, "weight must be"},
		{"cost negative", `{"ip":"192.0.2.1","weight":1,"cost":-0.5}`, "cost must be"},
		{"cost too high", `{"ip":"192.0.2.1","weight":1,"cost":100.5}`, "cost must be"},
		{"cost rounds to zero", `{"ip":"192.0.2.1","cost":0.001}`, "cost must be at least 0.01"},
		{"bad ip", `{"ip":"not-an-ip","weight":10}`, "must be a valid IP"},
		{"duplicate ip", `{"ip":"192.0.2.1","weight":10},{"ip":"192.0.2.1","weight":20}`, "duplicate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validatePayload(t, `{"name":"p","match_mode":"fallback","balance_mode":"pool","servers":[`+dnsServer(tc.ipWeights)+`,{"name":"b","host":"192.0.2.2","port":443}]}`)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	// requires dns_pool
	_, err := validatePayload(t, `{"name":"p","match_mode":"fallback","servers":[{"name":"a","host":"192.0.2.1","port":443,"ip_weights":[{"ip":"192.0.2.1","weight":10}]},{"name":"b","host":"192.0.2.2","port":443}]}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ip_weights requires dns_pool")

	// max entries
	var many string
	for i := 0; i < maxRouteServerIPWeights+1; i++ {
		if i > 0 {
			many += ","
		}
		many += `{"ip":"10.0.` + string(rune('0'+i/256)) + `.` + string(rune('0'+i%256%10)) + `","weight":1}`
	}
	_, err = validatePayload(t, `{"name":"p","match_mode":"fallback","servers":[`+dnsServer(many)+`,{"name":"b","host":"192.0.2.2","port":443}]}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not exceed")

	// valid
	spec, err := validatePayload(t, `{"name":"p","match_mode":"fallback","servers":[`+dnsServer(`{"ip":"192.0.2.1","weight":10},{"ip":"2001:0db8::1","weight":20}`)+`,{"name":"b","host":"192.0.2.2","port":443}]}`)
	require.NoError(t, err)
	require.Len(t, spec.Servers[0].IPWeights, 2)
	assert.Equal(t, "192.0.2.1", spec.Servers[0].IPWeights[0].IP)
	assert.Equal(t, "2001:db8::1", spec.Servers[0].IPWeights[1].IP, "IPv6 canonicalised")
}

func TestIPWeightsRequiresDynamicWeightAlgorithm(t *testing.T) {
	dnsServer := `{"name":"dp","host":"pool.example.com","port":443,"dns_pool":true,"ip_weights":[{"ip":"192.0.2.1","weight":10}]}`
	payload := func(extra string) string {
		return `{"name":"p","match_mode":"fallback","balance_mode":"pool","servers":[` + dnsServer + `,{"name":"b","host":"192.0.2.2","port":443}]` + extra + `}`
	}
	_, err := validatePayload(t, payload(`,"balance_algorithm":"static-rr"`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ip_weights need a dynamic-weight algorithm")

	_, err = validatePayload(t, payload(`,"sticky_mode":"source","sticky_hash":"map-based"`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ip_weights need a dynamic-weight algorithm")

	// Consistent hash is dynamic: allowed.
	_, err = validatePayload(t, payload(`,"sticky_mode":"source"`))
	require.NoError(t, err)

	// leastconn/leastping/random are dynamic: allowed.
	_, err = validatePayload(t, payload(`,"balance_algorithm":"leastconn"`))
	require.NoError(t, err)
}

// --- Rendering per balance_algorithm ------------------------------------

func TestBalanceAlgorithmRenderCombinations(t *testing.T) {
	cases := []struct {
		name, payload, want string
		absent              []string
	}{
		{"static-rr", `{"name":"p","match_mode":"fallback","balance_algorithm":"static-rr",` + poolServers + `}`,
			"    balance static-rr\n", nil},
		{"random default draws", `{"name":"p","match_mode":"fallback","balance_algorithm":"random",` + poolServers + `}`,
			"    balance random(2)\n", nil},
		{"random draws=1", `{"name":"p","match_mode":"fallback","balance_algorithm":"random","balance_random_draws":1,` + poolServers + `}`,
			"    balance random(1)\n", nil},
		{"leastconn", `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn",` + poolServers + `}`,
			"    balance leastconn\n", nil},
		{"roundrobin explicit", `{"name":"p","match_mode":"fallback","balance_algorithm":"roundrobin",` + poolServers + `}`,
			"", []string{"balance"}},
		{"leastping renders no balance line", `{"name":"p","match_mode":"fallback","balance_algorithm":"leastping",` + poolServers + `}`,
			"", []string{"balance "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got := renderPayload(t, tc.payload)
			backend := backendSection(got.Config)
			if tc.want != "" {
				assert.Contains(t, backend, tc.want)
			}
			for _, s := range tc.absent {
				assert.NotContains(t, backend, s)
			}
		})
	}
}

func TestSourceTableUsesAlgorithmAndDefaultsToLeastConn(t *testing.T) {
	// balance_algorithm absent with sticky_mode=source_table input: rc12
	// leastconn default.
	_, got := renderPayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source_table",`+poolServers+`}`)
	assert.Contains(t, backendSection(got.Config), "    balance leastconn\n")

	// Explicit algorithm is honoured.
	_, got = renderPayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source_table","balance_algorithm":"static-rr",`+poolServers+`}`)
	assert.Contains(t, backendSection(got.Config), "    balance static-rr\n")

	// roundrobin/leastping render no balance line, table still applies.
	_, got = renderPayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source_table","balance_algorithm":"roundrobin",`+poolServers+`}`)
	backend := backendSection(got.Config)
	assert.NotContains(t, backend, "balance ")
	assert.Contains(t, backend, "    stick-table type ipv6")
}

func TestSourceTableEntriesAndIPv6Prefix(t *testing.T) {
	cases := map[string]string{
		"":     "1m", // «Авто» without known node RAM
		"10k":  "10k",
		"100k": "100k",
		"1m":   "1m",
		"50k":  "50k",
		"2m":   "2m",
	}
	for entries, size := range cases {
		extra := ""
		if entries != "" {
			extra = `,"sticky_table_entries":"` + entries + `"`
		}
		_, got := renderPayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source_table"`+extra+`,`+poolServers+`}`)
		assert.Contains(t, backendSection(got.Config), "stick-table type ipv6 size "+size+" expire 1h peers nf_peers\n", entries)
	}

	_, got := renderPayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source_table","sticky_ipv6_prefix":48,`+poolServers+`}`)
	assert.Contains(t, backendSection(got.Config), "    stick on src,ipmask(32,48)\n")

	_, got = renderPayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source_table",`+poolServers+`}`)
	assert.Contains(t, backendSection(got.Config), "    stick on src,ipmask(32,64)\n", "default prefix is 64")
}

func TestSourceHashRenderVariants(t *testing.T) {
	// Default (prefix 0): byte-identical to the pre-052 output.
	_, got := renderPayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source",`+poolServers+`}`)
	assert.Contains(t, backendSection(got.Config), "    balance source\n    hash-type consistent sdbm avalanche\n")

	// prefix=128 is also plain "balance source".
	_, got = renderPayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source","sticky_ipv6_prefix":128,`+poolServers+`}`)
	assert.Contains(t, backendSection(got.Config), "    balance source\n")

	// Any other prefix uses the masked hash.
	_, got = renderPayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source","sticky_ipv6_prefix":56,`+poolServers+`}`)
	backend := backendSection(got.Config)
	assert.Contains(t, backend, "    balance hash src,ipmask(32,56)\n")
	assert.NotContains(t, backend, "balance source")

	// map-based hash type.
	_, got = renderPayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source","sticky_hash":"map-based",`+poolServers+`}`)
	assert.Contains(t, backendSection(got.Config), "    hash-type map-based sdbm avalanche\n")

	// hash-balance-factor only with consistent.
	_, got = renderPayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source","sticky_hash_balance_factor":150,`+poolServers+`}`)
	assert.Contains(t, backendSection(got.Config), "    hash-balance-factor 150\n")
}

// --- Peers section --------------------------------------------------------

func TestPeersSectionOnlyForSourceTable(t *testing.T) {
	noTable := fallbackRoute(testRouteID)
	got, err := RenderHAProxyConfig([]Route{noTable})
	require.NoError(t, err)
	assert.NotContains(t, got.Config, "localpeer")
	assert.NotContains(t, got.Config, "peers nf_peers")

	withTable := Route{
		ID: testRouteID, Name: "table", MatchMode: "fallback", Fallback: true, ListenerIP: "*", ListenerPort: 443,
		TargetType: "tcp", TargetHost: "192.0.2.1", TargetPort: 443, HealthCheck: true, ProxyProtocol: "none", Enabled: true,
		BalanceMode: "pool", StickyMode: StickyModeSourceTable, StickyTTL: "1h",
		Servers: []RouteServer{
			{Position: 1, Name: "a", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
			{Position: 2, Name: "b", TargetType: "tcp", Host: "192.0.2.2", Port: 443},
		},
	}
	got, err = RenderHAProxyConfig([]Route{withTable})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    localpeer nf_local\n")
	assert.Contains(t, got.Config, "\npeers nf_peers\n    bind unix@/run/haproxy/nf_peers.sock\n    server nf_local\n")
	// Peers section is declared once, before the first frontend.
	assert.Equal(t, 1, strings.Count(got.Config, "\npeers nf_peers\n"))
	assert.Contains(t, got.Config, "expire 1h peers nf_peers\n")
	assert.True(t, strings.Index(got.Config, "\npeers nf_peers\n") < strings.Index(got.Config, "\nfrontend "))
}

// --- weight / slowstart render -------------------------------------------

func TestWeightAndSlowstartOnServerLines(t *testing.T) {
	payload := `{"name":"p","match_mode":"fallback","balance_mode":"failover","slowstart":"30s",
		"servers":[{"name":"pref","host":"vpn.example.com","port":443,"dns_pool":true,"preferred_ip":"192.0.2.10","weight":200}]}`
	_, got := renderPayload(t, payload)
	backend := backendSection(got.Config)
	assert.Contains(t, backend, "    server pref_pref 192.0.2.10:443 weight 200 slowstart 30s check")
	assert.Contains(t, backend, "    server-template pref_ 32 vpn.example.com:443 weight 200 slowstart 30s check")
	_, got = renderPayload(t, `{"name":"p","match_mode":"fallback","slowstart":"30s",
		"servers":[{"name":"st","host":"192.0.2.2","port":443,"weight":50},{"name":"b","host":"192.0.2.3","port":443}]}`)
	backend2 := backendSection(got.Config)
	assert.Contains(t, backend2, "    server st 192.0.2.2:443 weight 50 slowstart 30s check")
	assert.Contains(t, backend2, "    server b 192.0.2.3:443 slowstart 30s check")
	assert.Contains(t, backend, "    server-template pref_ 32 vpn.example.com:443 weight 200 slowstart 30s check inter 5s fall 3 rise 2 resolvers nf_dns init-addr last,none resolve-opts prevent-dup-ip hash-key addr backup\n")
}

func TestSlowstartOnSingleTargetServerLine(t *testing.T) {
	_, got := renderPayload(t, `{"name":"p","match_mode":"fallback","target_host":"192.0.2.1","target_port":443,"slowstart":"60s"}`)
	assert.Contains(t, got.Config, "slowstart 60s check inter 5s fall 3 rise 2\n")
}

// --- Agent weight-controller annotations ----------------------------------

func TestWeightAnnotationsLeastPing(t *testing.T) {
	payload := `{"name":"p","match_mode":"fallback","balance_algorithm":"leastping","leastping_tolerance":0.3,"leastping_tolerance_ms":20,
		"servers":[{"name":"a","host":"192.0.2.1","port":443,"weight":100,"cost":1.5},{"name":"b","host":"192.0.2.2","port":443}]}`
	_, got := renderPayload(t, payload)
	backend := backendSection(got.Config)
	assert.Contains(t, backend, "# nf-weights backend=nf_be_"+routeRuntimeID(reviewRouteID)+" algo=leastping tolerance=0.30 tolerance_ms=20\n")
	assert.Contains(t, backend, "# nf-weight server=a base=100 cost=1.50\n")
	assert.Contains(t, backend, "# nf-weight server=b base=1 cost=0.00\n")
}

func TestWeightAnnotationsStaticIPWeights(t *testing.T) {
	payload := `{"name":"p","match_mode":"fallback","balance_mode":"pool",
		"servers":[{"name":"dp","host":"pool.example.com","port":443,"dns_pool":true,"weight":10,
			"ip_weights":[{"ip":"192.0.2.5","weight":30},{"ip":"2001:0db8::5","weight":40}]},
			{"name":"b","host":"192.0.2.2","port":443}]}`
	_, got := renderPayload(t, payload)
	backend := backendSection(got.Config)
	assert.Contains(t, backend, "algo=static")
	assert.Contains(t, backend, "# nf-weight template=dp_ slots=32 base=10 cost=0.00 ips=192.0.2.5=30,2001:db8::5=40\n")
	// algo=static manages only templates with ip_weights; the plain server
	// keeps its configured weight.
	assert.NotContains(t, backend, "server=b base=")
}

func TestIPCostValidationAndStorage(t *testing.T) {
	dnsServer := `{"name":"dp","host":"pool.example.com","port":443,"dns_pool":true,"ip_weights":[{"ip":"192.0.2.1","cost":1.234},{"ip":"192.0.2.2","weight":20,"cost":2},{"ip":"192.0.2.3","weight":5}]}`
	spec, err := validatePayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastping","servers":[`+dnsServer+`,{"name":"b","host":"192.0.2.9","port":443}]}`)
	require.NoError(t, err)
	assert.Equal(t, []RouteServerIPWeight{
		{IP: "192.0.2.1", Weight: 0, Cost: 1.23},
		{IP: "192.0.2.2", Weight: 20, Cost: 2},
		{IP: "192.0.2.3", Weight: 5},
	}, spec.Servers[0].IPWeights)
}

func TestWeightAnnotationsLeastPingIPCosts(t *testing.T) {
	// A DNS pool defaults to sticky source (algorithm inert): latency mode
	// needs sticky none explicitly.
	payload := `{"name":"p","match_mode":"fallback","balance_algorithm":"leastping","sticky_mode":"none",
		"servers":[{"name":"dp","host":"pool.example.com","port":443,"dns_pool":true,"weight":10,"cost":1.5,
			"ip_weights":[{"ip":"192.0.2.5","cost":3},{"ip":"192.0.2.6","weight":40,"cost":0.5},{"ip":"192.0.2.7","weight":7}]},
			{"name":"b","host":"192.0.2.2","port":443}]}`
	_, got := renderPayload(t, payload)
	backend := backendSection(got.Config)
	assert.Contains(t, backend, "# nf-weight template=dp_ slots=32 base=10 cost=1.50 ips=192.0.2.6=40,192.0.2.7=7 ipcosts=192.0.2.5=3.00,192.0.2.6=0.50\n")
	assert.Contains(t, backend, "# nf-weight server=b base=1 cost=0.00\n")
}

func TestWeightAnnotationsCostOnlyIPWeightsInertWithoutLeastPing(t *testing.T) {
	// A per-IP cost only scales latency: without leastping it changes
	// nothing, so no controller annotations are rendered for it and the
	// static-weight restriction does not apply.
	cost := `{"name":"dp","host":"pool.example.com","port":443,"dns_pool":true,"ip_weights":[{"ip":"192.0.2.5","cost":3}]}`
	payload := `{"name":"p","match_mode":"fallback","balance_algorithm":"static-rr","servers":[` + cost + `,{"name":"b","host":"192.0.2.2","port":443}]}`
	_, got := renderPayload(t, payload)
	assert.NotContains(t, got.Config, "nf-weight")

	// Mixed: the weighted entry is rendered in ips=, the cost-only entry is
	// left out and no ipcosts= appears outside leastping.
	mixed := `{"name":"dp","host":"pool.example.com","port":443,"dns_pool":true,"ip_weights":[{"ip":"192.0.2.5","cost":3},{"ip":"192.0.2.6","weight":9,"cost":2}]}`
	_, got = renderPayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn","servers":[`+mixed+`,{"name":"b","host":"192.0.2.2","port":443}]}`)
	backend := backendSection(got.Config)
	assert.Contains(t, backend, "algo=static")
	assert.Contains(t, backend, "# nf-weight template=dp_ slots=32 base=1 cost=0.00 ips=192.0.2.6=9\n")
	assert.NotContains(t, backend, "ipcosts=")
}

func TestIPWeightsWithoutCostKeepStoredJSONShape(t *testing.T) {
	// Entries written before per-IP cost existed must serialise (DB jsonb,
	// fingerprint) exactly as before: cost is omitted when unset.
	encoded, err := json.Marshal([]RouteServerIPWeight{{IP: "192.0.2.1", Weight: 10}})
	require.NoError(t, err)
	assert.Equal(t, `[{"ip":"192.0.2.1","weight":10}]`, string(encoded))
}

func TestWeightAnnotationsAbsentWithoutLeastPingOrIPWeights(t *testing.T) {
	_, got := renderPayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn",`+poolServers+`}`)
	assert.NotContains(t, got.Config, "nf-weight")
}

func TestWeightAnnotationsInertWhenStickySource(t *testing.T) {
	// leastping is not "effective" while sticky=source: no annotations.
	payload := `{"name":"p","match_mode":"fallback","balance_algorithm":"leastping","sticky_mode":"source",` + poolServers + `}`
	_, got := renderPayload(t, payload)
	assert.NotContains(t, got.Config, "nf-weight")
}

// --- Agent version gates (leastping / ip_weights) -------------------------

func TestLeastPingIPWeightsAgentPredicates(t *testing.T) {
	leastPingSpec := RouteSpec{BalanceAlgorithm: BalanceAlgorithmLeastPing, BalanceMode: "pool", StickyMode: StickyModeNone}
	assert.True(t, specNeedsLeastPingAgent(leastPingSpec))
	sourceSpec := leastPingSpec
	sourceSpec.StickyMode = StickyModeSource
	assert.False(t, specNeedsLeastPingAgent(sourceSpec), "source ignores the algorithm")
	failoverSpec := leastPingSpec
	failoverSpec.BalanceMode = "failover"
	assert.False(t, specNeedsLeastPingAgent(failoverSpec))

	ipWeightsSpec := RouteSpec{Servers: []RouteServerSpec{{IPWeights: []RouteServerIPWeight{{IP: "192.0.2.1", Weight: 10}}}}}
	assert.True(t, specNeedsIPWeightsAgent(ipWeightsSpec))
	assert.False(t, specNeedsIPWeightsAgent(RouteSpec{}))
}

func TestLeastPingAgentGateAtPublish(t *testing.T) {
	route := fallbackRoute(testRouteID)
	route.BalanceMode = "pool"
	route.BalanceAlgorithm = BalanceAlgorithmLeastPing
	route.StickyMode = StickyModeNone
	route.Servers = []RouteServer{
		{Position: 1, Name: "a", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
		{Position: 2, Name: "b", TargetType: "tcp", Host: "192.0.2.2", Port: 443},
	}
	for _, version := range []string{"1.0.5", ""} {
		err := checkLeastPingAgentRoutes([]Route{route}, version)
		var leastPingErr *LeastPingUnsupportedError
		require.ErrorAs(t, err, &leastPingErr, version)
		status, code, _ := renderErrorResponse(err)
		assert.Equal(t, 422, status)
		assert.Equal(t, "leastping_requires_agent_1_1", code)
	}
	assert.NoError(t, checkLeastPingAgentRoutes([]Route{route}, "1.1.0"))

	f := &fakeStore{nodes: []Node{{ID: testNodeID}}, routes: []Route{route}, agentVersion: "1.0.5"}
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/render-config", `{}`, testAdminToken)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"leastping_requires_agent_1_1"`)
	f.agentVersion = "1.1.0"
	w = request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/render-config", `{}`, testAdminToken)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestIPWeightsAgentGateAtPublish(t *testing.T) {
	route := fallbackRoute(testRouteID)
	route.BalanceMode = "pool"
	route.Servers = []RouteServer{
		{Position: 1, Name: "dp", TargetType: "tcp", Host: "pool.example.com", Port: 443, DNSPool: true,
			IPWeights: []RouteServerIPWeight{{IP: "192.0.2.1", Weight: 10}}},
		{Position: 2, Name: "b", TargetType: "tcp", Host: "192.0.2.2", Port: 443},
	}
	for _, version := range []string{"1.0.5", ""} {
		err := checkIPWeightsAgentRoutes([]Route{route}, version)
		var ipWeightsErr *IPWeightsUnsupportedError
		require.ErrorAs(t, err, &ipWeightsErr, version)
		status, code, _ := renderErrorResponse(err)
		assert.Equal(t, 422, status)
		assert.Equal(t, "ip_weights_requires_agent_1_1", code)
	}
	assert.NoError(t, checkIPWeightsAgentRoutes([]Route{route}, "1.1.0"))

	f := &fakeStore{nodes: []Node{{ID: testNodeID}}, routes: []Route{route}, agentVersion: "1.0.5"}
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/render-config", `{}`, testAdminToken)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"ip_weights_requires_agent_1_1"`)
	f.agentVersion = "1.1.0"
	w = request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/render-config", `{}`, testAdminToken)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// --- Fingerprint compatibility for migrated representations ---------------

func TestFingerprintMigratedLeastConnRepresentationsMatch(t *testing.T) {
	base := RouteSpec{
		Name: "compat", ListenerIP: "*", ListenerPort: 443, MatchMode: "fallback", Fallback: true,
		TargetType: "tcp", TargetHost: "192.0.2.1", TargetPort: 443, HealthCheck: true, ProxyProtocol: "none",
		QuotaAction: "observe", QuotaPeriod: "calendar_month", Enabled: true, BalanceMode: "pool",
		Servers: []RouteServerSpec{
			{Position: 1, Name: "a", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
			{Position: 2, Name: "b", TargetType: "tcp", Host: "192.0.2.2", Port: 443},
		},
	}
	oldShape := base
	oldShape.StickyMode = StickyModeLeastConn // pre-000052: flat sticky_mode=leastconn, no balance_algorithm column.

	newShape := base
	newShape.StickyMode, newShape.BalanceAlgorithm = StickyModeNone, BalanceAlgorithmLeastConn // 000052 backfill.

	assert.Equal(t, routeSpecFingerprint(oldShape), routeSpecFingerprint(newShape))
}

func TestFingerprintMigratedSourceTableRepresentationsMatch(t *testing.T) {
	base := RouteSpec{
		Name: "compat", ListenerIP: "*", ListenerPort: 443, MatchMode: "fallback", Fallback: true,
		TargetType: "tcp", TargetHost: "192.0.2.1", TargetPort: 443, HealthCheck: true, ProxyProtocol: "none",
		QuotaAction: "observe", QuotaPeriod: "calendar_month", Enabled: true, BalanceMode: "pool",
		StickyMode: StickyModeSourceTable, StickyTTL: "1h", StickyEnabled: true,
		Servers: []RouteServerSpec{
			{Position: 1, Name: "a", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
			{Position: 2, Name: "b", TargetType: "tcp", Host: "192.0.2.2", Port: 443},
		},
	}
	oldShape := base // pre-000052: balance_algorithm column does not exist (zero value "").

	newShape := base
	newShape.BalanceAlgorithm = BalanceAlgorithmLeastConn // 000052 backfill: source_table with algorithm '' => leastconn.

	assert.Equal(t, routeSpecFingerprint(oldShape), routeSpecFingerprint(newShape))
}

func TestFingerprintNewShapesDoNotCollideWithLegacy(t *testing.T) {
	base := RouteSpec{
		Name: "distinct", ListenerIP: "*", ListenerPort: 443, MatchMode: "fallback", Fallback: true,
		TargetType: "tcp", TargetHost: "192.0.2.1", TargetPort: 443, HealthCheck: true, ProxyProtocol: "none",
		QuotaAction: "observe", QuotaPeriod: "calendar_month", Enabled: true, BalanceMode: "pool",
		StickyMode: StickyModeNone, BalanceAlgorithm: BalanceAlgorithmRoundRobin,
		Servers: []RouteServerSpec{
			{Position: 1, Name: "a", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
			{Position: 2, Name: "b", TargetType: "tcp", Host: "192.0.2.2", Port: 443},
		},
	}
	staticRR := base
	staticRR.BalanceAlgorithm = BalanceAlgorithmStaticRR
	assert.NotEqual(t, routeSpecFingerprint(base), routeSpecFingerprint(staticRR))

	leastPing := base
	leastPing.BalanceAlgorithm = BalanceAlgorithmLeastPing
	leastPing.LeastPingTolerance = 0.3
	assert.NotEqual(t, routeSpecFingerprint(base), routeSpecFingerprint(leastPing))

	slowstart := base
	slowstart.Slowstart = "30s"
	assert.NotEqual(t, routeSpecFingerprint(base), routeSpecFingerprint(slowstart))

	weighted := base
	weighted.Servers = append([]RouteServerSpec(nil), base.Servers...)
	weighted.Servers[0].Weight = 100
	assert.NotEqual(t, routeSpecFingerprint(base), routeSpecFingerprint(weighted))
}
