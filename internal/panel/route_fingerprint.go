package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// routeSpecFingerprint identifies the operator-visible desired route shape.
// Deployment state, revision numbers, timestamps and optimistic version are
// deliberately excluded. Struct field order makes the JSON input stable.
func routeSpecFingerprint(spec RouteSpec) string {
	payload := struct {
		Name               string            `json:"name"`
		ListenerIP         string            `json:"listener_ip"`
		ListenerPort       int               `json:"listener_port"`
		MatchMode          string            `json:"match_mode"`
		SNIs               []string          `json:"snis"`
		Fallback           bool              `json:"fallback"`
		TargetType         string            `json:"target_type"`
		TargetHost         string            `json:"target_host"`
		TargetPort         int               `json:"target_port"`
		DNSPool            bool              `json:"dns_pool"`
		UnixSocketPath     string            `json:"unix_socket_path"`
		HealthCheck        bool              `json:"health_check"`
		ProxyProtocol      string            `json:"proxy_protocol"`
		QuotaBytes         *int64            `json:"quota_bytes"`
		QuotaAction        string            `json:"quota_action"`
		QuotaPeriod        string            `json:"quota_period"`
		ClientUploadMbps   *int64            `json:"client_upload_mbps"`
		ClientDownloadMbps *int64            `json:"client_download_mbps"`
		Enabled            bool              `json:"enabled"`
		CustomFragment     string            `json:"custom_fragment"`
		AcceptProxyFrom    []string          `json:"accept_proxy_from"`
		Servers            []RouteServerSpec `json:"servers,omitempty"`
		StickyEnabled      bool              `json:"sticky_enabled"`
		// sticky_table_size/sticky_expire were removed in migration 000030 but
		// stay in the hash input as their former defaults, so routes created
		// before 1.1.0 keep a byte-identical fingerprint.
		StickyTableSize string `json:"sticky_table_size"`
		StickyExpire    string `json:"sticky_expire"`
		// balance_mode is deliberately not hashed: the rendered backend is fully
		// determined by servers[].backup and sticky_enabled (validation forces
		// sticky off in failover mode), so hashing it would only change the
		// fingerprint of existing routes without any render change.
		// Omitted for the default so existing route fingerprints stay stable.
		ShaperMode string `json:"shaper_mode,omitempty"`
		// sticky_mode is omitted when it equals what pre-000051 fields
		// imply, so existing route fingerprints stay stable.
		StickyMode string `json:"sticky_mode,omitempty"`
		StickyTTL  string `json:"sticky_ttl,omitempty"`
		// Migration 000052 additions. All omitempty: a migrated row's legacy
		// sticky_mode token (fingerprintStickyMode) already carries whatever
		// these fields cannot express, so a fresh route that only used the
		// pre-052 shape hashes identically to before the migration.
		BalanceAlgorithm        string  `json:"balance_algorithm,omitempty"`
		BalanceRandomDraws      int     `json:"balance_random_draws,omitempty"`
		LeastPingTolerance      float64 `json:"leastping_tolerance,omitempty"`
		LeastPingToleranceMS    int     `json:"leastping_tolerance_ms,omitempty"`
		StickyHash              string  `json:"sticky_hash,omitempty"`
		StickyHashBalanceFactor int     `json:"sticky_hash_balance_factor,omitempty"`
		StickyTableEntries      string  `json:"sticky_table_entries,omitempty"`
		StickyIPv6Prefix        int     `json:"sticky_ipv6_prefix,omitempty"`
		Slowstart               string  `json:"slowstart,omitempty"`
		// Migration 000054: only the non-default IPv4-only value is hashed.
		ClientIPv4Only bool `json:"client_ipv4_only,omitempty"`
	}{
		Name: spec.Name, ListenerIP: spec.ListenerIP, ListenerPort: spec.ListenerPort, MatchMode: spec.MatchMode, SNIs: spec.SNIs,
		Fallback: spec.Fallback, TargetType: spec.TargetType, TargetHost: spec.TargetHost,
		TargetPort: spec.TargetPort, DNSPool: spec.DNSPool, UnixSocketPath: spec.UnixSocketPath, HealthCheck: spec.HealthCheck,
		ProxyProtocol: spec.ProxyProtocol, QuotaBytes: spec.QuotaBytes,
		QuotaAction: spec.QuotaAction, QuotaPeriod: spec.QuotaPeriod,
		ClientUploadMbps: spec.ClientUploadMbps, ClientDownloadMbps: spec.ClientDownloadMbps,
		Enabled: spec.Enabled, CustomFragment: spec.CustomFragment, AcceptProxyFrom: spec.AcceptProxyFrom,
		Servers:       spec.Servers,
		StickyEnabled: spec.StickyEnabled, StickyTableSize: "1m", StickyExpire: "30m",
		ShaperMode:              fingerprintShaperMode(spec.ShaperMode),
		StickyMode:              fingerprintStickyMode(spec),
		StickyTTL:               spec.StickyTTL,
		BalanceAlgorithm:        fingerprintBalanceAlgorithm(spec),
		BalanceRandomDraws:      spec.BalanceRandomDraws,
		LeastPingTolerance:      spec.LeastPingTolerance,
		LeastPingToleranceMS:    spec.LeastPingToleranceMS,
		StickyHash:              spec.StickyHash,
		StickyHashBalanceFactor: spec.StickyHashBalanceFactor,
		StickyTableEntries:      spec.StickyTableEntries,
		StickyIPv6Prefix:        spec.StickyIPv6Prefix,
		Slowstart:               spec.Slowstart,
		ClientIPv4Only:          spec.ClientIPv4Only,
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func routeAsSpec(route Route, enabled bool) RouteSpec {
	return RouteSpec{
		Name: route.Name, ListenerIP: route.ListenerIP, ListenerPort: route.ListenerPort, MatchMode: route.MatchMode,
		SNIs: append([]string(nil), route.SNIs...), Fallback: route.Fallback, Hostname: route.Hostname,
		TargetType: route.TargetType, TargetHost: route.TargetHost, TargetPort: route.TargetPort, DNSPool: route.DNSPool,
		UnixSocketPath: route.UnixSocketPath, HealthCheck: route.HealthCheck, ProxyProtocol: route.ProxyProtocol,
		QuotaBytes: route.QuotaBytes, QuotaAction: route.QuotaAction, QuotaPeriod: route.QuotaPeriod, Enabled: enabled,
		ClientUploadMbps: route.ClientUploadMbps, ClientDownloadMbps: route.ClientDownloadMbps,
		CustomFragment: route.CustomFragment, AcceptProxyFrom: append([]string(nil), route.AcceptProxyFrom...),
		Servers:       routeServersToSpec(route.Servers),
		StickyEnabled: route.StickyEnabled, BalanceMode: route.BalanceMode,
		ShaperMode: route.ShaperMode, StickyMode: route.StickyMode, StickyTTL: route.StickyTTL,
		BalanceAlgorithm: route.BalanceAlgorithm, BalanceRandomDraws: route.BalanceRandomDraws,
		LeastPingTolerance: route.LeastPingTolerance, LeastPingToleranceMS: route.LeastPingToleranceMS,
		StickyHash: route.StickyHash, StickyHashBalanceFactor: route.StickyHashBalanceFactor,
		StickyTableEntries: route.StickyTableEntries, StickyIPv6Prefix: route.StickyIPv6Prefix,
		ClientIPv4Only: routeClientIPv4Only(route),
		Slowstart:      route.Slowstart,
	}
}

// legacyStickyModeToken reconstructs the pre-000052 flat sticky_mode value
// (source, source_table, leastconn, roundrobin) from the two orthogonal
// 000052 fields, so a migrated route hashes exactly like it did before the
// migration. spec.StickyMode == "" is the pre-000051 sentinel (derived
// entirely from sticky_enabled/DNS pools elsewhere) and is never itself
// hashed, matching the original function.
func legacyStickyModeToken(spec RouteSpec) string {
	switch {
	case spec.StickyMode == "":
		return ""
	case spec.StickyMode == StickyModeSourceTable && (spec.BalanceAlgorithm == "" || spec.BalanceAlgorithm == BalanceAlgorithmLeastConn):
		return StickyModeSourceTable
	case spec.StickyMode == StickyModeSource:
		return StickyModeSource
	case spec.StickyMode == StickyModeNone && spec.BalanceAlgorithm == BalanceAlgorithmLeastConn:
		return StickyModeLeastConn
	case spec.StickyMode == StickyModeNone && (spec.BalanceAlgorithm == "" || spec.BalanceAlgorithm == BalanceAlgorithmRoundRobin):
		return StickyModeRoundRobin
	default:
		// A legacy leftover value (leastconn/roundrobin/sni stored directly,
		// single-target routes) or a shape 000052 cannot express as a legacy
		// token (e.g. sticky none with a static-rr/random/leastping
		// algorithm): hash the raw stored value.
		return spec.StickyMode
	}
}

// fingerprintStickyMode hashes the legacy sticky_mode token only when it
// differs from the mode implied by sticky_enabled and DNS pools (the
// pre-000051 derivation).
func fingerprintStickyMode(spec RouteSpec) string {
	legacy := legacyStickyModeToken(spec)
	if legacy == "" {
		return ""
	}
	implied := StickyModeRoundRobin
	if spec.StickyEnabled || spec.DNSPool || anyServerDNSPool(spec.Servers) {
		implied = StickyModeSource
	}
	if legacy == implied {
		return ""
	}
	return legacy
}

// fingerprintBalanceAlgorithm hashes balance_algorithm only when it is not
// already expressed by the legacy sticky_mode token: source ignores the
// algorithm entirely (inert while rendering); source_table and none/leastconn
// already fold ” and roundrobin/leastconn into legacyStickyModeToken.
func fingerprintBalanceAlgorithm(spec RouteSpec) string {
	switch spec.StickyMode {
	case StickyModeSource:
		return ""
	case StickyModeSourceTable:
		if spec.BalanceAlgorithm == "" || spec.BalanceAlgorithm == BalanceAlgorithmLeastConn {
			return ""
		}
		return spec.BalanceAlgorithm
	default:
		if spec.BalanceAlgorithm == "" || spec.BalanceAlgorithm == BalanceAlgorithmRoundRobin || spec.BalanceAlgorithm == BalanceAlgorithmLeastConn {
			return ""
		}
		return spec.BalanceAlgorithm
	}
}

// routeServersToSpec converts []RouteServer to []RouteServerSpec for use in fingerprinting.
func routeServersToSpec(servers []RouteServer) []RouteServerSpec {
	if len(servers) == 0 {
		return nil
	}
	out := make([]RouteServerSpec, len(servers))
	for i, s := range servers {
		out[i] = RouteServerSpec{
			Position: s.Position, Name: s.Name, TargetType: s.TargetType,
			Host: s.Host, Port: s.Port, UnixSocketPath: s.UnixSocketPath,
			Backup: s.Backup, DNSPool: s.DNSPool, PreferredIP: s.PreferredIP,
			Weight: s.Weight, Cost: s.Cost, IPWeights: routeServerIPWeightsCopy(s.IPWeights),
		}
	}
	return out
}

func routeServerIPWeightsCopy(weights []RouteServerIPWeight) []RouteServerIPWeight {
	if len(weights) == 0 {
		return nil
	}
	return append([]RouteServerIPWeight(nil), weights...)
}

// normalizeRouteSpecDefaults keeps internal/legacy callers compatible while
// preserving an explicit health_check=false from the v6 API. HTTP validation
// always sets MatchMode, so an empty mode identifies a pre-v6 caller.
func normalizeRouteSpecDefaults(spec *RouteSpec) {
	legacy := spec.MatchMode == ""
	// Normalize legacy match_mode values to canonical "fallback".
	if spec.MatchMode == "any_tcp" || spec.MatchMode == "destination_ip" {
		spec.MatchMode = "fallback"
	}
	if legacy {
		if spec.Fallback {
			spec.MatchMode = "fallback"
		} else {
			spec.MatchMode = "sni"
		}
		spec.HealthCheck = true
	}
	if spec.BalanceMode == "" {
		// Internal callers that predate balance_mode (000031) build specs
		// without it; the column only accepts pool or failover.
		spec.BalanceMode = "pool"
		for _, server := range spec.Servers {
			if server.Backup {
				spec.BalanceMode = "failover"
				break
			}
		}
	}
	if spec.Name == "" {
		switch spec.MatchMode {
		case "sni":
			spec.Name = spec.Hostname
		case "fallback":
			if wildcardListenerIP(spec.ListenerIP) {
				spec.Name = "tcp-" + fmt.Sprint(spec.ListenerPort)
			} else {
				spec.Name = "ip-" + spec.ListenerIP + "-" + fmt.Sprint(spec.ListenerPort)
			}
		default:
			spec.Name = "tcp-" + fmt.Sprint(spec.ListenerPort)
		}
	}
}
