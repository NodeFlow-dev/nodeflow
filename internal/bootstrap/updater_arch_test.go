package bootstrap

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenArchUpdaterPrefersNodeArchitectureBinary(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "node-updater")
	require.NoError(t, os.WriteFile(base, []byte("native"), 0o755))
	require.NoError(t, os.WriteFile(base+"-linux-arm64", []byte("arm64"), 0o755))

	f := openArchUpdater(base, "linux", "arm64")
	require.NotNil(t, f)
	defer f.Close()
	content, err := io.ReadAll(f)
	require.NoError(t, err)
	assert.Equal(t, "arm64", string(content))

	assert.Nil(t, openArchUpdater(base, "linux", "amd64"), "missing per-arch binary falls back to the base binary")
	assert.Nil(t, openArchUpdater("", "linux", "arm64"))
	assert.Nil(t, openArchUpdater(base, "", ""))
}
