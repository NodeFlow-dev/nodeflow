package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const MaxUpdateArtifactSize int64 = 64 << 20

type UpdateMode string

const (
	UpdateModeOff        UpdateMode = "off"
	UpdateModeVerifyOnly UpdateMode = "verify-only"
	UpdateModeApply      UpdateMode = "apply"
)

type UpdateVerificationStatus string

const (
	UpdateVerificationOff         UpdateVerificationStatus = "off"
	UpdateVerificationVerified    UpdateVerificationStatus = "verified"
	UpdateVerificationDownloading UpdateVerificationStatus = "downloading"
	UpdateVerificationActivating  UpdateVerificationStatus = "activating"
	UpdateVerificationInstalled   UpdateVerificationStatus = "installed"
	UpdateVerificationRolledBack  UpdateVerificationStatus = "rolled_back"
	UpdateVerificationRejected    UpdateVerificationStatus = "rejected"
)

type UpdateManifest struct {
	Version      string `json:"version"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	Sequence     uint64 `json:"sequence"`
	Signature    string `json:"signature"`
	ArtifactPath string `json:"artifact_path"`
}

type UpdateVerification struct {
	Status   UpdateVerificationStatus `json:"status"`
	Version  string                   `json:"version,omitempty"`
	Sequence uint64                   `json:"sequence,omitempty"`
	SHA256   string                   `json:"sha256,omitempty"`
	Size     int64                    `json:"size,omitempty"`
	Code     string                   `json:"code,omitempty"`
}

type UpdateVerifierConfig struct {
	Mode            UpdateMode
	StagingDir      string
	PublicKeyBase64 string
	CurrentSequence uint64
	OS              string
	Arch            string
}

type UpdateVerifier struct {
	mode            UpdateMode
	stagingDir      string
	publicKey       ed25519.PublicKey
	currentSequence uint64
	os              string
	arch            string
	verifyMu        sync.Mutex
	stateMu         sync.Mutex
	last            *UpdateVerification
}

type UpdateVerificationError struct {
	Code  string
	cause error
}

func (e *UpdateVerificationError) Error() string { return e.Code }
func (e *UpdateVerificationError) Unwrap() error { return e.cause }

var (
	updateVersionPattern  = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)
	updatePlatformPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	updateCodePattern     = regexp.MustCompile(`^[a-z0-9_]{0,100}$`)
)

func NewUpdateVerifier(config UpdateVerifierConfig) (*UpdateVerifier, error) {
	if config.Mode != UpdateModeOff && config.Mode != UpdateModeVerifyOnly && config.Mode != UpdateModeApply {
		return nil, fmt.Errorf("invalid update mode")
	}
	verifier := &UpdateVerifier{
		mode:            config.Mode,
		currentSequence: config.CurrentSequence,
		os:              config.OS,
		arch:            config.Arch,
	}
	if verifier.os == "" {
		verifier.os = runtime.GOOS
	}
	if verifier.arch == "" {
		verifier.arch = runtime.GOARCH
	}
	if config.Mode == UpdateModeOff {
		verifier.last = &UpdateVerification{Status: UpdateVerificationOff}
		return verifier, nil
	}
	if !updatePlatformPattern.MatchString(verifier.os) || !updatePlatformPattern.MatchString(verifier.arch) {
		return nil, fmt.Errorf("invalid verifier platform")
	}
	if config.StagingDir == "" || !filepath.IsAbs(config.StagingDir) || filepath.Clean(config.StagingDir) != config.StagingDir || config.StagingDir == string(filepath.Separator) {
		return nil, fmt.Errorf("invalid update staging directory")
	}
	root, err := openDirectoryNoSymlink(config.StagingDir)
	if err != nil {
		return nil, fmt.Errorf("invalid update staging directory: %w", err)
	}
	_ = unix.Close(root)
	key, err := decodeCanonicalBase64(config.PublicKeyBase64)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid update public key")
	}
	verifier.stagingDir = config.StagingDir
	verifier.publicKey = append(ed25519.PublicKey(nil), key...)
	return verifier, nil
}

func (v *UpdateVerifier) Verify(ctx context.Context, manifest UpdateManifest) (UpdateVerification, error) {
	if v == nil {
		return rejectedUpdate(manifest, "verifier_not_configured", nil)
	}
	v.verifyMu.Lock()
	defer v.verifyMu.Unlock()
	result, err := v.verify(ctx, manifest)
	v.stateMu.Lock()
	copy := result
	v.last = &copy
	v.stateMu.Unlock()
	return result, err
}

func (v *UpdateVerifier) verify(ctx context.Context, manifest UpdateManifest) (UpdateVerification, error) {
	if v == nil {
		return rejectedUpdate(manifest, "verifier_not_configured", nil)
	}
	if v.mode == UpdateModeOff {
		return UpdateVerification{Status: UpdateVerificationOff}, nil
	}
	if (v.mode != UpdateModeVerifyOnly && v.mode != UpdateModeApply) || len(v.publicKey) != ed25519.PublicKeySize || v.stagingDir == "" {
		return rejectedUpdate(manifest, "verifier_not_configured", nil)
	}
	if err := ctx.Err(); err != nil {
		return rejectedUpdate(manifest, "verification_canceled", err)
	}
	canonical, err := canonicalUpdateManifest(manifest)
	if err != nil {
		return rejectedUpdate(manifest, "invalid_manifest", err)
	}
	if manifest.Size > MaxUpdateArtifactSize {
		return rejectedUpdate(manifest, "artifact_too_large", nil)
	}
	if manifest.OS != v.os || manifest.Arch != v.arch {
		return rejectedUpdate(manifest, "platform_mismatch", nil)
	}
	if manifest.Sequence <= v.currentSequence {
		return rejectedUpdate(manifest, "sequence_replay", nil)
	}
	signature, err := decodeCanonicalBase64(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(v.publicKey, canonical, signature) {
		return rejectedUpdate(manifest, "signature_invalid", err)
	}

	artifact, info, err := openUpdateArtifact(v.stagingDir, manifest.ArtifactPath)
	if err != nil {
		return rejectedUpdate(manifest, "artifact_path_rejected", err)
	}
	defer artifact.Close()
	if info.Size() != manifest.Size {
		return rejectedUpdate(manifest, "artifact_size_mismatch", nil)
	}
	digest, copied, err := hashUpdateArtifact(ctx, artifact)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return rejectedUpdate(manifest, "verification_canceled", err)
		}
		return rejectedUpdate(manifest, "artifact_read_failed", err)
	}
	if copied != manifest.Size {
		return rejectedUpdate(manifest, "artifact_size_mismatch", nil)
	}
	if !strings.EqualFold(hex.EncodeToString(digest), manifest.SHA256) {
		return rejectedUpdate(manifest, "artifact_hash_mismatch", nil)
	}
	return UpdateVerification{
		Status:   UpdateVerificationVerified,
		Version:  manifest.Version,
		Sequence: manifest.Sequence,
		SHA256:   manifest.SHA256,
		Size:     manifest.Size,
	}, nil
}

func (v *UpdateVerifier) Process(ctx context.Context, manifest UpdateManifest) (UpdateVerification, error) {
	return v.Verify(ctx, manifest)
}

func (v *UpdateVerifier) Snapshot() *UpdateVerification {
	if v == nil {
		return nil
	}
	v.stateMu.Lock()
	defer v.stateMu.Unlock()
	if v.last == nil {
		return nil
	}
	copy := *v.last
	return &copy
}

func (v *UpdateVerifier) Mode() UpdateMode {
	if v == nil {
		return UpdateModeOff
	}
	return v.mode
}

// CanonicalUpdateManifest returns the exact bytes covered by the Ed25519
// signature. Signature is deliberately excluded; every other manifest field,
// including the staging-relative artifact path, is authenticated.
func CanonicalUpdateManifest(manifest UpdateManifest) ([]byte, error) {
	return canonicalUpdateManifest(manifest)
}

func canonicalUpdateManifest(manifest UpdateManifest) ([]byte, error) {
	if !updateVersionPattern.MatchString(manifest.Version) {
		return nil, fmt.Errorf("invalid version")
	}
	if !updatePlatformPattern.MatchString(manifest.OS) || !updatePlatformPattern.MatchString(manifest.Arch) {
		return nil, fmt.Errorf("invalid platform")
	}
	if len(manifest.SHA256) != sha256.Size*2 {
		return nil, fmt.Errorf("invalid sha256")
	}
	digest, err := hex.DecodeString(manifest.SHA256)
	if err != nil || hex.EncodeToString(digest) != manifest.SHA256 {
		return nil, fmt.Errorf("invalid sha256")
	}
	if manifest.Size <= 0 {
		return nil, fmt.Errorf("invalid artifact size")
	}
	if manifest.Sequence == 0 {
		return nil, fmt.Errorf("invalid sequence")
	}
	if err := validateUpdateArtifactPath(manifest.ArtifactPath); err != nil {
		return nil, err
	}
	type signedManifest struct {
		Schema       string `json:"schema"`
		Version      string `json:"version"`
		OS           string `json:"os"`
		Arch         string `json:"arch"`
		SHA256       string `json:"sha256"`
		Size         int64  `json:"size"`
		Sequence     uint64 `json:"sequence"`
		ArtifactPath string `json:"artifact_path"`
	}
	return json.Marshal(signedManifest{
		Schema:       "nodeflow-agent-update/v1",
		Version:      manifest.Version,
		OS:           manifest.OS,
		Arch:         manifest.Arch,
		SHA256:       manifest.SHA256,
		Size:         manifest.Size,
		Sequence:     manifest.Sequence,
		ArtifactPath: manifest.ArtifactPath,
	})
}

func rejectedUpdate(manifest UpdateManifest, code string, cause error) (UpdateVerification, error) {
	return UpdateVerification{
		Status:   UpdateVerificationRejected,
		Version:  manifest.Version,
		Sequence: manifest.Sequence,
		SHA256:   manifest.SHA256,
		Size:     manifest.Size,
		Code:     code,
	}, &UpdateVerificationError{Code: code, cause: cause}
}

func decodeCanonicalBase64(value string) ([]byte, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return nil, fmt.Errorf("invalid base64")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, fmt.Errorf("invalid base64")
	}
	return decoded, nil
}

func validateUpdateArtifactPath(path string) error {
	if path == "" || len(path) > 512 || filepath.IsAbs(path) || filepath.Clean(path) != path || path == "." {
		return fmt.Errorf("invalid artifact path")
	}
	if strings.Contains(path, `\`) || strings.ContainsRune(path, 0) {
		return fmt.Errorf("invalid artifact path")
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." || len(part) > 255 {
			return fmt.Errorf("invalid artifact path")
		}
		for _, r := range part {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("invalid artifact path")
			}
		}
	}
	return nil
}

func openDirectoryNoSymlink(path string) (int, error) {
	if !filepath.IsAbs(path) {
		return -1, fmt.Errorf("directory must be absolute")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func openUpdateArtifact(stagingDir, relativePath string) (*os.File, os.FileInfo, error) {
	if err := validateUpdateArtifactPath(relativePath); err != nil {
		return nil, nil, err
	}
	fd, err := openDirectoryNoSymlink(stagingDir)
	if err != nil {
		return nil, nil, err
	}
	parts := strings.Split(relativePath, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, nil, openErr
		}
		fd = next
	}
	artifactFD, err := unix.Openat(fd, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	_ = unix.Close(fd)
	if err != nil {
		return nil, nil, err
	}
	artifact := os.NewFile(uintptr(artifactFD), relativePath)
	if artifact == nil {
		_ = unix.Close(artifactFD)
		return nil, nil, fmt.Errorf("open artifact")
	}
	info, err := artifact.Stat()
	if err != nil {
		artifact.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		artifact.Close()
		return nil, nil, fmt.Errorf("artifact is not a regular file")
	}
	if info.Size() < 0 || info.Size() > MaxUpdateArtifactSize {
		artifact.Close()
		return nil, nil, fmt.Errorf("artifact size is out of bounds")
	}
	return artifact, info, nil
}

func hashUpdateArtifact(ctx context.Context, artifact io.Reader) ([]byte, int64, error) {
	digest := sha256.New()
	written, err := copyUpdateArtifact(ctx, digest, artifact)
	if err != nil {
		return nil, written, err
	}
	return digest.Sum(nil), written, nil
}

func copyUpdateArtifact(ctx context.Context, destination hash.Hash, source io.Reader) (int64, error) {
	buffer := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
		if read == 0 {
			return total, io.ErrNoProgress
		}
	}
}
