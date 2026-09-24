package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ConfigManager struct {
	Runner        Runner
	ManagedConfig string
	HAProxyBinary string
	ServiceName   string
	// PrepareConfig, when set, runs under the manager lock before every
	// "haproxy -c" of a candidate (validation, apply, rollback). It creates
	// files the config references, such as the pp-trusted ACL files.
	PrepareConfig func(ctx context.Context, config []byte) error
	mu            sync.Mutex
	observedHash  observedConfigHash
	// mutations counts apply/rollback attempts. Each one may reload or start
	// HAProxy, so cached service state keyed on an older value is stale.
	mutations atomic.Uint64
}

// MutationEpoch returns a counter that changes whenever apply or rollback may
// have changed the HAProxy config or process state.
func (m *ConfigManager) MutationEpoch() uint64 {
	if m == nil {
		return 0
	}
	return m.mutations.Load()
}

// RunSerialized executes a HAProxy runtime operation under the same lock used
// by config validation, apply and rollback. This prevents runtime mutations
// from racing a reload and being applied to the wrong HAProxy generation.
func (m *ConfigManager) RunSerialized(operation func() error) error {
	if m == nil {
		return fmt.Errorf("config manager is not configured")
	}
	if operation == nil {
		return fmt.Errorf("serialized operation is not configured")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return operation()
}

type ApplyResult struct {
	Revision   string
	Idempotent bool
}

const maxKnownGoodBackups = 10

type ApplyError struct {
	Code              string
	RollbackAttempted bool
	RollbackSucceeded *bool
	cause             error
}

func (e *ApplyError) Error() string { return e.Code }
func (e *ApplyError) Unwrap() error { return e.cause }

func applyError(code string, cause error) *ApplyError {
	return &ApplyError{Code: code, cause: cause}
}

func rollbackApplyError(code string, succeeded bool, cause error) *ApplyError {
	return &ApplyError{Code: code, RollbackAttempted: true, RollbackSucceeded: &succeeded, cause: cause}
}

func (m *ConfigManager) Validate(ctx context.Context, config []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.validate(ctx, config)
}

func (m *ConfigManager) validate(ctx context.Context, config []byte) error {
	if len(config) == 0 || len(config) > MaxManagedConfigBytes {
		return fmt.Errorf("invalid config size")
	}
	if m.PrepareConfig != nil {
		if err := m.PrepareConfig(ctx, config); err != nil {
			return fmt.Errorf("prepare config files: %w", err)
		}
	}
	dir := filepath.Dir(m.ManagedConfig)
	tmp, err := os.CreateTemp(dir, ".nodeflow-validate-*")
	if err != nil {
		return fmt.Errorf("create validation file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(config); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	output, err := m.Runner.Run(ctx, m.HAProxyBinary, "-c", "-f", name)
	if err != nil {
		return fmt.Errorf("haproxy validation failed: %s", output)
	}
	return nil
}

func (m *ConfigManager) Apply(ctx context.Context, config []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.markMutated()
	result, backup, err := m.applyRevision(ctx, config, "")
	_ = result
	return backup, err
}

func (m *ConfigManager) ApplyRevision(ctx context.Context, config []byte, revision string) (ApplyResult, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.markMutated()
	return m.applyRevision(ctx, config, revision)
}

func (m *ConfigManager) applyRevision(ctx context.Context, config []byte, revision string) (ApplyResult, string, error) {
	if len(config) == 0 || len(config) > MaxManagedConfigBytes {
		return ApplyResult{}, "", applyError("invalid_config_size", fmt.Errorf("invalid config size"))
	}
	revision = normalizedRevision(config, revision)
	if err := validateRevision(revision); err != nil {
		return ApplyResult{}, "", applyError("invalid_revision", err)
	}
	previousRevision, revisionErr := os.ReadFile(m.revisionPath())
	hadRevision := revisionErr == nil
	if revisionErr != nil && !os.IsNotExist(revisionErr) {
		return ApplyResult{}, "", applyError("state_read_failed", revisionErr)
	}
	currentConfig, configErr := os.ReadFile(m.ManagedConfig)
	hadConfig := configErr == nil
	if configErr != nil && !os.IsNotExist(configErr) {
		return ApplyResult{}, "", applyError("state_read_failed", configErr)
	}
	actual := strings.TrimSpace(string(previousRevision))
	if actual == revision && hadConfig && configSHA256Matches(currentConfig, config) {
		return ApplyResult{Revision: revision, Idempotent: true}, "", nil
	}
	if err := m.validate(ctx, config); err != nil {
		return ApplyResult{}, "", applyError("validation_failed", err)
	}
	dir := filepath.Dir(m.ManagedConfig)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ApplyResult{}, "", applyError("prepare_failed", err)
	}
	backup := ""
	if hadConfig {
		backup = fmt.Sprintf("%s.bak.%s", m.ManagedConfig, time.Now().UTC().Format("20060102T150405.000000000Z"))
		if err := atomicWrite(backup, currentConfig, 0640); err != nil {
			return ApplyResult{}, "", applyError("backup_failed", err)
		}
		if hadRevision {
			if err := atomicWrite(backup+".revision", previousRevision, 0640); err != nil {
				return ApplyResult{}, backup, applyError("backup_failed", err)
			}
		}
	}
	if err := atomicWrite(m.ManagedConfig, config, 0640); err != nil {
		return ApplyResult{}, backup, applyError("write_failed", err)
	}
	// reload-or-restart preserves a seamless reload for an active HAProxy and
	// starts it when this is the first managed configuration on a fresh node.
	if output, err := m.Runner.Run(ctx, "systemctl", "reload-or-restart", m.ServiceName); err != nil {
		rolledBack := m.restoreManagedConfig(backup, hadConfig)
		if rolledBack {
			_, rollbackReloadErr := m.Runner.Run(ctx, "systemctl", "reload", m.ServiceName)
			rolledBack = rollbackReloadErr == nil
		}
		if rolledBack {
			rolledBack = m.restoreRevision(previousRevision, hadRevision)
		}
		return ApplyResult{}, backup, rollbackApplyError("reload_failed", rolledBack, fmt.Errorf("reload failed: %s", output))
	}
	// The marker is a commit record for the running HAProxy state. Publishing it
	// before a successful reload would make a crashed/failed apply look complete.
	if err := atomicWrite(m.revisionPath(), []byte(revision+"\n"), 0640); err != nil {
		rolledBack := m.restoreManagedConfig(backup, hadConfig)
		if rolledBack {
			_, rollbackReloadErr := m.Runner.Run(ctx, "systemctl", "reload", m.ServiceName)
			rolledBack = rollbackReloadErr == nil
		}
		if rolledBack {
			rolledBack = m.restoreRevision(previousRevision, hadRevision)
		} else {
			// The new reload already succeeded. If rollback cannot be confirmed,
			// best-effort realign the files and marker with that active revision.
			_ = atomicWrite(m.ManagedConfig, config, 0640)
			_ = atomicWrite(m.revisionPath(), []byte(revision+"\n"), 0640)
		}
		return ApplyResult{}, backup, rollbackApplyError("state_write_failed", rolledBack, err)
	}
	// Retention runs only after the new running state and its marker are committed.
	// A cleanup failure must not turn a successfully activated revision into a
	// reported apply failure; the next successful apply retries the cleanup.
	_ = m.pruneBackups(maxKnownGoodBackups)
	return ApplyResult{Revision: revision}, backup, nil
}

func (m *ConfigManager) pruneBackups(limit int) error {
	if limit < 1 {
		limit = 1
	}
	backups, err := filepath.Glob(m.ManagedConfig + ".bak.*")
	if err != nil {
		return err
	}
	filtered := backups[:0]
	for _, path := range backups {
		if !strings.HasSuffix(path, ".revision") {
			filtered = append(filtered, path)
		}
	}
	sort.Strings(filtered)
	for _, backup := range filtered[:max(0, len(filtered)-limit)] {
		if err := os.Remove(backup + ".revision"); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (m *ConfigManager) restoreManagedConfig(backup string, hadConfig bool) bool {
	if hadConfig {
		data, err := os.ReadFile(backup)
		if err != nil {
			return false
		}
		return atomicWrite(m.ManagedConfig, data, 0640) == nil
	}
	err := os.Remove(m.ManagedConfig)
	return err == nil || os.IsNotExist(err)
}

func (m *ConfigManager) restoreRevision(previousRevision []byte, hadRevision bool) bool {
	if hadRevision {
		current, err := os.ReadFile(m.revisionPath())
		if err == nil && string(current) == string(previousRevision) {
			return true
		}
		return atomicWrite(m.revisionPath(), previousRevision, 0640) == nil
	}
	err := os.Remove(m.revisionPath())
	return err == nil || os.IsNotExist(err)
}

func (m *ConfigManager) ActualRevision() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.actualRevision()
}

func (m *ConfigManager) actualRevision() (string, error) {
	data, err := os.ReadFile(m.revisionPath())
	if os.IsNotExist(err) {
		return "", nil
	}
	return strings.TrimSpace(string(data)), err
}

func (m *ConfigManager) Rollback(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.markMutated()
	backups, err := filepath.Glob(m.ManagedConfig + ".bak.*")
	if err != nil {
		return "", err
	}
	filtered := backups[:0]
	for _, path := range backups {
		if !strings.HasSuffix(path, ".revision") {
			filtered = append(filtered, path)
		}
	}
	sort.Strings(filtered)
	if len(filtered) == 0 {
		return "", fmt.Errorf("no known-good revision available")
	}
	backup := filtered[len(filtered)-1]
	data, err := os.ReadFile(backup)
	if err != nil {
		return "", err
	}
	if err := m.validate(ctx, data); err != nil {
		return "", fmt.Errorf("known-good revision is no longer valid")
	}
	current, currentErr := os.ReadFile(m.ManagedConfig)
	hadCurrent := currentErr == nil
	if currentErr != nil && !os.IsNotExist(currentErr) {
		return "", currentErr
	}
	previousRevision, revisionErr := os.ReadFile(m.revisionPath())
	hadRevision := revisionErr == nil
	if revisionErr != nil && !os.IsNotExist(revisionErr) {
		return "", revisionErr
	}
	if err := atomicWrite(m.ManagedConfig, data, 0640); err != nil {
		return "", err
	}
	revisionData, _ := os.ReadFile(backup + ".revision")
	revision := strings.TrimSpace(string(revisionData))
	if revision == "" {
		revision = normalizedRevision(data, "")
	}
	if _, err := m.Runner.Run(ctx, "systemctl", "reload", m.ServiceName); err != nil {
		restored := restoreConfigBytes(m.ManagedConfig, current, hadCurrent)
		if restored {
			_, restoreReloadErr := m.Runner.Run(ctx, "systemctl", "reload", m.ServiceName)
			restored = restoreReloadErr == nil
		}
		if restored {
			restored = m.restoreRevision(previousRevision, hadRevision)
		}
		return "", fmt.Errorf("rollback reload failed; previous state restored=%t", restored)
	}
	if err := atomicWrite(m.revisionPath(), []byte(revision+"\n"), 0640); err != nil {
		restored := restoreConfigBytes(m.ManagedConfig, current, hadCurrent)
		if restored {
			_, restoreReloadErr := m.Runner.Run(ctx, "systemctl", "reload", m.ServiceName)
			restored = restoreReloadErr == nil
		}
		if restored {
			restored = m.restoreRevision(previousRevision, hadRevision)
		}
		if !restored {
			// The rollback reload succeeded, so preserve a recoverable on-disk
			// state matching that active revision when restoration is uncertain.
			_ = atomicWrite(m.ManagedConfig, data, 0640)
			_ = atomicWrite(m.revisionPath(), []byte(revision+"\n"), 0640)
		}
		return "", fmt.Errorf("rollback state write failed")
	}
	return revision, nil
}

func (m *ConfigManager) revisionPath() string { return m.ManagedConfig + ".revision" }
func normalizedRevision(config []byte, revision string) string {
	if strings.TrimSpace(revision) != "" {
		return strings.TrimSpace(revision)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(config))
}

func configSHA256Matches(actual, expected []byte) bool {
	return sha256.Sum256(actual) == sha256.Sum256(expected)
}

func restoreConfigBytes(path string, previous []byte, hadPrevious bool) bool {
	if hadPrevious {
		return atomicWrite(path, previous, 0640) == nil
	}
	err := os.Remove(path)
	return err == nil || os.IsNotExist(err)
}

func validateRevision(revision string) error {
	if revision == "" || len(revision) > 128 {
		return fmt.Errorf("invalid revision")
	}
	for _, r := range revision {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r)) {
			return fmt.Errorf("invalid revision")
		}
	}
	return nil
}
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".nodeflow-state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
