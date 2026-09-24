package panel

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"
)

const maxFirewallPorts = 1024

func normalizeFirewallMode(mode string) (string, bool) {
	if mode != "off" && mode != "observe" && mode != "apply" {
		return "observe", false
	}
	return mode, true
}

// buildFirewallAssignment returns the immutable active listener set. When a
// config assignment accompanies the heartbeat it also includes the immutable
// desired set, allowing the Agent to pre-open their union before HAProxy reload
// and prune only after activation succeeds.
func buildFirewallAssignment(ctx context.Context, tx pgx.Tx, nodeID, mode string, includeDesired bool) (*FirewallAssignment, error) {
	mode, _ = normalizeFirewallMode(mode)
	includeDesired = includeDesired && mode == "apply"
	assignment := &FirewallAssignment{Mode: mode, TCPPorts: []int{}, ActivePlanComplete: true}
	if mode == "off" {
		return assignment, nil
	}
	var actualRevision, desiredRevision *int64
	var actualRaw, desiredRaw []byte
	var actualComplete, desiredComplete bool
	err := tx.QueryRow(ctx, `
		SELECT state.actual_revision,state.desired_revision,
		       actual.metadata->'listener_tcp_ports',
		       COALESCE(actual.metadata->>'renderer'=ANY($2::text[])
		                AND actual.metadata ? 'listener_tcp_ports',false),
		       desired.metadata->'listener_tcp_ports',
		       COALESCE(desired.metadata->>'renderer'=ANY($2::text[])
		                AND desired.metadata ? 'listener_tcp_ports',false)
		FROM node_config_state AS state
		LEFT JOIN config_revisions AS actual
		  ON actual.node_id=state.node_id AND actual.revision=state.actual_revision
		LEFT JOIN config_revisions AS desired
		  ON desired.node_id=state.node_id AND desired.revision=state.desired_revision
		WHERE state.node_id=$1`, nodeID, supportedHAProxyRenderers()).Scan(
		&actualRevision, &desiredRevision, &actualRaw, &actualComplete, &desiredRaw, &desiredComplete,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return assignment, nil
	}
	if err != nil {
		return nil, err
	}
	if actualRevision != nil {
		if !actualComplete {
			assignment.ActivePlanComplete = false
			if !includeDesired {
				// Never prune every managed rule merely because a legacy revision
				// predates listener_tcp_ports metadata.
				assignment.Mode = "observe"
				return assignment, nil
			}
		} else {
			ports, parseErr := parseFirewallPorts(actualRaw)
			if parseErr != nil {
				return nil, parseErr
			}
			assignment.TCPPorts = ports
		}
	}
	if !includeDesired {
		return assignment, nil
	}
	if desiredRevision == nil || !desiredComplete {
		return nil, errors.New("desired firewall listener plan is incomplete")
	}
	desiredPorts, err := parseFirewallPorts(desiredRaw)
	if err != nil {
		return nil, err
	}
	assignment.DesiredTCPPorts = desiredPorts
	assignment.Transition = true
	return assignment, nil
}

func parseFirewallPorts(raw []byte) ([]int, error) {
	var ports []int
	if json.Unmarshal(raw, &ports) != nil || len(ports) > maxFirewallPorts {
		return nil, errors.New("invalid firewall ports in active revision")
	}
	seen := make(map[int]struct{}, len(ports))
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return nil, errors.New("invalid firewall port in active revision")
		}
		seen[port] = struct{}{}
	}
	result := make([]int, 0, len(seen))
	for port := range seen {
		result = append(result, port)
	}
	sort.Ints(result)
	return result, nil
}
