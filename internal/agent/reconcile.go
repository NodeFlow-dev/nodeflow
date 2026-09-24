package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	MaxManagedConfigBytes = 512 << 10
	maxConfigReportBytes  = 64 << 10
)

type ConfigAssignment struct {
	Revision int64  `json:"revision"`
	Config   string `json:"config"`
	SHA256   string `json:"sha256"`
}

type ConfigReport struct {
	Revision          int64          `json:"revision"`
	State             string         `json:"state"`
	ActualRevision    *int64         `json:"actual_revision,omitempty"`
	Error             string         `json:"error,omitempty"`
	RollbackAttempted bool           `json:"rollback_attempted"`
	RollbackSucceeded *bool          `json:"rollback_succeeded,omitempty"`
	Details           map[string]any `json:"details,omitempty"`
}

type ConfigReporter struct {
	URL    string
	Token  string
	Client *http.Client
}

func (r ConfigReporter) Post(ctx context.Context, report ConfigReport) error {
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode config report")
	}
	if len(body) > maxConfigReportBytes {
		return fmt.Errorf("config report exceeds size limit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL+"/agent/v1/config-report", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Content-Type", "application/json")
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxConfigReportBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("panel returned HTTP %d", resp.StatusCode)
	}
	return nil
}

type Reconciler struct {
	Manager  *ConfigManager
	Reporter ConfigReporter
}

func (r *Reconciler) Reconcile(ctx context.Context, assignment ConfigAssignment) error {
	if r == nil || r.Manager == nil {
		return fmt.Errorf("reconciler is not configured")
	}
	if code := validateAssignment(assignment); code != "" {
		if assignment.Revision > 0 {
			report := ConfigReport{Revision: assignment.Revision, State: "failed", ActualRevision: r.actualRevision(), Error: code, Details: map[string]any{"reason": code}}
			if err := r.Reporter.Post(ctx, report); err != nil {
				return fmt.Errorf("%s; config report failed: %w", code, err)
			}
		}
		return fmt.Errorf("%s", code)
	}

	actualBefore := r.actualRevision()
	_ = r.Reporter.Post(ctx, ConfigReport{Revision: assignment.Revision, State: "applying", ActualRevision: actualBefore})
	revision := strconv.FormatInt(assignment.Revision, 10)
	result, _, applyErr := r.Manager.ApplyRevision(ctx, []byte(assignment.Config), revision)
	if applyErr == nil {
		report := ConfigReport{
			Revision: assignment.Revision, State: "applied", ActualRevision: &assignment.Revision,
			Details: map[string]any{"idempotent": result.Idempotent},
		}
		if err := r.Reporter.Post(ctx, report); err != nil {
			return fmt.Errorf("revision %d applied; config report failed: %w", assignment.Revision, err)
		}
		return nil
	}

	code := "apply_failed"
	report := ConfigReport{Revision: assignment.Revision, State: "failed", ActualRevision: r.actualRevision(), Error: code, Details: map[string]any{"reason": code}}
	var typed *ApplyError
	if errors.As(applyErr, &typed) {
		code = typed.Code
		report.Error = code
		report.Details["reason"] = code
		report.RollbackAttempted = typed.RollbackAttempted
		report.RollbackSucceeded = typed.RollbackSucceeded
		if typed.RollbackAttempted && typed.RollbackSucceeded != nil && *typed.RollbackSucceeded {
			report.State = "rolled_back"
		}
	}
	if err := r.Reporter.Post(ctx, report); err != nil {
		return fmt.Errorf("revision %d %s; config report failed: %w", assignment.Revision, code, err)
	}
	return fmt.Errorf("revision %d %s", assignment.Revision, code)
}

func (r *Reconciler) actualRevision() *int64 {
	actual, err := r.Manager.ActualRevision()
	if err != nil {
		return nil
	}
	revision, err := strconv.ParseInt(actual, 10, 64)
	if err != nil || revision < 1 {
		return nil
	}
	return &revision
}

func validateAssignment(assignment ConfigAssignment) string {
	if assignment.Revision < 1 {
		return "invalid_assignment_revision"
	}
	if len(assignment.Config) == 0 || len(assignment.Config) > MaxManagedConfigBytes || strings.TrimSpace(assignment.Config) == "" {
		return "invalid_assignment_size"
	}
	if len(assignment.SHA256) != sha256.Size*2 {
		return "invalid_assignment_checksum"
	}
	if _, err := hex.DecodeString(assignment.SHA256); err != nil {
		return "invalid_assignment_checksum"
	}
	sum := sha256.Sum256([]byte(assignment.Config))
	if !strings.EqualFold(hex.EncodeToString(sum[:]), assignment.SHA256) {
		return "assignment_checksum_mismatch"
	}
	return ""
}
