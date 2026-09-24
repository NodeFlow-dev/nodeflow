package panel

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (s *PGStore) ReorderNodes(ctx context.Context, ids []string) ([]Node, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `LOCK TABLE nodes IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return nil, err
	}
	current, err := listNodesTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !sameOrderedResourceSet(ids, nodeIDs(current)) {
		return nil, ErrOrderConflict
	}
	for index, id := range ids {
		if _, err = tx.Exec(ctx, `UPDATE nodes SET sort_order=$2 WHERE id=$1`, id, -int64(index)-1); err != nil {
			return nil, err
		}
	}
	for index, id := range ids {
		if _, err = tx.Exec(ctx, `UPDATE nodes SET sort_order=$2 WHERE id=$1`, id, int64(index)+1); err != nil {
			return nil, err
		}
	}
	ordered, err := listNodesTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	return ordered, tx.Commit(ctx)
}

func (s *PGStore) ReorderRoutes(ctx context.Context, nodeID string, ids []string) ([]Route, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockRouteNode(ctx, tx, nodeID); err != nil {
		return nil, err
	}
	current, err := listRoutesTx(ctx, tx, nodeID)
	if err != nil {
		return nil, err
	}
	if !sameOrderedResourceSet(ids, routeIDs(current)) {
		return nil, ErrOrderConflict
	}
	for index, id := range ids {
		if _, err = tx.Exec(ctx, `UPDATE routes SET sort_order=$3 WHERE node_id=$1 AND id=$2`, nodeID, id, -int64(index)-1); err != nil {
			return nil, err
		}
	}
	for index, id := range ids {
		if _, err = tx.Exec(ctx, `UPDATE routes SET sort_order=$3 WHERE node_id=$1 AND id=$2`, nodeID, id, int64(index)+1); err != nil {
			return nil, err
		}
	}
	ordered, err := listRoutesTx(ctx, tx, nodeID)
	if err != nil {
		return nil, err
	}
	return ordered, tx.Commit(ctx)
}

func listNodesTx(ctx context.Context, tx pgx.Tx) ([]Node, error) {
	rows, err := tx.Query(ctx, `SELECT `+nodeColumns+` FROM nodes ORDER BY sort_order,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Node, 0)
	for rows.Next() {
		node, scanErr := scanNode(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, node)
	}
	return result, rows.Err()
}

func nodeIDs(nodes []Node) []string {
	result := make([]string, len(nodes))
	for i := range nodes {
		result[i] = nodes[i].ID
	}
	return result
}

func routeIDs(routes []Route) []string {
	result := make([]string, len(routes))
	for i := range routes {
		result[i] = routes[i].ID
	}
	return result
}

func sameOrderedResourceSet(requested, current []string) bool {
	if len(requested) != len(current) {
		return false
	}
	known := make(map[string]struct{}, len(current))
	for _, id := range current {
		known[id] = struct{}{}
	}
	for _, id := range requested {
		if _, ok := known[id]; !ok || !validID(id) {
			return false
		}
		delete(known, id)
	}
	return len(known) == 0
}

func (s *PGStore) GetHAProxyControl(ctx context.Context, nodeID string) (NodeHAProxyControl, error) {
	return scanHAProxyControl(s.pool.QueryRow(ctx, `
		SELECT node_id::text,supported,desired_enabled,generation,actual_enabled,active_state,
		       report_generation,restart_generation,restart_report_generation,last_error,reported_at,updated_at
		FROM node_haproxy_control WHERE node_id=$1`, nodeID))
}

func (s *PGStore) UpdateHAProxyControl(ctx context.Context, nodeID string, enabled bool, expectedGeneration int64) (NodeHAProxyControl, error) {
	control, err := scanHAProxyControl(s.pool.QueryRow(ctx, `
		UPDATE node_haproxy_control
		SET desired_enabled=$2,generation=generation+1,last_error='',updated_at=clock_timestamp(),
		    restart_report_generation=CASE WHEN NOT $2 THEN restart_generation ELSE restart_report_generation END
		WHERE node_id=$1 AND generation=$3 AND supported
		RETURNING node_id::text,supported,desired_enabled,generation,actual_enabled,active_state,
		          report_generation,restart_generation,restart_report_generation,last_error,reported_at,updated_at`, nodeID, enabled, expectedGeneration))
	if errors.Is(err, ErrNotFound) {
		var exists bool
		if checkErr := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM node_haproxy_control WHERE node_id=$1)`, nodeID).Scan(&exists); checkErr != nil {
			return NodeHAProxyControl{}, checkErr
		}
		if exists {
			var supported bool
			if checkErr := s.pool.QueryRow(ctx, `SELECT supported FROM node_haproxy_control WHERE node_id=$1`, nodeID).Scan(&supported); checkErr != nil {
				return NodeHAProxyControl{}, checkErr
			}
			if !supported {
				return NodeHAProxyControl{}, ErrControlUnsupported
			}
			return NodeHAProxyControl{}, ErrControlGenerationConflict
		}
	}
	return control, err
}

// RestartHAProxy requests a hard HAProxy restart without changing the desired
// enable state. restart_generation is both the optimistic-lock value for this
// operation and an independent monotonic command for Agents.
func (s *PGStore) RestartHAProxy(ctx context.Context, nodeID string, expectedGeneration int64) (NodeHAProxyControl, error) {
	control, err := scanHAProxyControl(s.pool.QueryRow(ctx, `
		UPDATE node_haproxy_control
		SET restart_generation=restart_generation+1,last_error='',updated_at=clock_timestamp()
		WHERE node_id=$1 AND restart_generation=$2 AND supported AND desired_enabled
		RETURNING node_id::text,supported,desired_enabled,generation,actual_enabled,active_state,
		          report_generation,restart_generation,restart_report_generation,last_error,reported_at,updated_at`, nodeID, expectedGeneration))
	if !errors.Is(err, ErrNotFound) {
		return control, err
	}
	var exists, supported, enabled bool
	if checkErr := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM node_haproxy_control WHERE node_id=$1),
		       COALESCE((SELECT supported FROM node_haproxy_control WHERE node_id=$1),false),
		       COALESCE((SELECT desired_enabled FROM node_haproxy_control WHERE node_id=$1),false)`, nodeID).Scan(&exists, &supported, &enabled); checkErr != nil {
		return NodeHAProxyControl{}, checkErr
	}
	if !exists {
		return NodeHAProxyControl{}, ErrNotFound
	}
	if !supported {
		return NodeHAProxyControl{}, ErrControlUnsupported
	}
	if !enabled {
		return NodeHAProxyControl{}, ErrHAProxyDisabled
	}
	return NodeHAProxyControl{}, ErrControlGenerationConflict
}

func scanHAProxyControl(row pgx.Row) (NodeHAProxyControl, error) {
	var control NodeHAProxyControl
	err := row.Scan(&control.NodeID, &control.Supported, &control.DesiredEnabled, &control.Generation, &control.ActualEnabled,
		&control.ActiveState, &control.ReportGeneration, &control.RestartGeneration, &control.RestartReportGeneration,
		&control.LastError, &control.ReportedAt, &control.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return NodeHAProxyControl{}, ErrNotFound
	}
	return control, err
}
