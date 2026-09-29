package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	HAProxyRendererVersion  = "haproxy-tcp-sni-v21"
	olderV20HAProxyRenderer = "haproxy-tcp-sni-v20"
	previousHAProxyRenderer = "haproxy-tcp-sni-v19"
	olderV18HAProxyRenderer = "haproxy-tcp-sni-v18"
	olderV17HAProxyRenderer = "haproxy-tcp-sni-v17"
	olderV16HAProxyRenderer = "haproxy-tcp-sni-v16"
	olderV15HAProxyRenderer = "haproxy-tcp-sni-v15"
	olderV14HAProxyRenderer = "haproxy-tcp-sni-v14"
	olderV13HAProxyRenderer = "haproxy-tcp-sni-v13"
	olderV12HAProxyRenderer = "haproxy-tcp-sni-v12"
	olderV11HAProxyRenderer = "haproxy-tcp-sni-v11"
	olderV10HAProxyRenderer = "haproxy-tcp-sni-v10"
	olderV9HAProxyRenderer  = "haproxy-tcp-sni-v9"
	olderV8HAProxyRenderer  = "haproxy-tcp-sni-v8"
	olderV7HAProxyRenderer  = "haproxy-tcp-sni-v7"
	legacyHAProxyRenderer   = "haproxy-tcp-sni-v6"
	olderV5HAProxyRenderer  = "haproxy-tcp-sni-v5"
	olderHAProxyRenderer    = "haproxy-tcp-sni-v4"
	olderV3HAProxyRenderer  = "haproxy-tcp-sni-v3"
	olderV2HAProxyRenderer  = "haproxy-tcp-sni-v2"
	initialHAProxyRenderer  = "haproxy-tcp-sni-v1"
	MaxRenderedRoutes       = 1024
	DNSPoolTemplateSlots    = 32
)

var ErrNoEnabledRoutes = errors.New("no enabled routes")

func supportedHAProxyRenderers() []string {
	return []string{HAProxyRendererVersion, olderV20HAProxyRenderer, previousHAProxyRenderer, olderV18HAProxyRenderer, olderV17HAProxyRenderer, olderV16HAProxyRenderer, olderV15HAProxyRenderer, olderV14HAProxyRenderer, olderV13HAProxyRenderer, olderV12HAProxyRenderer, olderV11HAProxyRenderer, olderV10HAProxyRenderer, olderV9HAProxyRenderer, olderV8HAProxyRenderer, olderV7HAProxyRenderer, legacyHAProxyRenderer, olderV5HAProxyRenderer, olderHAProxyRenderer, olderV3HAProxyRenderer, olderV2HAProxyRenderer, initialHAProxyRenderer}
}

// RouteSetError means persisted route intent is internally inconsistent or
// unsafe to render. The renderer validates stored data again instead of
// relying only on HTTP and database constraints.
type RouteSetError struct {
	Reason string
}

func (e *RouteSetError) Error() string { return "invalid route set: " + e.Reason }

type RouteRuntimeNames struct {
	RouteID  string `json:"route_id"`
	Frontend string `json:"frontend"`
	Backend  string `json:"backend"`
	Server   string `json:"server"`
	DNSPool  bool   `json:"dns_pool,omitempty"`
	// MultiServer marks a servers[] backend: Server names the legacy key but
	// no such HAProxy server exists, so runtime quota actions are skipped.
	MultiServer   bool       `json:"multi_server,omitempty"`
	QuotaBytes    *int64     `json:"quota_bytes,omitempty"`
	QuotaAction   string     `json:"quota_action"`
	QuotaPeriod   string     `json:"quota_period"`
	QuotaAnchorAt *time.Time `json:"quota_anchor_at,omitempty"`
}

type HAProxyRenderResult struct {
	Config              string              `json:"config"`
	SHA256              string              `json:"sha256"`
	Renderer            string              `json:"renderer"`
	EnabledRoutes       int                 `json:"enabled_routes"`
	Listeners           int                 `json:"listeners"`
	QuotaMetadataRoutes int                 `json:"quota_metadata_routes"`
	QuotaRuntimeRoutes  int                 `json:"quota_runtime_routes"`
	ManualBackendRoutes int                 `json:"manual_backend_routes"`
	Warnings            []string            `json:"warnings"`
	ListenerPorts       []int               `json:"listener_ports"`
	RuntimeNames        []RouteRuntimeNames `json:"runtime_names"`
	RouteBackends       map[string]string   `json:"route_backends"`
	RouteFingerprints   map[string]string   `json:"route_fingerprints"`
	// PipeSize is the rendered tune.pipesize; AutoStickyTableEntries the size
	// of every «Авто» source_table stick-table ('' = none rendered). Both are
	// stored in revision metadata so a heartbeat can tell whether the node
	// facts behind the published revision changed.
	PipeSize               int    `json:"pipe_size"`
	AutoStickyTableEntries string `json:"auto_sticky_table_entries,omitempty"`
	// HAProxyLogs is the node's «Логи соединений HAProxy» setting as
	// rendered (true = syslog connection logging, the default).
	HAProxyLogs bool `json:"haproxy_logs"`
}

type renderListener struct {
	IP     string
	Port   int
	Routes []renderRoute
}

type renderServerSpec struct {
	Name           string
	TargetType     string
	Host           string
	Port           int
	UnixSocketPath string
	Backup         bool
	DNSPool        bool
	PreferredIP    string
	Weight         int
	Cost           float64
	IPWeights      []RouteServerIPWeight
}

type renderRoute struct {
	ID                      string
	Name                    string
	MatchMode               string
	SNIs                    []string
	Fallback                bool
	TargetType              string
	TargetHost              string
	TargetPort              int
	DNSPool                 bool
	UnixSocketPath          string
	HealthCheck             bool
	ProxyProtocol           string
	AcceptProxyFrom         []string
	Servers                 []renderServerSpec
	StickyEnabled           bool
	StickyMode              string
	StickyTTL               string
	BalanceMode             string
	BalanceAlgorithm        string
	BalanceRandomDraws      int
	LeastPingTolerance      float64
	LeastPingToleranceMS    int
	StickyHash              string
	StickyHashBalanceFactor int
	StickyTableEntries      string
	// AutoStickyTableEntries is the adaptive size used when
	// StickyTableEntries is '' («Авто»).
	AutoStickyTableEntries string
	StickyIPv6Prefix       int
	ClientIPv4Only         bool
	Slowstart              string
	QuotaBytes             *int64
	QuotaAction            string
	QuotaPeriod            string
	QuotaAnchorAt          *time.Time
	ClientDownloadMbps     *int64
	ClientUploadMbps       *int64
	ShaperMode             string
	CustomFragment         string
	CustomBytes            int
	Frontend               string
	Backend                string
	Server                 string
	// Spec is the canonical route intent used for the ledger fingerprint; it
	// must match what Store hashes at write time (including servers).
	Spec RouteSpec
}

// RenderHAProxyConfig deterministically turns validated route intent into a
// complete HAProxy TCP configuration. Disabled routes do not affect the data
// plane. custom_fragment is constrained to directives inside its own backend.
func RenderHAProxyConfig(routes []Route) (HAProxyRenderResult, error) {
	return renderHAProxyConfig(routes, NodeRenderFacts{}, false)
}

// RenderHAProxyConfigForNode renders with the node facts from the latest
// heartbeat (splice pipe size, adaptive stick-table size).
func RenderHAProxyConfigForNode(routes []Route, facts NodeRenderFacts) (HAProxyRenderResult, error) {
	return renderHAProxyConfig(routes, facts, false)
}

// renderHAProxyConfigForLifecycle permits a route-free configuration. HAProxy
// still receives a complete global/defaults configuration, which is required
// when the last active route is disabled or deleted. Operator preview keeps
// rejecting an empty route set so an accidental empty preview remains obvious.
func renderHAProxyConfigForLifecycle(routes []Route, facts NodeRenderFacts) (HAProxyRenderResult, error) {
	return renderHAProxyConfig(routes, facts, true)
}

func renderHAProxyConfig(routes []Route, facts NodeRenderFacts, allowEmpty bool) (HAProxyRenderResult, error) {
	if err := facts.HAProxySettings.validate(); err != nil {
		return HAProxyRenderResult{}, err
	}
	result := HAProxyRenderResult{
		Renderer:          HAProxyRendererVersion,
		PipeSize:          facts.PipeSize(),
		Warnings:          []string{},
		RuntimeNames:      []RouteRuntimeNames{},
		RouteBackends:     map[string]string{},
		RouteFingerprints: map[string]string{},
	}

	groups := make(map[string]*renderListener)
	seenIDs := make(map[string]struct{})
	seenRuntimeNames := make(map[string]string)
	// «Авто» stick-tables share 5 % of node RAM; every one gets the same size.
	autoTables := 0
	for _, route := range routes {
		if route.Enabled && routeUsesAutoStickyTable(route) {
			autoTables++
		}
	}
	autoTableSize := ""
	if autoTables > 0 {
		autoTableSize = facts.AutoStickyTableEntries(autoTables)
		result.AutoStickyTableEntries = autoTableSize
	}
	// v21 differs from v20 only by these node facts (and the per-node
	// connection-log switch); a node without them renders byte-identical v20
	// output, header included.
	result.HAProxyLogs = !facts.HAProxyLogsDisabled
	if result.PipeSize == defaultPipeSize && autoTables == 0 && result.HAProxyLogs {
		result.Renderer = olderV20HAProxyRenderer
	}
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		if len(seenIDs) >= MaxRenderedRoutes {
			return HAProxyRenderResult{}, &RouteSetError{Reason: fmt.Sprintf("enabled route count exceeds %d", MaxRenderedRoutes)}
		}
		if !validID(route.ID) {
			return HAProxyRenderResult{}, &RouteSetError{Reason: "enabled route has an invalid id"}
		}
		if _, exists := seenIDs[route.ID]; exists {
			return HAProxyRenderResult{}, &RouteSetError{Reason: "duplicate route id " + route.ID}
		}
		seenIDs[route.ID] = struct{}{}

		enabled := true
		healthCheck := route.HealthCheck
		if route.MatchMode == "" {
			// Unit/legacy callers that predate persisted match_mode also predate
			// health_check; old renderers always emitted active TCP checks.
			healthCheck = true
		}
		listenerPort := route.ListenerPort
		spec, err := validateRoute(routeInput{
			Name:            route.Name,
			Hostname:        route.Hostname,
			ListenerIP:      route.ListenerIP,
			ListenerPort:    &listenerPort,
			MatchMode:       route.MatchMode,
			SNIs:            append([]string(nil), route.SNIs...),
			Fallback:        route.Fallback,
			TargetType:      route.TargetType,
			TargetHost:      route.TargetHost,
			TargetPort:      route.TargetPort,
			DNSPool:         route.DNSPool,
			UnixSocketPath:  route.UnixSocketPath,
			HealthCheck:     &healthCheck,
			ProxyProtocol:   route.ProxyProtocol,
			AcceptProxyFrom: append([]string(nil), route.AcceptProxyFrom...),
			QuotaBytes:      route.QuotaBytes,
			QuotaAction:     route.QuotaAction,
			QuotaPeriod:     route.QuotaPeriod,
			Enabled:         &enabled,
			CustomFragment:  route.CustomFragment,
		}, true)
		if err != nil {
			return HAProxyRenderResult{}, &RouteSetError{Reason: "route " + route.ID + " failed validation: " + err.Error()}
		}
		// Route.Servers come pre-loaded from DB (already validated); re-use them directly.
		spec.Servers = routeServersToSpec(route.Servers)
		// Position order is priority order; failover primary/backup and
		// preferred_ip placement are defined on the sorted list.
		sort.SliceStable(spec.Servers, func(i, j int) bool { return spec.Servers[i].Position < spec.Servers[j].Position })
		if anyServerDNSPool(spec.Servers) {
			// Same rule as validateRoute: DNS-pool templates always carry
			// health checks, so the backend must enable tcp-check too.
			spec.HealthCheck = true
		}
		// F4: sticky session fields are pre-validated at write time; copy through.
		spec.StickyEnabled = route.StickyEnabled
		spec.StickyMode = route.StickyMode
		spec.StickyTTL = route.StickyTTL
		spec.BalanceMode = route.BalanceMode
		spec.BalanceAlgorithm = route.BalanceAlgorithm
		spec.BalanceRandomDraws = route.BalanceRandomDraws
		spec.LeastPingTolerance = route.LeastPingTolerance
		spec.LeastPingToleranceMS = route.LeastPingToleranceMS
		spec.StickyHash = route.StickyHash
		spec.StickyHashBalanceFactor = route.StickyHashBalanceFactor
		spec.StickyTableEntries = route.StickyTableEntries
		spec.StickyIPv6Prefix = route.StickyIPv6Prefix
		spec.ClientIPv4Only = routeClientIPv4Only(route)
		spec.Slowstart = route.Slowstart
		if _, err := bandwidthLimitBytes(route.ClientDownloadMbps); err != nil {
			return HAProxyRenderResult{}, &RouteSetError{Reason: "route " + route.ID + " has invalid client_download_mbps: " + err.Error()}
		}
		if _, err := bandwidthLimitBytes(route.ClientUploadMbps); err != nil {
			return HAProxyRenderResult{}, &RouteSetError{Reason: "route " + route.ID + " has invalid client_upload_mbps: " + err.Error()}
		}

		ledgerSpec := spec
		ledgerSpec.ExpectedVersion = nil
		ledgerSpec.Enabled = true
		ledgerSpec.ClientUploadMbps = route.ClientUploadMbps
		ledgerSpec.ClientDownloadMbps = route.ClientDownloadMbps
		ledgerSpec.ShaperMode = route.ShaperMode

		key := listenerKey(spec.ListenerIP, spec.ListenerPort)
		group := groups[key]
		if group == nil {
			group = &renderListener{IP: spec.ListenerIP, Port: spec.ListenerPort}
			groups[key] = group
		}
		backend := RouteBackendKey(route.ID)
		if previousID, exists := seenRuntimeNames[backend]; exists && previousID != route.ID {
			return HAProxyRenderResult{}, &RouteSetError{Reason: "runtime name collision between routes " + previousID + " and " + route.ID}
		}
		seenRuntimeNames[backend] = route.ID
		group.Routes = append(group.Routes, renderRoute{
			ID:                      route.ID,
			Name:                    spec.Name,
			MatchMode:               spec.MatchMode,
			SNIs:                    spec.SNIs,
			Fallback:                spec.Fallback,
			TargetType:              spec.TargetType,
			TargetHost:              spec.TargetHost,
			TargetPort:              spec.TargetPort,
			DNSPool:                 spec.DNSPool,
			UnixSocketPath:          spec.UnixSocketPath,
			HealthCheck:             spec.HealthCheck,
			ProxyProtocol:           spec.ProxyProtocol,
			AcceptProxyFrom:         spec.AcceptProxyFrom,
			Servers:                 routeServersForRender(spec.Servers),
			StickyEnabled:           spec.StickyEnabled,
			StickyMode:              spec.StickyMode,
			StickyTTL:               spec.StickyTTL,
			BalanceMode:             spec.BalanceMode,
			BalanceAlgorithm:        spec.BalanceAlgorithm,
			BalanceRandomDraws:      spec.BalanceRandomDraws,
			LeastPingTolerance:      spec.LeastPingTolerance,
			LeastPingToleranceMS:    spec.LeastPingToleranceMS,
			StickyHash:              spec.StickyHash,
			StickyHashBalanceFactor: spec.StickyHashBalanceFactor,
			StickyTableEntries:      spec.StickyTableEntries,
			AutoStickyTableEntries:  autoTableSize,
			StickyIPv6Prefix:        spec.StickyIPv6Prefix,
			ClientIPv4Only:          spec.ClientIPv4Only,
			Slowstart:               spec.Slowstart,
			QuotaBytes:              spec.QuotaBytes,
			QuotaAction:             spec.QuotaAction,
			QuotaPeriod:             spec.QuotaPeriod,
			QuotaAnchorAt:           nonZeroTimePointer(route.CreatedAt),
			ClientDownloadMbps:      route.ClientDownloadMbps,
			ClientUploadMbps:        route.ClientUploadMbps,
			ShaperMode:              route.ShaperMode,
			CustomFragment:          spec.CustomFragment,
			CustomBytes:             len(spec.CustomFragment),
			Frontend:                frontendRuntimeName(spec.ListenerIP, spec.ListenerPort),
			Backend:                 backend,
			Server:                  RouteServerKey(route.ID),
			Spec:                    ledgerSpec,
		})
	}

	if len(groups) == 0 && !allowEmpty {
		return HAProxyRenderResult{}, ErrNoEnabledRoutes
	}
	if err := validateListenerBinds(groups); err != nil {
		return HAProxyRenderResult{}, err
	}

	listeners := make([]renderListener, 0, len(groups))
	for _, group := range groups {
		sort.Slice(group.Routes, func(i, j int) bool { return group.Routes[i].ID < group.Routes[j].ID })
		if err := validateListenerRoutes(*group); err != nil {
			return HAProxyRenderResult{}, err
		}
		if err := validateKernelShaperListener(*group); err != nil {
			return HAProxyRenderResult{}, err
		}
		listeners = append(listeners, *group)
	}
	sort.Slice(listeners, func(i, j int) bool {
		if listeners[i].IP != listeners[j].IP {
			return listeners[i].IP < listeners[j].IP
		}
		return listeners[i].Port < listeners[j].Port
	})
	hasDynamicDNS := false
	needsPeers := false
	for _, listener := range listeners {
		for _, route := range listener.Routes {
			if route.TargetType == "tcp" && net.ParseIP(route.TargetHost) == nil {
				hasDynamicDNS = true
			}
			// Multi-server backends reference nf_dns from any hostname server
			// (static or dns-pool template), not only from the legacy target.
			for _, srv := range route.Servers {
				if srv.TargetType == "tcp" && net.ParseIP(srv.Host) == nil {
					hasDynamicDNS = true
				}
			}
			if routeNeedsSourceTablePeers(route, legacyDNSPoolRoute(route)) {
				needsPeers = true
			}
		}
	}

	var b strings.Builder
	b.WriteString("# Generated by NodeFlow. Do not edit.\n")
	b.WriteString("# renderer: " + result.Renderer + "\n\n")
	b.WriteString("global\n")
	if facts.HAProxySettings.MaxConnections > 0 {
		fmt.Fprintf(&b, "    maxconn %d\n", facts.HAProxySettings.MaxConnections)
	}
	if facts.HAProxySettings.Threads > 0 {
		fmt.Fprintf(&b, "    nbthread %d\n", facts.HAProxySettings.Threads)
	}
	// Connection logging (rsyslog -> /var/log/haproxy.log on Debian/Ubuntu)
	// is on by default. With the node setting off no log target exists at
	// all; startup alerts still reach stderr, i.e. the systemd journal.
	if result.HAProxyLogs {
		b.WriteString("    log /dev/log local0\n")
		b.WriteString("    log /dev/log local1 notice\n")
	}
	b.WriteString("    stats socket /run/haproxy/admin.sock mode 660 level admin expose-fd listeners\n")
	b.WriteString("    stats timeout 30s\n")
	b.WriteString("    user haproxy\n")
	b.WriteString("    group haproxy\n")
	// Splice pipes default to 64 KiB (16 pages), which caps each splice() at
	// 64 KiB and costs ~4x more syscalls per byte. Measured on HAProxy 3.4.2,
	// 4 TCP streams on loopback: 102 % CPU for 11.9 Gbit/s at 64 KiB vs
	// 28.5 % CPU for 14.4 Gbit/s at 256 KiB (8.6 -> 2.0 %CPU per Gbit/s).
	// 256 KiB keeps 256 pipes inside the default fs.pipe-user-pages-soft
	// (16384 pages per user); above that the kernel silently shrinks pipes to
	// one page, so the value stays conservative. 1 MiB pipes are rendered
	// only when the node's Agent reports fs.pipe-user-pages-soft=0 and
	// fs.pipe-max-size >= 1 MiB (NodeRenderFacts.KernelPipesTuned).
	b.WriteString("    tune.pipesize " + strconv.Itoa(result.PipeSize) + "\n")
	if needsPeers {
		b.WriteString("    localpeer nf_local\n")
	}
	b.WriteByte('\n')
	b.WriteString("defaults\n")
	if result.HAProxyLogs {
		b.WriteString("    log global\n")
	}
	b.WriteString("    mode tcp\n")
	if result.HAProxyLogs {
		b.WriteString("    option tcplog\n")
		b.WriteString("    option dontlognull\n")
	}
	b.WriteString("    option clitcpka\n")
	b.WriteString("    option srvtcpka\n")
	b.WriteString("    clitcpka-idle 300s\n")
	b.WriteString("    clitcpka-intvl 30s\n")
	b.WriteString("    clitcpka-cnt 3\n")
	b.WriteString("    srvtcpka-idle 300s\n")
	b.WriteString("    srvtcpka-intvl 30s\n")
	b.WriteString("    srvtcpka-cnt 3\n")
	b.WriteString("    .if enabled(SPLICE)\n")
	b.WriteString("        option splice-request\n")
	b.WriteString("        option splice-response\n")
	b.WriteString("    .endif\n")
	fmt.Fprintf(&b, "    timeout connect %s\n", timeoutOr(facts.HAProxySettings.TimeoutConnect, "5s"))
	fmt.Fprintf(&b, "    timeout client %s\n", timeoutOr(facts.HAProxySettings.TimeoutClient, "15m"))
	fmt.Fprintf(&b, "    timeout server %s\n", timeoutOr(facts.HAProxySettings.TimeoutServer, "15m"))
	if hasDynamicDNS {
		b.WriteString("\nresolvers nf_dns\n")
		b.WriteString("    nameserver adguard_local 127.0.0.1:53\n")
		b.WriteString("    nameserver systemd_resolved 127.0.0.53:53\n")
		b.WriteString("    nameserver cloudflare 1.1.1.1:53\n")
		b.WriteString("    nameserver google 8.8.8.8:53\n")
		b.WriteString("    nameserver quad9 9.9.9.9:53\n")
		b.WriteString("    resolve_retries 3\n")
		b.WriteString("    timeout resolve 1s\n")
		b.WriteString("    timeout retry 1s\n")
		b.WriteString("    hold valid 10s\n")
		b.WriteString("    hold obsolete 30s\n")
		b.WriteString("    hold nx 30s\n")
		b.WriteString("    hold timeout 30s\n")
		b.WriteString("    hold refused 30s\n")
	}
	if needsPeers {
		b.WriteString("\npeers nf_peers\n")
		b.WriteString("    bind unix@/run/haproxy/nf_peers.sock\n")
		b.WriteString("    server nf_local\n")
	}

	for _, listener := range listeners {
		result.ListenerPorts = append(result.ListenerPorts, listener.Port)
		b.WriteString("\nfrontend " + frontendRuntimeName(listener.IP, listener.Port) + "\n")
		b.WriteString("    bind " + renderBind(listener.IP, listener.Port) + "\n")
		b.WriteString("    mode tcp\n")
		if result.HAProxyLogs {
			b.WriteString("    option tcplog\n")
		}
		// F1: conditional expect-proxy — union of accept_proxy_from across all
		// enabled routes on this listener. Direct clients (no PROXY header) keep
		// working; only packets from listed sources are parsed as PROXY protocol.
		acceptProxyUnion := listenerAcceptProxyUnion(listener.Routes)
		acceptProxyStatic, acceptProxyDomains := splitAcceptProxyEntries(acceptProxyUnion)
		if acceptProxyCoversListener(listener.IP, acceptProxyUnion) {
			// "Accept from everyone" sentinel (0.0.0.0/0 and/or ::/0 covering
			// every address family this bind can receive): every connection
			// must carry a PROXY header, so no src ACL is needed.
			b.WriteString("    # accept PROXY protocol header from all sources\n")
			b.WriteString("    tcp-request connection expect-proxy layer4\n")
		} else if len(acceptProxyDomains) > 0 {
			// Hostnames stand for every A/AAAA address of the name. The Node
			// Agent (>= 1.1.3) resolves them into the ACL file before
			// validation/reload and keeps the loaded ACL current through the
			// runtime API (prepare/add/commit acl) without a reload.
			file := ppTrustedACLFile(frontendRuntimeName(listener.IP, listener.Port))
			b.WriteString("    # accept PROXY protocol header from trusted sources only\n")
			b.WriteString("    # nf-pp-trusted file=" + file + " domains=" + strings.Join(acceptProxyDomains, ",") + "\n")
			b.WriteString("    acl nf_pp_trusted src -f " + file + "\n")
			condition := "nf_pp_trusted"
			if len(acceptProxyStatic) > 0 {
				condition = "{ src " + strings.Join(acceptProxyStatic, " ") + " } || nf_pp_trusted"
			}
			b.WriteString("    tcp-request connection expect-proxy layer4 if " + condition + "\n")
		} else if len(acceptProxyUnion) > 0 {
			b.WriteString("    # accept PROXY protocol header from trusted sources only\n")
			b.WriteString("    tcp-request connection expect-proxy layer4 if { src " + strings.Join(acceptProxyUnion, " ") + " }\n")
		}
		hasBandwidthLimit := false
		for _, route := range listener.Routes {
			b.WriteString(kernelShaperAnnotation(listener, route))
			if routeUsesHAProxyBandwidth(route) {
				hasBandwidthLimit = true
			}
		}
		if hasBandwidthLimit {
			for _, route := range listener.Routes {
				if !routeUsesHAProxyBandwidth(route) {
					continue
				}
				if route.ClientDownloadMbps != nil {
					limit, _ := bandwidthLimitBytes(route.ClientDownloadMbps)
					b.WriteString("    filter bwlim-out " + routeBandwidthFilterName(route.ID, "download") + " limit " + strconv.FormatInt(limit, 10) + " key " + bandwidthLimitKey(route) + " table " + routeBandwidthTableName(route.ID, "download") + " min-size 2896\n")
				}
				if route.ClientUploadMbps != nil {
					limit, _ := bandwidthLimitBytes(route.ClientUploadMbps)
					b.WriteString("    filter bwlim-in " + routeBandwidthFilterName(route.ID, "upload") + " limit " + strconv.FormatInt(limit, 10) + " key " + bandwidthLimitKey(route) + " table " + routeBandwidthTableName(route.ID, "upload") + " min-size 2896\n")
				}
			}
		}
		hasSNIRoute := false
		for _, route := range listener.Routes {
			if route.MatchMode == "sni" {
				hasSNIRoute = true
				break
			}
		}
		// A bandwidth limit selected by SNI needs its ACL declared before the
		// set-bandwidth-limit rule that references it, and the rule must run
		// before "tcp-request content accept": accept stops content-rule
		// evaluation, so a later set-bandwidth-limit is never executed.
		// Listeners without bandwidth limits keep the historical layout.
		limitBeforeAccept := hasSNIRoute && hasBandwidthLimit
		if limitBeforeAccept {
			for _, route := range listener.Routes {
				if route.MatchMode == "sni" {
					writeSNIACL(&b, route)
				}
			}
		}
		if hasSNIRoute {
			b.WriteString("    tcp-request inspect-delay 5s\n")
			if !limitBeforeAccept {
				b.WriteString("    tcp-request content accept if { req.ssl_hello_type 1 }\n")
			}
		}
		if hasBandwidthLimit {
			for _, route := range listener.Routes {
				if !routeUsesHAProxyBandwidth(route) {
					continue
				}
				condition := bandwidthLimitCondition(listener.Routes, route)
				if route.ClientDownloadMbps != nil {
					b.WriteString("    tcp-request content set-bandwidth-limit " + routeBandwidthFilterName(route.ID, "download") + condition + "\n")
				}
				if route.ClientUploadMbps != nil {
					b.WriteString("    tcp-request content set-bandwidth-limit " + routeBandwidthFilterName(route.ID, "upload") + condition + "\n")
				}
			}
		}
		if limitBeforeAccept {
			b.WriteString("    tcp-request content accept if { req.ssl_hello_type 1 }\n")
		}
		var fallback *renderRoute
		for i := range listener.Routes {
			route := &listener.Routes[i]
			if route.MatchMode != "sni" {
				fallback = route
				continue
			}
			if !limitBeforeAccept {
				writeSNIACL(&b, *route)
			}
			b.WriteString("    use_backend " + route.Backend + " if " + routeACLName(route.ID) + "\n")
		}
		if fallback != nil {
			b.WriteString("    default_backend " + fallback.Backend + "\n")
		}
	}

	for _, listener := range listeners {
		for _, route := range listener.Routes {
			if !routeUsesHAProxyBandwidth(route) {
				continue
			}
			if route.ClientDownloadMbps != nil {
				b.WriteString("\nbackend " + routeBandwidthTableName(route.ID, "download") + "\n")
				b.WriteString("    stick-table type " + clientTableType(route) + " size 1m expire 1h store bytes_out_rate(1s)\n")
			}
			if route.ClientUploadMbps != nil {
				b.WriteString("\nbackend " + routeBandwidthTableName(route.ID, "upload") + "\n")
				b.WriteString("    stick-table type " + clientTableType(route) + " size 1m expire 1h store bytes_in_rate(1s)\n")
			}
		}
	}

	for _, listener := range listeners {
		for _, route := range listener.Routes {
			b.WriteString("\nbackend " + route.Backend + "\n")
			b.WriteString("    mode tcp\n")
			legacyPool := legacyDNSPoolRoute(route)
			writeBalance(&b, route, legacyPool)
			writeWeightAnnotations(&b, route, legacyPool)
			if route.HealthCheck {
				b.WriteString("    option tcp-check\n")
			}
			b.WriteString("    # nodeflow route_id=" + route.ID + "\n")
			if route.QuotaBytes != nil {
				enforcement := "metadata-only"
				if route.QuotaAction == "block_new" {
					enforcement = "runtime-block-new"
					result.QuotaRuntimeRoutes++
				} else {
					result.QuotaMetadataRoutes++
				}
				b.WriteString("    # quota=" + formatIECBytes(*route.QuotaBytes) + " enforcement=" + enforcement + "\n")
			}
			if route.CustomBytes > 0 {
				b.WriteString("    # manual_backend_directives_bytes=" + strconv.Itoa(route.CustomBytes) + "\n")
				b.WriteString(route.CustomFragment)
				b.WriteByte('\n')
				result.ManualBackendRoutes++
			}
			if legacyPool {
				// Legacy route-level DNS pool (no servers): single server-template.
				b.WriteString("    server-template " + routeServerTemplatePrefix(route.ID) + " " + strconv.Itoa(DNSPoolTemplateSlots) + " " + renderTarget(route))
				if route.Slowstart != "" {
					b.WriteString(" slowstart " + route.Slowstart)
				}
				b.WriteString(" check inter 5s fall 3 rise 2 resolvers nf_dns init-addr last,none resolve-opts prevent-dup-ip hash-key addr")
			} else if len(route.Servers) > 0 {
				writeMultiServerBackend(&b, route)
				// Multi-server backends have no single nf_srv_<id> runtime server,
				// so runtime quota enforcement must skip them.
				result.RuntimeNames = append(result.RuntimeNames, RouteRuntimeNames{
					RouteID: route.ID, Frontend: route.Frontend, Backend: route.Backend, Server: runtimeServerName(route), MultiServer: true,
					QuotaBytes: route.QuotaBytes, QuotaAction: route.QuotaAction,
					QuotaPeriod: route.QuotaPeriod, QuotaAnchorAt: route.QuotaAnchorAt,
				})
				result.RouteBackends[route.ID] = route.Backend
				result.RouteFingerprints[route.ID] = routeSpecFingerprint(route.Spec)
				result.EnabledRoutes++
				continue
			} else {
				b.WriteString("    server " + route.Server + " " + renderTarget(route))
				if route.Slowstart != "" {
					b.WriteString(" slowstart " + route.Slowstart)
				}
				if route.HealthCheck {
					b.WriteString(" check inter 5s fall 3 rise 2")
				}
				if route.TargetType == "tcp" && net.ParseIP(route.TargetHost) == nil {
					b.WriteString(" resolvers nf_dns init-addr last,none")
				}
			}
			switch route.ProxyProtocol {
			case "v1":
				b.WriteString(" send-proxy")
			case "v2":
				b.WriteString(" send-proxy-v2")
			}
			// F2: health checks must also send PROXY header when the backend expects it.
			if route.HealthCheck && route.ProxyProtocol != "none" && route.ProxyProtocol != "" {
				b.WriteString(" check-send-proxy")
			}
			b.WriteByte('\n')

			result.RuntimeNames = append(result.RuntimeNames, RouteRuntimeNames{
				RouteID: route.ID, Frontend: route.Frontend, Backend: route.Backend, Server: runtimeServerName(route), DNSPool: route.DNSPool,
				QuotaBytes: route.QuotaBytes, QuotaAction: route.QuotaAction,
				QuotaPeriod: route.QuotaPeriod, QuotaAnchorAt: route.QuotaAnchorAt,
			})
			result.RouteBackends[route.ID] = route.Backend
			result.RouteFingerprints[route.ID] = routeSpecFingerprint(route.Spec)
			result.EnabledRoutes++
		}
	}
	result.Listeners = len(listeners)
	if result.QuotaMetadataRoutes > 0 {
		result.Warnings = append(result.Warnings, "observe-only traffic quotas do not block HAProxy connections")
	}
	if result.QuotaRuntimeRoutes > 0 {
		result.Warnings = append(result.Warnings, "quota enforcement blocks new backend connections through the HAProxy Runtime API")
	}
	if result.ManualBackendRoutes > 0 {
		result.Warnings = append(result.Warnings, "manual backend directives are rendered and must pass HAProxy validation before apply")
	}
	result.Config = b.String()
	if len(result.Config) > MaxManagedConfigBytes {
		return HAProxyRenderResult{}, &RouteSetError{Reason: "rendered configuration exceeds size limit"}
	}
	sum := sha256.Sum256([]byte(result.Config))
	result.SHA256 = hex.EncodeToString(sum[:])
	return result, nil
}

func nonZeroTimePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}

func validateListenerBinds(groups map[string]*renderListener) error {
	listeners := make([]*renderListener, 0, len(groups))
	for _, listener := range groups {
		listeners = append(listeners, listener)
	}
	for i := 0; i < len(listeners); i++ {
		for j := i + 1; j < len(listeners); j++ {
			left, right := listeners[i], listeners[j]
			if left.Port != right.Port || left.IP == right.IP {
				continue
			}
			if wildcardListenerIP(left.IP) || wildcardListenerIP(right.IP) {
				return &RouteSetError{Reason: fmt.Sprintf("listener binds %s and %s overlap", listenerKey(left.IP, left.Port), listenerKey(right.IP, right.Port))}
			}
		}
	}
	return nil
}

func wildcardListenerIP(ip string) bool {
	return ip == "*" || ip == "0.0.0.0" || ip == "::"
}

func validateListenerRoutes(listener renderListener) error {
	seenSNI := make(map[string]string)
	fallbackID := ""
	for _, route := range listener.Routes {
		if route.MatchMode != "sni" {
			if fallbackID != "" {
				return &RouteSetError{Reason: "listener " + listenerKey(listener.IP, listener.Port) + " has multiple fallback routes"}
			}
			fallbackID = route.ID
			continue
		}
		for _, sni := range route.SNIs {
			if previous, exists := seenSNI[sni]; exists {
				return &RouteSetError{Reason: "listener " + listenerKey(listener.IP, listener.Port) + " assigns SNI " + sni + " to routes " + previous + " and " + route.ID}
			}
			seenSNI[sni] = route.ID
		}
	}
	return nil
}

func listenerKey(ip string, port int) string {
	return ip + "|" + strconv.Itoa(port)
}

func frontendRuntimeName(ip string, port int) string {
	name := "any"
	if ip != "*" {
		name = sanitizeRuntimeName(ip)
	}
	sum := sha256.Sum256([]byte(listenerKey(ip, port)))
	return "nf_fe_" + name + "_" + strconv.Itoa(port) + "_" + hex.EncodeToString(sum[:4])
}

// sniACLChunk bounds SNI values per "acl" line. HAProxy 3.4 caps a config
// line at 64 words ("too many words, truncating after word 64"); the prefix
// "acl <name> req.ssl_sni -i" uses 4, and MaxRouteSNIs is 64, so a route
// needs at most two lines.
const sniACLChunk = 32

// writeSNIACL declares one ACL per route. Values sharing an "acl" line form a
// single pattern expression (one req.ssl_sni fetch per line), while every
// extra line with the same name adds another expression and another fetch.
// Measured on HAProxy 3.4.2 with 256 routes x 64 SNIs: ~1350 us/conn with one
// value per line vs ~340 us/conn merged; routing results identical.
// Order of values is kept as stored, so output stays deterministic.
func writeSNIACL(b *strings.Builder, route renderRoute) {
	acl := routeACLName(route.ID)
	for start := 0; start < len(route.SNIs); start += sniACLChunk {
		end := min(start+sniACLChunk, len(route.SNIs))
		b.WriteString("    acl " + acl + " req.ssl_sni -i " + strings.Join(route.SNIs[start:end], " ") + "\n")
	}
}

func routeACLName(id string) string {
	return "nf_sni_" + routeRuntimeID(id)
}

func routeServerTemplatePrefix(routeID string) string {
	return RouteServerKey(routeID) + "_"
}

// legacyDNSPoolRoute reports the route-level DNS pool shape. Before 1.1.0 a
// dns_pool route could carry at most one explicit server and was always
// rendered from the route target, so that shape keeps its byte-identical
// legacy render. With more servers, servers[] is the source of truth and
// route-level dns_pool is ignored (each server carries its own flag).
func legacyDNSPoolRoute(route renderRoute) bool {
	if !route.DNSPool {
		return false
	}
	return len(route.Servers) == 0 || (len(route.Servers) == 1 && route.Servers[0].PreferredIP == "")
}

// effectiveStickyMode returns the client distribution rendered for a route,
// or "" when no balance directives are emitted. Rows stored before migration
// 000051 carry an empty sticky_mode and keep their earlier render, except that
// a DNS pool is always source-hash sticky, as it was in 1.0.8. Failover keeps
// strict priority order; a legacy route-level DNS pool never is failover.
func effectiveStickyMode(route renderRoute, legacyPool bool) string {
	mode := route.StickyMode
	if mode == "" {
		switch {
		case legacyPool:
			return StickyModeSource
		case route.BalanceMode == "failover":
			return ""
		case route.StickyEnabled || route.DNSPool || anyRenderServerDNSPool(route.Servers):
			return StickyModeSource
		default:
			return ""
		}
	}
	if route.BalanceMode == "failover" && !legacyPool {
		return ""
	}
	switch mode {
	case StickyModeSource, StickyModeSourceTable, StickyModeLeastConn:
		// StickyModeLeastConn only ever appears on a single-target route
		// (the pre-000052 flat model): resolveStickyMode never stores it for
		// a servers[] pool.
		return mode
	default:
		// StickyModeNone (000052 servers[] pool: the algorithm decides,
		// handled by writeBalance's default branch), StickyModeRoundRobin
		// and StickyModeSNI (legacy single-target literals, sni's render
		// path removed by 000052): render nothing.
		return ""
	}
}

func anyRenderServerDNSPool(servers []renderServerSpec) bool {
	for _, s := range servers {
		if s.DNSPool {
			return true
		}
	}
	return false
}

// writeBalance emits the backend distribution directives:
//   - source: consistent hash (or masked hash) of the client address, no
//     state.
//   - source_table: a client stays on the server it first got (chosen by
//     balance_algorithm) until sticky_ttl of inactivity. The key is the IPv4
//     address or the IPv6 prefix; IPv4 keys are stored IPv4-mapped in the
//     ipv6 table. redispatch lets a client leave a dead server.
//   - leastconn: least connections, no stickiness (legacy single-target
//     sticky_mode literal).
//   - none (or unset/failover/legacy roundrobin/sni): balance_algorithm
//     decides — nothing for roundrobin/leastping, an explicit line for
//     static-rr/random/leastconn.
func writeBalance(b *strings.Builder, route renderRoute, legacyPool bool) {
	switch effectiveStickyMode(route, legacyPool) {
	case StickyModeSource:
		writeSourceHashBalance(b, route)
	case StickyModeSourceTable:
		writeSourceTableBalance(b, route)
	case StickyModeLeastConn:
		b.WriteString("    balance leastconn\n")
	default:
		writeAlgorithmBalance(b, renderedBalanceAlgorithm(route, legacyPool, route.BalanceAlgorithm), route.BalanceRandomDraws)
	}
}

// renderedBalanceAlgorithm is the algorithm written to the balance line.
// leastconn with a tolerance is Agent-managed (effectiveLeastConnAgent): the
// Agent reads the live per-server connection counts every pass and pins the
// runtime weights so that only the "equally loaded" servers take new clients,
// shared by weight/cost. HAProxy's leastconn cannot express that (it always
// picks the strictly lowest connections/weight), weighted round-robin
// follows the weights exactly, so the backend renders HAProxy's default
// roundrobin (no balance line) — the same as leastping.
func renderedBalanceAlgorithm(route renderRoute, legacyPool bool, algorithm string) string {
	if algorithm == BalanceAlgorithmLeastConn && effectiveLeastConnAgent(route, legacyPool) {
		return BalanceAlgorithmRoundRobin
	}
	return algorithm
}

// writeAlgorithmBalance emits the balance_algorithm line per table A of the
// spec: nothing for roundrobin/leastping/"" (HAProxy default / agent-managed
// runtime weights), an explicit line otherwise.
func writeAlgorithmBalance(b *strings.Builder, algorithm string, randomDraws int) {
	switch algorithm {
	case BalanceAlgorithmStaticRR:
		b.WriteString("    balance static-rr\n")
	case BalanceAlgorithmRandom:
		draws := randomDraws
		if draws == 0 {
			draws = defaultBalanceRandomDraws
		}
		b.WriteString("    balance random(" + strconv.Itoa(draws) + ")\n")
	case BalanceAlgorithmLeastConn:
		b.WriteString("    balance leastconn\n")
	}
}

// writeSourceHashBalance renders sticky_mode=source. A zero or full-width
// (128) IPv6 prefix is the existing byte-identical "balance source"; any
// narrower prefix hashes a masked client address instead. IPv4-only routes
// (client_ipv6=false) always hash the plain source address.
func writeSourceHashBalance(b *strings.Builder, route renderRoute) {
	prefix := route.StickyIPv6Prefix
	if route.ClientIPv4Only || prefix == 0 || prefix == maxStickyIPv6Prefix {
		b.WriteString("    balance source\n")
	} else {
		b.WriteString("    balance hash src,ipmask(32," + strconv.Itoa(prefix) + ")\n")
	}
	hashType := "consistent"
	if route.StickyHash == "map-based" {
		hashType = "map-based"
	}
	b.WriteString("    hash-type " + hashType + " sdbm avalanche\n")
	if route.StickyHashBalanceFactor != 0 {
		b.WriteString("    hash-balance-factor " + strconv.Itoa(route.StickyHashBalanceFactor) + "\n")
	}
}

// writeSourceTableBalance renders sticky_mode=source_table: the balance line
// picks the first server for a new client (empty/unset balance_algorithm is
// the rc12 leastconn default), then the stick-table remembers it.
func writeSourceTableBalance(b *strings.Builder, route renderRoute) {
	algorithm := route.BalanceAlgorithm
	if algorithm == "" {
		algorithm = BalanceAlgorithmLeastConn
	}
	writeAlgorithmBalance(b, renderedBalanceAlgorithm(route, false, algorithm), route.BalanceRandomDraws)
	// '' («Авто») is sized from node RAM (NodeRenderFacts); v20 and older
	// rendered a fixed 100k.
	size := route.AutoStickyTableEntries
	if size == "" {
		size = autoStickyTableUnknownSize
	}
	if validStickyTableEntries(route.StickyTableEntries) && route.StickyTableEntries != "" {
		size = route.StickyTableEntries
	}
	ttl := route.StickyTTL
	if ttl == "" {
		ttl = defaultStickyTTL
	}
	b.WriteString("    stick-table type " + clientTableType(route) + " size " + size + " expire " + ttl + " peers nf_peers\n")
	if route.ClientIPv4Only {
		b.WriteString("    stick on src\n")
	} else {
		prefix := route.StickyIPv6Prefix
		if prefix == 0 {
			prefix = defaultStickyIPv6Prefix
		}
		b.WriteString("    stick on src,ipmask(32," + strconv.Itoa(prefix) + ")\n")
	}
	b.WriteString("    option redispatch\n")
}

// routeClientIPv4Only reports whether a stored route disabled IPv6 client
// tables (client_ipv6=false). A nil ClientIPv6 is the default (true).
func routeClientIPv4Only(route Route) bool {
	return route.ClientIPv6 != nil && !*route.ClientIPv6
}

// clientTableType is the stick-table key type of the route's per-client
// tables: "ipv6" (default; IPv4 clients are stored as mapped addresses) or
// "ip" for IPv4-only routes.
func clientTableType(route renderRoute) string {
	if route.ClientIPv4Only {
		return "ip"
	}
	return "ipv6"
}

// bandwidthLimitKey is the bwlim filter key: one IPv4 address or one IPv6
// /64 per client by default, the plain IPv4 source for IPv4-only routes.
func bandwidthLimitKey(route renderRoute) string {
	if route.ClientIPv4Only {
		return "src"
	}
	return "src,ipmask(32,64)"
}

// routeNeedsSourceTablePeers reports whether the route's effective
// distribution needs the shared nf_peers stick-table section.
func routeNeedsSourceTablePeers(route renderRoute, legacyPool bool) bool {
	return effectiveStickyMode(route, legacyPool) == StickyModeSourceTable
}

// effectiveLeastPing reports whether balance_algorithm=leastping actually
// governs this backend's server choice: sticky=source ignores the algorithm
// entirely, so leastping is inert (and needs no Agent weight controller)
// there.
func effectiveLeastPing(route renderRoute, legacyPool bool) bool {
	return route.BalanceAlgorithm == BalanceAlgorithmLeastPing && effectiveStickyMode(route, legacyPool) != StickyModeSource
}

// effectiveLeastConn reports whether balance_algorithm=leastconn governs the
// choice of server for a new client of a servers[] backend (sticky=source
// ignores the algorithm; source_table uses it for the first pick).
func effectiveLeastConn(route renderRoute, legacyPool bool) bool {
	return len(route.Servers) > 0 && route.BalanceAlgorithm == BalanceAlgorithmLeastConn &&
		effectiveStickyMode(route, legacyPool) != StickyModeSource
}

// effectiveLeastConnAgent reports whether a leastconn backend is managed by
// the Agent: a non-zero tolerance needs the live connection counts.
func effectiveLeastConnAgent(route renderRoute, legacyPool bool) bool {
	return effectiveLeastConn(route, legacyPool) && route.LeastPingTolerance > 0
}

// leastConnCostWeights holds the static weights that express leastconn
// connection costs. HAProxy's leastconn (and the weighted round-robin the
// Agent drives) picks the server with the lowest connections/weight, so a
// server with cost c behaves as if every connection counted c times when its
// weight is divided by c. Weights are base/cost scaled by one factor
// K = min(100, 256 / max(base/cost)) for the whole backend, so the ratios
// survive integer rounding, and clamped to 1..256.
type leastConnCostWeights struct {
	servers []int            // index-aligned with route.Servers
	ips     []map[string]int // per-server DNS-pool address weights
}

// routeLeastConnCostWeights returns the cost-scaled weights of an effective
// leastconn backend, or nil when no server or address sets a cost (the
// configured weights are then rendered unchanged, byte-identical to before).
func routeLeastConnCostWeights(route renderRoute, legacyPool bool) *leastConnCostWeights {
	if !effectiveLeastConn(route, legacyPool) {
		return nil
	}
	hasCost := false
	for _, srv := range route.Servers {
		if srv.Cost > 0 {
			hasCost = true
		}
		for _, w := range srv.IPWeights {
			if w.Cost > 0 {
				hasCost = true
			}
		}
	}
	if !hasCost {
		return nil
	}
	ratio := func(base int, cost float64) float64 {
		if base <= 0 {
			base = 1
		}
		if cost <= 0 {
			cost = 1
		}
		return float64(base) / cost
	}
	maxRatio := 0.0
	for _, srv := range route.Servers {
		maxRatio = math.Max(maxRatio, ratio(srv.Weight, srv.Cost))
		for _, w := range srv.IPWeights {
			maxRatio = math.Max(maxRatio, ratio(ipBase(w, srv), ipCost(w, srv)))
		}
	}
	scale := math.Min(100, 256/maxRatio)
	out := &leastConnCostWeights{servers: make([]int, len(route.Servers)), ips: make([]map[string]int, len(route.Servers))}
	for i, srv := range route.Servers {
		out.servers[i] = clampRenderWeight(ratio(srv.Weight, srv.Cost) * scale)
		if len(srv.IPWeights) > 0 {
			out.ips[i] = make(map[string]int, len(srv.IPWeights))
			for _, w := range srv.IPWeights {
				out.ips[i][w.IP] = clampRenderWeight(ratio(ipBase(w, srv), ipCost(w, srv)) * scale)
			}
		}
	}
	return out
}

func ipBase(w RouteServerIPWeight, srv renderServerSpec) int {
	if w.Weight > 0 {
		return w.Weight
	}
	return srv.Weight
}

func ipCost(w RouteServerIPWeight, srv renderServerSpec) float64 {
	if w.Cost > 0 {
		return w.Cost
	}
	return srv.Cost
}

func clampRenderWeight(value float64) int {
	weight := int(math.Round(value))
	if weight < 1 {
		return 1
	}
	if weight > 256 {
		return 256
	}
	return weight
}

// writeWeightAnnotations emits the "# nf-weights"/"# nf-weight" comments
// consumed by internal/agent/weights.go, right after the balance/stick block
// and before "option tcp-check". Only emitted for a servers[] backend with
// effective leastping, agent-managed leastconn (tolerance > 0) or any
// ip_weights that change a slot weight.
func writeWeightAnnotations(b *strings.Builder, route renderRoute, legacyPool bool) {
	if len(route.Servers) == 0 {
		return
	}
	leastPing := effectiveLeastPing(route, legacyPool)
	leastConnAgent := effectiveLeastConnAgent(route, legacyPool)
	dynamic := leastPing || leastConnAgent
	costWeights := routeLeastConnCostWeights(route, legacyPool)
	// Without a dynamic algorithm only per-IP weights matter: a per-IP cost
	// alone is inert, except under leastconn where it becomes a static slot
	// weight (base/cost).
	managed := func(srv renderServerSpec) bool {
		if dynamic || costWeights != nil {
			return len(srv.IPWeights) > 0
		}
		return routeServerHasIPWeight(srv.IPWeights)
	}
	hasIPWeights := false
	for _, srv := range route.Servers {
		if managed(srv) {
			hasIPWeights = true
			break
		}
	}
	if !dynamic && !hasIPWeights {
		return
	}
	algo := "static"
	switch {
	case leastPing:
		algo = "leastping"
	case leastConnAgent:
		algo = "leastconn"
	}
	fmt.Fprintf(b, "    # nf-weights backend=%s algo=%s tolerance=%.2f tolerance_ms=%d\n",
		route.Backend, algo, route.LeastPingTolerance, route.LeastPingToleranceMS)
	preferred := func(i int, srv renderServerSpec) bool {
		return srv.DNSPool && srv.PreferredIP != "" && !srv.Backup && i == 0 && route.BalanceMode == "failover"
	}
	for i, srv := range route.Servers {
		base := srv.Weight
		if base == 0 {
			base = 1
		}
		if !dynamic && !managed(srv) {
			// algo=static only manages templates with ip_weights; every
			// other server keeps its configured weight.
			continue
		}
		if !srv.DNSPool {
			fmt.Fprintf(b, "    # nf-weight server=%s base=%d cost=%.2f\n", srv.Name, base, srv.Cost)
			continue
		}
		if preferred(i, srv) {
			fmt.Fprintf(b, "    # nf-weight server=%s_pref base=%d cost=%.2f\n", srv.Name, base, srv.Cost)
		}
		if !dynamic && costWeights != nil {
			// Static leastconn costs: the Agent only pins each resolved
			// address to its precomputed base/cost weight (the algo=static
			// contract every 1.1.0 Agent already applies).
			fmt.Fprintf(b, "    # nf-weight template=%s_ slots=%d base=%d cost=0.00%s\n",
				srv.Name, DNSPoolTemplateSlots, costWeights.servers[i], formatIPWeightMap(srv.IPWeights, costWeights.ips[i]))
			continue
		}
		ipCosts := ""
		if dynamic {
			ipCosts = formatRouteServerIPCosts(srv.IPWeights)
		}
		fmt.Fprintf(b, "    # nf-weight template=%s_ slots=%d base=%d cost=%.2f%s%s\n",
			srv.Name, DNSPoolTemplateSlots, base, srv.Cost, formatRouteServerIPWeights(srv.IPWeights), ipCosts)
	}
}

// formatIPWeightMap renders " ips=<ip>=<w>,..." in the stored ip_weights
// order using precomputed weights.
func formatIPWeightMap(order []RouteServerIPWeight, weights map[string]int) string {
	parts := make([]string, 0, len(order))
	for _, w := range order {
		if weight, ok := weights[w.IP]; ok {
			parts = append(parts, w.IP+"="+strconv.Itoa(weight))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " ips=" + strings.Join(parts, ",")
}

func routeServerHasIPWeight(weights []RouteServerIPWeight) bool {
	for _, w := range weights {
		if w.Weight > 0 {
			return true
		}
	}
	return false
}

// formatRouteServerIPWeights renders the "ips=<ip>=<w>,<ip>=<w>" suffix, or
// "" when there are none. IPs are already stored canonicalised
// (net.IP.String()); the separator before each weight is the LAST '=' so an
// agent parser can split unambiguously.
//
// Entries with weight 0 (cost-only) inherit the template base and are left
// out, so every emitted weight stays within the 1..256 range older Agents
// accept.
func formatRouteServerIPWeights(weights []RouteServerIPWeight) string {
	parts := make([]string, 0, len(weights))
	for _, w := range weights {
		if w.Weight > 0 {
			parts = append(parts, w.IP+"="+strconv.Itoa(w.Weight))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " ips=" + strings.Join(parts, ",")
}

// formatRouteServerIPCosts renders the "ipcosts=<ip>=<cost>,..." suffix of
// a leastping template: the per-address latency multiplier that replaces the
// template cost for that slot. It is a separate key rather than an extension
// of ips= so an Agent that predates per-IP cost ignores it (unknown
// annotation keys are skipped) instead of rejecting the whole backend.
func formatRouteServerIPCosts(weights []RouteServerIPWeight) string {
	parts := make([]string, 0, len(weights))
	for _, w := range weights {
		if w.Cost > 0 {
			parts = append(parts, w.IP+"="+strconv.FormatFloat(w.Cost, 'f', 2, 64))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " ipcosts=" + strings.Join(parts, ",")
}

func runtimeServerName(route renderRoute) string {
	if legacyDNSPoolRoute(route) {
		return routeServerTemplatePrefix(route.ID) + "1"
	}
	return route.Server
}

const megabitPerSecondBytes = int64(125000)

// bandwidthLimitBytes converts the operator-facing decimal Mbps value to the
// bytes per second expected by HAProxy's bwlim filters.
func bandwidthLimitBytes(mbps *int64) (int64, error) {
	if mbps == nil {
		return 0, nil
	}
	if *mbps <= 0 {
		return 0, errors.New("must be positive")
	}
	if *mbps > (int64(^uint64(0)>>1) / megabitPerSecondBytes) {
		return 0, errors.New("is too large")
	}
	return *mbps * megabitPerSecondBytes, nil
}

func routeBandwidthFilterName(routeID, direction string) string {
	return "nf_bw_" + direction + "_" + routeRuntimeID(routeID)
}

func routeBandwidthTableName(routeID, direction string) string {
	return "nf_bw_" + direction + "_table_" + routeRuntimeID(routeID)
}

// bandwidthLimitCondition selects a route before backend routing occurs.
// A non-SNI route is the listener fallback, so it applies only when none of
// that listener's SNI routes matched. This also leaves a single fallback route
// unconditional, avoiding an unnecessary TLS inspection dependency.
func bandwidthLimitCondition(routes []renderRoute, route renderRoute) string {
	if route.MatchMode == "sni" {
		return " if " + routeACLName(route.ID)
	}
	var sniACLs []string
	for _, candidate := range routes {
		if candidate.MatchMode == "sni" {
			sniACLs = append(sniACLs, "!"+routeACLName(candidate.ID))
		}
	}
	if len(sniACLs) == 0 {
		return ""
	}
	return " if " + strings.Join(sniACLs, " ")
}

func formatIECBytes(bytes int64) string {
	units := [...]string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	value := float64(bytes)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	precision := 2
	if value >= 100 || unit == 0 {
		precision = 0
	} else if value >= 10 {
		precision = 1
	}
	formatted := strconv.FormatFloat(value, 'f', precision, 64)
	if strings.Contains(formatted, ".") {
		formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	}
	return formatted + " " + units[unit]
}

func sanitizeRuntimeName(value string) string {
	var b strings.Builder
	underscore := false
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			underscore = false
			continue
		}
		if !underscore {
			b.WriteByte('_')
			underscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

func renderBind(ip string, port int) string {
	if ip == "*" {
		return ":" + strconv.Itoa(port)
	}
	if strings.ContainsRune(ip, ':') {
		return "[" + ip + "]:" + strconv.Itoa(port)
	}
	return ip + ":" + strconv.Itoa(port)
}

// routeServersForRender converts RouteServerSpec slice to renderServerSpec slice.
func routeServersForRender(servers []RouteServerSpec) []renderServerSpec {
	if len(servers) == 0 {
		return nil
	}
	out := make([]renderServerSpec, len(servers))
	for i, s := range servers {
		out[i] = renderServerSpec{
			Name: s.Name, TargetType: s.TargetType,
			Host: s.Host, Port: s.Port,
			UnixSocketPath: s.UnixSocketPath, Backup: s.Backup,
			DNSPool: s.DNSPool, PreferredIP: s.PreferredIP,
			Weight: s.Weight, Cost: s.Cost, IPWeights: s.IPWeights,
		}
	}
	return out
}

// writeWeightSlowstart appends " weight <n>" (when weight is set) and
// " slowstart <t>" (when the route has one) to a server/server-template line.
func writeWeightSlowstart(b *strings.Builder, weight int, slowstart string) {
	if weight != 0 {
		b.WriteString(" weight " + strconv.Itoa(weight))
	}
	if slowstart != "" {
		b.WriteString(" slowstart " + slowstart)
	}
}

// writeMultiServerBackend renders the server lines of a servers[] backend.
// Each server is static or a DNS-pool server-template. HAProxy has two tiers
// only (active and backup); when a DNS pool is a reserve (a backup template,
// or the template behind a preferred_ip), "option allbackups" spreads load
// over all of its resolved addresses. Validation guarantees such a pool is
// the only reserve, so allbackups never flattens a priority order.
func writeMultiServerBackend(b *strings.Builder, route renderRoute) {
	// preferred_ip is honoured only for the first server of a failover
	// backend (validation enforces this; stored rows from 1.1.0-rc may not).
	preferred := func(i int, srv renderServerSpec) bool {
		return srv.DNSPool && srv.PreferredIP != "" && !srv.Backup && i == 0 && route.BalanceMode == "failover"
	}
	dnsPoolReserve := false
	for i, srv := range route.Servers {
		if srv.DNSPool && (srv.Backup || preferred(i, srv)) {
			dnsPoolReserve = true
			break
		}
	}
	if dnsPoolReserve {
		b.WriteString("    option allbackups\n")
	}
	checkSendProxy := route.HealthCheck && route.ProxyProtocol != "none" && route.ProxyProtocol != ""
	costWeights := routeLeastConnCostWeights(route, false)
	for i, srv := range route.Servers {
		if costWeights != nil {
			// leastconn costs are expressed as static base/cost weights.
			srv.Weight = costWeights.servers[i]
		}
		if !srv.DNSPool {
			b.WriteString("    server " + srv.Name + " " + renderServerSpecTarget(srv))
			writeWeightSlowstart(b, srv.Weight, route.Slowstart)
			if route.HealthCheck {
				b.WriteString(" check inter 5s fall 3 rise 2")
			}
			if srv.TargetType == "tcp" && net.ParseIP(srv.Host) == nil {
				b.WriteString(" resolvers nf_dns init-addr last,none")
			}
			proxyStr(b, route)
			if checkSendProxy {
				b.WriteString(" check-send-proxy")
			}
			if srv.Backup {
				b.WriteString(" backup")
			}
			b.WriteByte('\n')
			continue
		}
		templateBackup := srv.Backup
		if preferred(i, srv) {
			// The preferred address is the active server; the resolved pool
			// is its reserve.
			b.WriteString("    server " + srv.Name + "_pref " + renderHostPort(srv.PreferredIP, srv.Port))
			writeWeightSlowstart(b, srv.Weight, route.Slowstart)
			if route.HealthCheck {
				b.WriteString(" check inter 5s fall 3 rise 2")
			}
			proxyStr(b, route)
			if checkSendProxy {
				b.WriteString(" check-send-proxy")
			}
			b.WriteByte('\n')
			templateBackup = true
		}
		b.WriteString("    server-template " + srv.Name + "_ " + strconv.Itoa(DNSPoolTemplateSlots) + " " + renderHostPort(srv.Host, srv.Port))
		writeWeightSlowstart(b, srv.Weight, route.Slowstart)
		b.WriteString(" check inter 5s fall 3 rise 2 resolvers nf_dns init-addr last,none resolve-opts prevent-dup-ip hash-key addr")
		proxyStr(b, route)
		if checkSendProxy {
			b.WriteString(" check-send-proxy")
		}
		if templateBackup {
			b.WriteString(" backup")
		}
		b.WriteByte('\n')
	}
}

// proxyStr appends the proxy protocol keyword(s) to b for a route.
func proxyStr(b *strings.Builder, route renderRoute) {
	switch route.ProxyProtocol {
	case "v1":
		b.WriteString(" send-proxy")
	case "v2":
		b.WriteString(" send-proxy-v2")
	}
}
func renderServerSpecTarget(s renderServerSpec) string {
	if s.TargetType == "unix" {
		return s.UnixSocketPath
	}
	return renderHostPort(s.Host, s.Port)
}

// renderHostPort brackets IPv6 literals so the port separator is unambiguous.
func renderHostPort(host string, port int) string {
	if strings.ContainsRune(host, ':') {
		return "[" + host + "]:" + strconv.Itoa(port)
	}
	return host + ":" + strconv.Itoa(port)
}

// listenerAcceptProxyUnion builds a sorted, deduplicated list of IP/CIDR
// strings from the union of all routes' accept_proxy_from on this listener.
func listenerAcceptProxyUnion(routes []renderRoute) []string {
	seen := make(map[string]struct{})
	for _, route := range routes {
		for _, cidr := range route.AcceptProxyFrom {
			seen[cidr] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for cidr := range seen {
		out = append(out, cidr)
	}
	sort.Strings(out)
	return out
}

// PPTrustedACLDir holds the Agent-maintained ACL files with the resolved
// addresses of accept_proxy_from hostnames. It lives under /etc/haproxy,
// which the Node Agent unit may write.
const PPTrustedACLDir = "/etc/haproxy/nodeflow"

func ppTrustedACLFile(frontend string) string {
	return PPTrustedACLDir + "/pp-trusted-" + frontend + ".acl"
}

// isAcceptProxyHostname reports whether a validated accept_proxy_from entry
// is a hostname rather than an IP address or CIDR.
func isAcceptProxyHostname(entry string) bool {
	return !strings.Contains(entry, "/") && net.ParseIP(entry) == nil
}

// splitAcceptProxyEntries separates a sorted accept_proxy_from union into
// static IP/CIDR entries and hostnames, both keeping the sorted order.
func splitAcceptProxyEntries(union []string) (static, domains []string) {
	for _, entry := range union {
		if isAcceptProxyHostname(entry) {
			domains = append(domains, entry)
		} else {
			static = append(static, entry)
		}
	}
	return static, domains
}

const (
	acceptProxyAllIPv4 = "0.0.0.0/0"
	acceptProxyAllIPv6 = "::/0"
)

// acceptProxyCoversListener reports whether the accept_proxy_from union
// matches every source address the listener can receive. "*" and IPv4
// binds only receive IPv4, a specific non-wildcard IPv6 bind only IPv6, and
// "::" is dual-stack, so it needs both 0.0.0.0/0 and ::/0.
func acceptProxyCoversListener(listenerIP string, union []string) bool {
	hasV4, hasV6 := false, false
	for _, cidr := range union {
		switch cidr {
		case acceptProxyAllIPv4:
			hasV4 = true
		case acceptProxyAllIPv6:
			hasV6 = true
		}
	}
	switch {
	case listenerIP == "*" || listenerIP == "":
		return hasV4
	case listenerIP == "::":
		return hasV4 && hasV6
	}
	ip := net.ParseIP(listenerIP)
	if ip == nil {
		return false
	}
	if ip.To4() != nil {
		return hasV4
	}
	return hasV6
}

func renderTarget(route renderRoute) string {
	if route.TargetType == "unix" {
		return route.UnixSocketPath
	}
	if strings.ContainsRune(route.TargetHost, ':') {
		return "[" + route.TargetHost + "]:" + strconv.Itoa(route.TargetPort)
	}
	return route.TargetHost + ":" + strconv.Itoa(route.TargetPort)
}

func renderMetadata(result HAProxyRenderResult) map[string]any {
	quotaEnforcement := "none"
	if result.QuotaMetadataRoutes > 0 {
		quotaEnforcement = "metadata_only"
	}
	if result.QuotaRuntimeRoutes > 0 {
		quotaEnforcement = "runtime_block_new"
		if result.QuotaMetadataRoutes > 0 {
			quotaEnforcement = "mixed"
		}
	}
	metadata := map[string]any{
		"source":                    "routes",
		"renderer":                  result.Renderer,
		"enabled_routes":            result.EnabledRoutes,
		"listeners":                 result.Listeners,
		"listener_tcp_ports":        result.ListenerPorts,
		"quota_enforcement":         quotaEnforcement,
		"custom_fragment_policy":    "route_backend_directives",
		"manual_backend_routes":     result.ManualBackendRoutes,
		"route_backends":            result.RouteBackends,
		"route_fingerprints":        result.RouteFingerprints,
		"runtime_names":             result.RuntimeNames,
		"pipe_size":                 result.PipeSize,
		"auto_sticky_table_entries": result.AutoStickyTableEntries,
	}
	if !result.HAProxyLogs {
		// Only recorded when off, so default revisions keep their metadata.
		metadata["haproxy_logs"] = false
	}
	return metadata
}

func renderErrorResponse(err error) (status int, code, message string) {
	if errors.Is(err, ErrNoEnabledRoutes) {
		return 422, "no_enabled_routes", "node has no enabled routes to render"
	}
	var kernelErr *KernelShaperUnsupportedError
	if errors.As(err, &kernelErr) {
		return 422, kernelShaperRequiresAgentCode, kernelErr.Error()
	}
	var leastPingErr *LeastPingUnsupportedError
	if errors.As(err, &leastPingErr) {
		return 422, leastPingRequiresAgentCode, leastPingErr.Error()
	}
	var ipWeightsErr *IPWeightsUnsupportedError
	if errors.As(err, &ipWeightsErr) {
		return 422, ipWeightsRequiresAgentCode, ipWeightsErr.Error()
	}
	var leastConnErr *LeastConnToleranceUnsupportedError
	if errors.As(err, &leastConnErr) {
		return 422, leastConnToleranceRequiresAgentCode, leastConnErr.Error()
	}
	var ppTrustedErr *PPTrustedDomainsUnsupportedError
	if errors.As(err, &ppTrustedErr) {
		return 422, ppTrustedDomainsRequiresAgentCode, ppTrustedErr.Error()
	}
	var invalid *RouteSetError
	if errors.As(err, &invalid) {
		return 422, "invalid_route_set", invalid.Error()
	}
	return 500, "internal_error", "internal server error"
}

func routeRenderSummary(result HAProxyRenderResult) string {
	return fmt.Sprintf("маршрутов %d, listener-ов %d", result.EnabledRoutes, result.Listeners)
}
