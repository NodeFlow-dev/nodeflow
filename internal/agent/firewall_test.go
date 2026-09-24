package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type firewallRunnerResponse struct {
	output []byte
	err    error
}

type firewallRunner struct {
	calls     [][]string
	responses []firewallRunnerResponse
}

func (r *firewallRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if len(r.responses) == 0 {
		return nil, errors.New("unexpected command")
	}
	response := r.responses[0]
	r.responses = r.responses[1:]
	return response.output, response.err
}

func TestFirewallLocalModeDefaultsToObserve(t *testing.T) {
	runner := &firewallRunner{responses: []firewallRunnerResponse{{output: []byte("Status: inactive\n")}}}
	reconciler := FirewallReconciler{Runner: runner}

	status, err := reconciler.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.LocalMode != FirewallModeObserve || status.EffectiveMode != FirewallModeObserve {
		t.Fatalf("status=%+v", status)
	}
	assertFirewallCalls(t, runner.calls, [][]string{{"ufw", "status", "numbered"}})
}

func TestFirewallLocalCeiling(t *testing.T) {
	tests := []struct {
		name          string
		local         string
		requested     string
		wantEffective string
		wantCalls     [][]string
	}{
		{
			name:          "local off cannot be raised",
			local:         FirewallModeOff,
			requested:     FirewallModeApply,
			wantEffective: FirewallModeOff,
		},
		{
			name:          "local observe cannot be raised",
			local:         FirewallModeObserve,
			requested:     FirewallModeApply,
			wantEffective: FirewallModeObserve,
			wantCalls:     [][]string{{"ufw", "status", "numbered"}},
		},
		{
			name:          "assignment can lower apply to observe",
			local:         FirewallModeApply,
			requested:     FirewallModeObserve,
			wantEffective: FirewallModeObserve,
			wantCalls:     [][]string{{"ufw", "status", "numbered"}},
		},
		{
			name:          "assignment can lower apply to off",
			local:         FirewallModeApply,
			requested:     FirewallModeOff,
			wantEffective: FirewallModeOff,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &firewallRunner{}
			if len(test.wantCalls) > 0 {
				runner.responses = []firewallRunnerResponse{{output: []byte("Status: active\n")}}
			}
			reconciler := FirewallReconciler{Config: FirewallConfig{Mode: test.local}, Runner: runner}
			status, err := reconciler.Reconcile(context.Background(), FirewallAssignment{
				Mode:     test.requested,
				TCPPorts: []int{4200},
			})
			if err != nil {
				t.Fatal(err)
			}
			if status.EffectiveMode != test.wantEffective {
				t.Fatalf("effective=%q want=%q", status.EffectiveMode, test.wantEffective)
			}
			if status.Applied != 0 {
				t.Fatalf("applied=%d", status.Applied)
			}
			assertFirewallCalls(t, runner.calls, test.wantCalls)
		})
	}
}

func TestFirewallApplySortsDeduplicatesAndUsesExecArguments(t *testing.T) {
	runner := &firewallRunner{responses: []firewallRunnerResponse{
		{output: []byte("Status: active\n\nTo Action From\n")},
		{output: []byte("Rule added\n")},
		{output: []byte("Rule added\n")},
		{output: []byte("Skipping adding existing rule\n")},
	}}
	reconciler := FirewallReconciler{Config: FirewallConfig{Mode: FirewallModeApply}, Runner: runner}

	status, err := reconciler.Reconcile(context.Background(), FirewallAssignment{
		Mode:     FirewallModeApply,
		TCPPorts: []int{4200, 443, 4200, 65535},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.Applied != 3 || !status.Active || !reflect.DeepEqual(status.TCPPorts, []int{443, 4200, 65535}) {
		t.Fatalf("status=%+v", status)
	}
	assertFirewallCalls(t, runner.calls, [][]string{
		{"ufw", "status", "numbered"},
		{"ufw", "allow", "443/tcp", "comment", "nodeflow"},
		{"ufw", "allow", "4200/tcp", "comment", "nodeflow"},
		{"ufw", "allow", "65535/tcp", "comment", "nodeflow"},
	})
	for _, call := range runner.calls {
		for _, forbidden := range []string{"enable", "disable", "reset", "reload", "delete", "install"} {
			for _, arg := range call {
				if arg == forbidden {
					t.Fatalf("unsafe command: %v", call)
				}
			}
		}
	}
}

func TestFirewallApplyRequiresExactActiveStatus(t *testing.T) {
	outputs := []string{
		"Status: inactive\n",
		"Status: active (running)\n",
		"warning\nStatus: active\n",
		"",
	}
	for _, output := range outputs {
		t.Run(fmt.Sprintf("status_%q", output), func(t *testing.T) {
			runner := &firewallRunner{responses: []firewallRunnerResponse{{output: []byte(output)}}}
			reconciler := FirewallReconciler{Config: FirewallConfig{Mode: FirewallModeApply}, Runner: runner}
			status, err := reconciler.Reconcile(context.Background(), FirewallAssignment{
				Mode:     FirewallModeApply,
				TCPPorts: []int{4200},
			})
			if err == nil || !strings.Contains(err.Error(), "UFW is not active") {
				t.Fatalf("status=%+v err=%v", status, err)
			}
			assertFirewallCalls(t, runner.calls, [][]string{{"ufw", "status", "numbered"}})
		})
	}
}

func TestFirewallApplyStopsAfterFirstRuleFailure(t *testing.T) {
	runner := &firewallRunner{responses: []firewallRunnerResponse{
		{output: []byte("Status: active\n")},
		{output: []byte("permission denied: private-host-detail"), err: errors.New("exit status 1")},
	}}
	reconciler := FirewallReconciler{Config: FirewallConfig{Mode: FirewallModeApply}, Runner: runner}

	status, err := reconciler.Reconcile(context.Background(), FirewallAssignment{
		Mode:     FirewallModeApply,
		TCPPorts: []int{80, 443},
	})
	if err == nil || !strings.Contains(err.Error(), "TCP port 80") {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if strings.Contains(err.Error(), "private-host-detail") {
		t.Fatalf("runner output leaked: %v", err)
	}
	assertFirewallCalls(t, runner.calls, [][]string{
		{"ufw", "status", "numbered"},
		{"ufw", "allow", "80/tcp", "comment", "nodeflow"},
	})
}

func TestFirewallApplyDiffsOnlyTaggedRules(t *testing.T) {
	statusOutput := []byte(`Status: active

[ 1] 80/tcp                    ALLOW IN    Anywhere                   # nodeflow
[ 2] 80/tcp (v6)               ALLOW IN    Anywhere (v6)              # nodeflow
[ 3] 443/tcp                   ALLOW IN    Anywhere                   # operator-rule
[ 4] 8443/tcp                  ALLOW IN    Anywhere                   # nodeflow
`)
	runner := &firewallRunner{responses: []firewallRunnerResponse{
		{output: statusOutput},
		{output: []byte("Rule deleted\n")},
		{output: []byte("Rule added\n")},
	}}
	reconciler := FirewallReconciler{Config: FirewallConfig{Mode: FirewallModeApply}, Runner: runner}

	status, err := reconciler.Reconcile(context.Background(), FirewallAssignment{Mode: FirewallModeApply, TCPPorts: []int{80, 443}})
	if err != nil {
		t.Fatal(err)
	}
	if status.Removed != 1 || status.Applied != 1 || !reflect.DeepEqual(status.ManagedPorts, []int{80, 443}) || len(status.StalePorts) != 0 {
		t.Fatalf("status=%+v", status)
	}
	assertFirewallCalls(t, runner.calls, [][]string{
		{"ufw", "status", "numbered"},
		{"ufw", "--force", "delete", "4"},
		{"ufw", "allow", "443/tcp", "comment", "nodeflow"},
	})
}

func TestFirewallPreOpenAddsDesiredWithoutPruningActiveOrStaleRules(t *testing.T) {
	runner := &firewallRunner{responses: []firewallRunnerResponse{
		{output: []byte(`Status: active
[ 1] 443/tcp ALLOW IN Anywhere # nodeflow
[ 2] 8443/tcp ALLOW IN Anywhere # nodeflow
`)},
		{output: []byte("Rule added\n")},
	}}
	reconciler := FirewallReconciler{Config: FirewallConfig{Mode: FirewallModeApply}, Runner: runner}

	status, err := reconciler.PreOpen(context.Background(), FirewallAssignment{
		Mode: FirewallModeApply, TCPPorts: []int{443, 10065},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.Removed != 0 || status.Applied != 1 ||
		!reflect.DeepEqual(status.ManagedPorts, []int{443, 8443, 10065}) ||
		!reflect.DeepEqual(status.StalePorts, []int{8443}) {
		t.Fatalf("status=%+v", status)
	}
	assertFirewallCalls(t, runner.calls, [][]string{
		{"ufw", "status", "numbered"},
		{"ufw", "allow", "10065/tcp", "comment", "nodeflow"},
	})
}

func TestFirewallObserveReportsStaleTaggedRulesWithoutMutation(t *testing.T) {
	runner := &firewallRunner{responses: []firewallRunnerResponse{{output: []byte(`Status: active
[ 1] 80/tcp ALLOW IN Anywhere # nodeflow
[ 2] 8443/tcp ALLOW IN Anywhere # nodeflow
`)}}}
	reconciler := FirewallReconciler{Config: FirewallConfig{Mode: FirewallModeApply}, Runner: runner}
	status, err := reconciler.Reconcile(context.Background(), FirewallAssignment{Mode: FirewallModeObserve, TCPPorts: []int{80}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(status.ManagedPorts, []int{80, 8443}) || !reflect.DeepEqual(status.StalePorts, []int{8443}) {
		t.Fatalf("status=%+v", status)
	}
	assertFirewallCalls(t, runner.calls, [][]string{{"ufw", "status", "numbered"}})
}

func TestFirewallEmptyDesiredSetRemovesOnlyManagedListenerAllows(t *testing.T) {
	runner := &firewallRunner{responses: []firewallRunnerResponse{
		{output: []byte(`Status: active
[ 1] 22/tcp ALLOW IN Anywhere # nodeflow-ssh
[ 2] 443/tcp ALLOW IN Anywhere # operator-rule
[ 3] 443/tcp ALLOW IN Anywhere # nodeflow
[ 4] 443/tcp (v6) ALLOW IN Anywhere (v6) # nodeflow
[ 5] 8443/tcp DENY IN Anywhere # nodeflow
[ 6] 9000/tcp ALLOW OUT Anywhere # nodeflow
`)},
		{output: []byte("Rule deleted\n")},
		{output: []byte("Rule deleted\n")},
	}}
	reconciler := FirewallReconciler{Config: FirewallConfig{Mode: FirewallModeApply}, Runner: runner}

	status, err := reconciler.Reconcile(context.Background(), FirewallAssignment{Mode: FirewallModeApply, TCPPorts: []int{}})
	if err != nil {
		t.Fatal(err)
	}
	if status.Removed != 2 || len(status.ManagedPorts) != 0 || len(status.StalePorts) != 0 {
		t.Fatalf("status=%+v", status)
	}
	assertFirewallCalls(t, runner.calls, [][]string{
		{"ufw", "status", "numbered"},
		{"ufw", "--force", "delete", "4"},
		{"ufw", "--force", "delete", "3"},
	})
}

func TestFirewallSharedListenerPortCreatesOneManagedRule(t *testing.T) {
	runner := &firewallRunner{responses: []firewallRunnerResponse{
		{output: []byte("Status: active\n")},
		{output: []byte("Rule added\n")},
	}}
	reconciler := FirewallReconciler{Config: FirewallConfig{Mode: FirewallModeApply}, Runner: runner}

	status, err := reconciler.Reconcile(context.Background(), FirewallAssignment{Mode: FirewallModeApply, TCPPorts: []int{443, 443, 443}})
	if err != nil {
		t.Fatal(err)
	}
	if status.Applied != 1 || !reflect.DeepEqual(status.ManagedPorts, []int{443}) {
		t.Fatalf("status=%+v", status)
	}
	assertFirewallCalls(t, runner.calls, [][]string{
		{"ufw", "status", "numbered"},
		{"ufw", "allow", "443/tcp", "comment", "nodeflow"},
	})
}

func TestFirewallRejectsInvalidAssignmentWithoutCommands(t *testing.T) {
	tooMany := make([]int, maxFirewallTCPPorts+1)
	for i := range tooMany {
		tooMany[i] = i + 1
	}
	tests := []FirewallAssignment{
		{Mode: ""},
		{Mode: "APPLY", TCPPorts: []int{443}},
		{Mode: FirewallModeObserve, TCPPorts: []int{0}},
		{Mode: FirewallModeObserve, TCPPorts: []int{65536}},
		{Mode: FirewallModeObserve, TCPPorts: tooMany},
	}
	for index, assignment := range tests {
		t.Run(fmt.Sprintf("case_%d", index), func(t *testing.T) {
			runner := &firewallRunner{}
			reconciler := FirewallReconciler{Config: FirewallConfig{Mode: FirewallModeApply}, Runner: runner}
			if _, err := reconciler.Reconcile(context.Background(), assignment); err == nil {
				t.Fatal("expected validation error")
			}
			if len(runner.calls) != 0 {
				t.Fatalf("commands executed: %v", runner.calls)
			}
		})
	}
}

func TestFirewallRejectsInvalidLocalModeWithoutCommands(t *testing.T) {
	runner := &firewallRunner{}
	reconciler := FirewallReconciler{Config: FirewallConfig{Mode: "manage"}, Runner: runner}
	if _, err := reconciler.Status(context.Background()); err == nil {
		t.Fatal("expected validation error")
	}
	if _, err := reconciler.Reconcile(context.Background(), FirewallAssignment{Mode: FirewallModeObserve}); err == nil {
		t.Fatal("expected validation error")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("commands executed: %v", runner.calls)
	}
}

func TestFirewallStatusCommandFailureIsReported(t *testing.T) {
	runner := &firewallRunner{responses: []firewallRunnerResponse{{
		output: []byte("sensitive output"),
		err:    errors.New("executable file not found"),
	}}}
	reconciler := FirewallReconciler{Config: FirewallConfig{Mode: FirewallModeObserve}, Runner: runner}
	status, err := reconciler.Status(context.Background())
	if err == nil || status.UFWStatus != "error" || status.UFWAvailable {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if strings.Contains(err.Error(), "sensitive output") {
		t.Fatalf("runner output leaked: %v", err)
	}
}

func assertFirewallCalls(t *testing.T, got, want [][]string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls=%v want=%v", got, want)
	}
}
