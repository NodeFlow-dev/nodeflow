package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeKernelPipes(t *testing.T, maxSize, soft string) (*KernelPipeTuner, string, string) {
	t.Helper()
	root := t.TempDir()
	proc := filepath.Join(root, "proc")
	sysctl := filepath.Join(root, "sysctl.d")
	require.NoError(t, os.MkdirAll(filepath.Join(proc, "sys/fs"), 0o755))
	require.NoError(t, os.MkdirAll(sysctl, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(proc, pipeMaxSizePath), []byte(maxSize), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(proc, pipeUserPagesSoftPath), []byte(soft), 0o644))
	return &KernelPipeTuner{ProcRoot: proc, SysctlDir: sysctl}, proc, sysctl
}

func readTrimmed(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(raw)
}

func TestKernelPipeTunerRaisesDefaults(t *testing.T) {
	tuner, proc, sysctl := fakeKernelPipes(t, "1048576\n", "16384\n")
	state := tuner.Ensure()
	assert.Equal(t, KernelPipes{PipeMaxSize: 1048576, PipeUserPagesSoft: 0, Tuned: true, Managed: true}, state)
	assert.Equal(t, "0\n", readTrimmed(t, filepath.Join(proc, pipeUserPagesSoftPath)))
	assert.Equal(t, "1048576\n", readTrimmed(t, filepath.Join(proc, pipeMaxSizePath)))
	conf := readTrimmed(t, filepath.Join(sysctl, KernelPipesSysctlFile))
	assert.Contains(t, conf, "fs.pipe-user-pages-soft = 0\n")
	assert.Contains(t, conf, "fs.pipe-max-size = 1048576\n")
}

func TestKernelPipeTunerRaisesSmallMaxSize(t *testing.T) {
	tuner, proc, _ := fakeKernelPipes(t, "65536\n", "0\n")
	state := tuner.Ensure()
	assert.True(t, state.Tuned)
	assert.Equal(t, "1048576\n", readTrimmed(t, filepath.Join(proc, pipeMaxSizePath)))
}

func TestKernelPipeTunerNeverLowersMaxSize(t *testing.T) {
	tuner, proc, sysctl := fakeKernelPipes(t, "4194304\n", "16384\n")
	state := tuner.Ensure()
	assert.True(t, state.Tuned)
	assert.Equal(t, int64(4194304), state.PipeMaxSize)
	assert.Equal(t, "4194304\n", readTrimmed(t, filepath.Join(proc, pipeMaxSizePath)))
	assert.Contains(t, readTrimmed(t, filepath.Join(sysctl, KernelPipesSysctlFile)), "fs.pipe-max-size = 4194304\n")
}

func TestKernelPipeTunerIdempotent(t *testing.T) {
	tuner, proc, sysctl := fakeKernelPipes(t, "1048576\n", "16384\n")
	first := tuner.Ensure()
	confPath := filepath.Join(sysctl, KernelPipesSysctlFile)
	before, err := os.Stat(confPath)
	require.NoError(t, err)
	// A second pass must not rewrite anything: make the files read-only so
	// any write attempt surfaces as an error.
	require.NoError(t, os.Chmod(filepath.Join(proc, pipeMaxSizePath), 0o444))
	require.NoError(t, os.Chmod(filepath.Join(proc, pipeUserPagesSoftPath), 0o444))
	second := tuner.Ensure()
	assert.Equal(t, first, second)
	after, err := os.Stat(confPath)
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime())
}

func TestKernelPipeTunerDisabledOnlyReports(t *testing.T) {
	tuner, proc, sysctl := fakeKernelPipes(t, "1048576\n", "16384\n")
	tuner.Disabled = true
	state := tuner.Ensure()
	assert.Equal(t, KernelPipes{PipeMaxSize: 1048576, PipeUserPagesSoft: 16384}, state)
	assert.Equal(t, "16384\n", readTrimmed(t, filepath.Join(proc, pipeUserPagesSoftPath)))
	_, err := os.Stat(filepath.Join(sysctl, KernelPipesSysctlFile))
	assert.True(t, os.IsNotExist(err))
}

func TestKernelPipeTunerReportsWriteFailure(t *testing.T) {
	tuner, proc, _ := fakeKernelPipes(t, "1048576\n", "16384\n")
	tuner.SysctlDir = filepath.Join(proc, "missing")
	require.NoError(t, os.Chmod(filepath.Join(proc, pipeUserPagesSoftPath), 0o444))
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	state := tuner.Ensure()
	assert.False(t, state.Tuned)
	assert.NotEmpty(t, state.Error)
	assert.Equal(t, int64(16384), state.PipeUserPagesSoft)
}

func TestKernelPipeTunerMissingProc(t *testing.T) {
	tuner := &KernelPipeTuner{ProcRoot: t.TempDir(), SysctlDir: t.TempDir()}
	state := tuner.Ensure()
	assert.False(t, state.Tuned)
	assert.Contains(t, state.Error, "read kernel pipe limits")
}

func TestNewKernelPipeTunerFromEnv(t *testing.T) {
	t.Setenv("NODE_AGENT_TUNE_SYSCTL", "off")
	assert.True(t, NewKernelPipeTunerFromEnv().Disabled)
	t.Setenv("NODE_AGENT_TUNE_SYSCTL", "")
	assert.False(t, NewKernelPipeTunerFromEnv().Disabled)
}
