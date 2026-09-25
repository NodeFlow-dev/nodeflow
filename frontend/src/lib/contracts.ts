export type NodeStatus = 'online' | 'offline' | 'pending' | 'error';

export interface NodeRecord {
  id: string;
  name: string;
  address: string;
  status: NodeStatus | string;
  metadata: Record<string, unknown> | null;
  /** «Логи соединений HAProxy»; absent on Panels older than 2.0.1 (= on). */
  haproxy_logs?: boolean;
  last_seen_at?: string;
  created_at: string;
  updated_at: string;
}

export interface MetricAverageDelta {
  average?: number;
  delta?: number;
  sample_count: number;
  observed_from?: string;
  observed_to?: string;
}

export interface NodeMetricSummary {
  range: string;
  sample_count: number;
  cpu_percent?: MetricAverageDelta;
  memory_percent?: MetricAverageDelta;
  rx_bps?: MetricAverageDelta;
  tx_bps?: MetricAverageDelta;
}

export interface HAProxyProxyStats {
  status?: string;
  connections_current?: number;
  sessions_current?: number;
  sessions_total?: number;
  session_rate?: number;
  tcp_sessions_current?: number;
  rx_bps?: number;
  tx_bps?: number;
  bytes_in?: number;
  bytes_out?: number;
}

export interface HAProxyServerStats {
	address?: string;
  status?: string;
  check_status?: string;
  check_description?: string;
  sessions_current?: number;
  sessions_total?: number;
  session_rate?: number;
  bytes_in?: number;
  bytes_out?: number;
  active?: boolean;
  backup?: boolean;
}

export interface HAProxyRuntime {
  connections_current?: number;
  connections_total?: number;
  connection_rate?: number;
  bytes_in?: number;
  bytes_out?: number;
  backends?: Record<string, HAProxyProxyStats>;
  servers?: Record<string, Record<string, HAProxyServerStats>>;
}

export interface HeartbeatMetrics {
  os?: string;
  arch?: string;
  cpu_count?: number;
  cpu_percent?: number;
  load?: number[];
  memory_total_bytes?: number;
  memory_available_bytes?: number;
  memory_percent?: number;
  network_bytes?: Record<string, number>;
  network_bytes_per_second?: Record<string, number>;
  uptime_seconds?: number;
  process_count?: number;
  process_names?: string[];
  haproxy_version?: string;
  haproxy_stats_available?: boolean;
  haproxy_runtime?: HAProxyRuntime;
  [key: string]: unknown;
}

export interface NodeHeartbeat {
  agent_version: string;
  status: string;
  metrics: HeartbeatMetrics;
  routes_ok?: boolean;
  received_at: string;
}

export interface NodeOperational {
  node: NodeRecord;
  latest_heartbeat?: NodeHeartbeat;
  routes_total: number;
  routes_enabled: number;
  traffic_month: string;
  traffic_bytes_in: number;
  traffic_bytes_out: number;
  traffic_used_bytes: number;
  traffic_observed: boolean;
  traffic_daily_observed_days?: number;
  traffic_day?: string;
  traffic_day_bytes_in?: number;
  traffic_day_bytes_out?: number;
  traffic_day_used_bytes?: number;
  traffic_day_observed?: boolean;
  traffic_daily_average_bytes_in?: number;
  traffic_daily_average_bytes_out?: number;
  traffic_daily_average_used_bytes?: number;
  metrics_summary?: NodeMetricSummary;
  rx_bits_per_second?: number | null;
  tx_bits_per_second?: number | null;
  rate_sampled_at?: string;
  credential_prefix?: string;
  credential_expires_at?: string;
  credential_last_used_at?: string;
  haproxy_control?: HAProxyControlState;
}

export interface HAProxyControlState {
  node_id: string;
  supported: boolean;
  desired_enabled: boolean;
  generation: number;
  restart_generation: number;
  actual_enabled?: boolean;
  active_state: 'unknown' | 'active' | 'reloading' | 'inactive' | 'failed' | 'activating' | 'deactivating' | string;
  report_generation: number;
  last_error?: string;
  reported_at?: string;
  updated_at?: string;
}

/** Runtime weight for one resolved DNS-pool address (agent-applied, dns-pool servers only). */
export interface RouteServerIPWeight {
  ip: string;
  /** 1..256; 0 = the server's weight (cost-only entry). */
  weight: number;
  /** leastping latency / leastconn connection multiplier for this address, 0.01..100; absent/0 = the server's cost. */
  cost?: number;
}

export interface RouteServer {
  id: string;
  route_id: string;
  position: number;
  name: string;
  target_type: string;
  host: string;
  port: number;
  unix_socket_path: string;
  backup: boolean;
  dns_pool?: boolean;
  preferred_ip?: string;
  /** 1..256; 0/absent = HAProxy default weight (not rendered). */
  weight?: number;
  /** leastping: latency = check_duration × cost; leastconn: load = connections × cost. 0/absent = 1. */
  cost?: number;
  /** dns-pool servers only, max 32 unique normalized IPs; each sets weight and/or cost. */
  ip_weights?: RouteServerIPWeight[];
  created_at?: string;
  updated_at?: string;
}

/** GET /api/v1/dns/resolve?host= — resolution runs on the panel host. */
export interface DNSResolveResult {
  host: string;
  addresses: string[];
  resolved_at: string;
  error?: string;
}

export interface RouteRecord {
  id: string;
  node_id: string;
  name: string;
  version: number;
  listener_ip: string;
  listener_port: number;
  match_mode: 'any_tcp' | 'sni' | 'destination_ip' | 'fallback';
  snis: string[];
  fallback: boolean;
  hostname?: string;
  target_type: 'tcp' | 'domain' | 'unix' | string;
  target_host: string;
  target_port: number;
  dns_pool: boolean;
  unix_socket_path: string;
  health_check: boolean;
  proxy_protocol: 'none' | 'v1' | 'v2' | string;
  quota_bytes: number | null;
  quota_action: string;
  quota_period: string;
  client_upload_mbps: number | null;
  client_download_mbps: number | null;
  enabled: boolean;
  deployed: boolean;
  deployment_state: string;
  deployment_error?: string;
  desired_revision?: number;
  applied_revision?: number;
  desired_fingerprint?: string;
  deployed_fingerprint?: string;
  delete_pending?: boolean;
  custom_fragment?: string;
  accept_proxy_from?: string[];
  servers?: RouteServer[];
  sticky_enabled?: boolean;
  /**
   * Client stickiness in pool mode; '' for rows stored before migration 000051.
   * leastconn/roundrobin/sni are legacy values that may still appear on read
   * (pre-000052 rows / rollback); the editor never writes them again.
   */
  sticky_mode?: '' | 'none' | 'source' | 'source_table' | 'leastconn' | 'roundrobin' | 'sni';
  /** source_table only: stick-table expiry (HAProxy time, 1m..7d). */
  sticky_ttl?: string;
  /** source/source_table only: '' = consistent (default), map-based = ровнее при смене серверов. */
  sticky_hash?: '' | 'consistent' | 'map-based';
  /** source only, consistent hash only: 0 = off, else 101..1000. */
  sticky_hash_balance_factor?: number;
  /** source_table only: '' = «Авто» (sized from node RAM); else <n>k (1..10000) or <n>m (1..10). */
  sticky_table_entries?: string;
  /** Read-only (GET): size the renderer uses for a source_table route, e.g. '919k' for «Авто». */
  sticky_table_entries_effective?: string;
  /** source/source_table only: 0 ⇒ 64; allowed 32..128. Must be 0 when client_ipv6 is false. */
  sticky_ipv6_prefix?: number;
  /**
   * Migration 000054, default true. false: IPv4-only client tables (stick-table
   * type ip keyed by src, balance source, bwlim tables type ip) for nodes
   * without IPv6.
   */
  client_ipv6?: boolean;
  /** Balancing algorithm choosing a server for a NEW client; '' ⇒ roundrobin (or leastconn for source_table). */
  balance_algorithm?: '' | 'roundrobin' | 'static-rr' | 'random' | 'leastconn' | 'leastping';
  /** random only: 1 or 2; 0 ⇒ 2. */
  balance_random_draws?: number;
  /** leastping only: relative tolerance 0..1; 0 ⇒ 0.2. Alias of balance_tolerance (1.1.0 name). */
  leastping_tolerance?: number;
  /**
   * Relative tolerance 0..1. leastping: latency tolerance (0 ⇒ 0.2);
   * leastconn: connection-load tolerance (0 = plain leastconn; > 0 needs
   * Node Agent 1.1.1).
   */
  balance_tolerance?: number;
  /** leastping only: absolute floor in ms, 0..1000. */
  leastping_tolerance_ms?: number;
  /** HAProxy time 1s..10m; rendered on every server line in pool mode. Requires health_check. */
  slowstart?: string;
  balance_mode?: 'pool' | 'failover';
  shaper_mode?: 'haproxy' | 'kernel';
  created_at?: string;
  updated_at?: string;
}

export interface TrafficRoute {
  route_id: string;
  backend_key: string;
  bytes_in: number;
  bytes_out: number;
  used_bytes: number;
  quota_used_bytes: number;
  limit_bytes: number | null;
  quota_period?: string;
  quota_window_start?: string;
  quota_window_end?: string;
  reached: boolean;
  quota_action?: string;
  enforcement?: boolean;
  block_requested?: boolean;
  blocked?: boolean;
  observed: boolean;
  applied: boolean;
}

export interface NodeTraffic {
  node_id: string;
  month: string;
  bytes_in: number;
  bytes_out: number;
  used_bytes: number;
  enforcement: boolean;
  routes: TrafficRoute[];
}

export interface TrafficHistorySample {
  timestamp: string;
  rx_bps: number | null;
  tx_bps: number | null;
  cpu_percent?: number;
  memory_percent?: number;
}

export interface TrafficHistory {
  range: string;
  bucket_seconds: number;
  samples: TrafficHistorySample[];
}

export interface NodeBundle {
  node: NodeRecord;
  operational: NodeOperational | null;
  routes: RouteRecord[];
  traffic: NodeTraffic | null;
  history: TrafficHistory | null;
}

export interface DashboardNode {
  node: NodeRecord;
  latest_heartbeat?: NodeHeartbeat;
  routes_total: number;
  routes_enabled: number;
  traffic_month: string;
  traffic_bytes_in: number;
  traffic_bytes_out: number;
  traffic_used_bytes: number;
  traffic_observed: boolean;
  rx_bits_per_second: number | null;
  tx_bits_per_second: number | null;
  rate_sampled_at?: string;
}

export interface DashboardRoute {
  route_id: string;
  node_id: string;
  node_name: string;
  name: string;
  listener_ip: string;
  listener_port: number;
  snis: string[];
  fallback: boolean;
  bytes_in: number;
  bytes_out: number;
  used_bytes: number;
  rx_bits_per_second: number;
  tx_bits_per_second: number;
  bits_per_second: number;
  share_percent: number;
}

export interface DashboardOverviewTotal {
  nodes_total: number;
  nodes_online: number;
  nodes_degraded: number;
  nodes_offline: number;
  routes_total: number;
  connections_current: number;
  rx_bits_per_second: number | null;
  tx_bits_per_second: number | null;
  current_rate_complete: boolean;
  traffic_month_bytes: number;
  backends_healthy: number;
  backends_degraded: number;
  backends_unavailable: number;
}

export interface DashboardOverview {
  range: string;
  selected_node_id?: string;
  nodes: DashboardNode[];
  traffic_history: TrafficHistory;
  top_routes: DashboardRoute[];
  totals: DashboardOverviewTotal;
}

export type FirewallMode = 'off' | 'observe' | 'apply';

export interface NodeFirewallPolicy {
  node_id: string;
  mode: FirewallMode;
  tcp_ports: number[];
  plan_complete: boolean;
  updated_at: string;
}

export interface AgentRelease {
  id: string;
  version: string;
  os: string;
  arch: string;
  sha256: string;
  size_bytes: number;
  sequence: number;
  signature: string;
  created_at: string;
}

export interface NodeAgentUpdateState {
  node_id: string;
  desired_release?: AgentRelease;
  actual_sequence: number;
  state: string;
  last_error?: string;
  last_report_at?: string;
  updated_at: string;
}

export interface AuditEntry {
  id: number;
  actor_type: string;
  actor_id?: string;
  action: string;
  resource_type: string;
  resource_id?: string;
  details: Record<string, unknown> | null;
  source_ip?: string;
  created_at: string;
}

export type PanelTheme = 'dark' | 'green' | 'rose' | 'cyan' | 'amber' | 'system';

export interface PanelSettings {
  theme: PanelTheme;
  accent: string;
  session_timeout_minutes: number;
  max_sessions: number;
  audit_retention_days: number;
  public_url: string;
  web_port: number;
  agent_port: number;
  metrics_enabled?: boolean;
  metrics_allow_cidrs?: number;
  updated_at?: string;
}

export type PanelSettingsUpdate = Pick<PanelSettings,
  'theme' | 'accent' | 'session_timeout_minutes' | 'max_sessions' | 'audit_retention_days'>;

export interface APIErrorPayload {
  error?: string | { code?: string; message?: string };
}

export type BootstrapAuthMode = 'password' | 'private_key';
export type BootstrapSudoMode = 'auto' | 'root' | 'password' | 'passwordless';

export interface HostKeyResult {
  algorithm: 'ssh-ed25519' | 'ecdsa-sha2-nistp256' | 'rsa-sha2-256';
  fingerprint: string;
  os?: string;
  arch?: string;
}

export interface BootstrapNodeRequest {
  name: string;
  address: string;
  ssh_port: number;
  username: string;
  auth_mode: BootstrapAuthMode;
  password?: string;
  private_key?: string;
  private_key_passphrase?: string;
  sudo_mode: BootstrapSudoMode;
  sudo_password?: string;
  agent_port: number;
  host_key_sha256: string;
  host_key_algorithm: HostKeyResult['algorithm'];
  allow_firewall_apply: boolean;
  release_id?: string;
}

export type BootstrapJobStatus = 'queued' | 'running' | 'installed' | 'failed';

export interface BootstrapJobJournalEntry {
  stage: string;
  status: BootstrapJobStatus;
  at: string;
}

export interface BootstrapJobResponse {
  job_id: string;
  status: BootstrapJobStatus;
  stage: string;
  node_id?: string;
  created_at: string;
  updated_at: string;
  journal?: BootstrapJobJournalEntry[];
  failure_summary?: string;
  failure_code?: string;
  exit_code?: number;
}
