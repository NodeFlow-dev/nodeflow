package panel

import (
	"time"

	"github.com/nodeflow/nodeflow/internal/agent"
)

const (
	MaxManagedConfigBytes     = 512 << 10
	MaxReportDetailsBytes     = 16 << 10
	MaxRouteSNIs              = 64
	MaxCustomFragmentBytes    = 8 << 10
	MaxUnixSocketPathBytes    = 107
	MaxAcceptProxyFromEntries = 256
	// MaxAcceptProxyFromDomains bounds the hostnames (DNS pools) inside one
	// route's accept_proxy_from list.
	MaxAcceptProxyFromDomains = 32
	MaxRouteServers           = 16
)

type Node struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Address   string     `json:"address"`
	Status    string     `json:"status"`
	Metadata  any        `json:"metadata"`
	LastSeen  *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	SortOrder int64      `json:"sort_order,omitempty"`
}

// RouteServerIPWeight is an agent-applied runtime weight override for the
// DNS-pool template slot whose resolved address equals IP.
//
// Weight 0 inherits the server's base weight (only valid together with a
// Cost). Cost is the per-address leastping latency multiplier (0 = the
// server's cost); it is omitted from JSON when unset so entries stored before
// per-IP cost existed keep their exact stored/fingerprinted shape.
type RouteServerIPWeight struct {
	IP     string  `json:"ip"`
	Weight int     `json:"weight"`
	Cost   float64 `json:"cost,omitempty"`
}

// RouteServer represents one backend server endpoint in a multi-server route.
// When a route has no explicit servers (Servers slice empty), the renderer
// falls back to the route-level TargetType/TargetHost/TargetPort/UnixSocketPath.
type RouteServer struct {
	ID             string `json:"id"`
	RouteID        string `json:"route_id"`
	Position       int    `json:"position"`
	Name           string `json:"name"`
	TargetType     string `json:"target_type"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	UnixSocketPath string `json:"unix_socket_path"`
	Backup         bool   `json:"backup"`
	// DNSPool: resolve all A/AAAA records for this server's hostname and use
	// them as a pool of HAProxy server-template slots.
	DNSPool     bool      `json:"dns_pool"`
	PreferredIP string    `json:"preferred_ip"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// Weight is the configured HAProxy server weight (0 = HAProxy default, not rendered).
	Weight int `json:"weight"`
	// Cost is the leastping latency multiplier (0/absent = 1).
	Cost float64 `json:"cost"`
	// IPWeights are agent-applied runtime weights for DNS-pool template slots.
	IPWeights []RouteServerIPWeight `json:"ip_weights"`
}

type Route struct {
	ID                  string        `json:"id"`
	NodeID              string        `json:"node_id"`
	Name                string        `json:"name"`
	Version             int64         `json:"version"`
	ListenerIP          string        `json:"listener_ip"`
	ListenerPort        int           `json:"listener_port"`
	MatchMode           string        `json:"match_mode"`
	SNIs                []string      `json:"snis"`
	Fallback            bool          `json:"fallback"`
	Hostname            string        `json:"hostname"`
	TargetType          string        `json:"target_type"`
	TargetHost          string        `json:"target_host"`
	TargetPort          int           `json:"target_port"`
	DNSPool             bool          `json:"dns_pool"`
	UnixSocketPath      string        `json:"unix_socket_path"`
	HealthCheck         bool          `json:"health_check"`
	ProxyProtocol       string        `json:"proxy_protocol"`
	AcceptProxyFrom     []string      `json:"accept_proxy_from"`
	QuotaBytes          *int64        `json:"quota_bytes"`
	QuotaAction         string        `json:"quota_action"`
	QuotaPeriod         string        `json:"quota_period"`
	ClientUploadMbps    *int64        `json:"client_upload_mbps"`
	ClientDownloadMbps  *int64        `json:"client_download_mbps"`
	Enabled             bool          `json:"enabled"`
	Deployed            bool          `json:"deployed"`
	DeploymentState     string        `json:"deployment_state"`
	DeploymentError     string        `json:"deployment_error,omitempty"`
	DesiredRevision     *int64        `json:"desired_revision,omitempty"`
	AppliedRevision     *int64        `json:"applied_revision,omitempty"`
	DesiredFingerprint  string        `json:"desired_fingerprint"`
	DeployedFingerprint string        `json:"deployed_fingerprint"`
	DeletePending       bool          `json:"delete_pending"`
	CustomFragment      string        `json:"custom_fragment"`
	Servers             []RouteServer `json:"servers,omitempty"`
	StickyEnabled       bool          `json:"sticky_enabled"`
	// BalanceMode controls how multiple servers share load.
	// "pool" = all active simultaneously (roundrobin or consistent hash when sticky).
	// "failover" = priority order; position > 1 rendered with backup keyword.
	BalanceMode string `json:"balance_mode"`
	// ShaperMode selects the client bandwidth enforcer: "haproxy" (bwlim
	// filters) or "kernel" (Node Agent nftables; HAProxy keeps splice).
	ShaperMode string `json:"shaper_mode"`
	// StickyMode selects client distribution in pool mode: none, source or
	// source_table (legacy leastconn/roundrobin/sni values may still be read
	// from rows stored before migration 000052). Empty marks a row stored
	// before migration 000051; it renders as sticky_enabled/DNS pools imply.
	StickyMode string `json:"sticky_mode"`
	// StickyTTL is the HAProxy expire of the source_table stick-table.
	StickyTTL string `json:"sticky_ttl"`
	// BalanceAlgorithm selects the server for a new client: '', roundrobin,
	// static-rr, random, leastconn or leastping. Stored '' for single-target
	// routes (nothing to balance between).
	BalanceAlgorithm string `json:"balance_algorithm"`
	// BalanceRandomDraws is the "of N" parameter of balance_algorithm=random
	// (1 or 2, default 2). Stored only for random.
	BalanceRandomDraws int `json:"balance_random_draws"`
	// LeastPingTolerance is the relative tolerance (0..1): for leastping the
	// latency tolerance (default 0.2), for leastconn the connection-load
	// tolerance (default 0 = plain HAProxy leastconn). Stored only for those
	// two algorithms. Returned under both leastping_tolerance (1.1.0 name)
	// and balance_tolerance.
	LeastPingTolerance float64 `json:"leastping_tolerance"`
	// BalanceTolerance mirrors LeastPingTolerance in API responses.
	BalanceTolerance float64 `json:"balance_tolerance"`
	// LeastPingToleranceMS is the absolute latency tolerance floor in
	// milliseconds (0..1000). Stored only for leastping.
	LeastPingToleranceMS int `json:"leastping_tolerance_ms"`
	// StickyHash selects the hash-type for sticky_mode=source: '' (consistent)
	// or map-based.
	StickyHash string `json:"sticky_hash"`
	// StickyHashBalanceFactor is the hash-balance-factor for a consistent
	// source hash (0 = off, else 101..1000).
	StickyHashBalanceFactor int `json:"sticky_hash_balance_factor"`
	// StickyTableEntries sizes the source_table stick-table ('' = «Авто»,
	// sized from node RAM, else <n>k or <n>m).
	StickyTableEntries string `json:"sticky_table_entries"`
	// StickyTableEntriesEffective is the read-only size the renderer uses for
	// a source_table route: the explicit value, or the adaptive «Авто» size
	// computed from the node's latest heartbeat (GET responses only).
	StickyTableEntriesEffective string `json:"sticky_table_entries_effective,omitempty"`
	// StickyIPv6Prefix is the client key prefix length for source and
	// source_table (0 = 64, else 32..128).
	StickyIPv6Prefix int `json:"sticky_ipv6_prefix"`
	// ClientIPv6 (migration 000054, default true) keys the client tables
	// (source_table stick-table, source hash, HAProxy bandwidth limiter) by
	// IPv6-capable addresses. False renders IPv4-only tables (type ip, key
	// src) for nodes without IPv6. A nil pointer (a Route built in code,
	// not read from the store) means true.
	ClientIPv6 *bool `json:"client_ipv6"`
	// Slowstart is the HAProxy server ramp-up time ('' or 1s..10m), rendered
	// on every server/server-template line of the backend.
	Slowstart string    `json:"slowstart"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	SortOrder int64     `json:"sort_order,omitempty"`
}

// RouteDeleteResult distinguishes an immediate draft deletion from an active
// route removal that must first be confirmed by the Node Agent.
type RouteDeleteResult struct {
	Route   Route
	Pending bool
}

// RouteSpec is the validated, canonical route representation accepted by Store.
// Hostname is intentionally retained as the first SNI for legacy API clients.
//
// RouteServerSpec is also serialised into route fingerprints. The original
// fields keep Go's default JSON keys (no tags) and the 1.1.0 additions are
// omitempty, so servers that do not use them keep their pre-1.1.0 hash input.
type RouteServerSpec struct {
	Position       int
	Name           string
	TargetType     string
	Host           string
	Port           int
	UnixSocketPath string
	Backup         bool
	// DNSPool: this server should be rendered as a server-template resolving all A/AAAA.
	DNSPool     bool   `json:"DNSPool,omitempty"`
	PreferredIP string `json:"PreferredIP,omitempty"`
	// 1.1.0 (migration 000052) additions: omitempty so servers that do not use
	// them keep their pre-052 fingerprint hash input.
	Weight    int                   `json:"Weight,omitempty"`
	Cost      float64               `json:"Cost,omitempty"`
	IPWeights []RouteServerIPWeight `json:"IPWeights,omitempty"`
}

type RouteSpec struct {
	ExpectedVersion         *int64
	Name                    string
	ListenerIP              string
	ListenerPort            int
	MatchMode               string
	SNIs                    []string
	Fallback                bool
	Hostname                string
	TargetType              string
	TargetHost              string
	TargetPort              int
	DNSPool                 bool
	UnixSocketPath          string
	HealthCheck             bool
	ProxyProtocol           string
	AcceptProxyFrom         []string
	QuotaBytes              *int64
	QuotaAction             string
	QuotaPeriod             string
	ClientUploadMbps        *int64
	ClientDownloadMbps      *int64
	Enabled                 bool
	CustomFragment          string
	Servers                 []RouteServerSpec
	StickyEnabled           bool
	BalanceMode             string
	ShaperMode              string
	StickyMode              string
	StickyTTL               string
	BalanceAlgorithm        string
	BalanceRandomDraws      int
	LeastPingTolerance      float64
	LeastPingToleranceMS    int
	StickyHash              string
	StickyHashBalanceFactor int
	StickyTableEntries      string
	StickyIPv6Prefix        int
	// ClientIPv4Only is the negation of the stored client_ipv6, so the zero
	// value keeps the default (IPv6-capable client tables).
	ClientIPv4Only bool
	Slowstart      string
}

type EnrollmentToken struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"node_id"`
	Prefix    string    `json:"prefix"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// AgentCredentialIdentity is derived only from the verified TLS leaf. It is
// never populated from JSON supplied by an Agent.
type AgentCredentialIdentity struct {
	NodeID              string
	CertificateSHA256   string
	CertificateSerial   string
	CertificateNotAfter time.Time
}

type CredentialRenewalRequest struct {
	RenewalID       string
	CSRHash         string
	CSRDER          []byte
	NextTokenHash   string
	NextTokenPrefix string
}

type CredentialRenewalCandidate struct {
	CredentialRenewalRequest
	CertificateSHA256   string
	CertificateSerial   string
	CertificateDER      []byte
	CertificateNotAfter time.Time
	ConfirmBy           time.Time
}

type CredentialRenewalRecord struct {
	ID                  string
	NodeID              string
	RenewalID           string
	PredecessorID       string
	CSRHash             string
	CSRDER              []byte
	NextTokenHash       string
	NextTokenPrefix     string
	CertificateSHA256   string
	CertificateSerial   string
	CertificateDER      []byte
	CertificateNotAfter time.Time
	ConfirmBy           time.Time
	ActivatedAt         *time.Time
	RevokedAt           *time.Time
	CreatedAt           time.Time
}

type AuditEvent struct {
	ActorType    string         `json:"actor_type"`
	ActorID      string         `json:"actor_id,omitempty"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id,omitempty"`
	Details      map[string]any `json:"details"`
	SourceIP     string         `json:"source_ip,omitempty"`
}

type AuditEntry struct {
	ID           int64          `json:"id"`
	ActorType    string         `json:"actor_type"`
	ActorID      string         `json:"actor_id,omitempty"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id,omitempty"`
	Details      map[string]any `json:"details"`
	SourceIP     string         `json:"source_ip,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

// PanelSettings contains the mutable, persisted operator preferences and
// security policy. Listener addresses and public URLs remain runtime-only
// Config values and are intentionally not part of this model.
type PanelSettings struct {
	Theme                    string    `json:"theme"`
	Accent                   string    `json:"accent"`
	InactivityTimeoutMinutes int       `json:"session_timeout_minutes"`
	MaxSessions              int       `json:"max_sessions"`
	AuditRetentionDays       int       `json:"audit_retention_days"`
	UpdatedAt                time.Time `json:"updated_at"`
}

type Heartbeat struct {
	Version                  string                  `json:"version"`
	Status                   string                  `json:"status"`
	Metrics                  map[string]any          `json:"metrics"`
	RoutesOK                 *bool                   `json:"routes_ok,omitempty"`
	ActualRevision           *int64                  `json:"actual_revision,omitempty"`
	ConfigSHA256             string                  `json:"config_sha256,omitempty"`
	TrafficInstanceID        string                  `json:"traffic_instance_id,omitempty"`
	TrafficInstanceStartedAt *time.Time              `json:"traffic_instance_started_at,omitempty"`
	TrafficSampleSeq         *int64                  `json:"traffic_sample_seq,omitempty"`
	HAProxyServiceState      string                  `json:"haproxy_service_state,omitempty"`
	HAProxyControlGeneration *int64                  `json:"haproxy_control_generation,omitempty"`
	HAProxyRestartGeneration *int64                  `json:"haproxy_restart_generation,omitempty"`
	HAProxyControlError      string                  `json:"haproxy_control_error,omitempty"`
	MTLSNodeID               string                  `json:"-"`
	Credential               AgentCredentialIdentity `json:"-"`
}

type ConfigAssignment struct {
	Revision int64  `json:"revision"`
	Config   string `json:"config"`
	SHA256   string `json:"sha256"`
}

type QuotaBackendPolicy struct {
	RouteID     string    `json:"route_id"`
	Backend     string    `json:"backend"`
	Server      string    `json:"server"`
	Action      string    `json:"action"`
	Block       bool      `json:"block"`
	UsedBytes   int64     `json:"used_bytes"`
	LimitBytes  *int64    `json:"limit_bytes,omitempty"`
	QuotaPeriod string    `json:"quota_period"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
}

type QuotaAssignment struct {
	Month    string               `json:"month"`
	Policies []QuotaBackendPolicy `json:"policies"`
}

type FirewallAssignment struct {
	Mode               string `json:"mode"`
	TCPPorts           []int  `json:"tcp_ports"`
	DesiredTCPPorts    []int  `json:"desired_tcp_ports,omitempty"`
	Transition         bool   `json:"transition,omitempty"`
	ActivePlanComplete bool   `json:"active_plan_complete,omitempty"`
}

type HAProxyServiceAssignment struct {
	Generation        int64 `json:"generation"`
	Enabled           bool  `json:"enabled"`
	Restart           bool  `json:"restart,omitempty"`
	RestartGeneration int64 `json:"restart_generation,omitempty"`
}

type NodeHAProxyControl struct {
	NodeID                  string     `json:"node_id"`
	Supported               bool       `json:"supported"`
	DesiredEnabled          bool       `json:"desired_enabled"`
	Generation              int64      `json:"generation"`
	ActualEnabled           *bool      `json:"actual_enabled,omitempty"`
	ActiveState             string     `json:"active_state"`
	ReportGeneration        int64      `json:"report_generation"`
	RestartGeneration       int64      `json:"restart_generation"`
	RestartReportGeneration int64      `json:"restart_report_generation"`
	LastError               string     `json:"last_error,omitempty"`
	ReportedAt              *time.Time `json:"reported_at,omitempty"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

type NodeFirewallPolicy struct {
	NodeID       string    `json:"node_id"`
	Mode         string    `json:"mode"`
	TCPPorts     []int     `json:"tcp_ports"`
	PlanComplete bool      `json:"plan_complete"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type HeartbeatResult struct {
	Status             string                    `json:"status"`
	NodeID             string                    `json:"node_id"`
	Assignment         *ConfigAssignment         `json:"assignment,omitempty"`
	QuotaAssignment    *QuotaAssignment          `json:"quota_assignment,omitempty"`
	FirewallAssignment *FirewallAssignment       `json:"firewall_assignment,omitempty"`
	UpdateAssignment   *agent.UpdateManifest     `json:"update_assignment,omitempty"`
	ServiceAssignment  *HAProxyServiceAssignment `json:"service_assignment,omitempty"`
}

type AgentRelease struct {
	ID           string    `json:"id"`
	Version      string    `json:"version"`
	OS           string    `json:"os"`
	Arch         string    `json:"arch"`
	SHA256       string    `json:"sha256"`
	SizeBytes    int64     `json:"size_bytes"`
	Sequence     int64     `json:"sequence"`
	Signature    string    `json:"signature"`
	ArtifactPath string    `json:"artifact_path,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type NodeAgentUpdateState struct {
	NodeID         string        `json:"node_id"`
	DesiredRelease *AgentRelease `json:"desired_release,omitempty"`
	ActualSequence int64         `json:"actual_sequence"`
	State          string        `json:"state"`
	LastError      string        `json:"last_error,omitempty"`
	LastReportAt   *time.Time    `json:"last_report_at,omitempty"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

type NodeHeartbeat struct {
	AgentVersion             string         `json:"agent_version"`
	Status                   string         `json:"status"`
	Metrics                  map[string]any `json:"metrics"`
	RoutesOK                 *bool          `json:"routes_ok,omitempty"`
	TrafficInstanceID        string         `json:"traffic_instance_id,omitempty"`
	TrafficInstanceStartedAt *time.Time     `json:"traffic_instance_started_at,omitempty"`
	TrafficSampleSeq         *int64         `json:"traffic_sample_seq,omitempty"`
	ReceivedAt               time.Time      `json:"received_at"`
}

type NodeOperationalDetail struct {
	Node                        Node                `json:"node"`
	LatestHeartbeat             *NodeHeartbeat      `json:"latest_heartbeat,omitempty"`
	RoutesTotal                 int                 `json:"routes_total"`
	RoutesEnabled               int                 `json:"routes_enabled"`
	TrafficMonth                string              `json:"traffic_month"`
	TrafficBytesIn              int64               `json:"traffic_bytes_in"`
	TrafficBytesOut             int64               `json:"traffic_bytes_out"`
	TrafficUsed                 int64               `json:"traffic_used_bytes"`
	TrafficObserved             bool                `json:"traffic_observed"`
	TrafficDay                  string              `json:"traffic_day"`
	TrafficDayBytesIn           int64               `json:"traffic_day_bytes_in"`
	TrafficDayBytesOut          int64               `json:"traffic_day_bytes_out"`
	TrafficDayUsed              int64               `json:"traffic_day_used_bytes"`
	TrafficDayObserved          bool                `json:"traffic_day_observed"`
	TrafficDailyAverageBytesIn  *float64            `json:"traffic_daily_average_bytes_in,omitempty"`
	TrafficDailyAverageBytesOut *float64            `json:"traffic_daily_average_bytes_out,omitempty"`
	TrafficDailyAverageUsed     *float64            `json:"traffic_daily_average_used_bytes,omitempty"`
	TrafficDailyObservedDays    int                 `json:"traffic_daily_observed_days"`
	RXBitsPerSecond             *float64            `json:"rx_bits_per_second,omitempty"`
	TXBitsPerSecond             *float64            `json:"tx_bits_per_second,omitempty"`
	RateSampledAt               *time.Time          `json:"rate_sampled_at,omitempty"`
	MetricsSummary              *NodeMetricSummary  `json:"metrics_summary,omitempty"`
	CredentialPrefix            string              `json:"credential_prefix,omitempty"`
	CredentialExpiresAt         *time.Time          `json:"credential_expires_at,omitempty"`
	CredentialLastUsed          *time.Time          `json:"credential_last_used_at,omitempty"`
	HAProxyControl              *NodeHAProxyControl `json:"haproxy_control,omitempty"`
}

// DashboardOverview is the single read model used by the Nodes screen.  It is
// intentionally assembled in the store so the browser does not perform one
// operational/traffic request per node.
type DashboardOverview struct {
	Range          string                 `json:"range"`
	SelectedNodeID string                 `json:"selected_node_id,omitempty"`
	Nodes          []DashboardNode        `json:"nodes"`
	TrafficHistory TrafficHistory         `json:"traffic_history"`
	TopRoutes      []DashboardRoute       `json:"top_routes"`
	Totals         DashboardOverviewTotal `json:"totals"`
}

type DashboardNode struct {
	Node            Node           `json:"node"`
	LatestHeartbeat *NodeHeartbeat `json:"latest_heartbeat,omitempty"`
	RoutesTotal     int            `json:"routes_total"`
	RoutesEnabled   int            `json:"routes_enabled"`
	TrafficMonth    string         `json:"traffic_month"`
	TrafficBytesIn  int64          `json:"traffic_bytes_in"`
	TrafficBytesOut int64          `json:"traffic_bytes_out"`
	TrafficUsed     int64          `json:"traffic_used_bytes"`
	TrafficObserved bool           `json:"traffic_observed"`
	RXBitsPerSecond *float64       `json:"rx_bits_per_second"`
	TXBitsPerSecond *float64       `json:"tx_bits_per_second"`
	RateSampledAt   *time.Time     `json:"rate_sampled_at,omitempty"`
}

type DashboardRoute struct {
	RouteID         string   `json:"route_id"`
	NodeID          string   `json:"node_id"`
	NodeName        string   `json:"node_name"`
	Name            string   `json:"name"`
	ListenerIP      string   `json:"listener_ip"`
	ListenerPort    int      `json:"listener_port"`
	SNIs            []string `json:"snis"`
	Fallback        bool     `json:"fallback"`
	BytesIn         int64    `json:"bytes_in"`
	BytesOut        int64    `json:"bytes_out"`
	UsedBytes       int64    `json:"used_bytes"`
	RXBitsPerSecond float64  `json:"rx_bits_per_second"`
	TXBitsPerSecond float64  `json:"tx_bits_per_second"`
	BitsPerSecond   float64  `json:"bits_per_second"`
	SharePercent    float64  `json:"share_percent"`
}

// DashboardOverviewTotal exposes current rates only when every non-offline
// node has a rate sample matching its latest heartbeat.
type DashboardOverviewTotal struct {
	NodesTotal          int      `json:"nodes_total"`
	NodesOnline         int      `json:"nodes_online"`
	NodesDegraded       int      `json:"nodes_degraded"`
	NodesOffline        int      `json:"nodes_offline"`
	RoutesTotal         int      `json:"routes_total"`
	ConnectionsCurrent  uint64   `json:"connections_current"`
	RXBitsPerSecond     *float64 `json:"rx_bits_per_second"`
	TXBitsPerSecond     *float64 `json:"tx_bits_per_second"`
	CurrentRateComplete bool     `json:"current_rate_complete"`
	TrafficMonthBytes   int64    `json:"traffic_month_bytes"`
	BackendsHealthy     int      `json:"backends_healthy"`
	BackendsDegraded    int      `json:"backends_degraded"`
	BackendsUnavailable int      `json:"backends_unavailable"`
}

type MetricAverageDelta struct {
	Average      *float64   `json:"average,omitempty"`
	Delta        *float64   `json:"delta,omitempty"`
	SampleCount  int64      `json:"sample_count"`
	ObservedFrom *time.Time `json:"observed_from,omitempty"`
	ObservedTo   *time.Time `json:"observed_to,omitempty"`
}

type NodeMetricSummary struct {
	Range         string              `json:"range"`
	From          *time.Time          `json:"from,omitempty"`
	To            *time.Time          `json:"to,omitempty"`
	SampleCount   int64               `json:"sample_count"`
	CPUPercent    *MetricAverageDelta `json:"cpu_percent,omitempty"`
	MemoryPercent *MetricAverageDelta `json:"memory_percent,omitempty"`
	RXBPS         *MetricAverageDelta `json:"rx_bps,omitempty"`
	TXBPS         *MetricAverageDelta `json:"tx_bps,omitempty"`
}

type ConfigRevision struct {
	ID        string         `json:"id"`
	NodeID    string         `json:"node_id"`
	Revision  int64          `json:"revision"`
	Config    string         `json:"config"`
	SHA256    string         `json:"sha256"`
	Note      string         `json:"note,omitempty"`
	Metadata  map[string]any `json:"metadata"`
	CreatedBy string         `json:"created_by"`
	CreatedAt time.Time      `json:"created_at"`
}

type NodeConfigState struct {
	NodeID          string     `json:"node_id"`
	DesiredRevision *int64     `json:"desired_revision"`
	ActualRevision  *int64     `json:"actual_revision"`
	State           string     `json:"state"`
	LastError       string     `json:"last_error,omitempty"`
	LastReportAt    *time.Time `json:"last_report_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type ApplyReport struct {
	ID                string                  `json:"id,omitempty"`
	NodeID            string                  `json:"node_id,omitempty"`
	Revision          int64                   `json:"revision"`
	State             string                  `json:"state"`
	ActualRevision    *int64                  `json:"actual_revision,omitempty"`
	Error             string                  `json:"error,omitempty"`
	RollbackAttempted bool                    `json:"rollback_attempted"`
	RollbackSucceeded *bool                   `json:"rollback_succeeded,omitempty"`
	Details           map[string]any          `json:"details,omitempty"`
	ReceivedAt        time.Time               `json:"received_at,omitempty"`
	MTLSNodeID        string                  `json:"-"`
	Credential        AgentCredentialIdentity `json:"-"`
}

// JobPayload is the versioned contract for future asynchronous bootstrap work.
type JobPayload struct {
	Version int            `json:"version"`
	Action  string         `json:"action"`
	Params  map[string]any `json:"params,omitempty"`
}
