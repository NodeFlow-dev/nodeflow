package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type UpdateActivationConfig struct {
	StagingDir      string
	PendingFile     string
	StateFile       string
	ResultFile      string
	ActivationFile  string
	PublicKeyBase64 string
	OS              string
	Arch            string
	TargetBinary    string
	AgentService    string
	Runner          Runner
	HealthCheck     func(context.Context, string) error
	Now             func() time.Time
}

type UpdateActivator struct{ Config UpdateActivationConfig }

const maxRetainedUpdateBackups = 3

func (a UpdateActivator) Activate(ctx context.Context) (UpdateResult, error) {
	config := a.Config
	if config.Runner == nil || config.HealthCheck == nil || config.StagingDir == "" || config.PendingFile == "" ||
		config.StateFile == "" || config.ResultFile == "" || config.ActivationFile == "" || config.TargetBinary == "" || config.AgentService == "" {
		return UpdateResult{}, errors.New("update activator configuration is incomplete")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if journal, journalErr := LoadUpdateActivationJournal(config.ActivationFile); journalErr == nil {
		return a.recoverInterrupted(ctx, config, journal)
	} else if !errors.Is(journalErr, os.ErrNotExist) {
		return UpdateResult{}, fmt.Errorf("load update activation journal: %w", journalErr)
	}
	pending, err := LoadPendingUpdate(config.PendingFile)
	if err != nil {
		return UpdateResult{}, err
	}
	previousState, err := LoadInstalledUpdateState(config.StateFile)
	if err != nil {
		return a.reject(config, pending.Manifest, "installed_state_invalid", err)
	}
	if pending.Manifest.Sequence == previousState.Sequence && pending.Manifest.Version == previousState.Version && strings.EqualFold(pending.Manifest.SHA256, previousState.SHA256) {
		result := UpdateResult{
			Status: UpdateVerificationInstalled, Version: previousState.Version,
			Sequence: previousState.Sequence, SHA256: previousState.SHA256,
			FinishedAt: config.Now().UTC(),
		}
		if err = WriteUpdateResult(config.ResultFile, result); err != nil {
			return result, err
		}
		if err = RemoveUpdateFile(config.PendingFile); err != nil {
			return result, err
		}
		cleanupActivatedUpdate(config, pending.Manifest)
		return result, nil
	}
	if pending.Manifest.Sequence <= previousState.Sequence {
		return a.reject(config, pending.Manifest, "sequence_replay", nil)
	}
	verifier, err := NewUpdateVerifier(UpdateVerifierConfig{
		Mode: UpdateModeVerifyOnly, StagingDir: config.StagingDir,
		PublicKeyBase64: config.PublicKeyBase64, CurrentSequence: previousState.Sequence,
		OS: config.OS, Arch: config.Arch,
	})
	if err != nil {
		return a.reject(config, pending.Manifest, "updater_verifier_invalid", err)
	}

	backupDirectory := filepath.Join(filepath.Dir(config.StateFile), "backups")
	if err = ensurePrivateDirectory(backupDirectory); err != nil {
		return a.reject(config, pending.Manifest, "backup_directory_failed", err)
	}
	backupBinary := filepath.Join(backupDirectory, fmt.Sprintf("node-agent-before-%020d-%d", pending.Manifest.Sequence, config.Now().UnixNano()))
	if err = copyRegularFileAtomic(config.TargetBinary, backupBinary, 0o500); err != nil {
		return a.reject(config, pending.Manifest, "backup_failed", err)
	}

	if _, err = config.Runner.Run(ctx, "systemctl", "stop", config.AgentService); err != nil {
		return a.reject(config, pending.Manifest, "agent_stop_failed", err)
	}
	// The unprivileged-to-filesystem Agent is now stopped. Reverify the signed
	// manifest and artifact here, inside the independently sandboxed updater, so
	// a compromised Agent cannot swap pending.json/artifact between verification
	// and activation.
	if _, err = verifier.Verify(ctx, pending.Manifest); err != nil {
		if _, restartErr := config.Runner.Run(ctx, "systemctl", "start", config.AgentService); restartErr != nil {
			return UpdateResult{}, fmt.Errorf("restart Agent after rejected update: %w", restartErr)
		}
		return a.reject(config, pending.Manifest, "updater_verification_failed", err)
	}
	journal := UpdateActivationJournal{
		Manifest: pending.Manifest, BackupBinary: backupBinary,
		PreviousState: previousState, StartedAt: config.Now().UTC(),
	}
	if err = WriteUpdateActivationJournal(config.ActivationFile, journal); err != nil {
		_, _ = config.Runner.Run(ctx, "systemctl", "start", config.AgentService)
		return a.reject(config, pending.Manifest, "activation_journal_failed", err)
	}
	candidate := filepath.Join(config.StagingDir, pending.Manifest.ArtifactPath)
	activationErr := copyRegularFileAtomic(candidate, config.TargetBinary, 0o755)
	if activationErr == nil {
		_, activationErr = config.Runner.Run(ctx, "systemctl", "start", config.AgentService)
	}
	if activationErr == nil {
		activationErr = config.HealthCheck(ctx, pending.Manifest.Version)
	}
	if activationErr == nil {
		activationErr = WriteInstalledUpdateState(config.StateFile, InstalledUpdateState{
			Version: pending.Manifest.Version, Sequence: pending.Manifest.Sequence,
			SHA256: pending.Manifest.SHA256, InstalledAt: config.Now().UTC(),
		})
	}
	if activationErr != nil {
		return a.rollback(ctx, config, pending.Manifest, previousState, backupBinary, activationErr)
	}

	result := UpdateResult{
		Status: UpdateVerificationInstalled, Version: pending.Manifest.Version,
		Sequence: pending.Manifest.Sequence, SHA256: pending.Manifest.SHA256, FinishedAt: config.Now().UTC(),
	}
	if err = WriteUpdateResult(config.ResultFile, result); err != nil {
		return result, err
	}
	// Removing the trusted journal is the commit marker. If power is lost after
	// this point but before pending cleanup, boot recovery sees the installed
	// sequence and only rejects the stale pending request.
	if err = RemoveUpdateFile(config.ActivationFile); err != nil {
		return result, err
	}
	if err = RemoveUpdateFile(config.PendingFile); err != nil {
		return result, err
	}
	cleanupActivatedUpdate(config, pending.Manifest)
	return result, nil
}

func (a UpdateActivator) reject(config UpdateActivationConfig, manifest UpdateManifest, code string, cause error) (UpdateResult, error) {
	result := UpdateResult{
		Status: UpdateVerificationRejected, Version: manifest.Version, Sequence: manifest.Sequence,
		SHA256: manifest.SHA256, Code: code, FinishedAt: config.Now().UTC(),
	}
	if err := WriteUpdateResult(config.ResultFile, result); err != nil {
		return result, err
	}
	if err := RemoveUpdateFile(config.PendingFile); err != nil {
		return result, err
	}
	return result, cause
}

func (a UpdateActivator) rollback(ctx context.Context, config UpdateActivationConfig, manifest UpdateManifest, previousState InstalledUpdateState, backupBinary string, activationCause error) (UpdateResult, error) {
	_, _ = config.Runner.Run(ctx, "systemctl", "stop", config.AgentService)
	if err := copyRegularFileAtomic(backupBinary, config.TargetBinary, 0o755); err != nil {
		return UpdateResult{}, fmt.Errorf("restore previous Agent binary: %w", err)
	}
	if previousState.Sequence > 0 {
		if err := WriteInstalledUpdateState(config.StateFile, previousState); err != nil {
			return UpdateResult{}, fmt.Errorf("restore previous update state: %w", err)
		}
	} else if err := RemoveUpdateFile(config.StateFile); err != nil {
		return UpdateResult{}, fmt.Errorf("clear failed update state: %w", err)
	}
	if _, err := config.Runner.Run(ctx, "systemctl", "start", config.AgentService); err != nil {
		return UpdateResult{}, fmt.Errorf("restart previous Agent: %w", err)
	}
	if err := config.HealthCheck(ctx, previousState.Version); err != nil {
		return UpdateResult{}, fmt.Errorf("previous Agent failed health check: %w", err)
	}
	result := UpdateResult{
		Status: UpdateVerificationRolledBack, Version: manifest.Version, Sequence: manifest.Sequence,
		SHA256: manifest.SHA256, Code: "activation_failed", FinishedAt: config.Now().UTC(),
	}
	if err := WriteUpdateResult(config.ResultFile, result); err != nil {
		return result, err
	}
	if err := RemoveUpdateFile(config.PendingFile); err != nil {
		return result, err
	}
	if err := RemoveUpdateFile(config.ActivationFile); err != nil {
		return result, err
	}
	cleanupActivatedUpdate(config, manifest)
	_ = activationCause
	return result, nil
}

func (a UpdateActivator) recoverInterrupted(ctx context.Context, config UpdateActivationConfig, journal UpdateActivationJournal) (UpdateResult, error) {
	_, _ = config.Runner.Run(ctx, "systemctl", "stop", config.AgentService)
	if err := copyRegularFileAtomic(journal.BackupBinary, config.TargetBinary, 0o755); err != nil {
		return UpdateResult{}, fmt.Errorf("recover interrupted Agent update: %w", err)
	}
	if journal.PreviousState.Sequence > 0 {
		if err := WriteInstalledUpdateState(config.StateFile, journal.PreviousState); err != nil {
			return UpdateResult{}, fmt.Errorf("recover previous update state: %w", err)
		}
	} else if err := RemoveUpdateFile(config.StateFile); err != nil {
		return UpdateResult{}, fmt.Errorf("clear interrupted update state: %w", err)
	}
	if _, err := config.Runner.Run(ctx, "systemctl", "start", config.AgentService); err != nil {
		return UpdateResult{}, fmt.Errorf("restart Agent after interrupted update: %w", err)
	}
	if err := config.HealthCheck(ctx, journal.PreviousState.Version); err != nil {
		return UpdateResult{}, fmt.Errorf("recovered Agent failed health check: %w", err)
	}
	result := UpdateResult{
		Status: UpdateVerificationRolledBack, Version: journal.Manifest.Version,
		Sequence: journal.Manifest.Sequence, SHA256: journal.Manifest.SHA256,
		Code: "interrupted_activation", FinishedAt: config.Now().UTC(),
	}
	if err := WriteUpdateResult(config.ResultFile, result); err != nil {
		return result, err
	}
	if err := RemoveUpdateFile(config.PendingFile); err != nil {
		return result, err
	}
	if err := RemoveUpdateFile(config.ActivationFile); err != nil {
		return result, err
	}
	cleanupActivatedUpdate(config, journal.Manifest)
	return result, nil
}

func cleanupActivatedUpdate(config UpdateActivationConfig, manifest UpdateManifest) {
	// Cleanup is deliberately best-effort: a healthy activated or restored Agent
	// must not be reported as failed merely because retention cleanup hit a disk
	// error. The next successful activation retries bounded backup retention.
	_ = RemoveUpdateFile(filepath.Join(config.StagingDir, manifest.ArtifactPath))
	_ = pruneUpdateBackups(filepath.Join(filepath.Dir(config.StateFile), "backups"), maxRetainedUpdateBackups)
}

func pruneUpdateBackups(directory string, limit int) error {
	if limit < 1 {
		limit = 1
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	backups := make([]string, 0, len(entries))
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() || strings.HasPrefix(entry.Name(), ".") || !strings.HasPrefix(entry.Name(), "node-agent-before-") {
			continue
		}
		backups = append(backups, entry.Name())
	}
	sort.Strings(backups)
	for _, name := range backups[:max(0, len(backups)-limit)] {
		if err := RemoveUpdateFile(filepath.Join(directory, name)); err != nil {
			return err
		}
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("private directory is not a real directory")
	}
	return os.Chmod(path, 0o700)
}

func copyRegularFileAtomic(sourcePath, destinationPath string, mode os.FileMode) error {
	source, err := os.OpenFile(sourcePath, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() {
		if err != nil {
			return err
		}
		return errors.New("source is not a regular file")
	}
	directory := filepath.Dir(destinationPath)
	base := filepath.Base(destinationPath)
	directoryFD, err := openDirectoryNoSymlink(directory)
	if err != nil {
		return err
	}
	defer unix.Close(directoryFD)
	temporary := fmt.Sprintf(".%s.update-%d", base, time.Now().UnixNano())
	fd, err := unix.Openat(directoryFD, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	destination := os.NewFile(uintptr(fd), temporary)
	keep := true
	defer func() {
		_ = destination.Close()
		if keep {
			_ = unix.Unlinkat(directoryFD, temporary, 0)
		}
	}()
	if _, err = io.Copy(destination, source); err != nil {
		return err
	}
	if err = destination.Sync(); err != nil {
		return err
	}
	if err = destination.Chmod(mode); err != nil {
		return err
	}
	if err = destination.Close(); err != nil {
		return err
	}
	if err = unix.Renameat(directoryFD, temporary, directoryFD, base); err != nil {
		return err
	}
	keep = false
	return unix.Fsync(directoryFD)
}
