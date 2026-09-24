package panel

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

const agentReleaseColumns = `id::text,version,os,arch,sha256,size_bytes,sequence,signature,artifact_path,created_at`

func scanAgentRelease(row pgx.Row) (AgentRelease, error) {
	var release AgentRelease
	err := row.Scan(
		&release.ID, &release.Version, &release.OS, &release.Arch, &release.SHA256,
		&release.SizeBytes, &release.Sequence, &release.Signature, &release.ArtifactPath, &release.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentRelease{}, ErrNotFound
	}
	return release, err
}

func (s *PGStore) ReserveAgentReleaseSequence(ctx context.Context) (int64, error) {
	var sequence int64
	err := s.pool.QueryRow(ctx, `SELECT nextval('agent_release_sequence')`).Scan(&sequence)
	return sequence, err
}

func (s *PGStore) CreateAgentRelease(ctx context.Context, release AgentRelease) (AgentRelease, error) {
	return scanAgentRelease(s.pool.QueryRow(ctx, `
		INSERT INTO agent_releases(version,os,arch,sha256,size_bytes,sequence,signature,artifact_path)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING `+agentReleaseColumns,
		release.Version, release.OS, release.Arch, release.SHA256, release.SizeBytes,
		release.Sequence, release.Signature, release.ArtifactPath,
	))
}

func (s *PGStore) ListAgentReleases(ctx context.Context) ([]AgentRelease, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+agentReleaseColumns+` FROM agent_releases ORDER BY sequence DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	releases := make([]AgentRelease, 0)
	for rows.Next() {
		release, err := scanAgentRelease(rows)
		if err != nil {
			return nil, err
		}
		releases = append(releases, release)
	}
	return releases, rows.Err()
}

func (s *PGStore) DeleteAgentRelease(ctx context.Context, releaseID string) (AgentRelease, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AgentRelease{}, err
	}
	defer tx.Rollback(ctx)
	release, err := scanAgentRelease(tx.QueryRow(ctx, `SELECT `+agentReleaseColumns+` FROM agent_releases WHERE id=$1 FOR UPDATE`, releaseID))
	if err != nil {
		return AgentRelease{}, err
	}
	var inUse bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM node_agent_updates u
			WHERE u.desired_release_id=$1 OR u.actual_sequence=$2
		)`, release.ID, release.Sequence).Scan(&inUse)
	if err != nil {
		return AgentRelease{}, err
	}
	if inUse {
		return AgentRelease{}, ErrReleaseInUse
	}
	command, err := tx.Exec(ctx, `DELETE FROM agent_releases WHERE id=$1`, release.ID)
	if err != nil {
		return AgentRelease{}, err
	}
	if command.RowsAffected() != 1 {
		return AgentRelease{}, ErrNotFound
	}
	if err = tx.Commit(ctx); err != nil {
		return AgentRelease{}, err
	}
	return release, nil
}

func (s *PGStore) PrepareBootstrapAgentRelease(ctx context.Context, nodeID, releaseID string) error {
	command, err := s.pool.Exec(ctx, `
		INSERT INTO node_agent_updates(node_id,desired_release_id,actual_sequence,state,last_error,updated_at)
		SELECT $1,id,0,'pending','',clock_timestamp() FROM agent_releases WHERE id=$2
		ON CONFLICT(node_id) DO UPDATE SET desired_release_id=EXCLUDED.desired_release_id,
			state='pending',last_error='',last_report_at=NULL,updated_at=clock_timestamp()`, nodeID, releaseID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) AssignAgentRelease(ctx context.Context, nodeID, releaseID string, expectedActualSequence, expectedDesiredSequence int64) (NodeAgentUpdateState, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return NodeAgentUpdateState{}, err
	}
	defer tx.Rollback(ctx)

	var lockedNodeID string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM nodes WHERE id=$1 FOR UPDATE`, nodeID).Scan(&lockedNodeID); errors.Is(err, pgx.ErrNoRows) {
		return NodeAgentUpdateState{}, ErrNotFound
	} else if err != nil {
		return NodeAgentUpdateState{}, err
	}

	var actualSequence, desiredSequence int64
	err = tx.QueryRow(ctx, `
		SELECT updates.actual_sequence,COALESCE(desired.sequence,0)
		FROM node_agent_updates AS updates
		LEFT JOIN agent_releases AS desired ON desired.id=updates.desired_release_id
		WHERE updates.node_id=$1
		FOR UPDATE OF updates`, nodeID).Scan(&actualSequence, &desiredSequence)
	if errors.Is(err, pgx.ErrNoRows) {
		actualSequence, desiredSequence, err = 0, 0, nil
	}
	if err != nil {
		return NodeAgentUpdateState{}, err
	}
	if actualSequence != expectedActualSequence || desiredSequence != expectedDesiredSequence {
		return NodeAgentUpdateState{}, ErrAgentUpdateStateChanged
	}

	var platformOS, platformArch string
	if err = tx.QueryRow(ctx, `
		SELECT COALESCE(actual.os,latest.metrics->>'os',''),
		       COALESCE(actual.arch,latest.metrics->>'arch','')
		FROM nodes
		LEFT JOIN node_agent_updates AS updates ON updates.node_id=nodes.id
		LEFT JOIN agent_releases AS actual ON actual.sequence=updates.actual_sequence
		LEFT JOIN LATERAL (
			SELECT metrics FROM node_heartbeats
			WHERE node_id=nodes.id ORDER BY received_at DESC LIMIT 1
		) AS latest ON true
		WHERE nodes.id=$1`, nodeID).Scan(&platformOS, &platformArch); err != nil {
		return NodeAgentUpdateState{}, err
	}
	release, err := scanAgentRelease(tx.QueryRow(ctx, `SELECT `+agentReleaseColumns+` FROM agent_releases WHERE id=$1`, releaseID))
	if err != nil {
		return NodeAgentUpdateState{}, err
	}
	if release.Sequence <= max(actualSequence, desiredSequence) {
		return NodeAgentUpdateState{}, ErrReleaseNotNewer
	}
	if platformOS == "" || platformArch == "" {
		return NodeAgentUpdateState{}, ErrReleasePlatformUnknown
	}
	if release.OS != platformOS || release.Arch != platformArch {
		return NodeAgentUpdateState{}, ErrReleasePlatform
	}
	updated := NodeAgentUpdateState{NodeID: nodeID, DesiredRelease: &release}
	err = tx.QueryRow(ctx, `
		INSERT INTO node_agent_updates(node_id,desired_release_id,actual_sequence,state,last_error,updated_at)
		VALUES($1,$2,$3,'pending','',clock_timestamp())
		ON CONFLICT(node_id) DO UPDATE SET
			desired_release_id=EXCLUDED.desired_release_id,
			state='pending',last_error='',last_report_at=NULL,updated_at=clock_timestamp()
		RETURNING actual_sequence,state,last_error,last_report_at,updated_at`, nodeID, releaseID, actualSequence).
		Scan(&updated.ActualSequence, &updated.State, &updated.LastError, &updated.LastReportAt, &updated.UpdatedAt)
	if err != nil {
		return NodeAgentUpdateState{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return NodeAgentUpdateState{}, err
	}
	return updated, nil
}

func (s *PGStore) GetAgentUpdateState(ctx context.Context, nodeID string) (NodeAgentUpdateState, error) {
	var state NodeAgentUpdateState
	var desiredID *string
	err := s.pool.QueryRow(ctx, `
		SELECT n.id::text,u.desired_release_id::text,
		       COALESCE(u.actual_sequence,0),COALESCE(u.state,'idle'),COALESCE(u.last_error,''),
		       u.last_report_at,COALESCE(u.updated_at,n.updated_at)
		FROM nodes n LEFT JOIN node_agent_updates u ON u.node_id=n.id
		WHERE n.id=$1`, nodeID).
		Scan(&state.NodeID, &desiredID, &state.ActualSequence, &state.State, &state.LastError, &state.LastReportAt, &state.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return NodeAgentUpdateState{}, ErrNotFound
	}
	if err != nil || desiredID == nil {
		return state, err
	}
	release, err := scanAgentRelease(s.pool.QueryRow(ctx, `SELECT `+agentReleaseColumns+` FROM agent_releases WHERE id=$1`, *desiredID))
	if err != nil {
		return NodeAgentUpdateState{}, err
	}
	state.DesiredRelease = &release
	return state, nil
}

func (s *PGStore) GetAssignedAgentRelease(ctx context.Context, token string, identity AgentCredentialIdentity, sequence int64) (AgentRelease, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AgentRelease{}, err
	}
	defer tx.Rollback(ctx)
	credential, err := authenticateActiveAgentCredential(ctx, tx, token, identity, true)
	if err != nil {
		return AgentRelease{}, err
	}
	nodeID := credential.NodeID
	release, err := scanAgentRelease(tx.QueryRow(ctx, `
		SELECT `+agentReleaseColumnsWithAlias("r")+`
		FROM node_agent_updates u
		JOIN agent_releases r ON r.id=u.desired_release_id
		WHERE u.node_id=$1 AND r.sequence=$2
		  AND u.state IN ('pending','downloading','verified','activating')`, nodeID, sequence))
	if err != nil {
		return AgentRelease{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AgentRelease{}, err
	}
	return release, nil
}

func agentReleaseColumnsWithAlias(alias string) string {
	return alias + `.id::text,` + alias + `.version,` + alias + `.os,` + alias + `.arch,` +
		alias + `.sha256,` + alias + `.size_bytes,` + alias + `.sequence,` + alias + `.signature,` +
		alias + `.artifact_path,` + alias + `.created_at`
}
