package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const maxUpdateStateBytes = 64 << 10

type InstalledUpdateState struct {
	Version     string    `json:"version"`
	Sequence    uint64    `json:"sequence"`
	SHA256      string    `json:"sha256"`
	InstalledAt time.Time `json:"installed_at"`
}

type PendingUpdate struct {
	Manifest    UpdateManifest `json:"manifest"`
	RequestedAt time.Time      `json:"requested_at"`
}

type UpdateActivationJournal struct {
	Manifest      UpdateManifest       `json:"manifest"`
	BackupBinary  string               `json:"backup_binary"`
	PreviousState InstalledUpdateState `json:"previous_state"`
	StartedAt     time.Time            `json:"started_at"`
}

type UpdateResult struct {
	Status     UpdateVerificationStatus `json:"status"`
	Version    string                   `json:"version"`
	Sequence   uint64                   `json:"sequence"`
	SHA256     string                   `json:"sha256"`
	Code       string                   `json:"code,omitempty"`
	FinishedAt time.Time                `json:"finished_at"`
}

func LoadInstalledUpdateState(path string) (InstalledUpdateState, error) {
	var state InstalledUpdateState
	found, err := readUpdateJSON(path, &state)
	if err != nil || !found {
		return state, err
	}
	if state.Sequence == 0 || !updateVersionPattern.MatchString(state.Version) || !validUpdateSHA(state.SHA256) || state.InstalledAt.IsZero() {
		return InstalledUpdateState{}, errors.New("invalid installed update state")
	}
	return state, nil
}

func LoadPendingUpdate(path string) (PendingUpdate, error) {
	var pending PendingUpdate
	found, err := readUpdateJSON(path, &pending)
	if err != nil {
		return pending, err
	}
	if !found {
		return pending, os.ErrNotExist
	}
	if _, err := canonicalUpdateManifest(pending.Manifest); err != nil || pending.RequestedAt.IsZero() {
		return PendingUpdate{}, errors.New("invalid pending update")
	}
	return pending, nil
}

func LoadUpdateResult(path string) (UpdateResult, error) {
	var result UpdateResult
	found, err := readUpdateJSON(path, &result)
	if err != nil {
		return result, err
	}
	if !found {
		return result, os.ErrNotExist
	}
	validStatus := result.Status == UpdateVerificationInstalled || result.Status == UpdateVerificationRolledBack || result.Status == UpdateVerificationRejected
	if !validStatus || result.Sequence == 0 || !updateVersionPattern.MatchString(result.Version) || !validUpdateSHA(result.SHA256) || result.FinishedAt.IsZero() {
		return UpdateResult{}, errors.New("invalid update result")
	}
	return result, nil
}

func LoadUpdateActivationJournal(path string) (UpdateActivationJournal, error) {
	var journal UpdateActivationJournal
	found, err := readUpdateJSON(path, &journal)
	if err != nil {
		return journal, err
	}
	if !found {
		return journal, os.ErrNotExist
	}
	if err := validateUpdateActivationJournal(journal); err != nil {
		return UpdateActivationJournal{}, err
	}
	return journal, nil
}

func WritePendingUpdate(path string, pending PendingUpdate) error {
	if _, err := canonicalUpdateManifest(pending.Manifest); err != nil {
		return err
	}
	if pending.RequestedAt.IsZero() {
		return errors.New("pending update timestamp is required")
	}
	return writeUpdateJSON(path, pending, 0o600)
}

func WriteInstalledUpdateState(path string, state InstalledUpdateState) error {
	if state.Sequence == 0 || !updateVersionPattern.MatchString(state.Version) || !validUpdateSHA(state.SHA256) || state.InstalledAt.IsZero() {
		return errors.New("invalid installed update state")
	}
	return writeUpdateJSON(path, state, 0o600)
}

func WriteUpdateResult(path string, result UpdateResult) error {
	validStatus := result.Status == UpdateVerificationInstalled || result.Status == UpdateVerificationRolledBack || result.Status == UpdateVerificationRejected
	if !validStatus || result.Sequence == 0 || !updateVersionPattern.MatchString(result.Version) || !validUpdateSHA(result.SHA256) || result.FinishedAt.IsZero() {
		return errors.New("invalid update result")
	}
	if result.Code != "" && !updateCodePattern.MatchString(result.Code) {
		return errors.New("invalid update result code")
	}
	return writeUpdateJSON(path, result, 0o600)
}

func WriteUpdateActivationJournal(path string, journal UpdateActivationJournal) error {
	if err := validateUpdateActivationJournal(journal); err != nil {
		return err
	}
	return writeUpdateJSON(path, journal, 0o600)
}

func validateUpdateActivationJournal(journal UpdateActivationJournal) error {
	if _, err := canonicalUpdateManifest(journal.Manifest); err != nil || journal.StartedAt.IsZero() ||
		!filepath.IsAbs(journal.BackupBinary) || filepath.Clean(journal.BackupBinary) != journal.BackupBinary {
		return errors.New("invalid update activation journal")
	}
	if journal.PreviousState.Sequence > 0 {
		if journal.PreviousState.InstalledAt.IsZero() || !updateVersionPattern.MatchString(journal.PreviousState.Version) || !validUpdateSHA(journal.PreviousState.SHA256) {
			return errors.New("invalid previous update state")
		}
	} else if journal.PreviousState.Version != "" || journal.PreviousState.SHA256 != "" || !journal.PreviousState.InstalledAt.IsZero() {
		return errors.New("invalid empty previous update state")
	}
	return nil
}

func RemoveUpdateFile(path string) error {
	directory, base, err := splitUpdateStatePath(path)
	if err != nil {
		return err
	}
	directoryFD, err := openDirectoryNoSymlink(directory)
	if err != nil {
		return err
	}
	defer unix.Close(directoryFD)
	err = unix.Unlinkat(directoryFD, base, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err == nil {
		err = unix.Fsync(directoryFD)
	}
	return err
}

func ValidatePreparedUpdate(ctx context.Context, stagingDirectory string, manifest UpdateManifest) error {
	if _, err := canonicalUpdateManifest(manifest); err != nil {
		return err
	}
	artifact, info, err := openUpdateArtifact(stagingDirectory, manifest.ArtifactPath)
	if err != nil {
		return err
	}
	defer artifact.Close()
	if info.Size() != manifest.Size || manifest.Size > MaxUpdateArtifactSize {
		return errors.New("prepared artifact size mismatch")
	}
	digest, copied, err := hashUpdateArtifact(ctx, artifact)
	if err != nil {
		return err
	}
	if copied != manifest.Size || !strings.EqualFold(hex.EncodeToString(digest), manifest.SHA256) {
		return errors.New("prepared artifact hash mismatch")
	}
	return nil
}

func readUpdateJSON(path string, destination any) (bool, error) {
	directory, base, err := splitUpdateStatePath(path)
	if err != nil {
		return false, err
	}
	file, info, err := openUpdateArtifact(directory, base)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	if info.Size() < 2 || info.Size() > maxUpdateStateBytes {
		return false, errors.New("invalid update state size")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxUpdateStateBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return false, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return false, errors.New("update state must contain one JSON object")
	}
	return true, nil
}

func writeUpdateJSON(path string, value any, mode os.FileMode) error {
	directory, base, err := splitUpdateStatePath(path)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil || len(payload) > maxUpdateStateBytes-1 {
		return errors.New("update state is too large")
	}
	payload = append(payload, '\n')
	directoryFD, err := openDirectoryNoSymlink(directory)
	if err != nil {
		return err
	}
	defer unix.Close(directoryFD)
	random := make([]byte, 12)
	if _, err = rand.Read(random); err != nil {
		return err
	}
	temporary := "." + base + "." + hex.EncodeToString(random)
	fd, err := unix.Openat(directoryFD, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporary)
	keep := true
	defer func() {
		_ = file.Close()
		if keep {
			_ = unix.Unlinkat(directoryFD, temporary, 0)
		}
	}()
	if _, err = file.Write(payload); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = unix.Renameat(directoryFD, temporary, directoryFD, base); err != nil {
		return err
	}
	keep = false
	return unix.Fsync(directoryFD)
}

func splitUpdateStatePath(path string) (string, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return "", "", fmt.Errorf("invalid update state path")
	}
	directory, base := filepath.Dir(path), filepath.Base(path)
	if base == "." || base == string(filepath.Separator) || strings.ContainsAny(base, `/\\`) {
		return "", "", fmt.Errorf("invalid update state path")
	}
	return directory, base, nil
}

func validUpdateSHA(value string) bool {
	if len(value) != 64 {
		return false
	}
	digest, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(digest) == value
}
