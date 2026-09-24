package panel

import (
	"bytes"
	"encoding/json"
)

// Full-route PUT bodies from older clients (and the pre-fix node-page
// toggle) omit the 1.1.0 fields. Treating an absent field as "empty" would
// silently delete every extra server, the trusted PROXY list, the kernel
// shaper and the distribution mode. These helpers make such fields
// tri-state: an absent key (or JSON null) keeps the stored value, while an
// explicit value, including an empty array, replaces it.

// routeInputKeys returns the set of top-level keys whose value is not JSON
// null in a route PUT body.
func routeInputKeys(body []byte) (map[string]bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(raw))
	for key, value := range raw {
		if !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			present[key] = true
		}
	}
	return present, nil
}

// route052AbsentFields are the migration 000052 route-level keys that fall
// back to the stored value when absent from a PUT body (see
// routeInputOmitsStoredFields/mergeAbsentRouteFields).
var route052AbsentFields = []string{
	"balance_algorithm", "balance_random_draws", "leastping_tolerance", "leastping_tolerance_ms",
	"sticky_hash", "sticky_hash_balance_factor", "sticky_table_entries", "sticky_ipv6_prefix", "slowstart",
	"client_ipv6",
}

// routeInputOmitsStoredFields reports whether the body leaves out any field
// that mergeAbsentRouteFields would take from the stored route.
func routeInputOmitsStoredFields(present map[string]bool) bool {
	if present["balance_tolerance"] {
		// balance_tolerance is the alias of leastping_tolerance.
		present = withKey(present, "leastping_tolerance")
	}
	if !present["servers"] || !present["accept_proxy_from"] || !present["shaper_mode"] ||
		(!present["sticky_mode"] && !present["sticky_enabled"]) || !present["sticky_ttl"] {
		return true
	}
	for _, key := range route052AbsentFields {
		if !present[key] {
			return true
		}
	}
	return false
}

// mergeAbsentRouteFields fills fields absent from a PUT body from the stored
// route.
//
//   - servers absent: the stored servers are kept. balance_mode is then left
//     absent on purpose, so the stored backup flags stay authoritative (an
//     explicit balance_mode would reshape rc-era layouts such as two
//     primaries plus one backup). Per-server fields (weight, cost,
//     ip_weights) inside an explicit servers[] are taken as sent; there is no
//     tri-state merge below the servers array itself.
//   - accept_proxy_from, shaper_mode, sticky_ttl absent: stored value.
//   - sticky_mode and sticky_enabled both absent: stored sticky_mode.
//   - balance_algorithm, balance_random_draws, leastping_tolerance(_ms),
//     sticky_hash, sticky_hash_balance_factor, sticky_table_entries,
//     sticky_ipv6_prefix, slowstart, client_ipv6 absent: stored value.
func mergeAbsentRouteFields(in *routeInput, present map[string]bool, current Route) {
	if !present["servers"] && len(current.Servers) > 0 {
		in.Servers = routeServersToInput(current.Servers)
		if !present["balance_mode"] {
			in.BalanceMode = ""
		}
	}
	if !present["accept_proxy_from"] {
		in.AcceptProxyFrom = append([]string{}, current.AcceptProxyFrom...)
	}
	if !present["shaper_mode"] {
		in.ShaperMode = current.ShaperMode
	}
	if !present["sticky_mode"] && !present["sticky_enabled"] {
		in.StickyMode = current.StickyMode
		in.StickyEnabled = current.StickyEnabled
	}
	if !present["sticky_ttl"] {
		in.StickyTTL = current.StickyTTL
	}
	if !present["balance_algorithm"] {
		in.BalanceAlgorithm = current.BalanceAlgorithm
	}
	if !present["balance_random_draws"] {
		in.BalanceRandomDraws = current.BalanceRandomDraws
	}
	if !present["leastping_tolerance"] && !present["balance_tolerance"] {
		tolerance := current.LeastPingTolerance
		in.LeastPingTolerance = &tolerance
	}
	if !present["leastping_tolerance_ms"] {
		in.LeastPingToleranceMS = current.LeastPingToleranceMS
	}
	if !present["sticky_hash"] {
		in.StickyHash = current.StickyHash
	}
	if !present["sticky_hash_balance_factor"] {
		in.StickyHashBalanceFactor = current.StickyHashBalanceFactor
	}
	if !present["sticky_table_entries"] {
		in.StickyTableEntries = current.StickyTableEntries
	}
	if !present["sticky_ipv6_prefix"] {
		in.StickyIPv6Prefix = current.StickyIPv6Prefix
	}
	if !present["slowstart"] {
		in.Slowstart = current.Slowstart
	}
	if !present["client_ipv6"] {
		clientIPv6 := !routeClientIPv4Only(current)
		in.ClientIPv6 = &clientIPv6
	}
}

func routeServersToInput(servers []RouteServer) []routeServerInput {
	out := make([]routeServerInput, len(servers))
	for i, s := range servers {
		out[i] = routeServerInput{
			Position: s.Position, Name: s.Name, TargetType: s.TargetType, Host: s.Host, Port: s.Port,
			UnixSocketPath: s.UnixSocketPath, Backup: s.Backup, DNSPool: s.DNSPool, PreferredIP: s.PreferredIP,
			Weight: s.Weight, Cost: s.Cost, IPWeights: routeServerIPWeightsCopy(s.IPWeights),
		}
	}
	return out
}

// withKey returns a copy of present with key marked present.
func withKey(present map[string]bool, key string) map[string]bool {
	out := make(map[string]bool, len(present)+1)
	for k, v := range present {
		out[k] = v
	}
	out[key] = true
	return out
}
