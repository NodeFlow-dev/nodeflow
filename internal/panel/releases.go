package panel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"strings"

	"github.com/nodeflow/nodeflow/internal/agent"
	"github.com/nodeflow/nodeflow/internal/bootstrap"
	"golang.org/x/sys/unix"
)

type ReleaseService struct {
	directory   string
	directoryFD int
	privateKey  ed25519.PrivateKey
	publicKey   string
	store       Store
}

const MaxAgentReleaseArtifactBytes = agent.MaxUpdateArtifactSize

type ReleaseInputError struct{ Message string }

func (e *ReleaseInputError) Error() string { return e.Message }

type ReleaseTooLargeError struct{}

func (*ReleaseTooLargeError) Error() string { return "Agent release exceeds 64 MiB" }

var ErrReleaseArtifactIntegrity = errors.New("Agent release artifact failed integrity verification")
var ErrReleaseInUse = errors.New("Agent release is installed or assigned")

var releaseArtifactPattern = regexp.MustCompile(`^[0-9]{20}-[a-z0-9_-]{1,32}-[a-z0-9_-]{1,32}-[0-9a-f]{16}\.bin$`)

func NewReleaseService(directory, privateKeyFile string, store Store) (*ReleaseService, error) {
	keyPEM, err := os.ReadFile(privateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read update signing key: %w", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("update signing key file contains no PEM private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse update signing key: %w", err)
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("update signing key must be Ed25519")
	}
	directoryFD, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open Agent release directory: %w", err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	return &ReleaseService{
		directory:   directory,
		directoryFD: directoryFD,
		privateKey:  append(ed25519.PrivateKey(nil), privateKey...),
		publicKey:   base64.StdEncoding.EncodeToString(publicKey),
		store:       store,
	}, nil
}

func (s *ReleaseService) PublicKeyBase64() string {
	if s == nil {
		return ""
	}
	return s.publicKey
}

func (s *ReleaseService) Close() error {
	if s == nil || s.directoryFD < 0 {
		return nil
	}
	err := unix.Close(s.directoryFD)
	s.directoryFD = -1
	return err
}

func (s *ReleaseService) Create(ctx context.Context, version, operatingSystem, architecture string, artifact io.Reader) (AgentRelease, error) {
	if s == nil || s.store == nil {
		return AgentRelease{}, errors.New("Agent release service is unavailable")
	}
	version = strings.TrimSpace(version)
	operatingSystem = strings.TrimSpace(operatingSystem)
	architecture = strings.TrimSpace(architecture)
	if operatingSystem == "" {
		operatingSystem = runtime.GOOS
	}
	if architecture == "" {
		architecture = runtime.GOARCH
	}
	if !regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`).MatchString(version) ||
		!regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`).MatchString(operatingSystem) ||
		!regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`).MatchString(architecture) {
		return AgentRelease{}, &ReleaseInputError{Message: "invalid release version or platform"}
	}
	sequence, err := s.store.ReserveAgentReleaseSequence(ctx)
	if err != nil {
		return AgentRelease{}, err
	}
	temporaryName, err := randomReleaseName()
	if err != nil {
		return AgentRelease{}, err
	}
	fd, err := unix.Openat(s.directoryFD, temporaryName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return AgentRelease{}, fmt.Errorf("create release artifact: %w", err)
	}
	temporary := os.NewFile(uintptr(fd), temporaryName)
	keepTemporary := true
	defer func() {
		_ = temporary.Close()
		if keepTemporary {
			_ = unix.Unlinkat(s.directoryFD, temporaryName, 0)
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(&contextReader{ctx: ctx, reader: artifact}, agent.MaxUpdateArtifactSize+1))
	if err != nil {
		return AgentRelease{}, fmt.Errorf("store release artifact: %w", err)
	}
	if written < 1 {
		return AgentRelease{}, &ReleaseInputError{Message: "release artifact must not be empty"}
	}
	if written > agent.MaxUpdateArtifactSize {
		return AgentRelease{}, &ReleaseTooLargeError{}
	}
	if err = temporary.Sync(); err != nil {
		return AgentRelease{}, fmt.Errorf("sync release artifact: %w", err)
	}
	if err = temporary.Chmod(0o440); err != nil {
		return AgentRelease{}, fmt.Errorf("protect release artifact: %w", err)
	}
	if err = temporary.Close(); err != nil {
		return AgentRelease{}, fmt.Errorf("close release artifact: %w", err)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	artifactPath := fmt.Sprintf("%020d-%s-%s-%s.bin", sequence, operatingSystem, architecture, digest[:16])
	manifest := agent.UpdateManifest{
		Version:      version,
		OS:           operatingSystem,
		Arch:         architecture,
		SHA256:       digest,
		Size:         written,
		Sequence:     uint64(sequence),
		ArtifactPath: artifactPath,
	}
	canonical, err := agent.CanonicalUpdateManifest(manifest)
	if err != nil {
		return AgentRelease{}, err
	}
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(s.privateKey, canonical))
	if err = unix.Renameat2(s.directoryFD, temporaryName, s.directoryFD, artifactPath, unix.RENAME_NOREPLACE); err != nil {
		return AgentRelease{}, fmt.Errorf("publish release artifact: %w", err)
	}
	keepTemporary = false
	if err = unix.Fsync(s.directoryFD); err != nil {
		_ = unix.Unlinkat(s.directoryFD, artifactPath, 0)
		return AgentRelease{}, fmt.Errorf("sync release directory: %w", err)
	}
	release, err := s.store.CreateAgentRelease(ctx, AgentRelease{
		Version:      manifest.Version,
		OS:           manifest.OS,
		Arch:         manifest.Arch,
		SHA256:       manifest.SHA256,
		SizeBytes:    manifest.Size,
		Sequence:     int64(manifest.Sequence),
		Signature:    manifest.Signature,
		ArtifactPath: manifest.ArtifactPath,
	})
	if err != nil {
		_ = unix.Unlinkat(s.directoryFD, artifactPath, 0)
		return AgentRelease{}, err
	}
	return release, nil
}

func (s *ReleaseService) OpenArtifact(path string) (*os.File, error) {
	if s == nil || !releaseArtifactPattern.MatchString(path) {
		return nil, ErrNotFound
	}
	fd, err := unix.Openat(s.directoryFD, path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrNotFound
	}
	return file, nil
}

func (s *ReleaseService) OpenBootstrapRelease(ctx context.Context, releaseID, operatingSystem, architecture string) (bootstrap.BootstrapRelease, error) {
	if s == nil || s.store == nil {
		return bootstrap.BootstrapRelease{}, errors.New("Agent release service is unavailable")
	}
	releases, err := s.store.ListAgentReleases(ctx)
	if err != nil {
		return bootstrap.BootstrapRelease{}, err
	}
	var selected *AgentRelease
	for index := range releases {
		release := &releases[index]
		if releaseID != "" && release.ID != releaseID {
			continue
		}
		if release.OS != operatingSystem || release.Arch != architecture {
			if releaseID != "" {
				return bootstrap.BootstrapRelease{}, ErrReleasePlatform
			}
			continue
		}
		if selected == nil || release.Sequence > selected.Sequence {
			selected = release
		}
	}
	if selected == nil {
		return bootstrap.BootstrapRelease{}, ErrNotFound
	}
	file, err := s.openVerifiedArtifact(ctx, *selected)
	if err != nil {
		return bootstrap.BootstrapRelease{}, err
	}
	return bootstrap.BootstrapRelease{
		ID: selected.ID, Version: selected.Version, Sequence: uint64(selected.Sequence),
		SHA256: selected.SHA256, Content: file,
	}, nil
}

func (s *ReleaseService) openVerifiedArtifact(ctx context.Context, release AgentRelease) (*os.File, error) {
	file, err := s.OpenArtifact(release.ArtifactPath)
	if err != nil {
		return nil, err
	}
	valid := false
	defer func() {
		if !valid {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || release.SizeBytes < 1 || release.SizeBytes > MaxAgentReleaseArtifactBytes || info.Size() != release.SizeBytes {
		return nil, ErrReleaseArtifactIntegrity
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(&contextReader{ctx: ctx, reader: file}, release.SizeBytes+1))
	if err != nil || written != release.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != release.SHA256 {
		return nil, ErrReleaseArtifactIntegrity
	}
	manifest := agent.UpdateManifest{Version: release.Version, OS: release.OS, Arch: release.Arch, SHA256: release.SHA256, Size: release.SizeBytes, Sequence: uint64(release.Sequence), ArtifactPath: release.ArtifactPath}
	canonical, err := agent.CanonicalUpdateManifest(manifest)
	if err != nil {
		return nil, ErrReleaseArtifactIntegrity
	}
	signature, err := base64.StdEncoding.DecodeString(release.Signature)
	publicKey, keyErr := base64.StdEncoding.DecodeString(s.publicKey)
	if err != nil || keyErr != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), canonical, signature) {
		return nil, ErrReleaseArtifactIntegrity
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	valid = true
	return file, nil
}

// CloneVerified publishes an existing immutable artifact as a new signed
// release. The new sequence preserves updater anti-rollback guarantees while
// allowing an operator to restore previously installed bytes.
func (s *ReleaseService) CloneVerified(ctx context.Context, source AgentRelease) (AgentRelease, error) {
	file, err := s.openVerifiedArtifact(ctx, source)
	if err != nil {
		return AgentRelease{}, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return AgentRelease{}, err
	}
	if source.SizeBytes < 1 || source.SizeBytes > MaxAgentReleaseArtifactBytes || info.Size() != source.SizeBytes {
		return AgentRelease{}, ErrReleaseArtifactIntegrity
	}
	payload, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: file}, source.SizeBytes+1))
	if err != nil {
		return AgentRelease{}, err
	}
	if int64(len(payload)) != source.SizeBytes {
		return AgentRelease{}, ErrReleaseArtifactIntegrity
	}
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != source.SHA256 {
		return AgentRelease{}, ErrReleaseArtifactIntegrity
	}
	return s.Create(ctx, source.Version, source.OS, source.Arch, bytes.NewReader(payload))
}

func (s *ReleaseService) Delete(ctx context.Context, releaseID string) error {
	release, err := s.store.DeleteAgentRelease(ctx, releaseID)
	if err != nil {
		return err
	}
	if err = unix.Unlinkat(s.directoryFD, release.ArtifactPath, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("delete release artifact: %w", err)
	}
	return unix.Fsync(s.directoryFD)
}

func randomReleaseName() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return ".upload-" + hex.EncodeToString(random), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
