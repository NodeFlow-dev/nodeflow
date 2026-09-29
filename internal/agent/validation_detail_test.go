package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type alertValidatorRunner struct{}

func (alertValidatorRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "haproxy" {
		return nil, nil
	}
	path := args[len(args)-1]
	out := "\x1b[1;31m[NOTICE]\x1b[0m   (1) : haproxy version is 3.0.5\n" +
		"\x1b[1;31m[ALERT]\x1b[0m    (1) : config : parsing [" + path + ":12] : unknown keyword 'bindd' in 'frontend' section\n" +
		"[ALERT]    (1) : config : userlist admin: password hash rejected at [" + path + ":20]\n" +
		"[WARNING]  (1) : config : parsing [" + path + ":7] : 'option httplog' not usable with frontend 'fe' (needs 'mode http').\n" +
		"[ALERT]    (1) : config : Error(s) found in configuration file : " + path + "\n"
	return []byte(out), errors.New("exit status 1")
}

func TestValidationDetailExtractsAlertsAndReplacesTempPath(t *testing.T) {
	manager := &ConfigManager{Runner: alertValidatorRunner{}, ManagedConfig: filepath.Join(t.TempDir(), "nodeflow.cfg"), HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	_, _, err := manager.ApplyRevision(context.Background(), []byte("global\n"), "147")
	var typed *ApplyError
	if !errors.As(err, &typed) || typed.Code != "validation_failed" {
		t.Fatalf("err=%v", err)
	}
	want := "[ALERT]    (1) : config : parsing [haproxy.cfg:12] : unknown keyword 'bindd' in 'frontend' section\n" +
		"[WARNING]  (1) : config : parsing [haproxy.cfg:7] : 'option httplog' not usable with frontend 'fe' (needs 'mode http').\n" +
		"[ALERT]    (1) : config : Error(s) found in configuration file : haproxy.cfg"
	if typed.Detail != want {
		t.Fatalf("detail=%q", typed.Detail)
	}
	if strings.Contains(typed.Detail, ".nodeflow-validate-") || strings.Contains(typed.Detail, "\x1b") || strings.Contains(typed.Detail, "password") {
		t.Fatalf("unsanitized detail=%q", typed.Detail)
	}
}

func TestValidationDetailFallbackAndTruncation(t *testing.T) {
	if got := ValidationDetail([]byte("plain failure\n\nsecret=x\n"), ""); got != "plain failure" {
		t.Fatalf("fallback=%q", got)
	}
	long := strings.Repeat("[ALERT] (1) : config : parsing [/etc/haproxy/.nodeflow-validate-123:1] : bad\n", 100)
	got := ValidationDetail([]byte(long), "")
	if len(got) > maxValidationDetailBytes || !strings.HasSuffix(got, "…") || strings.Contains(got, "nodeflow-validate") {
		t.Fatalf("len=%d detail=%q", len(got), got[:80])
	}
}

func TestReconcileReportsValidationDetail(t *testing.T) {
	var report ConfigReport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got ConfigReport
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got.State == "failed" {
			report = got
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	manager := &ConfigManager{Runner: alertValidatorRunner{}, ManagedConfig: filepath.Join(t.TempDir(), "nodeflow.cfg"), HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	reconciler := &Reconciler{Manager: manager, Reporter: ConfigReporter{URL: server.URL, Token: "t", Client: server.Client()}}
	err := reconciler.Reconcile(context.Background(), assignment(147, "global\n"))
	if err == nil || err.Error() != "revision 147 validation_failed" {
		t.Fatalf("err=%v", err)
	}
	detail, _ := report.Details["error_detail"].(string)
	if report.Error != "validation_failed" || !strings.Contains(detail, "[haproxy.cfg:12] : unknown keyword 'bindd'") {
		t.Fatalf("report=%+v", report)
	}
}
