package panel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateRouteDNSPoolCanonicalizesHealthCheck(t *testing.T) {
	port := 443
	disabled := false
	enabled := true
	spec, err := validateRoute(routeInput{
		Name: "germany", ListenerIP: "*", ListenerPort: &port, MatchMode: "sni",
		SNIs: []string{"vpn.example.com"}, TargetType: "tcp", TargetHost: "de.example.com", TargetPort: 443,
		DNSPool: true, HealthCheck: &disabled, ProxyProtocol: "none", QuotaAction: "observe", Enabled: &enabled,
	}, true)
	require.NoError(t, err)
	assert.True(t, spec.DNSPool)
	assert.True(t, spec.HealthCheck)
}

func TestValidateRouteDNSPoolRejectsIncompatibleShape(t *testing.T) {
	port := 443
	enabled := true
	quota := int64(1024)
	base := routeInput{
		Name: "germany", ListenerIP: "*", ListenerPort: &port, MatchMode: "sni",
		SNIs: []string{"vpn.example.com"}, TargetType: "tcp", TargetHost: "de.example.com", TargetPort: 443,
		DNSPool: true, ProxyProtocol: "none", QuotaAction: "observe", Enabled: &enabled,
	}

	for name, mutate := range map[string]func(*routeInput){
		"IP target":               func(in *routeInput) { in.TargetHost = "192.0.2.10" },
		"runtime quota":           func(in *routeInput) { in.QuotaBytes, in.QuotaAction = &quota, "block_new" },
		"managed server override": func(in *routeInput) { in.CustomFragment = "server extra 192.0.2.11:443" },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			mutate(&input)
			_, err := validateRoute(input, true)
			require.Error(t, err)
		})
	}
}

func TestDNSPoolChangesRouteFingerprint(t *testing.T) {
	base := RouteSpec{Name: "germany", ListenerIP: "*", ListenerPort: 443, MatchMode: "sni", SNIs: []string{"vpn.example.com"}, TargetType: "tcp", TargetHost: "de.example.com", TargetPort: 443, HealthCheck: true, ProxyProtocol: "none", QuotaAction: "observe", QuotaPeriod: "calendar_month", Enabled: true}
	pooled := base
	pooled.DNSPool = true
	assert.NotEqual(t, routeSpecFingerprint(base), routeSpecFingerprint(pooled))
}
