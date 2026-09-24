package bootstrap

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"time"
)

const nodeCertificateLifetime = 825 * 24 * time.Hour

var (
	ErrAgentCAExpiresSoon = errors.New("Agent CA expires too soon")
	ErrInvalidNodeCSR     = errors.New("invalid node certificate request")
)

type NodeTLSIdentity struct {
	CACertificatePEM []byte
	CertificatePEM   []byte
	PrivateKeyPEM    []byte
	ServerName       string
}

type IdentityIssuer interface {
	Issue(nodeID string) (NodeTLSIdentity, error)
}

// CSRIdentityIssuer signs an Agent-generated key without accepting any
// identity or extension from the CSR. Panel remains the sole identity owner.
type CSRIdentityIssuer interface {
	IssueCSR(nodeID string, csr *x509.CertificateRequest) ([]byte, error)
}

type MTLSIssuer struct {
	caCertificate *x509.Certificate
	caPEM         []byte
	caKey         crypto.Signer
	serverName    string
	now           func() time.Time
}

func LoadMTLSIssuer(certificateFile, keyFile, serverName string) (*MTLSIssuer, error) {
	certificatePEM, err := os.ReadFile(certificateFile)
	if err != nil {
		return nil, fmt.Errorf("read Agent CA certificate: %w", err)
	}
	block, _ := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("Agent CA certificate file contains no certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !certificate.IsCA || certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("Agent CA certificate is not a signing CA")
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read Agent CA key: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, errors.New("Agent CA key file contains no private key")
	}
	key, err := parseSigner(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	certificatePublic, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal Agent CA public key: %w", err)
	}
	keyPublic, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil || !bytes.Equal(certificatePublic, keyPublic) {
		return nil, errors.New("Agent CA certificate and private key do not match")
	}
	return &MTLSIssuer{
		caCertificate: certificate,
		caPEM:         append([]byte(nil), certificatePEM...),
		caKey:         key,
		serverName:    serverName,
		now:           time.Now,
	}, nil
}

func parseSigner(raw []byte) (crypto.Signer, error) {
	if key, err := x509.ParsePKCS8PrivateKey(raw); err == nil {
		if signer, ok := key.(crypto.Signer); ok {
			return signer, nil
		}
	}
	if key, err := x509.ParsePKCS1PrivateKey(raw); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(raw); err == nil {
		return key, nil
	}
	return nil, errors.New("Agent CA private key format is unsupported")
}

func (i *MTLSIssuer) Issue(nodeID string) (NodeTLSIdentity, error) {
	if !validNodeID(nodeID) {
		return NodeTLSIdentity{}, errors.New("invalid node identity")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return NodeTLSIdentity{}, fmt.Errorf("generate node private key: %w", err)
	}
	certificatePEM, err := i.issuePublicKey(nodeID, publicKey)
	if err != nil {
		return NodeTLSIdentity{}, err
	}
	rawKey, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return NodeTLSIdentity{}, fmt.Errorf("marshal node private key: %w", err)
	}
	return NodeTLSIdentity{
		CACertificatePEM: append([]byte(nil), i.caPEM...),
		CertificatePEM:   certificatePEM,
		PrivateKeyPEM:    pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rawKey}),
		ServerName:       i.serverName,
	}, nil
}

func (i *MTLSIssuer) IssueCSR(nodeID string, csr *x509.CertificateRequest) ([]byte, error) {
	if !validNodeID(nodeID) {
		return nil, errors.New("invalid node identity")
	}
	if csr == nil || csr.CheckSignature() != nil {
		return nil, ErrInvalidNodeCSR
	}
	publicKey, ok := csr.PublicKey.(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return nil, ErrInvalidNodeCSR
	}
	return i.issuePublicKey(nodeID, append(ed25519.PublicKey(nil), publicKey...))
}

func (i *MTLSIssuer) issuePublicKey(nodeID string, publicKey ed25519.PublicKey) ([]byte, error) {
	now := i.now().UTC()
	notAfter := now.Add(nodeCertificateLifetime)
	if caLimit := i.caCertificate.NotAfter.Add(-time.Minute); notAfter.After(caLimit) {
		notAfter = caLimit
	}
	if !notAfter.After(now.Add(24 * time.Hour)) {
		return nil, ErrAgentCAExpiresSoon
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, fmt.Errorf("generate node certificate serial: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               i.caCertificate.Subject,
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	template.Subject.CommonName = nodeID
	rawCertificate, err := x509.CreateCertificate(rand.Reader, template, i.caCertificate, publicKey, i.caKey)
	if err != nil {
		return nil, fmt.Errorf("sign node certificate: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rawCertificate}), nil
}

func validNodeID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}
