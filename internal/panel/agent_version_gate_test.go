package panel

import "testing"

// TestAgentCapabilityGatesAcceptMajorVersions guards the 2.0.0 version jump:
// every Agent capability gate must compare numerically, so a 2.x Agent passes
// the 1.1.x minimums and 1.10.0 sorts above 1.9.x.
func TestAgentCapabilityGatesAcceptMajorVersions(t *testing.T) {
	gates := []string{KernelShaperMinAgentVersion, NodeAgent110, NodeAgent111, NodeAgent113}
	for _, version := range []string{"2.0.0", "v2.0.0", "2.0.0+build.1", "2.1.0", "10.0.0", "1.10.0"} {
		for _, gate := range gates {
			if !agentVersionAtLeast(version, gate) {
				t.Errorf("agentVersionAtLeast(%q, %q) = false, want true", version, gate)
			}
		}
	}
	for _, tc := range []struct{ version, gate string }{
		{"1.0.5", NodeAgent110},
		{"1.1.0", NodeAgent111},
		{"1.1.2", NodeAgent113},
		{"2.0.0-rc1", "2.0.0"},
		{"1.9.9", "1.10.0"},
		{"", NodeAgent110},
		{"dev", NodeAgent110},
	} {
		if agentVersionAtLeast(tc.version, tc.gate) {
			t.Errorf("agentVersionAtLeast(%q, %q) = true, want false", tc.version, tc.gate)
		}
	}
	if !agentSupportsKernelShaper("2.0.0") || !agentSupportsRoute110Features("2.0.0") {
		t.Fatal("Agent 2.0.0 must support the 1.1.0 route features")
	}
	for _, check := range []func([]Route, string) error{checkLeastConnAgentRoutes, checkPPTrustedDomainsAgentRoutes} {
		if err := check([]Route{{Enabled: true}}, "2.0.0"); err != nil {
			t.Fatalf("Agent 2.0.0 gate rejected: %v", err)
		}
	}
}
