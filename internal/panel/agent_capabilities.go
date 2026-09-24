package panel

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// KernelShaperMinAgentVersion is the first Node Agent release that enforces
// shaper_mode=kernel (the nftables per-client shaper in
// internal/agent/shaper_tc.go). Older Agents ignore the frontend annotation,
// so a kernel-shaped limit would silently disappear on them.
const KernelShaperMinAgentVersion = "1.1.0"

// kernelShaperRequiresAgentCode is the stable API error code returned when a
// kernel-shaped route targets a node whose Agent cannot enforce it.
const kernelShaperRequiresAgentCode = "kernel_shaper_requires_agent_1_1"

// KernelShaperUnsupportedError reports that a node's last reported Agent
// version cannot enforce shaper_mode=kernel.
type KernelShaperUnsupportedError struct {
	RouteID      string
	AgentVersion string
}

func (e *KernelShaperUnsupportedError) Error() string {
	reported := e.AgentVersion
	if strings.TrimSpace(reported) == "" {
		reported = "unknown (no heartbeat)"
	}
	subject := "shaper_mode kernel"
	if e.RouteID != "" {
		subject = "route " + e.RouteID + " uses shaper_mode kernel and"
	}
	return subject + " requires Node Agent " + KernelShaperMinAgentVersion +
		" or newer; the node reports Agent " + reported + ". Upgrade the Agent or use shaper_mode haproxy"
}

// agentSupportsKernelShaper reports whether the Agent version reported by the
// latest heartbeat is at least KernelShaperMinAgentVersion. An empty or
// unparsable version is treated as unsupported.
func agentSupportsKernelShaper(agentVersion string) bool {
	return agentVersionAtLeast(agentVersion, KernelShaperMinAgentVersion)
}

// agentVersionAtLeast compares MAJOR.MINOR.PATCH[-PRERELEASE][+BUILD]
// versions with semver precedence for the numeric core. A pre-release of
// the minimum (for example 1.1.0-rc1) is below it, as in semver.
func agentVersionAtLeast(version, minimum string) bool {
	have, havePre, ok := parseAgentVersion(version)
	if !ok {
		return false
	}
	want, _, _ := parseAgentVersion(minimum)
	for i := range have {
		if have[i] != want[i] {
			return have[i] > want[i]
		}
	}
	return !havePre
}

func parseAgentVersion(value string) ([3]int, bool, bool) {
	var out [3]int
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if plus := strings.IndexByte(value, '+'); plus >= 0 {
		value = value[:plus]
	}
	prerelease := false
	if dash := strings.IndexByte(value, '-'); dash >= 0 {
		prerelease = true
		value = value[:dash]
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return out, false, false
	}
	for i, part := range parts {
		if part == "" || strings.Trim(part, "0123456789") != "" || len(part) > 9 {
			return out, false, false
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return out, false, false
		}
		out[i] = n
	}
	return out, prerelease, true
}

// specNeedsKernelShaper reports whether the route spec relies on the Agent
// kernel shaper. Kernel mode without any client limit renders nothing and
// therefore needs no Agent support.
func specNeedsKernelShaper(spec RouteSpec) bool {
	return spec.ShaperMode == ShaperModeKernel && (spec.ClientDownloadMbps != nil || spec.ClientUploadMbps != nil)
}

func routeNeedsKernelShaper(route Route) bool {
	return route.ShaperMode == ShaperModeKernel && (route.ClientDownloadMbps != nil || route.ClientUploadMbps != nil)
}

// NodeAgent110 is the first Node Agent version that supports shaper_mode
// kernel, effective leastping and ip_weights (the internal/agent/weights.go
// runtime weight controller).
const NodeAgent110 = "1.1.0"

// leastPingRequiresAgentCode and ipWeightsRequiresAgentCode are the stable
// API error codes returned when a route needing the corresponding Agent
// feature targets a node whose Agent cannot enforce it.
const (
	leastPingRequiresAgentCode = "leastping_requires_agent_1_1"
	ipWeightsRequiresAgentCode = "ip_weights_requires_agent_1_1"
)

// LeastPingUnsupportedError reports that a node's last reported Agent version
// cannot run the leastping runtime weight controller.
type LeastPingUnsupportedError struct {
	RouteID      string
	AgentVersion string
}

func (e *LeastPingUnsupportedError) Error() string {
	reported := e.AgentVersion
	if strings.TrimSpace(reported) == "" {
		reported = "unknown (no heartbeat)"
	}
	subject := "balance_algorithm leastping"
	if e.RouteID != "" {
		subject = "route " + e.RouteID + " uses balance_algorithm leastping and"
	}
	return subject + " requires Node Agent " + NodeAgent110 +
		" or newer; the node reports Agent " + reported + ". Upgrade the Agent or choose a different balance_algorithm"
}

// IPWeightsUnsupportedError reports that a node's last reported Agent version
// cannot apply per-IP runtime weight overrides.
type IPWeightsUnsupportedError struct {
	RouteID      string
	AgentVersion string
}

func (e *IPWeightsUnsupportedError) Error() string {
	reported := e.AgentVersion
	if strings.TrimSpace(reported) == "" {
		reported = "unknown (no heartbeat)"
	}
	subject := "ip_weights"
	if e.RouteID != "" {
		subject = "route " + e.RouteID + " uses ip_weights and"
	}
	return subject + " requires Node Agent " + NodeAgent110 +
		" or newer; the node reports Agent " + reported + ". Upgrade the Agent or remove ip_weights"
}

// agentSupportsRoute110Features reports whether the Agent version reported by
// the latest heartbeat can run the leastping/ip_weights runtime weight
// controller.
func agentSupportsRoute110Features(agentVersion string) bool {
	return agentVersionAtLeast(agentVersion, NodeAgent110)
}

// specNeedsLeastPingAgent reports whether the route spec relies on the Agent
// leastping weight controller: an effective leastping algorithm (pool mode,
// sticky not source — source ignores the algorithm entirely).
func specNeedsLeastPingAgent(spec RouteSpec) bool {
	return spec.BalanceAlgorithm == BalanceAlgorithmLeastPing && spec.BalanceMode == "pool" && spec.StickyMode != StickyModeSource
}

func routeNeedsLeastPingAgent(route Route) bool {
	return route.BalanceAlgorithm == BalanceAlgorithmLeastPing && route.BalanceMode == "pool" && route.StickyMode != StickyModeSource
}

// specNeedsIPWeightsAgent reports whether any server of the route spec
// carries a non-empty ip_weights override.
func specNeedsIPWeightsAgent(spec RouteSpec) bool {
	return anyServerIPWeights(spec.Servers)
}

func routeNeedsIPWeightsAgent(route Route) bool {
	for _, server := range route.Servers {
		if len(server.IPWeights) > 0 {
			return true
		}
	}
	return false
}

// checkLeastPingAgentRoutes rejects a route set for publication when an
// enabled route needing effective leastping targets an Agent that cannot run
// the weight controller. Mirrors checkKernelShaperRoutes.
func checkLeastPingAgentRoutes(routes []Route, agentVersion string) error {
	if agentSupportsRoute110Features(agentVersion) {
		return nil
	}
	for _, route := range routes {
		if route.Enabled && !route.DeletePending && routeNeedsLeastPingAgent(route) {
			return &LeastPingUnsupportedError{RouteID: route.ID, AgentVersion: agentVersion}
		}
	}
	return nil
}

// checkIPWeightsAgentRoutes rejects a route set for publication when an
// enabled route with ip_weights targets an Agent that cannot apply them.
// Mirrors checkKernelShaperRoutes.
func checkIPWeightsAgentRoutes(routes []Route, agentVersion string) error {
	if agentSupportsRoute110Features(agentVersion) {
		return nil
	}
	for _, route := range routes {
		if route.Enabled && !route.DeletePending && routeNeedsIPWeightsAgent(route) {
			return &IPWeightsUnsupportedError{RouteID: route.ID, AgentVersion: agentVersion}
		}
	}
	return nil
}

// NodeAgent111 is the first Node Agent version whose weight controller
// understands "# nf-weights ... algo=leastconn" (the leastconn connection
// tolerance). A 1.1.0 Agent rejects the unknown algo and would stop applying
// every runtime weight of the node, so the Panel gates it.
const NodeAgent111 = "1.1.1"

// leastConnToleranceRequiresAgentCode is the stable API error code returned
// when a leastconn route with a tolerance targets a node whose Agent cannot
// apply it.
const leastConnToleranceRequiresAgentCode = "leastconn_tolerance_requires_agent_1_1_1"

// LeastConnToleranceUnsupportedError reports that a node's last reported
// Agent version cannot run the leastconn tolerance controller.
type LeastConnToleranceUnsupportedError struct {
	RouteID      string
	AgentVersion string
}

func (e *LeastConnToleranceUnsupportedError) Error() string {
	reported := e.AgentVersion
	if strings.TrimSpace(reported) == "" {
		reported = "unknown (no heartbeat)"
	}
	subject := "balance_algorithm leastconn with balance_tolerance"
	if e.RouteID != "" {
		subject = "route " + e.RouteID + " uses balance_algorithm leastconn with balance_tolerance and"
	}
	return subject + " requires Node Agent " + NodeAgent111 +
		" or newer; the node reports Agent " + reported + ". Upgrade the Agent or set balance_tolerance to 0"
}

// specNeedsLeastConnAgent reports whether the route spec relies on the Agent
// leastconn tolerance controller: an effective leastconn algorithm (pool
// mode, sticky not source) with a non-zero tolerance and a servers[] pool.
// Connection costs alone render as static weights and need no Agent.
func specNeedsLeastConnAgent(spec RouteSpec) bool {
	return spec.BalanceAlgorithm == BalanceAlgorithmLeastConn && spec.BalanceMode == "pool" &&
		spec.StickyMode != StickyModeSource && spec.LeastPingTolerance > 0 && len(spec.Servers) > 0
}

func routeNeedsLeastConnAgent(route Route) bool {
	return route.BalanceAlgorithm == BalanceAlgorithmLeastConn && route.BalanceMode == "pool" &&
		route.StickyMode != StickyModeSource && route.LeastPingTolerance > 0 && len(route.Servers) > 0
}

// checkLeastConnAgentRoutes rejects a route set for publication when an
// enabled leastconn route with a tolerance targets an Agent older than
// NodeAgent111. Mirrors checkKernelShaperRoutes.
func checkLeastConnAgentRoutes(routes []Route, agentVersion string) error {
	if agentVersionAtLeast(agentVersion, NodeAgent111) {
		return nil
	}
	for _, route := range routes {
		if route.Enabled && !route.DeletePending && routeNeedsLeastConnAgent(route) {
			return &LeastConnToleranceUnsupportedError{RouteID: route.ID, AgentVersion: agentVersion}
		}
	}
	return nil
}

// checkKernelShaperRoutes rejects a route set for publication when an enabled
// kernel-shaped route targets an Agent that cannot enforce it. This is the
// config-build gate: it also catches an Agent downgrade after the route was
// saved, so the next revision cannot silently drop the limit.
func checkKernelShaperRoutes(routes []Route, agentVersion string) error {
	if agentSupportsKernelShaper(agentVersion) {
		return nil
	}
	for _, route := range routes {
		if route.Enabled && !route.DeletePending && routeNeedsKernelShaper(route) {
			return &KernelShaperUnsupportedError{RouteID: route.ID, AgentVersion: agentVersion}
		}
	}
	return nil
}

// nodeAgentVersionTx returns the Agent version from the node's latest
// heartbeat, or "" when the node never reported one.
func nodeAgentVersionTx(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, nodeID string) (string, error) {
	var version string
	err := q.QueryRow(ctx, `SELECT COALESCE(agent_version,'') FROM node_heartbeats WHERE node_id=$1`, nodeID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return version, err
}

// GetNodeAgentVersion returns the Agent version from the latest heartbeat.
func (s *PGStore) GetNodeAgentVersion(ctx context.Context, nodeID string) (string, error) {
	return nodeAgentVersionTx(ctx, s.pool, nodeID)
}

// NodeAgent113 is the first Node Agent version that resolves hostnames in
// accept_proxy_from ("# nf-pp-trusted" annotation) into the trusted-source
// ACL file and keeps it current through the HAProxy runtime API. An older
// Agent would never create the ACL file, so HAProxy validation would fail.
const NodeAgent113 = "1.1.3"

// ppTrustedDomainsRequiresAgentCode is the stable API error code returned
// when a route trusts PROXY protocol from a hostname on a node whose Agent
// cannot resolve it.
const ppTrustedDomainsRequiresAgentCode = "pp_trusted_domains_requires_agent_1_1_3"

// PPTrustedDomainsUnsupportedError reports that a node's last reported Agent
// version cannot maintain hostname-based PROXY protocol trust.
type PPTrustedDomainsUnsupportedError struct {
	RouteID      string
	AgentVersion string
}

func (e *PPTrustedDomainsUnsupportedError) Error() string {
	reported := e.AgentVersion
	if strings.TrimSpace(reported) == "" {
		reported = "unknown (no heartbeat)"
	}
	subject := "hostnames in accept_proxy_from"
	if e.RouteID != "" {
		subject = "route " + e.RouteID + " uses hostnames in accept_proxy_from and"
	}
	return subject + " require Node Agent " + NodeAgent113 +
		" or newer; the node reports Agent " + reported + ". Upgrade the Agent or list IP addresses/CIDRs only"
}

func acceptProxyFromHasHostname(entries []string) bool {
	for _, entry := range entries {
		if isAcceptProxyHostname(strings.TrimSpace(entry)) {
			return true
		}
	}
	return false
}

// specNeedsPPTrustedDomainsAgent reports whether accept_proxy_from of the
// route spec contains a hostname.
func specNeedsPPTrustedDomainsAgent(spec RouteSpec) bool {
	return acceptProxyFromHasHostname(spec.AcceptProxyFrom)
}

func routeNeedsPPTrustedDomainsAgent(route Route) bool {
	return acceptProxyFromHasHostname(route.AcceptProxyFrom)
}

// checkPPTrustedDomainsAgentRoutes rejects a route set for publication when
// an enabled route trusts a hostname on an Agent older than NodeAgent113.
// Mirrors checkKernelShaperRoutes.
func checkPPTrustedDomainsAgentRoutes(routes []Route, agentVersion string) error {
	if agentVersionAtLeast(agentVersion, NodeAgent113) {
		return nil
	}
	for _, route := range routes {
		if route.Enabled && !route.DeletePending && routeNeedsPPTrustedDomainsAgent(route) {
			return &PPTrustedDomainsUnsupportedError{RouteID: route.ID, AgentVersion: agentVersion}
		}
	}
	return nil
}
