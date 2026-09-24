package panel

import (
	"strings"
	"testing"
)

// A single DNS-pool server with source_table + leastconn tolerance must keep
// the tolerance (it used to collapse to the legacy single-target route and
// silently drop balance_algorithm and the tolerance).
func TestValidateRouteSingleDNSPoolKeepsLeastConnTolerance(t *testing.T) {
	port, enabled, check := 443, true, true
	tolerance := 0.2
	in := routeInput{
		Name: "de", ListenerIP: "*", ListenerPort: &port, MatchMode: "sni",
		SNIs: []string{"co-de.example.com"}, ProxyProtocol: "v2", QuotaAction: "observe",
		Enabled: &enabled, HealthCheck: &check, BalanceMode: "pool",
		StickyMode: StickyModeSourceTable, BalanceAlgorithm: BalanceAlgorithmLeastConn,
		BalanceTolerance: &tolerance,
		Servers:          []routeServerInput{{Name: "de", TargetType: "tcp", Host: "co-de.example.com", Port: 443, DNSPool: true}},
	}
	spec, err := validateRoute(in, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Servers) != 1 || !spec.Servers[0].DNSPool {
		t.Fatalf("servers = %+v, want one explicit DNS-pool server", spec.Servers)
	}
	if spec.BalanceAlgorithm != BalanceAlgorithmLeastConn || spec.LeastPingTolerance != 0.2 {
		t.Fatalf("algorithm=%q tolerance=%v, want leastconn 0.2", spec.BalanceAlgorithm, spec.LeastPingTolerance)
	}
	if !specNeedsLeastConnAgent(spec) {
		t.Fatal("tolerance route must require the leastconn Agent controller")
	}

	// Without a tolerance the same input stays the byte-identical legacy route.
	in.BalanceTolerance = nil
	in.BalanceAlgorithm = ""
	spec, err = validateRoute(in, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Servers) != 0 || !spec.DNSPool {
		t.Fatalf("plain single DNS pool must stay legacy: servers=%d dns_pool=%v", len(spec.Servers), spec.DNSPool)
	}

	// sticky source ignores algorithm and tolerance: stays legacy too.
	in.StickyMode, in.BalanceTolerance = StickyModeSource, &tolerance
	spec, err = validateRoute(in, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Servers) != 0 {
		t.Fatalf("source-hash single DNS pool must stay legacy, got %d servers", len(spec.Servers))
	}
}

func TestSingleDNSPoolToleranceRendersAgentManagedTemplate(t *testing.T) {
	_, got := renderPayload(t, `{"name":"de","match_mode":"fallback","balance_mode":"pool","balance_algorithm":"leastconn","sticky_mode":"source_table","balance_tolerance":0.2,
		"servers":[{"name":"de","host":"co-de.example.com","port":443,"dns_pool":true}]}`)
	backend := backendSection(got.Config)
	if !strings.Contains(backend, "algo=leastconn tolerance=0.20") {
		t.Fatalf("missing Agent tolerance annotation:\n%s", backend)
	}
	if !strings.Contains(backend, "server-template ") || !strings.Contains(backend, "stick on src") {
		t.Fatalf("want DNS-pool server-template with source_table stickiness:\n%s", backend)
	}
}
