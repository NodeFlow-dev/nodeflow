package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKernelShaperRealNft exercises the generated ruleset against the real nft
// binary. It needs CAP_NET_ADMIN in a disposable network namespace, e.g.:
//
//	NODEFLOW_NFT_TEST=1 unshare -rn go test ./internal/agent -run RealNft
func TestKernelShaperRealNft(t *testing.T) {
	if os.Getenv("NODEFLOW_NFT_TEST") != "1" {
		t.Skip("set NODEFLOW_NFT_TEST=1 and run inside a private network namespace")
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "haproxy.cfg")
	config := "frontend a\n    # nf-kernel-shaper listen=* port=8443 download_bps=12500000 upload_bps=3125000\n" +
		"frontend b\n    # nf-kernel-shaper listen=192.0.2.1 port=443 download_bps=1250000 upload_bps=0\n" +
		"frontend c\n    # nf-kernel-shaper listen=2001:db8::1 port=443 download_bps=0 upload_bps=1250000\n"
	require.NoError(t, os.WriteFile(cfg, []byte(config), 0o600))
	shaper := &KernelShaper{Runner: ExecRunner{}, Manager: &ConfigManager{ManagedConfig: cfg}, Mode: KernelShaperModeDryRun, TempDir: dir}
	ctx := context.Background()

	status, err := shaper.Reconcile(ctx)
	require.NoError(t, err, "nft -c must accept the generated script")
	assert.True(t, status.Changed)
	_, present, err := shaper.liveHash(ctx)
	require.NoError(t, err)
	assert.False(t, present, "dry-run must not install the table")

	shaper.Mode = KernelShaperModeApply
	shaper.lastOK = false
	status, err = shaper.Reconcile(ctx)
	require.NoError(t, err)
	assert.True(t, status.Changed)
	live, present, err := shaper.liveHash(ctx)
	require.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, status.Hash, live)

	shaper.lastOK = false
	status, err = shaper.Reconcile(ctx)
	require.NoError(t, err)
	assert.False(t, status.Changed, "second pass must be idempotent")

	require.NoError(t, os.WriteFile(cfg, []byte("frontend a\n"), 0o600))
	status, err = shaper.Reconcile(ctx)
	require.NoError(t, err)
	assert.True(t, status.Removed)
	_, present, err = shaper.liveHash(ctx)
	require.NoError(t, err)
	assert.False(t, present)
}
