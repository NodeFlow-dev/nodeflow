package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/nodeflow/nodeflow/internal/agent"
	"golang.org/x/sys/unix"
)

func main() {
	lock, err := os.OpenFile(env("NODE_UPDATER_LOCK_FILE", "/run/nodeflow-node-updater/lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		log.Fatal("another Agent update activation is running")
	}

	staging := env("NODE_UPDATER_STAGING_DIR", "/var/lib/nodeflow/updates")
	healthURL, err := agentHealthURL()
	if err != nil {
		log.Fatalf("invalid Agent health URL: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	activator := agent.UpdateActivator{Config: agent.UpdateActivationConfig{
		StagingDir:      staging,
		PendingFile:     env("NODE_UPDATER_PENDING_FILE", staging+"/pending.json"),
		StateFile:       env("NODE_UPDATER_STATE_FILE", "/var/lib/nodeflow-updater/state.json"),
		ResultFile:      env("NODE_UPDATER_RESULT_FILE", staging+"/result.json"),
		ActivationFile:  env("NODE_UPDATER_ACTIVATION_FILE", "/var/lib/nodeflow-updater/activation.json"),
		PublicKeyBase64: updaterPublicKey(),
		TargetBinary:    env("NODE_UPDATER_AGENT_BINARY", "/usr/local/bin/nodeflow-node-agent"),
		AgentService:    env("NODE_UPDATER_AGENT_SERVICE", "nodeflow-node-agent.service"),
		Runner:          agent.ExecRunner{},
		HealthCheck: func(ctx context.Context, expectedVersion string) error {
			return waitForAgentHealth(ctx, expectedVersion, healthURL)
		},
	}}
	result, err := activator.Activate(ctx)
	if err != nil {
		log.Fatalf("Agent update activation failed: status=%s code=%s error=%v", result.Status, result.Code, err)
	}
	log.Printf("Agent update activation finished: status=%s version=%s sequence=%d", result.Status, result.Version, result.Sequence)
}

func updaterPublicKey() string {
	if value := strings.TrimSpace(os.Getenv("NODE_UPDATER_PUBLIC_KEY")); value != "" {
		return value
	}
	// Compatibility with nodes installed before the updater received its own
	// environment file. New installs never need the Agent credential-bearing
	// environment in the updater service.
	return strings.TrimSpace(os.Getenv("NODE_AGENT_UPDATE_PUBLIC_KEY"))
}

func waitForAgentHealth(ctx context.Context, expectedVersion, healthURL string) error {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: time.Second}).DialContext,
		ResponseHeaderTimeout: time.Second,
		DisableKeepAlives:     true,
	}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err == nil {
			response, requestErr := client.Do(request)
			if requestErr == nil {
				var payload struct {
					Status  string `json:"status"`
					Version string `json:"version"`
				}
				decodeErr := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&payload)
				response.Body.Close()
				if response.StatusCode == http.StatusOK && decodeErr == nil && payload.Status == "ok" && (expectedVersion == "" || payload.Version == expectedVersion) {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("Agent health check timed out for version %q", expectedVersion)
		case <-ticker.C:
		}
	}
}

func agentHealthURL() (string, error) {
	raw := strings.TrimSpace(os.Getenv("NODE_UPDATER_HEALTH_URL"))
	if raw == "" {
		host, port, err := net.SplitHostPort(env("NODE_AGENT_LISTEN", "127.0.0.1:4200"))
		if err != nil {
			return "", fmt.Errorf("invalid NODE_AGENT_LISTEN: %w", err)
		}
		raw = "http://" + net.JoinHostPort(host, port) + "/v1/health"
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if parsed.Scheme != "http" || parsed.User != nil || parsed.Port() == "" || (host != "localhost" && (ip == nil || !ip.IsLoopback())) || parsed.Path != "/v1/health" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("health URL must be an absolute loopback http URL ending in /v1/health")
	}
	return parsed.String(), nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
