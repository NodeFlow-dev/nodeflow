package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/jackc/pgx/v5"
)

const nodeHAProxySettingsKey = "haproxy_settings"

// Zero values preserve the existing renderer defaults.
type HAProxySettings struct {
	MaxConnections int    `json:"max_connections,omitempty"`
	Threads        int    `json:"threads,omitempty"`
	TimeoutConnect string `json:"timeout_connect,omitempty"`
	TimeoutClient  string `json:"timeout_client,omitempty"`
	TimeoutServer  string `json:"timeout_server,omitempty"`
}

var haproxyTimeoutPattern = regexp.MustCompile(`^[1-9][0-9]{0,7}(ms|s|m|h)$`)
var ErrAdvancedConfig = errors.New("manual HAProxy configuration is active; return to generated configuration before changing routes")

func (s HAProxySettings) validate() error {
	if s.MaxConnections < 0 || s.MaxConnections > 10000000 {
		return fmt.Errorf("max_connections must be between 0 (automatic) and 10000000")
	}
	if s.Threads < 0 || s.Threads > 256 {
		return fmt.Errorf("threads must be between 0 (automatic) and 256")
	}
	for name, value := range map[string]string{"timeout_connect": s.TimeoutConnect, "timeout_client": s.TimeoutClient, "timeout_server": s.TimeoutServer} {
		if value != "" && !haproxyTimeoutPattern.MatchString(value) {
			return fmt.Errorf("%s must be a positive duration with ms, s, m or h suffix", name)
		}
	}
	return nil
}

func parseHAProxySettings(value any) (HAProxySettings, error) {
	var settings HAProxySettings
	if value == nil {
		return settings, errors.New("haproxy_settings must be an object")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return settings, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return settings, fmt.Errorf("invalid haproxy_settings: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return settings, errors.New("invalid haproxy_settings")
	}
	return settings, settings.validate()
}

func settingsFromMetadata(metadata map[string]any) (HAProxySettings, error) {
	value, exists := metadata[nodeHAProxySettingsKey]
	if !exists {
		return HAProxySettings{}, nil
	}
	return parseHAProxySettings(value)
}

func timeoutOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// ResumeGeneratedConfig renders and assigns under the same node lock as route
// mutations. This prevents a stale preview from replacing newer route intent.
func (s *PGStore) ResumeGeneratedConfig(ctx context.Context, nodeID string) (ConfigRevision, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ConfigRevision{}, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM nodes WHERE id=$1 FOR UPDATE`, nodeID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ConfigRevision{}, ErrNotFound
		}
		return ConfigRevision{}, err
	}
	revision, err := createAndAssignGeneratedRevisionTx(ctx, tx, nodeID, "", "Resume generated configuration", true)
	if err != nil {
		return ConfigRevision{}, err
	}
	return revision, tx.Commit(ctx)
}
