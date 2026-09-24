package panel

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// LoadAgentTLSConfig builds the dedicated Agent listener. Browser traffic stays
// on the regular Panel listener; this listener always requires a client
// certificate rooted in the configured private CA.
func LoadAgentTLSConfig(cfg Config) (*tls.Config, error) {
	if cfg.AgentTLSListenAddr == "" {
		return nil, nil
	}
	certificate, err := tls.LoadX509KeyPair(cfg.AgentTLSCertFile, cfg.AgentTLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load Agent TLS certificate: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.AgentTLSClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("read Agent client CA: %w", err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("Agent client CA file contains no certificates")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
