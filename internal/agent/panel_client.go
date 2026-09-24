package agent

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// NewPanelHTTPClient creates the one shared client used by heartbeat and
// configuration reports. Sharing the transport keeps the authenticated TLS
// connection alive between control-plane requests.
func NewPanelHTTPClient(cfg Config) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 10 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second

	tlsConfigured := cfg.PanelTLSCA != "" || cfg.PanelTLSCert != "" || cfg.PanelTLSKey != "" || cfg.PanelTLSServerName != ""
	if tlsConfigured {
		if cfg.PanelTLSCA == "" || cfg.PanelTLSCert == "" || cfg.PanelTLSKey == "" {
			return nil, errors.New("Panel mTLS requires CA, client certificate and client key")
		}
		panelURL, err := url.Parse(cfg.PanelURL)
		if err != nil || !strings.EqualFold(panelURL.Scheme, "https") || panelURL.Host == "" {
			return nil, errors.New("Panel mTLS requires an absolute https NODE_AGENT_PANEL_URL")
		}
		caPEM, err := os.ReadFile(cfg.PanelTLSCA)
		if err != nil {
			return nil, fmt.Errorf("read Panel CA: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("Panel CA file contains no certificates")
		}
		certificate, err := tls.LoadX509KeyPair(cfg.PanelTLSCert, cfg.PanelTLSKey)
		if err != nil {
			return nil, fmt.Errorf("load node client certificate: %w", err)
		}
		transport.TLSClientConfig = &tls.Config{
			Certificates: []tls.Certificate{certificate},
			RootCAs:      roots,
			ServerName:   cfg.PanelTLSServerName,
			MinVersion:   tls.VersionTLS13,
		}
	}

	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}
