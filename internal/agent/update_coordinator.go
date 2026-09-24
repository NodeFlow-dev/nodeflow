package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type UpdateProcessor interface {
	Process(context.Context, UpdateManifest) (UpdateVerification, error)
	Snapshot() *UpdateVerification
}

type UpdateCoordinatorConfig struct {
	Verifier      *UpdateVerifier
	Client        *http.Client
	PanelURL      string
	Token         string
	StagingDir    string
	PendingFile   string
	StateFile     string
	ResultFile    string
	HelperService string
	Runner        Runner
}

type UpdateCoordinator struct {
	config UpdateCoordinatorConfig
	mu     sync.Mutex
	last   *UpdateVerification
}

func NewUpdateCoordinator(config UpdateCoordinatorConfig) (*UpdateCoordinator, error) {
	if config.Verifier == nil {
		return nil, errors.New("update verifier is required")
	}
	coordinator := &UpdateCoordinator{config: config}
	if config.Verifier.Mode() == UpdateModeOff {
		coordinator.last = config.Verifier.Snapshot()
		return coordinator, nil
	}
	if config.Client == nil || config.PanelURL == "" || config.Token == "" || config.StagingDir == "" {
		return nil, errors.New("update download configuration is incomplete")
	}
	panelURL, err := url.Parse(config.PanelURL)
	if err != nil || panelURL.Host == "" || panelURL.Scheme != "https" {
		return nil, errors.New("Agent updates require an https Panel URL")
	}
	if config.Verifier.Mode() == UpdateModeApply {
		if config.PendingFile == "" || config.StateFile == "" || config.ResultFile == "" || config.HelperService == "" || config.Runner == nil {
			return nil, errors.New("update activation configuration is incomplete")
		}
	}
	if result, err := LoadUpdateResult(config.ResultFile); err == nil {
		coordinator.last = verificationFromResult(result)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load update result: %w", err)
	}
	if coordinator.last == nil {
		if state, err := LoadInstalledUpdateState(config.StateFile); err == nil && state.Sequence > 0 {
			coordinator.last = &UpdateVerification{Status: UpdateVerificationInstalled, Version: state.Version, Sequence: state.Sequence, SHA256: state.SHA256}
		} else if err != nil {
			return nil, fmt.Errorf("load installed update state: %w", err)
		}
	}
	return coordinator, nil
}

func (c *UpdateCoordinator) Process(ctx context.Context, manifest UpdateManifest) (UpdateVerification, error) {
	if c == nil || c.config.Verifier == nil {
		return rejectedUpdate(manifest, "coordinator_not_configured", nil)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.config.Verifier.Mode() == UpdateModeOff {
		return c.config.Verifier.Process(ctx, manifest)
	}
	if c.last != nil && c.last.Sequence == manifest.Sequence {
		switch c.last.Status {
		case UpdateVerificationActivating, UpdateVerificationInstalled, UpdateVerificationRolledBack, UpdateVerificationRejected:
			return *c.last, nil
		case UpdateVerificationVerified:
			if c.config.Verifier.Mode() == UpdateModeVerifyOnly {
				return *c.last, nil
			}
		}
	}
	if result, err := LoadUpdateResult(c.config.ResultFile); err == nil && result.Sequence == manifest.Sequence {
		verification := verificationFromResult(result)
		c.last = verification
		return *verification, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return c.reject(manifest, "result_state_invalid", err)
	}
	if state, err := LoadInstalledUpdateState(c.config.StateFile); err == nil && state.Sequence >= manifest.Sequence {
		if state.Sequence == manifest.Sequence && state.Version == manifest.Version && strings.EqualFold(state.SHA256, manifest.SHA256) {
			verification := UpdateVerification{Status: UpdateVerificationInstalled, Version: state.Version, Sequence: state.Sequence, SHA256: state.SHA256}
			c.last = &verification
			return verification, nil
		}
		return c.reject(manifest, "sequence_replay", nil)
	} else if err != nil {
		return c.reject(manifest, "installed_state_invalid", err)
	}

	c.setLast(UpdateVerification{Status: UpdateVerificationDownloading, Version: manifest.Version, Sequence: manifest.Sequence, SHA256: manifest.SHA256, Size: manifest.Size})
	if err := c.download(ctx, manifest); err != nil {
		return c.reject(manifest, "artifact_download_failed", err)
	}
	verification, err := c.config.Verifier.Verify(ctx, manifest)
	if err != nil {
		c.last = &verification
		return verification, err
	}
	if c.config.Verifier.Mode() == UpdateModeVerifyOnly {
		c.last = &verification
		return verification, nil
	}
	if err = RemoveUpdateFile(c.config.ResultFile); err != nil {
		return c.reject(manifest, "result_state_write_failed", err)
	}
	if err = WritePendingUpdate(c.config.PendingFile, PendingUpdate{Manifest: manifest, RequestedAt: time.Now().UTC()}); err != nil {
		return c.reject(manifest, "pending_state_write_failed", err)
	}
	activating := UpdateVerification{Status: UpdateVerificationActivating, Version: manifest.Version, Sequence: manifest.Sequence, SHA256: manifest.SHA256, Size: manifest.Size}
	c.last = &activating
	if _, err = c.config.Runner.Run(ctx, "systemctl", "start", "--no-block", c.config.HelperService); err != nil {
		return c.reject(manifest, "updater_start_failed", err)
	}
	return activating, nil
}

func (c *UpdateCoordinator) Snapshot() *UpdateVerification {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.config.ResultFile != "" {
		if result, err := LoadUpdateResult(c.config.ResultFile); err == nil {
			c.last = verificationFromResult(result)
		}
	}
	if c.last == nil {
		return nil
	}
	copy := *c.last
	return &copy
}

func (c *UpdateCoordinator) setLast(verification UpdateVerification) {
	copy := verification
	c.last = &copy
}

func (c *UpdateCoordinator) reject(manifest UpdateManifest, code string, cause error) (UpdateVerification, error) {
	verification, err := rejectedUpdate(manifest, code, cause)
	c.last = &verification
	return verification, err
}

func (c *UpdateCoordinator) download(ctx context.Context, manifest UpdateManifest) error {
	if err := validateUpdateArtifactPath(manifest.ArtifactPath); err != nil || strings.Contains(manifest.ArtifactPath, string(os.PathSeparator)) {
		return errors.New("remote artifact path must be one safe filename")
	}
	if existing, _, err := openUpdateArtifact(c.config.StagingDir, manifest.ArtifactPath); err == nil {
		existing.Close()
		validationErr := ValidatePreparedUpdate(ctx, c.config.StagingDir, manifest)
		if validationErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A same-name regular file may be left behind by disk corruption or an
		// interrupted older attempt. Remove it through the no-symlink state-file
		// helper and download the immutable signed artifact again. Unsafe file
		// types still fail closed in openUpdateArtifact above.
		if err := RemoveUpdateFile(filepath.Join(c.config.StagingDir, manifest.ArtifactPath)); err != nil {
			return fmt.Errorf("remove invalid staged artifact: %w", err)
		}
	} else if !errors.Is(err, unix.ENOENT) && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	base, err := url.Parse(c.config.PanelURL)
	if err != nil {
		return err
	}
	endpoint := base.JoinPath("agent", "v1", "updates", strconv.FormatUint(manifest.Sequence, 10), "artifact")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.config.Token)
	request.Header.Set("Accept", "application/octet-stream")
	response, err := c.config.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Panel returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != manifest.Size {
		return errors.New("artifact Content-Length mismatch")
	}
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return errors.New("encoded update artifacts are not accepted")
	}
	directoryFD, err := openDirectoryNoSymlink(c.config.StagingDir)
	if err != nil {
		return err
	}
	defer unix.Close(directoryFD)
	temporary := fmt.Sprintf(".download-%d-%d", manifest.Sequence, time.Now().UnixNano())
	fd, err := unix.Openat(directoryFD, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
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
	written, err := io.Copy(file, io.LimitReader(response.Body, manifest.Size+1))
	if err != nil {
		return err
	}
	if written != manifest.Size {
		return errors.New("downloaded artifact size mismatch")
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Chmod(0o500); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = unix.Renameat2(directoryFD, temporary, directoryFD, manifest.ArtifactPath, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	keep = false
	return unix.Fsync(directoryFD)
}

func verificationFromResult(result UpdateResult) *UpdateVerification {
	return &UpdateVerification{Status: result.Status, Version: result.Version, Sequence: result.Sequence, SHA256: result.SHA256, Code: result.Code}
}
