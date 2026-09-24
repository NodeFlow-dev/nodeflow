package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

type HostKeyResult struct {
	Algorithm   string `json:"algorithm"`
	Fingerprint string `json:"fingerprint"`
}

var errHostKeyCaptured = errors.New("host key captured")

// ScanHostKey performs only the SSH key exchange. It never authenticates and
// therefore never needs or transmits the node password.
func ScanHostKey(ctx context.Context, address string, port int, algorithm string) (HostKeyResult, error) {
	if net.ParseIP(address) == nil || port < 1 || port > 65535 || !validHostKeyAlgorithm(algorithm) {
		return HostKeyResult{}, errors.New("invalid host key scan parameters")
	}
	var captured ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User:              "nodeflow-keyscan",
		HostKeyAlgorithms: []string{algorithm},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			captured = key
			return errHostKeyCaptured
		},
		Timeout: 10 * time.Second,
	}
	dialer := net.Dialer{Timeout: cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address, strconv.Itoa(port)))
	if err != nil {
		return HostKeyResult{}, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()
	_, _, _, handshakeErr := ssh.NewClientConn(conn, net.JoinHostPort(address, strconv.Itoa(port)), cfg)
	if captured == nil || !errors.Is(handshakeErr, errHostKeyCaptured) {
		return HostKeyResult{}, errors.New("SSH host key unavailable")
	}
	return HostKeyResult{Algorithm: algorithm, Fingerprint: ssh.FingerprintSHA256(captured)}, nil
}

func validHostKeyAlgorithm(algorithm string) bool {
	return algorithm == ssh.KeyAlgoED25519 || algorithm == ssh.KeyAlgoECDSA256 || algorithm == ssh.KeyAlgoRSASHA256
}
