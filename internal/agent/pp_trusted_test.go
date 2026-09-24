package agent

import (
	"context"
	"errors"
	"io"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePPResolver struct {
	mu    sync.Mutex
	addrs map[string][]string
	fail  map[string]bool
	calls int
}

func (r *fakePPResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.fail[host] {
		return nil, errors.New("temporary failure")
	}
	var out []netip.Addr
	for _, text := range r.addrs[host] {
		out = append(out, netip.MustParseAddr(text))
	}
	if len(out) == 0 {
		return nil, errors.New("no such host")
	}
	return out, nil
}

type fakePPRuntime struct {
	loaded   map[string][]string
	shows    int
	replaces int
}

func (r *fakePPRuntime) ShowACL(_ context.Context, ref string) ([]string, bool, error) {
	r.shows++
	values, ok := r.loaded[ref]
	return append([]string(nil), values...), ok, nil
}

func (r *fakePPRuntime) ReplaceACL(_ context.Context, ref string, values []string) error {
	r.replaces++
	r.loaded[ref] = append([]string(nil), values...)
	return nil
}

func ppTrustedFixture(t *testing.T, domains string) (*PPTrustedController, *fakePPResolver, *fakePPRuntime, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "nodeflow")
	file := filepath.Join(dir, "pp-trusted-nf_fe_any_443_abcd0123.acl")
	config := "frontend nf_fe_any_443_abcd0123\n    # nf-pp-trusted file=" + file + " domains=" + domains + "\n    acl nf_pp_trusted src -f " + file + "\n"
	managed := filepath.Join(root, "haproxy.cfg")
	require.NoError(t, os.WriteFile(managed, []byte(config), 0o600))
	resolver := &fakePPResolver{addrs: map[string][]string{}, fail: map[string]bool{}}
	runtime := &fakePPRuntime{loaded: map[string][]string{}}
	manager := &ConfigManager{ManagedConfig: managed}
	c := NewPPTrustedController(manager, runtime, resolver)
	c.Dir = dir
	c.Logger = log.New(io.Discard, "", 0)
	return c, resolver, runtime, file
}

func TestParsePPTrustedAnnotationsRejectsForeignPaths(t *testing.T) {
	for _, line := range []string{
		"# nf-pp-trusted file=/etc/passwd domains=a.example.com",
		"# nf-pp-trusted file=/etc/haproxy/nodeflow/../haproxy.cfg domains=a.example.com",
		"# nf-pp-trusted file=/etc/haproxy/nodeflow/pp-trusted-x.acl domains=",
		"# nf-pp-trusted file=/etc/haproxy/nodeflow/pp-trusted-x.acl domains=A;B",
	} {
		_, err := ParsePPTrustedAnnotations([]byte(line+"\n"), "")
		assert.Error(t, err, line)
	}
	sets, err := ParsePPTrustedAnnotations([]byte("    # nf-pp-trusted file=/etc/haproxy/nodeflow/pp-trusted-nf_fe_any_443_ab.acl domains=b.example.com,a.example.com\n"), "")
	require.NoError(t, err)
	assert.Equal(t, []PPTrustedSet{{File: "/etc/haproxy/nodeflow/pp-trusted-nf_fe_any_443_ab.acl", Domains: []string{"a.example.com", "b.example.com"}}}, sets)
}

func TestPPTrustedPrepareWritesFileBeforeValidation(t *testing.T) {
	c, resolver, _, file := ppTrustedFixture(t, "a.example.com,b.example.com")
	resolver.addrs["a.example.com"] = []string{"192.0.2.1", "2001:db8::1"}
	resolver.fail["b.example.com"] = true
	config, err := os.ReadFile(c.Manager.ManagedConfig)
	require.NoError(t, err)
	require.NoError(t, c.Prepare(context.Background(), config))
	got, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, "192.0.2.1\n2001:db8::1\n", string(got))

	// Nothing resolves on a fresh start: an empty file keeps HAProxy valid.
	c2, _, _, file2 := ppTrustedFixture(t, "down.example.com")
	config2, _ := os.ReadFile(c2.Manager.ManagedConfig)
	require.NoError(t, c2.Prepare(context.Background(), config2))
	got, err = os.ReadFile(file2)
	require.NoError(t, err)
	assert.Equal(t, "", string(got))
}

func TestPPTrustedReconcileUpdatesRuntimeOnlyOnChange(t *testing.T) {
	c, resolver, runtime, file := ppTrustedFixture(t, "a.example.com")
	resolver.addrs["a.example.com"] = []string{"192.0.2.1"}
	config, _ := os.ReadFile(c.Manager.ManagedConfig)
	require.NoError(t, c.Prepare(context.Background(), config))
	runtime.loaded[file] = []string{"192.0.2.1"} // HAProxy loaded the file

	replaced, err := c.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Empty(t, replaced)
	assert.Equal(t, 1, runtime.shows, "one show acl after start/reload")

	// Idle: unchanged set, no socket traffic at all.
	for i := 0; i < 3; i++ {
		replaced, err = c.Reconcile(context.Background())
		require.NoError(t, err)
		assert.Empty(t, replaced)
	}
	assert.Equal(t, 1, runtime.shows)
	assert.Equal(t, 0, runtime.replaces)

	// DNS changes: file and runtime are replaced once.
	resolver.addrs["a.example.com"] = []string{"192.0.2.2", "192.0.2.3"}
	replaced, err = c.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{file}, replaced)
	assert.Equal(t, []string{"192.0.2.2", "192.0.2.3"}, runtime.loaded[file])
	got, _ := os.ReadFile(file)
	assert.Equal(t, "192.0.2.2\n192.0.2.3\n", string(got))

	// Transient failure keeps the last known set.
	resolver.fail["a.example.com"] = true
	replaced, err = c.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Empty(t, replaced)
	assert.Equal(t, []string{"192.0.2.2", "192.0.2.3"}, runtime.loaded[file])
	assert.Equal(t, 1, runtime.replaces)
	assert.Equal(t, 1, runtime.shows)

	// A reload (mutation epoch change) re-reads the runtime ACL once.
	c.Manager.markMutated()
	_, err = c.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, runtime.shows)
}

func TestPPTrustedReconcileRemovesStaleFiles(t *testing.T) {
	c, resolver, runtime, file := ppTrustedFixture(t, "a.example.com")
	resolver.addrs["a.example.com"] = []string{"192.0.2.1"}
	runtime.loaded[file] = []string{"192.0.2.1"}
	stale := filepath.Join(c.Dir, "pp-trusted-nf_fe_any_8443_ffff0000.acl")
	require.NoError(t, os.MkdirAll(c.Dir, 0o755))
	require.NoError(t, os.WriteFile(stale, []byte("192.0.2.9\n"), 0o644))
	other := filepath.Join(c.Dir, "unrelated.txt")
	require.NoError(t, os.WriteFile(other, nil, 0o644))
	_, err := c.Reconcile(context.Background())
	require.NoError(t, err)
	assert.NoFileExists(t, stale)
	assert.FileExists(t, other)
	assert.FileExists(t, file)

	// No annotations any more: every pp-trusted file goes, no socket traffic.
	require.NoError(t, os.WriteFile(c.Manager.ManagedConfig, []byte("frontend x\n"), 0o600))
	shows := runtime.shows
	_, err = c.Reconcile(context.Background())
	require.NoError(t, err)
	assert.NoFileExists(t, file)
	assert.Equal(t, shows, runtime.shows)
}

func TestPPTrustedValidateRunsPrepare(t *testing.T) {
	c, resolver, _, file := ppTrustedFixture(t, "a.example.com")
	resolver.addrs["a.example.com"] = []string{"192.0.2.7"}
	runner := &recordingRunner{}
	c.Manager.Runner = runner
	c.Manager.HAProxyBinary = "haproxy"
	config, _ := os.ReadFile(c.Manager.ManagedConfig)
	require.NoError(t, c.Manager.Validate(context.Background(), config))
	got, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, "192.0.2.7\n", string(got))
	assert.True(t, strings.Contains(strings.Join(runner.calls, "|"), "haproxy -c -f"))
}

type recordingRunner struct{ calls []string }

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return nil, nil
}
