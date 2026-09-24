package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCollectStatsReportsAgentPlatform(t *testing.T) {
	stats, err := CollectStats()
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, runtime.GOOS, stats.OS)
	assert.Equal(t, runtime.GOARCH, stats.Arch)
}

func TestParseProcessCount(t *testing.T) {
	count := parseProcessCount(strings.Fields("0.12 0.34 0.56 2/137 900"))
	if assert.NotNil(t, count) {
		assert.Equal(t, 137, *count)
	}
	for _, raw := range []string{"", "null", "0.12 0.34 0.56", "0 0 0 /137", "0 0 0 x/137", "0 0 0 2/x", "0 0 0 2/-1", "0 0 0 2/0", "0 0 0 4/3", "0 0 0 2/3/4"} {
		assert.Nil(t, parseProcessCount(strings.Fields(raw)), raw)
	}
}

func TestProcessSamplerRanksBoundsAndCaches(t *testing.T) {
	root := t.TempDir()
	writeProcessCmdline(t, root, "100", "/usr/bin/alpha")
	writeProcessCmdline(t, root, "101", "/usr/bin/beta")
	writeProcessCmdline(t, root, "102", "/usr/bin/alpha")
	writeProcessCmdline(t, root, "103", "/usr/bin/gamma")
	writeProcessCmdline(t, root, "104", "/usr/bin/beta")
	if err := os.Mkdir(filepath.Join(root, "not-a-pid"), 0o755); err != nil {
		t.Fatal(err)
	}

	sampler := &ProcessSampler{ProcRoot: root, Limit: 2}
	start := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	assert.Equal(t, []string{"alpha", "beta"}, sampler.Sample(start))

	writeProcessCmdline(t, root, "100", "/usr/bin/delta")
	assert.Equal(t, []string{"alpha", "beta"}, sampler.Sample(start.Add(4*time.Minute)))
	assert.Equal(t, []string{"beta", "alpha"}, sampler.Sample(start.Add(5*time.Minute)))

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, []string{"beta", "alpha"}, sampler.Sample(start.Add(10*time.Minute)))
}

func TestCollectProcessNamesDefaultBound(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < defaultProcessNameLimit+3; i++ {
		writeProcessCmdline(t, root, strconv.Itoa(100+i), "/usr/local/bin/"+fmt.Sprintf("process-%02d", i))
	}
	names, err := collectProcessNames(root, defaultProcessNameLimit+100)
	if err != nil {
		t.Fatal(err)
	}
	assert.Len(t, names, defaultProcessNameLimit)
	assert.Equal(t, "process-00", names[0])
}

func TestCollectProcessNamesUsesCmdlineAndSkipsKernelThreads(t *testing.T) {
	root := t.TempDir()
	writeProcessCmdline(t, root, "100", "/usr/local/bin/nodeflow-node-agent")
	writeProcessCmdline(t, root, "101", "haproxy")
	writeProcessFile(t, root, "102", "cmdline", nil)
	writeProcessFile(t, root, "102", "comm", []byte("kworker/0:1\n"))
	writeProcessFile(t, root, "103", "comm", []byte("ksoftirqd/0\n"))
	writeProcessFile(t, root, "104", "cmdline", []byte{'\x00', '-', '-', 'x', '\x00'})
	writeProcessFile(t, root, "104", "comm", []byte("fallback-name\n"))

	names, err := collectProcessNames(root, defaultProcessNameLimit)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, []string{"fallback-name", "haproxy", "nodeflow-node-agent"}, names)
}

func writeProcessCmdline(t *testing.T, root, pid, argv0 string) {
	t.Helper()
	writeProcessFile(t, root, pid, "cmdline", append([]byte(argv0), '\x00', '-', '-', 't', 'e', 's', 't', '\x00'))
}

func writeProcessFile(t *testing.T, root, pid, name string, content []byte) {
	t.Helper()
	dir := filepath.Join(root, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkRateSamplerDeltaResetAndNewInterface(t *testing.T) {
	sampler := &NetworkRateSampler{}
	start := time.Unix(100, 0)
	assert.Nil(t, sampler.Sample(map[string]uint64{"eth0_rx": 1000, "eth0_tx": 500}, start))
	rates := sampler.Sample(map[string]uint64{"eth0_rx": 3000, "eth0_tx": 1500}, start.Add(2*time.Second))
	assert.Equal(t, float64(1000), rates["eth0_rx"])
	assert.Equal(t, float64(500), rates["eth0_tx"])
	rates = sampler.Sample(map[string]uint64{"eth0_rx": 50, "eth0_tx": 1700, "wg0_rx": 10}, start.Add(4*time.Second))
	_, hasResetRate := rates["eth0_rx"]
	_, hasNewRate := rates["wg0_rx"]
	assert.False(t, hasResetRate)
	assert.False(t, hasNewRate)
	assert.Equal(t, float64(100), rates["eth0_tx"])
}

func TestCPUUsageSamplerDeltaAndReset(t *testing.T) {
	sampler := &CPUUsageSampler{}
	assert.Nil(t, sampler.Sample(1000, 700))
	percent := sampler.Sample(1200, 800)
	if assert.NotNil(t, percent) {
		assert.InDelta(t, 50, *percent, 0.001)
	}
	assert.Nil(t, sampler.Sample(100, 70))
}

func TestParseCPUCountersIgnoresGuestCounters(t *testing.T) {
	total, idle, ok := parseCPUCounters("cpu  10 2 3 40 5 6 7 8 99 100\ncpu0 1 1 1 1\n")
	assert.True(t, ok)
	assert.Equal(t, uint64(81), total)
	assert.Equal(t, uint64(45), idle)
}

type versionRunner struct{ output []byte }

func (r versionRunner) Run(context.Context, string, ...string) ([]byte, error) { return r.output, nil }

func TestHAProxyVersionReturnsVersionOnly(t *testing.T) {
	runner := versionRunner{output: []byte("HAProxy version 2.8.16-0ubuntu0.24.04.3 2026/06/19 - https://haproxy.org/\n")}
	if got := HAProxyVersion(context.Background(), runner, "haproxy"); got != "2.8.16-0ubuntu0.24.04.3" {
		t.Fatalf("unexpected version: %q", got)
	}
}

type countingVersionRunner struct {
	mu    sync.Mutex
	calls int
}

func (r *countingVersionRunner) Run(context.Context, string, ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return []byte("HAProxy version 2.8.16\n"), nil
}

func TestHAProxyVersionSamplerCachesAndRefreshes(t *testing.T) {
	runner := &countingVersionRunner{}
	sampler := &HAProxyVersionSampler{Runner: runner, Binary: "haproxy", RefreshInterval: time.Minute}
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	if got := sampler.Sample(context.Background(), now); got != "2.8.16" {
		t.Fatalf("version=%q", got)
	}
	_ = sampler.Sample(context.Background(), now.Add(30*time.Second))
	_ = sampler.Sample(context.Background(), now.Add(time.Minute))
	runner.mu.Lock()
	calls := runner.calls
	runner.mu.Unlock()
	if calls != 2 {
		t.Fatalf("haproxy -v calls=%d", calls)
	}
}

func TestParseMeminfoLine(t *testing.T) {
	cases := []struct {
		line string
		key  string
		kb   uint64
		ok   bool
	}{
		{"MemTotal:       16318784 kB", "MemTotal:", 16318784, true},
		{"MemAvailable:    9876543 kB", "MemAvailable:", 9876543, true},
		{"MemFree:         1234 kB", "", 0, false},
		{"MemTotal:       16318784", "", 0, false},
		{"MemTotal:       abc kB", "", 0, false},
		{"HugePages_Total:       0", "", 0, false},
	}
	for _, c := range cases {
		key, kb, ok := parseMeminfoLine([]byte(c.line))
		if key != c.key || kb != c.kb || ok != c.ok {
			t.Fatalf("%q: key=%q kb=%d ok=%t", c.line, key, kb, ok)
		}
	}
}

func TestNetworkRateSamplerReusesMapsWithoutAliasing(t *testing.T) {
	sampler := &NetworkRateSampler{}
	start := time.Unix(1700000000, 0)
	counters := map[string]uint64{"eth0_rx": 100, "eth0_tx": 200}
	if rates := sampler.Sample(counters, start); rates != nil {
		t.Fatalf("first sample=%v", rates)
	}
	// Mutating the caller's map must not change the stored baseline.
	counters["eth0_rx"] = 1_000_000
	rates := sampler.Sample(map[string]uint64{"eth0_rx": 150, "eth0_tx": 260, "eth1_rx": 5}, start.Add(10*time.Second))
	if rates["eth0_rx"] != 5 || rates["eth0_tx"] != 6 || len(rates) != 2 {
		t.Fatalf("second rates=%v", rates)
	}
	// Interfaces missing from the latest sample must not survive map reuse.
	rates = sampler.Sample(map[string]uint64{"eth1_rx": 25}, start.Add(20*time.Second))
	if rates["eth1_rx"] != 2 || len(rates) != 1 {
		t.Fatalf("third rates=%v", rates)
	}
	rates = sampler.Sample(map[string]uint64{"eth0_rx": 500}, start.Add(30*time.Second))
	if rates != nil {
		t.Fatalf("stale eth0 baseline leaked through reused map: %v", rates)
	}
}
