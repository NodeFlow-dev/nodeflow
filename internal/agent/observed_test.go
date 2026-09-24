package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestObservedConfigStateSamplesRevisionAndChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "haproxy.cfg")
	config := []byte("global\n  daemon\n")
	if err := os.WriteFile(path, config, 0640); err != nil {
		t.Fatal(err)
	}
	m := &ConfigManager{ManagedConfig: path}
	if err := os.WriteFile(m.revisionPath(), []byte("42\n"), 0640); err != nil {
		t.Fatal(err)
	}
	got := m.ObservedConfigState()
	if got.ActualRevision == nil || *got.ActualRevision != 42 {
		t.Fatalf("actual revision=%v", got.ActualRevision)
	}
	sum := sha256.Sum256(config)
	if got.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256=%q", got.SHA256)
	}
}

func TestObservedConfigStateKeepsChecksumWhenMarkerMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "haproxy.cfg")
	if err := os.WriteFile(path, []byte("defaults\n  mode tcp\n"), 0640); err != nil {
		t.Fatal(err)
	}
	got := (&ConfigManager{ManagedConfig: path}).ObservedConfigState()
	if got.ActualRevision != nil || got.SHA256 == "" {
		t.Fatalf("observed=%+v", got)
	}
}

func TestObservedConfigStateCacheTracksFileChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "haproxy.cfg")
	m := &ConfigManager{ManagedConfig: path}
	if got := m.ObservedConfigState(); got.SHA256 != "" {
		t.Fatalf("missing config must have no checksum: %+v", got)
	}
	first := []byte("global\n  daemon\n")
	if err := atomicWrite(path, first, 0640); err != nil {
		t.Fatal(err)
	}
	firstSum := sha256.Sum256(first)
	if got := m.ObservedConfigState().SHA256; got != hex.EncodeToString(firstSum[:]) {
		t.Fatalf("sha256=%q", got)
	}
	if got := m.ObservedConfigState().SHA256; got != hex.EncodeToString(firstSum[:]) {
		t.Fatalf("cached sha256=%q", got)
	}
	// Same size and forced identical mtime: an atomic rename still changes the
	// inode, so the cached checksum must not be reused.
	second := []byte("global\n  nbthrd\n")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(path, second, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	secondSum := sha256.Sum256(second)
	if got := m.ObservedConfigState().SHA256; got != hex.EncodeToString(secondSum[:]) {
		t.Fatalf("stale sha256 after rename=%q", got)
	}
	// In-place edit with a different mtime must also be observed.
	third := []byte("global\n  quiet\n\n")
	if err := os.WriteFile(path, third, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime().Add(time.Second), info.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	thirdSum := sha256.Sum256(third)
	if got := m.ObservedConfigState().SHA256; got != hex.EncodeToString(thirdSum[:]) {
		t.Fatalf("stale sha256 after edit=%q", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := m.ObservedConfigState().SHA256; got != "" {
		t.Fatalf("removed config must clear checksum: %q", got)
	}
	if err := os.WriteFile(path, nil, 0640); err != nil {
		t.Fatal(err)
	}
	if got := m.ObservedConfigState().SHA256; got != "" {
		t.Fatalf("empty config must have no checksum: %q", got)
	}
}
