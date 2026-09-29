package panel

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
	if mode == "off" {
		return resolveFirewallAssignment(nodeID, mode, includeDesired, firewallPlanRow{})
	}
	var row firewallPlanRow
	err := tx.QueryRow(ctx, `
		SELECT state.actual_revision,state.desired_revision,
		       actual.metadata->'listener_tcp_ports',
		       COALESCE(`+firewallPlanCompleteSQL("actual")+`,false),
		       desired.metadata->'listener_tcp_ports',
		       COALESCE(`+firewallPlanCompleteSQL("desired")+`,false)
		FROM node_config_state AS state
		LEFT JOIN config_revisions AS actual
		  ON actual.node_id=state.node_id AND actual.revision=state.actual_revision
		LEFT JOIN config_revisions AS desired
		  ON desired.node_id=state.node_id AND desired.revision=state.desired_revision
		WHERE state.node_id=$1`, nodeID, supportedHAProxyRenderers()).Scan(
		&row.ActualRevision, &row.DesiredRevision, &row.ActualPorts, &row.ActualComplete, &row.DesiredPorts, &row.DesiredComplete,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return resolveFirewallAssignment(nodeID, mode, includeDesired, firewallPlanRow{})
	}
	if err != nil {
		return nil, err
	}
	return resolveFirewallAssignment(nodeID, mode, includeDesired, row)
}

// firewallPlanCompleteSQL reports whether a revision carries a trustworthy
// listener plan: a known renderer, or an advanced-editor revision whose
// ports were extracted by the Panel linter. $2 is supportedHAProxyRenderers().
func firewallPlanCompleteSQL(alias string) string {
	return `((` + alias + `.metadata->>'renderer'=ANY($2::text[])
	          OR ` + alias + `.metadata->>'source'='advanced_editor')
	         AND ` + alias + `.metadata ? 'listener_tcp_ports')`
}

type firewallPlanRow struct {
	ActualRevision, DesiredRevision *int64
	ActualPorts, DesiredPorts       []byte
	ActualComplete, DesiredComplete bool
}

// resolveFirewallAssignment turns the active/desired listener plans into
// the heartbeat assignment. An incomplete plan never prunes managed rules and
// never fails the heartbeat: the Agent must still receive its config.
func resolveFirewallAssignment(nodeID, mode string, includeDesired bool, row firewallPlanRow) (*FirewallAssignment, error) {
	mode, _ = normalizeFirewallMode(mode)
	includeDesired = includeDesired && mode == "apply"
	assignment := &FirewallAssignment{Mode: mode, TCPPorts: []int{}, ActivePlanComplete: true}
	if mode == "off" {
		return assignment, nil
	}
	if row.ActualRevision != nil {
		if !row.ActualComplete {
			assignment.ActivePlanComplete = false
			if !includeDesired {
				// Never prune every managed rule merely because a legacy revision
				// predates listener_tcp_ports metadata.
				assignment.Mode = "observe"
				return assignment, nil
			}
		} else {
			ports, parseErr := parseFirewallPorts(row.ActualPorts)
			if parseErr != nil {
				return nil, parseErr
			}
			assignment.TCPPorts = ports
		}
	}
	if !includeDesired {
		return assignment, nil
	}
	if row.DesiredRevision == nil || !row.DesiredComplete {
		// A legacy manual revision (saved before the Panel extracted its
		// bind ports) has no plan. Degrade to observe for this transition:
		// the config is still delivered, current rules stay untouched and
		// nothing is pruned.
		slog.Warn("desired firewall listener plan is incomplete; firewall kept in observe mode for this config transition", "node_id", nodeID)
		assignment.Mode = "observe"
		return assignment, nil
	}
	desiredPorts, err := parseFirewallPorts(row.DesiredPorts)
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
