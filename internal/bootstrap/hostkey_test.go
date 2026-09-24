package bootstrap

import (
	"context"
	"testing"
)

func TestScanHostKeyRejectsInvalidParameters(t *testing.T) {
	for _, test := range []struct {
		address   string
		port      int
		algorithm string
	}{
		{"not-an-ip", 22, "ssh-ed25519"},
		{"192.0.2.1", 0, "ssh-ed25519"},
		{"192.0.2.1", 22, "ssh-dss"},
	} {
		if _, err := ScanHostKey(context.Background(), test.address, test.port, test.algorithm); err == nil {
			t.Fatalf("expected validation error for %+v", test)
		}
	}
}
