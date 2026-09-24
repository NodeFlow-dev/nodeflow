package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type reportCollector struct {
	mu      sync.Mutex
	reports []ConfigReport
}

func (c *reportCollector) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/agent/v1/config-report" || r.Header.Get("Authorization") != "Bearer secret" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var report ConfigReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	c.reports = append(c.reports, report)
	c.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
}

func (c *reportCollector) snapshot() []ConfigReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ConfigReport(nil), c.reports...)
}

func assignment(revision int64, config string) ConfigAssignment {
	sum := sha256.Sum256([]byte(config))
	return ConfigAssignment{Revision: revision, Config: config, SHA256: hex.EncodeToString(sum[:])}
}

func TestReconcileAppliesIntegerRevisionAndReports(t *testing.T) {
	collector := &reportCollector{}
	panel := httptest.NewServer(http.HandlerFunc(collector.handler))
	defer panel.Close()
	path := filepath.Join(t.TempDir(), "nodeflow.cfg")
	runner := &fakeRunner{}
	manager := &ConfigManager{Runner: runner, ManagedConfig: path, HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	r := Reconciler{Manager: manager, Reporter: ConfigReporter{URL: panel.URL, Token: "secret", Client: panel.Client()}}

	if err := r.Reconcile(context.Background(), assignment(42, "global\n  daemon\n")); err != nil {
		t.Fatal(err)
	}
	actual, err := manager.ActualRevision()
	if err != nil || actual != "42" {
		t.Fatalf("actual=%q err=%v", actual, err)
	}
	reports := collector.snapshot()
	if len(reports) != 2 || reports[0].State != "applying" || reports[1].State != "applied" {
		t.Fatalf("reports=%+v", reports)
	}
	if reports[1].ActualRevision == nil || *reports[1].ActualRevision != 42 {
		t.Fatalf("actual report=%+v", reports[1])
	}
}

func TestReconcileRejectsChecksumBeforeHAProxy(t *testing.T) {
	collector := &reportCollector{}
	panel := httptest.NewServer(http.HandlerFunc(collector.handler))
	defer panel.Close()
	runner := &fakeRunner{}
	manager := &ConfigManager{Runner: runner, ManagedConfig: filepath.Join(t.TempDir(), "nodeflow.cfg"), HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	r := Reconciler{Manager: manager, Reporter: ConfigReporter{URL: panel.URL, Token: "secret", Client: panel.Client()}}

	err := r.Reconcile(context.Background(), ConfigAssignment{Revision: 7, Config: "global\n", SHA256: strings.Repeat("0", 64)})
	if err == nil || !strings.Contains(err.Error(), "assignment_checksum_mismatch") {
		t.Fatalf("err=%v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("HAProxy was called: %v", runner.calls)
	}
	reports := collector.snapshot()
	if len(reports) != 1 || reports[0].State != "failed" || reports[0].Error != "assignment_checksum_mismatch" {
		t.Fatalf("reports=%+v", reports)
	}
}

type failFirstReloadRunner struct {
	reloads int
}

type blockingRunner struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	active  int
	maximum int
}

func (r *blockingRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, error) {
	r.mu.Lock()
	r.active++
	if r.active > r.maximum {
		r.maximum = r.active
	}
	r.mu.Unlock()
	r.once.Do(func() {
		close(r.entered)
		<-r.release
	})
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	return []byte("ok"), nil
}

func (r *failFirstReloadRunner) Run(_ context.Context, name string, _ ...string) ([]byte, error) {
	if name == "systemctl" {
		r.reloads++
		if r.reloads == 1 {
			return []byte("private diagnostic"), errors.New("reload failed")
		}
	}
	return []byte("ok"), nil
}

func TestReconcileReportsSuccessfulAutomaticRollback(t *testing.T) {
	collector := &reportCollector{}
	panel := httptest.NewServer(http.HandlerFunc(collector.handler))
	defer panel.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "nodeflow.cfg")
	if err := os.WriteFile(path, []byte("old config"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".revision", []byte("41\n"), 0640); err != nil {
		t.Fatal(err)
	}
	runner := &failFirstReloadRunner{}
	manager := &ConfigManager{Runner: runner, ManagedConfig: path, HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	r := Reconciler{Manager: manager, Reporter: ConfigReporter{URL: panel.URL, Token: "secret", Client: panel.Client()}}

	err := r.Reconcile(context.Background(), assignment(42, "new config"))
	if err == nil || !strings.Contains(err.Error(), "reload_failed") {
		t.Fatalf("err=%v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "old config" {
		t.Fatalf("config=%q", data)
	}
	reports := collector.snapshot()
	last := reports[len(reports)-1]
	if last.State != "rolled_back" || !last.RollbackAttempted || last.RollbackSucceeded == nil || !*last.RollbackSucceeded {
		t.Fatalf("report=%+v", last)
	}
	if last.ActualRevision == nil || *last.ActualRevision != 41 || strings.Contains(last.Error, "private diagnostic") {
		t.Fatalf("report=%+v", last)
	}
}

func TestReconcileSerializesWithAgentHTTPApply(t *testing.T) {
	collector := &reportCollector{}
	panel := httptest.NewServer(http.HandlerFunc(collector.handler))
	defer panel.Close()
	runner := &blockingRunner{entered: make(chan struct{}), release: make(chan struct{})}
	path := filepath.Join(t.TempDir(), "nodeflow.cfg")
	manager := &ConfigManager{Runner: runner, ManagedConfig: path, HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	server := NewServer(Config{Token: "secret", ManagedConfig: path, HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}, manager, "test")

	httpDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		httpDone <- request(server.Handler(), http.MethodPost, "/v1/config/apply", "secret", `{"config":"manual config","revision":"manual"}`)
	}()
	<-runner.entered

	r := Reconciler{Manager: manager, Reporter: ConfigReporter{URL: panel.URL, Token: "secret", Client: panel.Client()}}
	reconcileDone := make(chan error, 1)
	go func() { reconcileDone <- r.Reconcile(context.Background(), assignment(42, "desired config")) }()
	select {
	case err := <-reconcileDone:
		t.Fatalf("reconcile bypassed active HTTP operation: %v", err)
	default:
	}
	close(runner.release)
	if rr := <-httpDone; rr.Code != http.StatusOK {
		t.Fatalf("HTTP apply status=%d body=%s", rr.Code, rr.Body)
	}
	if err := <-reconcileDone; err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	maximum := runner.maximum
	runner.mu.Unlock()
	if maximum != 1 {
		t.Fatalf("maximum concurrent config operations=%d", maximum)
	}
}
