package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type countingUFWRunner struct {
	status string
	calls  map[string]int
}

func (r *countingUFWRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name
	if len(args) > 0 {
		key += " " + args[0]
	}
	r.calls[key]++
	if name == "ufw" && len(args) > 0 && args[0] == "status" {
		return []byte(r.status), nil
	}
	return nil, nil
}

const ufwActiveStatus = "Status: active\n\n     To                         Action      From\n     --                         ------      ----\n[ 1] 443/tcp                    ALLOW IN    Anywhere                   # nodeflow\n"

func TestFirewallStatusCacheSkipsUFWWhileRulesUnchanged(t *testing.T) {
	dir := t.TempDir()
	rules := filepath.Join(dir, "user.rules")
	if err := os.WriteFile(rules, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &countingUFWRunner{status: ufwActiveStatus, calls: map[string]int{}}
	r := &FirewallReconciler{Config: FirewallConfig{Mode: FirewallModeApply}, Runner: runner,
		StatusCacheTTL: time.Minute, StatusCacheFiles: []string{rules}}
	assignment := FirewallAssignment{Mode: FirewallModeApply, TCPPorts: []int{443}}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := r.Reconcile(ctx, assignment); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	if got := runner.calls["ufw status"]; got != 1 {
		t.Fatalf("ufw status calls = %d, want 1 while rule files are unchanged", got)
	}
	// An out-of-band rule change (size/mtime) must force a fresh read.
	future := time.Now().Add(2 * time.Second)
	if err := os.WriteFile(rules, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(rules, future, future)
	if _, err := r.Reconcile(ctx, assignment); err != nil {
		t.Fatal(err)
	}
	if got := runner.calls["ufw status"]; got != 2 {
		t.Fatalf("ufw status calls = %d, want 2 after rule file change", got)
	}
}

func TestFirewallStatusCacheInvalidatedByOwnMutation(t *testing.T) {
	dir := t.TempDir()
	rules := filepath.Join(dir, "user.rules")
	if err := os.WriteFile(rules, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &countingUFWRunner{status: ufwActiveStatus, calls: map[string]int{}}
	r := &FirewallReconciler{Config: FirewallConfig{Mode: FirewallModeApply}, Runner: runner,
		StatusCacheTTL: time.Minute, StatusCacheFiles: []string{rules}}
	ctx := context.Background()
	// Port 8443 is missing: reconcile adds it, which must drop the cache even
	// if the fake rule file does not change.
	if _, err := r.Reconcile(ctx, FirewallAssignment{Mode: FirewallModeApply, TCPPorts: []int{443, 8443}}); err != nil {
		t.Fatal(err)
	}
	if runner.calls["ufw allow"] != 1 {
		t.Fatalf("ufw allow calls = %d, want 1", runner.calls["ufw allow"])
	}
	if _, err := r.Reconcile(ctx, FirewallAssignment{Mode: FirewallModeApply, TCPPorts: []int{443}}); err != nil {
		t.Fatal(err)
	}
	if got := runner.calls["ufw status"]; got != 2 {
		t.Fatalf("ufw status calls = %d, want 2 (cache dropped after allow)", got)
	}
}

func TestFirewallStatusCacheDisabledByDefault(t *testing.T) {
	runner := &countingUFWRunner{status: ufwActiveStatus, calls: map[string]int{}}
	r := &FirewallReconciler{Config: FirewallConfig{Mode: FirewallModeApply}, Runner: runner}
	for i := 0; i < 3; i++ {
		_, _ = r.Reconcile(context.Background(), FirewallAssignment{Mode: FirewallModeApply, TCPPorts: []int{443}})
	}
	if got := runner.calls["ufw status"]; got != 3 {
		t.Fatalf("ufw status calls = %d, want 3 without cache", got)
	}
}
