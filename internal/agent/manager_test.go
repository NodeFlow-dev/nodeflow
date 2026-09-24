package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type revisionObservingRunner struct {
	marker          string
	failFirstReload bool
	reloads         int
	seenRevisions   []string
}

func (r *revisionObservingRunner) Run(_ context.Context, name string, _ ...string) ([]byte, error) {
	if name != "systemctl" {
		return []byte("ok"), nil
	}
	r.reloads++
	marker, err := os.ReadFile(r.marker)
	if os.IsNotExist(err) {
		r.seenRevisions = append(r.seenRevisions, "")
	} else if err != nil {
		return nil, err
	} else {
		r.seenRevisions = append(r.seenRevisions, strings.TrimSpace(string(marker)))
	}
	if r.failFirstReload && r.reloads == 1 {
		return []byte("reload failed"), errors.New("reload failed")
	}
	return []byte("ok"), nil
}

func TestApplyRollsBackOnReloadFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodeflow.cfg")
	if err := os.WriteFile(path, []byte("old config"), 0640); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{failReload: true}
	m := ConfigManager{Runner: runner, ManagedConfig: path, HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	backup, err := m.Apply(context.Background(), []byte("new config"))
	if err == nil {
		t.Fatal("expected reload error")
	}
	if backup == "" {
		t.Fatal("expected backup path")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "old config" {
		t.Fatalf("data=%q err=%v", data, readErr)
	}
}

type concurrencyRunner struct {
	mu      sync.Mutex
	active  int
	maximum int
}

func (r *concurrencyRunner) Run(context.Context, string, ...string) ([]byte, error) {
	r.mu.Lock()
	r.active++
	if r.active > r.maximum {
		r.maximum = r.active
	}
	r.mu.Unlock()
	time.Sleep(10 * time.Millisecond)
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	return []byte("ok"), nil
}

func TestConfigManagerSerializesConcurrentOperations(t *testing.T) {
	runner := &concurrencyRunner{}
	m := &ConfigManager{Runner: runner, ManagedConfig: filepath.Join(t.TempDir(), "nodeflow.cfg"), HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Validate(context.Background(), []byte("global\n")); err != nil {
				t.Errorf("validate: %v", err)
			}
		}()
	}
	wg.Wait()
	if runner.maximum != 1 {
		t.Fatalf("maximum concurrent operations=%d", runner.maximum)
	}
}

func TestApplyRemovesNewFileOnReloadFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodeflow.cfg")
	m := ConfigManager{Runner: &fakeRunner{failReload: true}, ManagedConfig: path, HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	_, err := m.Apply(context.Background(), []byte("new config"))
	if err == nil {
		t.Fatal("expected reload error")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("stat error=%v", statErr)
	}
}

func TestApplyRevisionIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodeflow.cfg")
	runner := &fakeRunner{}
	m := ConfigManager{Runner: runner, ManagedConfig: path, HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	first, _, err := m.ApplyRevision(context.Background(), []byte("config"), "rev-1")
	if err != nil || first.Idempotent {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	calls := len(runner.calls)
	second, _, err := m.ApplyRevision(context.Background(), []byte("config"), "rev-1")
	if err != nil || !second.Idempotent {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if len(runner.calls) != calls {
		t.Fatalf("idempotent apply ran commands: %v", runner.calls[calls:])
	}
}

func TestApplyRevisionDoesNotPublishMarkerBeforeReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodeflow.cfg")
	if err := os.WriteFile(path, []byte("old config"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".revision", []byte("rev-old\n"), 0640); err != nil {
		t.Fatal(err)
	}
	runner := &revisionObservingRunner{marker: path + ".revision"}
	m := ConfigManager{Runner: runner, ManagedConfig: path, HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}

	result, _, err := m.ApplyRevision(context.Background(), []byte("new config"), "rev-new")
	if err != nil || result.Idempotent {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(runner.seenRevisions) != 1 || runner.seenRevisions[0] != "rev-old" {
		t.Fatalf("marker visible during reload=%v", runner.seenRevisions)
	}
	actual, err := m.ActualRevision()
	if err != nil || actual != "rev-new" {
		t.Fatalf("actual=%q err=%v", actual, err)
	}
}

func TestApplyRevisionReloadFailureRestoresActiveMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodeflow.cfg")
	if err := os.WriteFile(path, []byte("old config"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".revision", []byte("rev-old\n"), 0640); err != nil {
		t.Fatal(err)
	}
	runner := &revisionObservingRunner{marker: path + ".revision", failFirstReload: true}
	m := ConfigManager{Runner: runner, ManagedConfig: path, HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}

	_, _, err := m.ApplyRevision(context.Background(), []byte("new config"), "rev-new")
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) || applyErr.Code != "reload_failed" || applyErr.RollbackSucceeded == nil || !*applyErr.RollbackSucceeded {
		t.Fatalf("err=%v", err)
	}
	if len(runner.seenRevisions) != 2 || runner.seenRevisions[0] != "rev-old" || runner.seenRevisions[1] != "rev-old" {
		t.Fatalf("markers visible during reloads=%v", runner.seenRevisions)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "old config" {
		t.Fatalf("config=%q err=%v", data, readErr)
	}
	actual, readErr := m.ActualRevision()
	if readErr != nil || actual != "rev-old" {
		t.Fatalf("actual=%q err=%v", actual, readErr)
	}
}

func TestApplyRevisionRepairsConfigWhenMarkerMatchesButHashDoesNot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodeflow.cfg")
	runner := &fakeRunner{}
	m := ConfigManager{Runner: runner, ManagedConfig: path, HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	if _, _, err := m.ApplyRevision(context.Background(), []byte("expected config"), "rev-1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tampered config"), 0640); err != nil {
		t.Fatal(err)
	}
	callsBefore := len(runner.calls)

	result, _, err := m.ApplyRevision(context.Background(), []byte("expected config"), "rev-1")
	if err != nil || result.Idempotent {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(runner.calls) != callsBefore+2 {
		t.Fatalf("expected validation and reload, calls=%v", runner.calls[callsBefore:])
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "expected config" {
		t.Fatalf("config=%q err=%v", data, readErr)
	}
}

func TestApplyRevisionRetainsTenNewestKnownGoodBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodeflow.cfg")
	m := ConfigManager{Runner: &fakeRunner{}, ManagedConfig: path, HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	for i := 0; i < maxKnownGoodBackups+2; i++ {
		config := fmt.Sprintf("config-%02d", i)
		revision := fmt.Sprintf("rev-%02d", i)
		if _, _, err := m.ApplyRevision(context.Background(), []byte(config), revision); err != nil {
			t.Fatalf("apply %d: %v", i, err)
		}
	}

	backups, err := configBackups(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != maxKnownGoodBackups {
		t.Fatalf("backups=%d want=%d: %v", len(backups), maxKnownGoodBackups, backups)
	}
	seen := make(map[string]bool, len(backups))
	for _, backup := range backups {
		data, readErr := os.ReadFile(backup)
		if readErr != nil {
			t.Fatal(readErr)
		}
		seen[string(data)] = true
		if _, statErr := os.Stat(backup + ".revision"); statErr != nil {
			t.Fatalf("missing revision sidecar for %s: %v", backup, statErr)
		}
	}
	if seen["config-00"] || !seen["config-01"] || !seen["config-10"] {
		t.Fatalf("retained configs=%v", seen)
	}
}

func TestApplyRevisionDoesNotPruneBackupsBeforeSuccessfulReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodeflow.cfg")
	if err := os.WriteFile(path, []byte("current"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".revision", []byte("rev-current\n"), 0640); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxKnownGoodBackups; i++ {
		backup := fmt.Sprintf("%s.bak.20260712T000000.%09dZ", path, i)
		if err := os.WriteFile(backup, []byte(fmt.Sprintf("old-%d", i)), 0640); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(backup+".revision", []byte(fmt.Sprintf("rev-%d\n", i)), 0640); err != nil {
			t.Fatal(err)
		}
	}
	m := ConfigManager{Runner: &fakeRunner{failReload: true}, ManagedConfig: path, HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	if _, _, err := m.ApplyRevision(context.Background(), []byte("candidate"), "rev-candidate"); err == nil {
		t.Fatal("expected reload failure")
	}

	backups, err := configBackups(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != maxKnownGoodBackups+1 {
		t.Fatalf("backups pruned before successful reload: %d", len(backups))
	}
}

func configBackups(path string) ([]string, error) {
	paths, err := filepath.Glob(path + ".bak.*")
	if err != nil {
		return nil, err
	}
	backups := paths[:0]
	for _, candidate := range paths {
		if !strings.HasSuffix(candidate, ".revision") {
			backups = append(backups, candidate)
		}
	}
	return backups, nil
}

func TestRollbackRestoresLastKnownGoodRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodeflow.cfg")
	runner := &fakeRunner{}
	m := ConfigManager{Runner: runner, ManagedConfig: path, HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	if _, _, err := m.ApplyRevision(context.Background(), []byte("one"), "rev-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ApplyRevision(context.Background(), []byte("two"), "rev-2"); err != nil {
		t.Fatal(err)
	}
	revision, err := m.Rollback(context.Background())
	if err != nil || revision != "rev-1" {
		t.Fatalf("revision=%q err=%v", revision, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "one" {
		t.Fatalf("config=%q", data)
	}
	actual, _ := m.ActualRevision()
	if actual != "rev-1" {
		t.Fatalf("actual=%q", actual)
	}
}

func TestRollbackDoesNotPublishMarkerBeforeReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodeflow.cfg")
	backup := path + ".bak.20260712T000000.000000000Z"
	for file, data := range map[string]string{
		path:                 "current config",
		path + ".revision":   "rev-current\n",
		backup:               "backup config",
		backup + ".revision": "rev-backup\n",
	} {
		if err := os.WriteFile(file, []byte(data), 0640); err != nil {
			t.Fatal(err)
		}
	}
	runner := &revisionObservingRunner{marker: path + ".revision"}
	m := ConfigManager{Runner: runner, ManagedConfig: path, HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}

	revision, err := m.Rollback(context.Background())
	if err != nil || revision != "rev-backup" {
		t.Fatalf("revision=%q err=%v", revision, err)
	}
	if len(runner.seenRevisions) != 1 || runner.seenRevisions[0] != "rev-current" {
		t.Fatalf("marker visible during reload=%v", runner.seenRevisions)
	}
	actual, err := m.ActualRevision()
	if err != nil || actual != "rev-backup" {
		t.Fatalf("actual=%q err=%v", actual, err)
	}
}
