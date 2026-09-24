package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// ObservedConfigState is sampled under the same lock as apply/rollback so a
// heartbeat can never combine a revision marker from one config with bytes
// from another.
type ObservedConfigState struct {
	ActualRevision *int64
	SHA256         string
}

// observedConfigHash memoizes the managed config checksum. The entry is keyed
// by the file identity (device+inode), size and mtime observed on the same
// descriptor the bytes were read from, so atomic renames and in-place edits
// both invalidate it. ConfigManager mutations reset it explicitly as well.
type observedConfigHash struct {
	info    os.FileInfo
	size    int64
	modTime time.Time
	sha256  string
}

func (c *observedConfigHash) matches(info os.FileInfo) bool {
	return c.info != nil && info != nil && os.SameFile(c.info, info) &&
		c.size == info.Size() && c.modTime.Equal(info.ModTime())
}

// invalidateObservedConfig must be called with m.mu held after any code path
// that may have rewritten the managed config.
func (m *ConfigManager) invalidateObservedConfig() {
	m.observedHash = observedConfigHash{}
}

// markMutated invalidates every cache derived from the managed config or the
// HAProxy process after apply/rollback. Must be called with m.mu held.
func (m *ConfigManager) markMutated() {
	m.invalidateObservedConfig()
	m.mutations.Add(1)
}

func (m *ConfigManager) ObservedConfigState() ObservedConfigState {
	if m == nil {
		return ObservedConfigState{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var observed ObservedConfigState
	if marker, err := m.actualRevision(); err == nil {
		if revision, parseErr := strconv.ParseInt(strings.TrimSpace(marker), 10, 64); parseErr == nil && revision > 0 {
			observed.ActualRevision = &revision
		}
	}
	observed.SHA256 = m.observedConfigSHA256()
	return observed
}

func (m *ConfigManager) observedConfigSHA256() string {
	if info, err := os.Stat(m.ManagedConfig); err != nil {
		m.invalidateObservedConfig()
		return ""
	} else if m.observedHash.matches(info) {
		return m.observedHash.sha256
	}
	file, err := os.Open(m.ManagedConfig)
	if err != nil {
		m.invalidateObservedConfig()
		return ""
	}
	defer file.Close()
	// Stat the descriptor the bytes are read from, not the path, so a rename
	// racing this read cannot pair new metadata with old contents.
	info, err := file.Stat()
	if err != nil {
		m.invalidateObservedConfig()
		return ""
	}
	hasher := sha256.New()
	written, err := io.Copy(hasher, file)
	if err != nil || written == 0 {
		m.invalidateObservedConfig()
		return ""
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	m.observedHash = observedConfigHash{info: info, size: info.Size(), modTime: info.ModTime(), sha256: sum}
	return sum
}
