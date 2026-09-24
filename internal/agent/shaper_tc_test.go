package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseKernelShaperRules(t *testing.T) {
	config := []byte(`global
frontend nf_fe_any_8443_x
    bind :8443
    # nf-kernel-shaper listen=* port=8443 download_bps=12500000 upload_bps=3125000
frontend nf_fe_10_0_0_1_443_y
    # nf-kernel-shaper listen=10.0.0.1 port=443 download_bps=1250000 upload_bps=0
    # nf-kernel-shaper listen=10.0.0.1 port=443 download_bps=1250000 upload_bps=0
    # nf-kernel-shaper listen=* port=9000 download_bps=0 upload_bps=0
`)
	rules, err := ParseKernelShaperRules(config)
	require.NoError(t, err)
	assert.Equal(t, []KernelShaperRule{
		{ListenIP: "10.0.0.1", Port: 443, DownloadBps: 1250000},
		{ListenIP: "*", Port: 8443, DownloadBps: 12500000, UploadBps: 3125000},
	}, rules)
}

func TestParseKernelShaperRulesRejectsMalformed(t *testing.T) {
	for _, line := range []string{
		"# nf-kernel-shaper listen=* port=0 download_bps=1",
		"# nf-kernel-shaper listen=bad port=443 download_bps=1",
		"# nf-kernel-shaper listen=* port=443 download_bps=-1",
		"# nf-kernel-shaper listen=* port=443 download_bps=1;drop",
		"# nf-kernel-shaper listen=* port=443 evil=1",
		"# nf-kernel-shaper port=443 download_bps=1",
		"# nf-kernel-shaper listen=* port=443 port=444 download_bps=1",
		"# nf-kernel-shaper listen=* port=443 download_bps=1\n# nf-kernel-shaper listen=* port=443 download_bps=2",
	} {
		_, err := ParseKernelShaperRules([]byte(line))
		assert.Error(t, err, line)
	}
}

func TestBuildKernelShaperScriptWildcard(t *testing.T) {
	script, hash := BuildKernelShaperScript([]KernelShaperRule{{ListenIP: "*", Port: 8443, DownloadBps: 12500000, UploadBps: 125000}})
	require.Len(t, hash, 64)
	want := `table inet nodeflow_shaper
delete table inet nodeflow_shaper
table inet nodeflow_shaper {
	set d4_0 { type ipv4_addr; size 65536; flags dynamic,timeout; timeout 5m; }
	set u4_0 { type ipv4_addr; size 65536; flags dynamic,timeout; timeout 5m; }
	set d6_0 { type ipv6_addr; size 65536; flags dynamic,timeout; timeout 5m; }
	set u6_0 { type ipv6_addr; size 65536; flags dynamic,timeout; timeout 5m; }
	chain output {
		type filter hook output priority filter; policy accept;
		comment "nodeflow-shaper sha256=` + hash + `"
		oifname "lo" accept
		meta nfproto ipv4 tcp sport 8443 update @d4_0 { ip daddr limit rate over 12500000 bytes/second burst 625000 bytes } drop
		meta nfproto ipv6 tcp sport 8443 update @d6_0 { ip6 daddr & ffff:ffff:ffff:ffff:: limit rate over 12500000 bytes/second burst 625000 bytes } drop
	}
	chain input {
		type filter hook input priority filter; policy accept;
		iifname "lo" accept
		meta nfproto ipv4 tcp dport 8443 update @u4_0 { ip saddr limit rate over 125000 bytes/second burst 131072 bytes } drop
		meta nfproto ipv6 tcp dport 8443 update @u6_0 { ip6 saddr & ffff:ffff:ffff:ffff:: limit rate over 125000 bytes/second burst 131072 bytes } drop
	}
}
`
	assert.Equal(t, want, script)
}

func TestBuildKernelShaperScriptSpecificAddresses(t *testing.T) {
	script, _ := BuildKernelShaperScript([]KernelShaperRule{
		{ListenIP: "192.0.2.1", Port: 443, DownloadBps: 1250000},
		{ListenIP: "2001:db8::1", Port: 443, UploadBps: 1250000},
	})
	assert.Contains(t, script, "meta nfproto ipv4 ip saddr 192.0.2.1 tcp sport 443 update @d4_0 { ip daddr limit rate over 1250000 bytes/second burst 131072 bytes } drop")
	assert.Contains(t, script, "meta nfproto ipv6 ip6 daddr 2001:db8::1 tcp dport 443 update @u6_1 { ip6 saddr & ffff:ffff:ffff:ffff:: limit")
	assert.NotContains(t, script, "d6_0")
	assert.NotContains(t, script, "u4_1")
	assert.NotContains(t, script, "d4_1")
}

func TestBuildKernelShaperScriptDeterministicAndEmpty(t *testing.T) {
	rules := []KernelShaperRule{{ListenIP: "*", Port: 1, DownloadBps: 1}}
	a, ha := BuildKernelShaperScript(rules)
	b, hb := BuildKernelShaperScript(rules)
	assert.Equal(t, a, b)
	assert.Equal(t, ha, hb)
	_, hc := BuildKernelShaperScript([]KernelShaperRule{{ListenIP: "*", Port: 1, DownloadBps: 2}})
	assert.NotEqual(t, ha, hc)
	s, h := BuildKernelShaperScript(nil)
	assert.Empty(t, s)
	assert.Empty(t, h)
}

// nftFake emulates the subset of nft used by KernelShaper.
type nftFake struct {
	mu      sync.Mutex
	table   string // installed script, "" when absent
	calls   []string
	failRun error
}

func (f *nftFake) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	switch {
	case len(args) >= 2 && args[0] == "list":
		if f.table == "" {
			return []byte("Error: No such file or directory\nlist chain inet nodeflow_shaper output"), errors.New("exit status 1")
		}
		idx := strings.Index(f.table, "chain output")
		return []byte("table inet nodeflow_shaper {\n\t" + f.table[idx:]), nil
	case len(args) >= 1 && args[0] == "delete":
		f.table = ""
		return nil, nil
	case len(args) >= 2 && args[0] == "-c":
		data, err := os.ReadFile(args[2])
		if err != nil {
			return nil, err
		}
		if !strings.Contains(string(data), "table inet nodeflow_shaper {") {
			return []byte("bad"), errors.New("exit 1")
		}
		return nil, nil
	case len(args) >= 2 && args[0] == "-f":
		if f.failRun != nil {
			return []byte("Error: Could not process rule: No such file or directory\n"), f.failRun
		}
		data, err := os.ReadFile(args[1])
		if err != nil {
			return nil, err
		}
		f.table = string(data)
		return nil, nil
	}
	return nil, errors.New("unexpected nft call")
}

func (f *nftFake) callCount(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

type recordingReporter struct {
	mu      sync.Mutex
	reports []ConfigReport
}

func (r *recordingReporter) Post(_ context.Context, report ConfigReport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, report)
	return nil
}

func newShaperFixture(t *testing.T, config string) (*KernelShaper, *nftFake, *recordingReporter, string, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "haproxy.cfg")
	require.NoError(t, os.WriteFile(cfg, []byte(config), 0o600))
	manager := &ConfigManager{ManagedConfig: cfg}
	require.NoError(t, os.WriteFile(manager.revisionPath(), []byte("7\n"), 0o600))
	fake := &nftFake{}
	reporter := &recordingReporter{}
	now := time.Unix(1_700_000_000, 0)
	shaper := &KernelShaper{Runner: fake, Manager: manager, Reporter: reporter, Mode: KernelShaperModeApply, TempDir: dir, Now: func() time.Time { return now }}
	return shaper, fake, reporter, cfg, &now
}

const shapedConfig = "frontend f\n    # nf-kernel-shaper listen=* port=8443 download_bps=12500000 upload_bps=0\n"

func TestKernelShaperReconcileIdempotentAndRemoval(t *testing.T) {
	shaper, fake, _, cfg, now := newShaperFixture(t, shapedConfig)
	ctx := context.Background()

	status, err := shaper.Reconcile(ctx)
	require.NoError(t, err)
	assert.True(t, status.Changed)
	assert.Equal(t, 1, status.Rules)
	assert.Equal(t, 1, fake.callCount("nft -f"))
	assert.Contains(t, fake.table, "tcp sport 8443")

	// Same config within the drift window: no nft calls at all.
	before := len(fake.calls)
	_, err = shaper.Reconcile(ctx)
	require.NoError(t, err)
	assert.Equal(t, before, len(fake.calls))

	// After the drift window: one list, no re-apply.
	*now = now.Add(2 * time.Minute)
	status, err = shaper.Reconcile(ctx)
	require.NoError(t, err)
	assert.False(t, status.Changed)
	assert.Equal(t, 1, fake.callCount("nft -f"))

	// Externally deleted table is restored on the next drift check.
	fake.table = ""
	*now = now.Add(2 * time.Minute)
	status, err = shaper.Reconcile(ctx)
	require.NoError(t, err)
	assert.True(t, status.Changed)
	assert.Equal(t, 2, fake.callCount("nft -f"))

	// Disabling kernel shaping (annotation gone) removes the table.
	require.NoError(t, os.WriteFile(cfg, []byte("frontend f\n"), 0o600))
	status, err = shaper.Reconcile(ctx)
	require.NoError(t, err)
	assert.True(t, status.Removed)
	assert.Empty(t, fake.table)
	assert.Equal(t, 1, fake.callCount("nft delete table inet nodeflow_shaper"))

	// Nothing desired and nothing installed: list only.
	*now = now.Add(2 * time.Minute)
	status, err = shaper.Reconcile(ctx)
	require.NoError(t, err)
	assert.False(t, status.Removed)
	assert.Equal(t, 1, fake.callCount("nft delete"))
}

func TestKernelShaperDryRunNeverMutates(t *testing.T) {
	shaper, fake, _, _, _ := newShaperFixture(t, shapedConfig)
	shaper.Mode = KernelShaperModeDryRun
	status, err := shaper.Reconcile(context.Background())
	require.NoError(t, err)
	assert.True(t, status.Changed)
	assert.Contains(t, status.Planned, "update @d4_0")
	assert.Empty(t, fake.table)
	assert.Equal(t, 0, fake.callCount("nft -f"))
	assert.Equal(t, 1, fake.callCount("nft -c -f"))

	fake.table = "table inet nodeflow_shaper {\n\tchain output {\n\t\tcomment \"nodeflow-shaper sha256=old\"\n\t}\n}\n"
	shaper.Mode = KernelShaperModeDryRun
	shaper.lastOK = false
	status, err = shaper.Reconcile(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, fake.table)
	assert.Equal(t, 0, fake.callCount("nft delete"))
	assert.Equal(t, 0, fake.callCount("nft -f"))
	_ = status
}

func TestKernelShaperOffModeDoesNothing(t *testing.T) {
	shaper, fake, _, _, _ := newShaperFixture(t, shapedConfig)
	shaper.Mode = KernelShaperModeOff
	_, err := shaper.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Empty(t, fake.calls)
}

func TestKernelShaperReportsPersistentFailureOnceAndRecovery(t *testing.T) {
	shaper, fake, reporter, _, _ := newShaperFixture(t, shapedConfig)
	fake.failRun = errors.New("exit status 1")
	ctx := context.Background()

	_, err := shaper.Reconcile(ctx)
	require.Error(t, err)
	assert.Empty(t, reporter.reports, "first failure may race the config applied report")

	for i := 0; i < 3; i++ {
		_, err = shaper.Reconcile(ctx)
		require.Error(t, err)
	}
	require.Len(t, reporter.reports, 1)
	report := reporter.reports[0]
	assert.Equal(t, int64(7), report.Revision)
	assert.Equal(t, "failed", report.State)
	assert.Equal(t, "kernel_shaper_failed", report.Error)
	require.NotNil(t, report.ActualRevision)
	assert.Equal(t, int64(7), *report.ActualRevision)
	assert.Equal(t, "kernel_shaper", report.Details["component"])
	assert.Contains(t, report.Details["message"], "Could not process rule")
	assert.Contains(t, shaper.Snapshot().LastError, "nft -f")

	fake.failRun = nil
	_, err = shaper.Reconcile(ctx)
	require.NoError(t, err)
	require.Len(t, reporter.reports, 2)
	assert.Equal(t, "applied", reporter.reports[1].State)
	assert.Equal(t, int64(7), reporter.reports[1].Revision)
}

func TestKernelShaperInvalidAnnotationReported(t *testing.T) {
	shaper, fake, reporter, _, _ := newShaperFixture(t, "# nf-kernel-shaper listen=* port=99999 download_bps=1\n")
	for i := 0; i < 2; i++ {
		_, err := shaper.Reconcile(context.Background())
		require.Error(t, err)
	}
	assert.Empty(t, fake.calls)
	require.Len(t, reporter.reports, 1)
	assert.Equal(t, "kernel_shaper_failed", reporter.reports[0].Error)
}
