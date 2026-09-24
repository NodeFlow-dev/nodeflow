package panel

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nodeflow/nodeflow/internal/agent"
)

type reportedAgentUpdate struct {
	Status   string
	Version  string
	Sequence int64
	SHA256   string
	Code     string
}

var (
	updateReportCodePattern = regexp.MustCompile(`^[a-z0-9_]{0,100}$`)
	updateSHA256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func parseReportedAgentUpdate(metrics map[string]any) (reportedAgentUpdate, bool, error) {
	raw, exists := metrics["update_verification"]
	if !exists || raw == nil {
		return reportedAgentUpdate{}, false, nil
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return reportedAgentUpdate{}, false, ErrInvalidUpdateMetrics
	}
	report := reportedAgentUpdate{}
	if value, exists := object["status"]; exists {
		report.Status, ok = value.(string)
		if !ok {
			return reportedAgentUpdate{}, false, ErrInvalidUpdateMetrics
		}
	}
	if value, exists := object["version"]; exists {
		report.Version, ok = value.(string)
		if !ok || len(report.Version) > 64 {
			return reportedAgentUpdate{}, false, ErrInvalidUpdateMetrics
		}
	}
	if value, exists := object["sha256"]; exists {
		report.SHA256, ok = value.(string)
		if !ok || (report.SHA256 != "" && !updateSHA256Pattern.MatchString(report.SHA256)) {
			return reportedAgentUpdate{}, false, ErrInvalidUpdateMetrics
		}
	}
	if value, exists := object["code"]; exists {
		report.Code, ok = value.(string)
		if !ok || !updateReportCodePattern.MatchString(report.Code) {
			return reportedAgentUpdate{}, false, ErrInvalidUpdateMetrics
		}
	}
	if value, exists := object["sequence"]; exists {
		report.Sequence, ok = metricInt64(value)
		if !ok || report.Sequence < 0 {
			return reportedAgentUpdate{}, false, ErrInvalidUpdateMetrics
		}
	}
	validStatus := report.Status == "off" || report.Status == "verified" || report.Status == "downloading" ||
		report.Status == "activating" || report.Status == "installed" || report.Status == "rejected" || report.Status == "rolled_back"
	if !validStatus || (report.Status != "off" && report.Sequence < 1) {
		return reportedAgentUpdate{}, false, ErrInvalidUpdateMetrics
	}
	return report, true, nil
}

func metricInt64(value any) (int64, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := number.Int64()
		return parsed, err == nil
	case int64:
		return number, true
	case int:
		return int64(number), true
	case float64:
		if number < math.MinInt64 || number > math.MaxInt64 || math.Trunc(number) != number {
			return 0, false
		}
		return int64(number), true
	default:
		return 0, false
	}
}

func reconcileReportedAgentUpdate(ctx context.Context, tx pgx.Tx, nodeID string, observedAt time.Time, report reportedAgentUpdate, present bool) error {
	if !present || report.Status == "off" {
		return nil
	}
	var desiredSequence *int64
	var desiredSHA, desiredVersion *string
	err := tx.QueryRow(ctx, `
		SELECT r.sequence,r.sha256,r.version
		FROM node_agent_updates u
		LEFT JOIN agent_releases r ON r.id=u.desired_release_id
		WHERE u.node_id=$1 FOR UPDATE OF u`, nodeID).Scan(&desiredSequence, &desiredSHA, &desiredVersion)
	if err == pgx.ErrNoRows || desiredSequence == nil {
		return nil
	}
	if err != nil {
		return err
	}
	if report.Sequence != *desiredSequence {
		return nil
	}
	state := report.Status
	lastError := ""
	switch report.Status {
	case "installed":
		if report.SHA256 != *desiredSHA || report.Version != *desiredVersion {
			return ErrInvalidUpdateMetrics
		}
		_, err = tx.Exec(ctx, `
			UPDATE node_agent_updates SET actual_sequence=$2,state='installed',last_error='',last_report_at=$3,updated_at=$3
			WHERE node_id=$1`, nodeID, report.Sequence, observedAt)
		return err
	case "rejected":
		state = "failed"
		lastError = report.Code
	case "rolled_back":
		lastError = report.Code
	}
	_, err = tx.Exec(ctx, `
		UPDATE node_agent_updates SET state=$2,last_error=$3,last_report_at=$4,updated_at=$4
		WHERE node_id=$1`, nodeID, state, lastError, observedAt)
	return err
}

func buildAgentUpdateAssignment(ctx context.Context, tx pgx.Tx, nodeID string) (*agent.UpdateManifest, error) {
	var release AgentRelease
	err := tx.QueryRow(ctx, `
		SELECT `+agentReleaseColumnsWithAlias("r")+`
		FROM node_agent_updates u
		JOIN agent_releases r ON r.id=u.desired_release_id
		WHERE u.node_id=$1 AND r.sequence>u.actual_sequence
		  AND u.state IN ('pending','downloading','verified','activating')`, nodeID).
		Scan(&release.ID, &release.Version, &release.OS, &release.Arch, &release.SHA256,
			&release.SizeBytes, &release.Sequence, &release.Signature, &release.ArtifactPath, &release.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &agent.UpdateManifest{
		Version:      release.Version,
		OS:           release.OS,
		Arch:         release.Arch,
		SHA256:       release.SHA256,
		Size:         release.SizeBytes,
		Sequence:     uint64(release.Sequence),
		Signature:    release.Signature,
		ArtifactPath: release.ArtifactPath,
	}, nil
}
