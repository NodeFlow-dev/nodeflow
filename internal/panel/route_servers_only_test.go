package panel

import "testing"

func TestValidateRouteServersOnlyMirrorsFirstPrimary(t *testing.T) {
	port := 9443
	in := routeInput{
		Name: "servers-only", ListenerIP: "*", ListenerPort: &port, MatchMode: "sni",
		SNIs: []string{"b.lab.test"}, ProxyProtocol: "v2",
		Servers: []routeServerInput{
			{Name: "dr", TargetType: "unix", UnixSocketPath: "/dev/shm/nf-test.sock", Backup: true},
			{Name: "p1", TargetType: "tcp", Host: "127.0.0.1", Port: 9902},
			{Name: "p2", TargetType: "tcp", Host: "127.0.0.1", Port: 9904},
		},
	}
	spec, err := validateRoute(in, false)
	if err != nil {
		t.Fatalf("servers-only route rejected: %v", err)
	}
	if spec.TargetType != "tcp" || spec.TargetHost != "127.0.0.1" || spec.TargetPort != 9902 {
		t.Fatalf("legacy target not mirrored from first primary: %+v", spec)
	}
	if len(spec.Servers) != 3 {
		t.Fatalf("servers = %d, want 3", len(spec.Servers))
	}
}
