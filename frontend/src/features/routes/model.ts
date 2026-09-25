import { randomUUID } from '../../lib/uuid';
import { agentVersionAtLeast } from '../../lib/agentVersion';
import type { RouteRecord } from '../../lib/contracts';
import { formatBytes } from '../../lib/format';
import { foldLeastConnCosts } from './leastconnFold';

export type RouteMatchMode = 'sni' | 'fallback';
export type RouteTargetMode = 'ip' | 'domain' | 'unix';
export type QuotaPeriod = 'hourly' | 'daily' | 'calendar_month' | 'monthly_from_creation';
export type QuotaAction = 'observe' | 'block_new';
export type ProxyProtocol = 'none' | 'v1' | 'v2';
export type BalanceMode = 'pool' | 'failover';
export type ShaperMode = 'haproxy' | 'kernel';

/** Client stickiness in pool mode: who a repeat connection lands on. */
export type StickyMode = 'none' | 'source' | 'source_table';
/** Which server a NEW client gets. static-rr is the "static weights" sub-choice of roundrobin. */
export type BalanceAlgorithm = 'roundrobin' | 'static-rr' | 'random' | 'leastconn' | 'leastping';
export type StickyHash = '' | 'map-based';
/**
 * «Распределение клиентов» top-level choice; a 1:1 view of stickyMode:
 * algorithm = none, hash = source, table = source_table.
 */
export type DistributionChoice = 'algorithm' | 'hash' | 'table';

/** One DNS-pool address override: weight and/or latency cost ('' = as the server). */
export interface IPWeightDraft {
  ip: string;
  weight: number | '';
  cost: number | '';
}

export interface ServerDraft {
  _key: string;
  name: string;
  targetType: 'tcp' | 'unix';
  host: string;
  port: number | '';
  unixSocketPath: string;
  /**
   * Failover role: false = primary (Основной), true = reserve (Резерв).
   * Several primaries share the load; reserves take over when every primary is
   * down. Ignored in pool mode.
   */
  backup: boolean;
  /** Resolve all A/AAAA records for this domain and use as a pool of HAProxy slots. */
  dnsPool: boolean;
  /** Optional preferred IP for a dns-pool server at failover position 1. */
  preferredIP: string;
  /** 1..256; empty = HAProxy default weight (not rendered). */
  weight: number | '';
  /** leastping only; effective latency = check_duration × cost. Empty = 1. */
  cost: number | '';
  /** dns-pool servers only: agent-applied runtime weight/cost per resolved address. */
  ipWeights: IPWeightDraft[];
}

/**
 * Legacy single-target origin of a loaded route (no servers[] rows). Kept so
 * an unchanged legacy route is re-saved with the same route-level fields
 * (including route-level dns_pool) and renders byte-identically.
 */
export interface LegacyTargetOrigin {
  targetType: 'tcp' | 'unix';
  host: string;
  port: number;
  unixSocketPath: string;
  dnsPool: boolean;
}

export interface RouteDraft {
  name: string;
  matchMode: RouteMatchMode;
  listenerIP: string;
  listenerPort: number | '';
  snis: string[];
  /** acceptProxyEnabled: derived from non-empty list on load; toggle in UI. */
  acceptProxyEnabled: boolean;
  /** acceptProxyFrom: chip-based, newline-separated IPs/CIDRs shown only when enabled. */
  acceptProxyFrom: string[];
  targetMode: RouteTargetMode;
  targetHost: string;
  targetPort: number | '';
  unixSocketPath: string;
  healthCheck: boolean;
  proxyProtocol: ProxyProtocol;
  /** servers: unified list; first entry is the primary destination. */
  servers: ServerDraft[];
  /**
   * balanceMode: 'pool' = roundrobin / consistent-hash; 'failover' = ordered backup.
   * Visible only when servers.length >= 2.
   */
  balanceMode: BalanceMode;
  /**
   * stickyMode: client stickiness in pool mode (ignored in failover).
   * null = not chosen by the operator: derived from the servers (DNS pool
   * => source, otherwise none), exactly like the backend default.
   */
  stickyMode: StickyMode | null;
  /** stickyTTL: source_table memory, HAProxy time. */
  stickyTTL: string;
  /** stickyHash: '' = consistent (default), 'map-based' = ровнее при смене серверов. */
  stickyHash: StickyHash;
  /** stickyHashBalanceFactor: '' = off, else 101..1000 % (only with consistent hash). */
  stickyHashBalanceFactor: number | '';
  /** stickyTableEntries: source_table stick-table size token (<n>k | <n>m); '' = «Авто» (sized from node RAM, autoTableEntries). */
  stickyTableEntries: string;
  /** stickyIPv6Prefix: ipmask grouping bits; 0 = default (hash: full address via bare `balance source`; table: /64). */
  stickyIPv6Prefix: number;
  /**
   * clientIPv6: stored client_ipv6 (default true). Off renders IPv4-only
   * client tables (stick-table type ip, key src; balance source; bwlim
   * tables type ip) for nodes without IPv6 and sends sticky_ipv6_prefix 0.
   */
  clientIPv6: boolean;
  /**
   * balanceAlgorithm: who gets a NEW connection. null = not chosen by the
   * operator: roundrobin, or leastconn when stickyMode is source_table
   * (rc12-compatible default).
   */
  balanceAlgorithm: BalanceAlgorithm | null;
  /** balanceRandomDraws: 1..5, only meaningful for balanceAlgorithm 'random'. '' ⇒ 2. */
  balanceRandomDraws: number | '';
  /**
   * leastpingTolerancePct: «Толерантность» in % (0..100). leastping: latency
   * tolerance, '' ⇒ 20. leastconn: connection-load tolerance, '' ⇒ 0 (plain
   * HAProxy leastconn). Sent as balance_tolerance.
   */
  leastpingTolerancePct: number | '';
  /** leastpingToleranceMs: absolute floor in ms, '' = off. */
  leastpingToleranceMs: number | '';
  /** slowstart: '' | HAProxy time (1s..10m); requires healthCheck. */
  slowstart: string;
  /** legacyTarget: set when the loaded route had no servers[] rows. */
  legacyTarget: LegacyTargetOrigin | null;
  quotaEnabled: boolean;
  quotaValue: number | '';
  quotaUnit: 'GiB' | 'TiB';
  quotaPeriod: QuotaPeriod;
  quotaAction: QuotaAction;
  clientBandwidthEnabled: boolean;
  clientUploadMbps: number | '';
  clientDownloadMbps: number | '';
  /** shaperMode: 'haproxy' = bwlim filters; 'kernel' = nftables on the node (keeps splice). */
  shaperMode: ShaperMode;
  expertOverride: string;
}

export interface RouteDraftError {
  field: keyof RouteDraft | 'listener' | 'target' | 'quota' | 'bandwidth' | 'expert' | 'servers' | 'acceptProxy';
  message: string;
  severity?: 'error' | 'warning';
}

const GIB = 1024 ** 3;
const TIB = 1024 ** 4;
const MAX_SAFE_BYTES = Number.MAX_SAFE_INTEGER;
const DNS_LABEL = /^(?!-)[a-z0-9-]{1,63}(?<!-)$/i;
// Octets without leading zeros: Go's net.ParseIP rejects 01.2.3.4.
const IPV4_OCTET = '(?:25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)';
const IPV4 = new RegExp(`^${IPV4_OCTET}(?:\\.${IPV4_OCTET}){3}$`);
const IPV4_CIDR = new RegExp(`^${IPV4_OCTET}(?:\\.${IPV4_OCTET}){3}(?:\\/(?:3[0-2]|[12]?\\d))?$`);
const IPV6_GROUP = /^[a-f0-9]{1,4}$/i;

/**
 * Parses an IPv6 literal the way Go's net.ParseIP does (hex groups only; the
 * editor never offered the dotted IPv4 tail) and returns its 8 groups, or
 * null. Rejects `1:::2`, `1::2::3`, `:::` and 9-group input.
 */
function parseIPv6Groups(value: string): number[] | null {
  const halves = value.split('::');
  if (halves.length > 2) return null;
  const groups = (part: string) => (part === '' ? [] : part.split(':'));
  const head = groups(halves[0]);
  const tail = halves.length === 2 ? groups(halves[1]) : [];
  if (![...head, ...tail].every((group) => IPV6_GROUP.test(group))) return null;
  const count = head.length + tail.length;
  if (halves.length === 2 ? count > 7 : count !== 8) return null;
  const zeros = Array<string>(8 - count).fill('0');
  return [...head, ...zeros, ...tail].map((group) => parseInt(group, 16));
}

function isIPv6(value: string): boolean {
  return parseIPv6Groups(value) !== null;
}

/** Canonical text of an IP address for duplicate checks (IPv6 case and zero compression folded). */
export function canonicalIP(value: string): string {
  const trimmed = value.trim();
  const groups = trimmed.includes(':') ? parseIPv6Groups(trimmed) : null;
  return groups ? groups.map((group) => group.toString(16)).join(':') : trimmed;
}
const FORBIDDEN_SECTIONS = new Set(['global', 'defaults', 'frontend', 'backend', 'listen', 'peers', 'resolvers', 'userlist', 'mailers', 'cache', 'program', 'ring', 'http-errors']);
/** Mirrors the backend limit: 58 leaves room for the `_pref` / `_N` HAProxy suffixes. */
export const SERVER_NAME_MAX = 58;
const SERVER_NAME_RE = new RegExp(`^[a-zA-Z0-9_-]{1,${SERVER_NAME_MAX}}$`);

/** accept_proxy_from sent when PROXY is accepted from everyone (toggle on, empty list). */
export const ACCEPT_PROXY_FROM_ALL = ['0.0.0.0/0', '::/0'] as const;

/** True when a stored accept_proxy_from list means "expect PROXY from every client". */
export function isAcceptProxyFromAll(list: readonly string[]): boolean {
  const set = new Set(list.map((value) => value.trim().toLowerCase()));
  if (set.size === 1) return set.has('0.0.0.0/0');
  return set.size === 2 && set.has('0.0.0.0/0') && set.has('::/0');
}

export const DEFAULT_STICKY_TTL = '1h';

export const DEFAULT_LEASTPING_TOLERANCE_PCT = 20;

/** «Как выбирать сервер»: the three distribution choices (stickyMode 1:1). */
export const distributionChoiceOptions: { value: DistributionChoice; label: string; description: string }[] = [
  { value: 'algorithm', label: 'По алгоритму', description: 'Каждое новое соединение получает сервер по алгоритму.' },
  { value: 'hash', label: 'Закреплять по IP', description: 'Сервер определяется IP клиента, алгоритм не нужен.' },
  { value: 'table', label: 'Запоминать клиента', description: 'Первый сервер выбирает алгоритм, дальше клиент остаётся на нём.' },
];

export function distributionChoice(mode: StickyMode): DistributionChoice {
  if (mode === 'source') return 'hash';
  if (mode === 'source_table') return 'table';
  return 'algorithm';
}

export function stickyModeForChoice(choice: DistributionChoice): StickyMode {
  if (choice === 'hash') return 'source';
  if (choice === 'table') return 'source_table';
  return 'none';
}

/** «Помнить» quick picks (source_table TTL). */
export const stickyTTLQuickPicks = ['1m', '5m', '15m', '30m', '1h', '12h', '24h'] as const;
export const STICKY_TTL_MIN_SECONDS = 60;
export const STICKY_TTL_MAX_SECONDS = 7 * 24 * 3600;

/** Parses an HAProxy time (<n>s|m|h|d) into seconds; null when malformed. */
export function haproxyTimeSeconds(value: string): number | null {
  const match = /^([1-9]\d{0,6})([smhd])$/.exec(value.trim().toLowerCase());
  if (!match) return null;
  const n = Number(match[1]);
  return n * ({ s: 1, m: 60, h: 3600, d: 86400 } as const)[match[2] as 's' | 'm' | 'h' | 'd'];
}

export type TTLUnit = 's' | 'm' | 'h';

/**
 * Number + unit view of a stored TTL. Days are shown in hours (the UI offers
 * мин/ч); seconds are kept as seconds so nothing is rounded. The stored
 * string is only rewritten when the operator edits it.
 */
export function ttlParts(value: string): { amount: number | ''; unit: TTLUnit } {
  const match = /^([1-9]\d{0,6})([smhd])$/.exec(value.trim().toLowerCase());
  if (!match) return { amount: '', unit: 'm' };
  const n = Number(match[1]);
  if (match[2] === 'd') return { amount: n * 24, unit: 'h' };
  return { amount: n, unit: match[2] as TTLUnit };
}

export const ttlUnitLabels: Record<TTLUnit, string> = { s: 'сек', m: 'мин', h: 'ч' };

export function ttlQuickPickLabel(value: string): string {
  const { amount, unit } = ttlParts(value);
  return `${amount} ${ttlUnitLabels[unit]}`;
}

export type TableUnit = 'k' | 'm';
/** HAProxy size suffixes: k = 1024, m = 1024². */
const TABLE_UNIT_ENTRIES: Record<TableUnit, number> = { k: 1024, m: 1024 * 1024 };
export const TABLE_MAX: Record<TableUnit, number> = { k: 10000, m: 10 };
export const tableUnitLabels: Record<TableUnit, string> = { k: 'тыс.', m: 'млн' };
/**
 * Measured HAProxy 3.4.2 RSS per stick-table entry with server_id (≈226-228 B;
 * `type ip` measured the same as `type ipv6`). Allocated as the table fills.
 */
export const STICK_TABLE_ENTRY_BYTES = 228;

export function tableEntriesParts(value: string): { amount: number | ''; unit: TableUnit } {
  const match = /^([1-9]\d{0,4})([km])$/.exec(value.trim().toLowerCase());
  if (!match) return { amount: '', unit: 'k' };
  return { amount: Number(match[1]), unit: match[2] as TableUnit };
}

export function validTableEntries(value: string): boolean {
  if (value === '') return true;
  const { amount, unit } = tableEntriesParts(value);
  return amount !== '' && amount >= 1 && amount <= TABLE_MAX[unit];
}

/** '' («Авто») renders this size when node RAM is unknown (node_facts.go). */
export const DEFAULT_TABLE_ENTRIES = '1m';
/** Largest table the backend accepts: 10m (10000k is smaller). */
export const TABLE_MAX_CLIENTS = TABLE_MAX.m * TABLE_UNIT_ENTRIES.m;

/** Number of clients a size token holds ('' ⇒ the «Авто» default). */
export function tableEntriesCount(value: string): number {
  const { amount, unit } = tableEntriesParts(value || DEFAULT_TABLE_ENTRIES);
  return amount === '' ? 0 : amount * TABLE_UNIT_ENTRIES[unit];
}

/**
 * «Авто» stick-table size, mirroring NodeRenderFacts.AutoStickyTableEntries
 * (internal/panel/node_facts.go): 5 % of node RAM (bucketed down to 256 MiB)
 * / 228 B per entry, shared by every enabled «Авто» table on the node,
 * clamped to 100k..10m and rounded down to whole k. Unknown RAM ⇒ 1m.
 */
export function autoTableEntries(memoryTotalBytes: number | undefined, autoTables: number): string {
  const bucket = 256 * 1024 * 1024;
  const memory = memoryTotalBytes && Number.isFinite(memoryTotalBytes) && memoryTotalBytes > 0
    ? Math.floor(memoryTotalBytes / bucket) * bucket
    : 0;
  if (memory <= 0) return DEFAULT_TABLE_ENTRIES;
  const tables = Math.max(1, Math.floor(autoTables));
  // Integer division at every step, like the Go code.
  let entries = Math.floor(Math.floor(Math.floor(memory / 20) / 228) / tables);
  entries = Math.max(entries, 100 * TABLE_UNIT_ENTRIES.k);
  if (entries >= TABLE_MAX_CLIENTS) return `${TABLE_MAX.m}m`;
  return `${Math.floor(entries / TABLE_UNIT_ENTRIES.k)}k`;
}

/** Whether a stored route renders an «Авто» source_table stick-table. */
export function routeUsesAutoTable(route: Pick<RouteRecord, 'enabled' | 'sticky_mode' | 'sticky_table_entries' | 'balance_mode'>): boolean {
  return route.enabled && route.sticky_mode === 'source_table' && !route.sticky_table_entries && route.balance_mode !== 'failover';
}

/**
 * Effective «Авто» size for the edited route: it will be one of the enabled
 * auto tables once applied, so it shares RAM with the other enabled ones.
 */
export function draftAutoTableEntries(peers: RouteRecord[], memoryTotalBytes: number | undefined): string {
  return autoTableEntries(memoryTotalBytes, peers.filter(routeUsesAutoTable).length + 1);
}

/**
 * Size token for a plain client count: rounded up to whole k (1024), or to m
 * when it is an exact multiple of 1024² or too big for k. Returns '' for an
 * empty/invalid count.
 */
export function tableEntriesToken(count: number | ''): string {
  if (count === '' || !Number.isFinite(count) || count <= 0) return '';
  const clamped = Math.min(Math.ceil(count), TABLE_MAX_CLIENTS);
  if (clamped % TABLE_UNIT_ENTRIES.m === 0) return `${clamped / TABLE_UNIT_ENTRIES.m}m`;
  const k = Math.ceil(clamped / TABLE_UNIT_ENTRIES.k);
  if (k <= TABLE_MAX.k) return `${k}k`;
  return `${Math.ceil(clamped / TABLE_UNIT_ENTRIES.m)}m`;
}

/** «102 400» — Russian thousands grouping. */
export function formatCount(value: number): string {
  return value.toLocaleString('ru-RU').replace(/\u00a0/g, ' ');
}

/** «Авто» hint: «Авто: ~941 000 клиентов (5% RAM ноды), ≈205 МБ при полной таблице». */
export function autoTableHint(effective: string, memoryKnown: boolean): string {
  // Unknown RAM renders 1m (1 048 576 entries).
  if (!memoryKnown) return 'Авто: 1 000 000, RAM ноды неизвестна';
  const clients = Math.round(tableEntriesCount(effective) / 1000) * 1000;
  return `Авто: ~${formatCount(clients)} клиентов (5% RAM ноды), ${tableMemoryEstimate(effective)} при полной таблице`;
}

/** Approximate memory at full fill, e.g. «≈22 МБ». */
export function tableMemoryEstimate(value: string): string {
  const entries = tableEntriesCount(value);
  if (entries === 0) return '';
  const mib = (entries * STICK_TABLE_ENTRY_BYTES) / (1024 * 1024);
  if (mib >= 1024) return `≈${(mib / 1024).toFixed(1).replace('.', ',')} ГБ`;
  if (mib >= 10) return `≈${Math.round(mib)} МБ`;
  return `≈${mib.toFixed(1).replace('.', ',')} МБ`;
}

/** «Алгоритм» choices (static-rr is roundrobin with «Веса меняются на лету» off). */
export const balanceAlgorithmOptions: { value: BalanceAlgorithm; label: string; description: string }[] = [
  { value: 'roundrobin', label: 'По кругу', description: 'Серверы получают клиентов по очереди, с учётом веса.' },
  { value: 'random', label: 'Случайно', description: 'Случайный сервер с учётом веса.' },
  { value: 'leastconn', label: 'Меньше соединений', description: 'Сервер, где сейчас меньше всего соединений.' },
  { value: 'leastping', label: 'Меньше задержка', description: 'Больше трафика серверам с меньшей задержкой (как leastLoad в Xray).' },
];

/** Top-level select family for a stored algorithm (static-rr collapses into roundrobin). */
export function balanceAlgorithmFamily(algorithm: BalanceAlgorithm): BalanceAlgorithm {
  return algorithm === 'static-rr' ? 'roundrobin' : algorithm;
}

/** Slowstart view in seconds ('' = off); the stored string is rewritten only on edit. */
export function slowstartSeconds(value: string): number | '' {
  if (!value) return '';
  return haproxyTimeSeconds(value) ?? '';
}

const LEGACY_STICKY_NONE: readonly string[] = ['leastconn', 'roundrobin'];

/**
 * Stored sticky_mode wins, mapped through the legacy compat rules (mirrors
 * route_validation.go resolveStickyMode): leastconn/roundrobin become
 * sticky none (their algorithm is picked up by loadedBalanceAlgorithm below);
 * sni (removed) becomes source; rows from before 000051 fall back to sticky_enabled.
 */
function loadedStickyMode(route: RouteRecord): StickyMode | null {
  const stored = route.sticky_mode ?? '';
  if (stored === '') return route.sticky_enabled ? 'source' : null;
  if (LEGACY_STICKY_NONE.includes(stored)) return 'none';
  if (stored === 'sni') return 'source';
  if (stored === 'none' || stored === 'source' || stored === 'source_table') return stored;
  return null;
}

/**
 * Stored balance_algorithm wins; legacy sticky_mode leastconn/roundrobin values
 * (pre-000052 rows) are picked up as the algorithm when no algorithm is stored.
 * Everything else is left null (auto): roundrobin, or leastconn for source_table
 * (see effectiveBalanceAlgorithm), matching the backend's absent-field default.
 */
function loadedBalanceAlgorithm(route: RouteRecord): BalanceAlgorithm | null {
  const stored = route.balance_algorithm ?? '';
  if (stored) return stored;
  const stickyStored = route.sticky_mode ?? '';
  if (stickyStored === 'leastconn') return 'leastconn';
  if (stickyStored === 'roundrobin') return 'roundrobin';
  return null;
}

export const quotaPeriodOptions = [
  { value: 'hourly', label: 'Каждый час', description: 'Сбрасывается в начале следующего часа.' },
  { value: 'daily', label: 'Ежедневно', description: 'Сбрасывается в 00:00 UTC.' },
  { value: 'calendar_month', label: 'Календарный месяц', description: 'Сбрасывается первого числа в 00:00 UTC.' },
  { value: 'monthly_from_creation', label: 'Месяц от создания', description: 'Окно начинается в дату создания маршрута.' },
] as const;

export function routeDisplayName(route: RouteRecord): string {
  if (route.name?.trim()) return route.name.trim();
  if (route.fallback) return `tcp-${route.listener_port}`;
  return route.snis[0] ?? route.hostname ?? `route-${route.listener_port}`;
}

/** Returns the first free `srvN` name so new servers never start with an empty required field. */
export function nextServerName(servers: Pick<ServerDraft, 'name'>[]): string {
  const used = new Set(servers.map((s) => s.name.trim()));
  for (let n = servers.length + 1; ; n += 1) {
    if (!used.has(`srv${n}`)) return `srv${n}`;
  }
}

export function emptyServerDraft(name = 'srv1'): ServerDraft {
  return {
    _key: randomUUID(), name, targetType: 'tcp', host: '', port: 443, unixSocketPath: '', backup: false,
    dnsPool: false, preferredIP: '', weight: '', cost: '', ipWeights: [],
  };
}

export function emptyRouteDraft(): RouteDraft {
  return {
    name: '', matchMode: 'sni', listenerIP: '*', listenerPort: 443, snis: [],
    acceptProxyEnabled: false, acceptProxyFrom: [],
    targetMode: 'ip', targetHost: '', targetPort: 443, unixSocketPath: '', healthCheck: true,
    proxyProtocol: 'none',
    servers: [emptyServerDraft()],
    balanceMode: 'pool', stickyMode: null, stickyTTL: DEFAULT_STICKY_TTL, legacyTarget: null,
    stickyHash: '', stickyHashBalanceFactor: '', stickyTableEntries: '', stickyIPv6Prefix: 0, clientIPv6: true,
    balanceAlgorithm: null, balanceRandomDraws: '', leastpingTolerancePct: '', leastpingToleranceMs: '', slowstart: '',
    quotaEnabled: false, quotaValue: '', quotaUnit: 'GiB',
    quotaPeriod: 'calendar_month', quotaAction: 'observe', expertOverride: '',
    clientBandwidthEnabled: false, clientUploadMbps: '', clientDownloadMbps: '', shaperMode: 'haproxy',
  };
}

export function routeToDraft(route: RouteRecord): RouteDraft {
  const bytes = route.quota_bytes ?? 0;
  const useTiB = bytes >= TIB && bytes % TIB === 0;
  const listenerIP = route.listener_ip || '*';
  const matchMode: RouteMatchMode = route.match_mode === 'sni' ? 'sni' : 'fallback';
  const host = route.target_host ?? '';
  const targetMode: RouteTargetMode = route.target_type === 'unix'
    ? 'unix'
    : isIPAddress(host) ? 'ip' : 'domain';

  // Build servers list. If no explicit servers, create synthetic first entry from route target.
  let servers: ServerDraft[];
  let legacyTarget: LegacyTargetOrigin | null = null;
  if ((route.servers ?? []).length > 0) {
    servers = (route.servers ?? [])
      .slice()
      .sort((a, b) => a.position - b.position)
      .map((s) => ({
        _key: s.id || randomUUID(),
        name: s.name,
        targetType: (s.target_type === 'unix' ? 'unix' : 'tcp') as 'tcp' | 'unix',
        host: s.host ?? '',
        port: s.port || 443,
        unixSocketPath: s.unix_socket_path ?? '',
        backup: Boolean(s.backup),
        dnsPool: Boolean(s.dns_pool),
        preferredIP: s.preferred_ip ?? '',
        weight: s.weight || '',
        cost: s.cost || '',
        ipWeights: (s.ip_weights ?? []).map((w) => ({ ip: w.ip, weight: w.weight || '', cost: w.cost || '' })),
      }));
  } else {
    // Synthesize first server from legacy single-target fields.
    legacyTarget = {
      targetType: route.target_type === 'unix' ? 'unix' : 'tcp',
      host,
      port: route.target_port || 443,
      unixSocketPath: route.unix_socket_path ?? '',
      dnsPool: Boolean(route.dns_pool),
    };
    servers = [{
      _key: randomUUID(),
      name: 'srv1',
      targetType: legacyTarget.targetType,
      host: legacyTarget.host,
      port: legacyTarget.port,
      unixSocketPath: legacyTarget.unixSocketPath,
      backup: false,
      dnsPool: legacyTarget.dnsPool,
      preferredIP: '',
      weight: '', cost: '', ipWeights: [],
    }];
  }

  const storedAcceptProxy = route.accept_proxy_from ?? [];
  // "From everyone" is shown as an enabled toggle with an empty trusted list.
  const acceptProxyFromAll = isAcceptProxyFromAll(storedAcceptProxy);
  const acceptProxyFrom = acceptProxyFromAll ? [] : storedAcceptProxy;
  // rc-era rows may carry explicit backup flags without balance_mode=failover;
  // loading them as 'pool' would turn every backup into a primary on save.
  // Stored backup flags are authoritative (e.g. 2 primaries + 1 reserve) and
  // are kept as per-server roles; only a failover row without any flag (should
  // not happen) falls back to the canonical first-primary layout.
  const balanceMode: BalanceMode = route.balance_mode === 'failover' || servers.some((s) => s.backup)
    ? 'failover' : 'pool';
  if (balanceMode === 'failover' && servers.length > 1 && !servers.some((s) => s.backup)) {
    servers = withCanonicalFailoverRoles(servers);
  }

  const draft: RouteDraft = {
    name: routeDisplayName(route), matchMode, listenerIP, listenerPort: route.listener_port,
    snis: route.snis ?? [],
    acceptProxyEnabled: storedAcceptProxy.length > 0,
    acceptProxyFrom,
    targetMode, targetHost: host, targetPort: route.target_port || 443,
    unixSocketPath: route.unix_socket_path ?? '', healthCheck: route.health_check ?? true,
    proxyProtocol: (['none', 'v1', 'v2'].includes(route.proxy_protocol) ? route.proxy_protocol : 'none') as ProxyProtocol,
    servers,
    balanceMode,
    stickyMode: loadedStickyMode(route),
    stickyTTL: route.sticky_ttl || DEFAULT_STICKY_TTL,
    stickyHash: route.sticky_hash === 'map-based' ? 'map-based' : '',
    stickyHashBalanceFactor: route.sticky_hash_balance_factor || '',
    stickyTableEntries: validTableEntries(route.sticky_table_entries ?? '') ? (route.sticky_table_entries ?? '') : '',
    stickyIPv6Prefix: route.sticky_ipv6_prefix || 0,
    clientIPv6: route.client_ipv6 ?? true,
    balanceAlgorithm: loadedBalanceAlgorithm(route),
    balanceRandomDraws: route.balance_random_draws || '',
    leastpingTolerancePct: loadedTolerancePct(route),
    leastpingToleranceMs: route.leastping_tolerance_ms || '',
    slowstart: route.slowstart || '',
    legacyTarget,
    quotaEnabled: bytes > 0, quotaValue: bytes > 0 ? bytes / (useTiB ? TIB : GIB) : '',
    quotaUnit: useTiB ? 'TiB' : 'GiB',
    quotaPeriod: (['hourly', 'daily', 'calendar_month', 'monthly_from_creation'].includes(route.quota_period)
      ? route.quota_period : 'calendar_month') as QuotaPeriod,
    quotaAction: route.quota_action === 'block_new' ? 'block_new' : 'observe',
    clientBandwidthEnabled: route.client_upload_mbps !== null || route.client_download_mbps !== null,
    clientUploadMbps: route.client_upload_mbps ?? '', clientDownloadMbps: route.client_download_mbps ?? '',
    shaperMode: route.shaper_mode === 'kernel' ? 'kernel' : 'haproxy',
    expertOverride: route.custom_fragment ?? '',
  };
  // «Меньше соединений» has no cost column: a stored cost is an inverse
  // weight there, so it is folded into weights with the same ratios.
  if (leastconnActive(draft)) {
    const folded = foldLeastConnCosts(draft.servers);
    if (folded) draft.servers = folded;
  }
  return draft;
}

export function isIPAddress(value: string): boolean {
  const trimmed = value.trim();
  return IPV4.test(trimmed) || (trimmed.includes(':') && isIPv6(trimmed));
}

/**
 * True for dotted all-numeric input like `1.2.3`, `010.0.0.1` or `300.1.1.1`.
 * Such input is meant as an IPv4 address, so it is never accepted as a DNS name
 * (mirrors the backend rule: all labels numeric => must be a valid IPv4).
 */
export function looksLikeNumericAddress(value: string): boolean {
  const normalized = value.trim().replace(/\.$/, '');
  return normalized !== '' && normalized.split('.').every((label) => /^\d+$/.test(label));
}

/** Valid backend host: an IP address or a DNS name (never a malformed numeric IP). */
export function isValidTargetHost(value: string): boolean {
  const trimmed = value.trim();
  return isIPAddress(trimmed) || isDNSName(trimmed);
}

/** Balance mode selector is meaningful for 2+ servers or any DNS pool (pool of addresses). */
export function balanceModeApplicable(draft: Pick<RouteDraft, 'servers'>): boolean {
  return draft.servers.length >= 2 || draft.servers.some((s) => s.targetType === 'tcp' && s.dnsPool);
}

/** Balance mode actually sent to the backend. */
/**
 * «Толерантность, %» of a stored route. Stored 0 is a real 0 % only for a
 * leastping route (its default is 20 %); for leastconn 0 is the default and
 * shows as empty.
 */
function loadedTolerancePct(route: RouteRecord): number | '' {
  const tolerance = route.balance_tolerance ?? route.leastping_tolerance;
  if (typeof tolerance !== 'number') return '';
  if (route.balance_algorithm === 'leastping') return Math.round(tolerance * 100);
  if (route.balance_algorithm === 'leastconn' && tolerance > 0) return Math.round(tolerance * 100);
  return '';
}

export function effectiveBalanceMode(draft: Pick<RouteDraft, 'servers' | 'balanceMode'>): BalanceMode {
  return balanceModeApplicable(draft) ? draft.balanceMode : 'pool';
}

/** First Node Agent release that enforces shaper_mode=kernel (nftables). */
export const KERNEL_SHAPER_MIN_AGENT = '1.1.0';

/**
 * False only when the node reports an Agent version older than 1.1.0. Unknown
 * or unparsable versions are allowed: the backend is authoritative and rejects
 * with kernel_shaper_requires_agent_1_1. Pre-release suffixes (1.1.0-rc11) are
 * compared by their numeric core, like the rc builds that already ship the shaper.
 */
export function kernelShaperSupported(agentVersion: string | undefined | null): boolean {
  return agentVersionAtLeast(agentVersion, KERNEL_SHAPER_MIN_AGENT);
}

/** Failover role of a server as sent to the backend (always primary in pool mode). */
export function serverIsBackup(draft: Pick<RouteDraft, 'servers' | 'balanceMode'>, index: number): boolean {
  // A lone server is always the primary, whatever role it had before removals.
  return draft.servers.length > 1 && effectiveBalanceMode(draft) === 'failover' && Boolean(draft.servers[index]?.backup);
}

/**
 * Canonical failover layout: the first server is the only primary and every
 * later server is a reserve. Only this layout is sent as balance_mode=failover
 * (the backend then derives backup flags from order). Anything else — several
 * primaries, a reserve before a primary — is sent with explicit backup flags and
 * no balance_mode, which the backend keeps as stored.
 */
export function isCanonicalFailover(servers: Pick<ServerDraft, 'backup'>[]): boolean {
  return servers.every((s, i) => s.backup === (i > 0));
}

/** Roles for a switch into failover: first server primary, the rest reserves. */
export function withCanonicalFailoverRoles(servers: ServerDraft[]): ServerDraft[] {
  return servers.map((s, i) => (s.backup === (i > 0) ? s : { ...s, backup: i > 0, preferredIP: i > 0 ? '' : s.preferredIP }));
}

/** preferred_ip is only valid on the first DNS-pool server in failover mode. */
export function preferredIPApplies(draft: Pick<RouteDraft, 'servers' | 'balanceMode'>, index: number): boolean {
  const server = draft.servers[index];
  return index === 0 && Boolean(server) && server.targetType === 'tcp' && server.dnsPool
    && effectiveBalanceMode(draft) === 'failover' && !serverIsBackup(draft, 0);
}

/** Client distribution matters only in pool mode with several candidates. */
export function stickyApplicable(draft: Pick<RouteDraft, 'servers' | 'balanceMode'>): boolean {
  if (effectiveBalanceMode(draft) !== 'pool') return false;
  return draft.servers.length >= 2 || draft.servers.some((s) => s.targetType === 'tcp' && s.dnsPool);
}

export function hasDNSPoolServer(draft: Pick<RouteDraft, 'servers'>): boolean {
  return draft.servers.some((s) => s.targetType === 'tcp' && s.dnsPool);
}

/** Default stickiness: a DNS pool is source-hash sticky (1.0.8 behaviour), anything else none. */
export function defaultStickyMode(draft: Pick<RouteDraft, 'servers'>): StickyMode {
  return hasDNSPoolServer(draft) ? 'source' : 'none';
}

/** sticky_mode actually used: the operator's choice if the row is shown, else the default. */
export function effectiveStickyMode(draft: Pick<RouteDraft, 'servers' | 'balanceMode' | 'stickyMode'>): StickyMode {
  if (!stickyApplicable(draft)) return defaultStickyMode(draft);
  return draft.stickyMode ?? defaultStickyMode(draft);
}

/** Default balancing algorithm: leastconn for source_table (rc12-compatible), otherwise roundrobin. */
export function defaultBalanceAlgorithm(draft: Pick<RouteDraft, 'servers' | 'balanceMode' | 'stickyMode'>): BalanceAlgorithm {
  return effectiveStickyMode(draft) === 'source_table' ? 'leastconn' : 'roundrobin';
}

/**
 * balance_algorithm actually used: the operator's choice if still meaningful,
 * else the default. Sticky = source hashes on IP, so the algorithm never
 * takes effect — the UI greys the control but this still returns a sane
 * value (unused by the renderer/payload in that case).
 */
export function effectiveBalanceAlgorithm(
  draft: Pick<RouteDraft, 'servers' | 'balanceMode' | 'stickyMode' | 'balanceAlgorithm'>,
): BalanceAlgorithm {
  if (effectiveStickyMode(draft) === 'source') return defaultBalanceAlgorithm(draft);
  return draft.balanceAlgorithm ?? defaultBalanceAlgorithm(draft);
}

/** DNS-pool templates always carry health checks, so the route check is forced on. */
export function effectiveHealthCheck(draft: Pick<RouteDraft, 'servers' | 'healthCheck'>): boolean {
  return draft.healthCheck || draft.servers.some((s) => s.targetType === 'tcp' && s.dnsPool);
}

function hasClientBandwidth(draft: RouteDraft): boolean {
  return draft.clientBandwidthEnabled && (draft.clientUploadMbps !== '' || draft.clientDownloadMbps !== '');
}

/**
 * payloadShape decides between the legacy single-target payload (no servers[])
 * and the multi-server payload. A single plain target keeps the legacy shape so
 * HAProxy keeps the stable `nf_srv_<id>` server name that runtime quota
 * enforcement relies on. Route-level dns_pool is only kept for an unchanged
 * legacy DNS-pool route; otherwise DNS pools travel per server.
 */
export function payloadShape(draft: RouteDraft): { kind: 'legacy'; dnsPool: boolean } | { kind: 'servers' } {
  if (draft.servers.length !== 1) return { kind: 'servers' };
  const [s] = draft.servers;
  // Per-IP rows left blank (or cost-only outside latency mode) are not sent.
  const untouchedRuntime = s.weight === '' && s.cost === '' && ipWeightsPayload(s.ipWeights, costActive(draft)).length === 0;
  if (s.targetType === 'unix' || !s.dnsPool) return untouchedRuntime ? { kind: 'legacy', dnsPool: false } : { kind: 'servers' };
  const legacy = draft.legacyTarget;
  const preferred = preferredIPApplies(draft, 0) && s.preferredIP.trim() !== '';
  if (untouchedRuntime && legacy && legacy.dnsPool && legacy.targetType === 'tcp' && !preferred
    && legacy.host.trim().toLowerCase() === s.host.trim().toLowerCase() && legacy.port === Number(s.port)) {
    return { kind: 'legacy', dnsPool: true };
  }
  return { kind: 'servers' };
}

/**
 * DNS name as the backend accepts it (validDNSName): the last label is never
 * a decimal or 0x-hex number, so `backend.1` or `host.0x1f` are rejected.
 */
function isDNSName(value: string): boolean {
  const normalized = value.trim().replace(/\.$/, '').toLowerCase();
  const labels = normalized.split('.');
  return normalized.length > 0 && normalized.length <= 253
    && labels.every((label) => DNS_LABEL.test(label))
    && !/^(?:\d+|0x[0-9a-f]+)$/.test(labels[labels.length - 1]);
}

function isWellFormedUTF16(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false;
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return false;
    }
  }
  return true;
}

function isCanonicalUnixSocketPath(value: string): boolean {
  const bytes = new TextEncoder().encode(value).length;
  if (!isWellFormedUTF16(value) || bytes < 2 || bytes > 107 || !value.startsWith('/')) return false;
  if (!/^[A-Za-z0-9/._-]+$/.test(value) || value.endsWith('/') || value.includes('//')) return false;
  return value.slice(1).split('/').every((segment) => segment !== '' && segment !== '.' && segment !== '..');
}

function listenerOverlaps(leftIP: string, leftPort: number, rightIP: string, rightPort: number): boolean {
  if (leftPort !== rightPort) return false;
  const leftWildcard = leftIP === '*' || leftIP === '0.0.0.0' || leftIP === '::';
  const rightWildcard = rightIP === '*' || rightIP === '0.0.0.0' || rightIP === '::';
  return leftIP === rightIP || leftWildcard || rightWildcard;
}

function sameListener(leftIP: string, leftPort: number, rightIP: string, rightPort: number): boolean {
  return leftIP.toLowerCase() === rightIP.toLowerCase() && leftPort === rightPort;
}

/** Validates a single IPv4/IPv6 address or CIDR (accept_proxy_from entries). */
export function isValidIPOrCIDR(value: string): boolean {
  const trimmed = value.trim();
  if (!trimmed) return false;
  if (trimmed.includes(':')) {
    const slashIdx = trimmed.indexOf('/');
    if (slashIdx === -1) return isIPv6(trimmed);
    const groups = parseIPv6Groups(trimmed.slice(0, slashIdx));
    const prefix = trimmed.slice(slashIdx + 1);
    if (!groups) return false;
    const num = Number(prefix);
    if (!Number.isInteger(num) || num < 0 || num > 128 || String(num) !== prefix) return false;
    // Backend (normalizeAcceptProxyCIDR): an IPv4-mapped prefix must be written
    // as IPv4, and /0 is only allowed as exactly ::/0.
    if (groups.slice(0, 5).every((group) => group === 0) && groups[5] === 0xffff) return false;
    return num !== 0 || groups.every((group) => group === 0);
  }
  if (!IPV4_CIDR.test(trimmed)) return false;
  const [addr, prefix] = trimmed.split('/');
  return prefix !== '0' || addr === '0.0.0.0';
}

/** Maximum hostnames per route in accept_proxy_from (backend MaxAcceptProxyFromDomains). */
export const MAX_ACCEPT_PROXY_DOMAINS = 32;

/** True for an accept_proxy_from hostname entry (not an IP/CIDR). */
export function isTrustedProxyHostname(value: string): boolean {
  const trimmed = value.trim();
  return !isValidIPOrCIDR(trimmed) && !trimmed.includes('/') && !trimmed.includes(':') && isDNSName(trimmed);
}

/**
 * Validates one accept_proxy_from entry: IPv4/IPv6 address, CIDR or hostname.
 * A hostname stands for every A/AAAA address of the name; the Node Agent
 * (>= 1.1.3) resolves it and keeps the trusted set current at runtime.
 */
export function isValidTrustedProxyEntry(value: string): boolean {
  return isValidIPOrCIDR(value) || isTrustedProxyHostname(value);
}

function normalizeTrustedProxyEntry(value: string): string {
  const trimmed = value.trim();
  return isTrustedProxyHostname(trimmed) ? trimmed.replace(/\.$/, '').toLowerCase() : trimmed;
}

export function quotaBytes(draft: RouteDraft): number | null {
  if (!draft.quotaEnabled || draft.quotaValue === '') return null;
  const multiplier = draft.quotaUnit === 'TiB' ? TIB : GIB;
  return Math.round(Number(draft.quotaValue) * multiplier);
}

export function validateExpertOverride(value: string): RouteDraftError[] {
  const encoder = new TextEncoder();
  if (encoder.encode(value).length > 8192) {
    return [{ field: 'expert', message: 'Экспертный слой превышает 8192 байт.' }];
  }
  if (!isWellFormedUTF16(value)) {
    return [{ field: 'expert', message: 'Экспертный слой должен быть корректным UTF-8.' }];
  }
  const normalizedInput = value.replace(/\r\n/g, '\n');
  if (/[\u0000-\u0008\u000b-\u001f\u007f]/.test(normalizedInput)) {
    return [{ field: 'expert', message: 'Экспертный слой содержит управляющий символ.' }];
  }
  if (normalizedInput.includes('\\')) {
    return [{ field: 'expert', message: 'Перенос и экранирование директив через \\ запрещены.' }];
  }

  const normalizedLines: string[] = [];
  let blankPending = false;
  for (const rawLine of normalizedInput.split('\n')) {
    const line = rawLine.trim();
    // Backend counts the directive without indentation (stored fragments are indented).
    if (encoder.encode(line).length > 512) {
      return [{ field: 'expert', message: 'Одна из директив длиннее 512 байт.' }];
    }
    if (!line) {
      if (normalizedLines.length > 0) blankPending = true;
      continue;
    }
    const directive = line.split(/\s+/)[0].toLowerCase();
    if (FORBIDDEN_SECTIONS.has(directive)) {
      return [{ field: 'expert', message: `Секция ${directive} управляется NodeFlow и не может быть переопределена.` }];
    }
    if (blankPending) {
      normalizedLines.push('');
      blankPending = false;
    }
    normalizedLines.push(`    ${line}`);
  }
  if (encoder.encode(normalizedLines.join('\n')).length > 8192) {
    return [{ field: 'expert', message: 'Экспертный слой превышает 8192 байт после нормализации.' }];
  }
  return [];
}

/** Range checks for the «Распределение клиентов» inputs that actually travel in the payload. */
export function validateDistribution(draft: RouteDraft): RouteDraftError[] {
  const errors: RouteDraftError[] = [];
  if (effectiveBalanceMode(draft) !== 'pool' || !stickyApplicable(draft)) return errors;
  const add = (field: RouteDraftError['field'], message: string) => errors.push({ field, message });
  const inRange = (value: number | '', min: number, max: number, integer = true) => value === ''
    || (Number.isFinite(value) && value >= min && value <= max && (!integer || Number.isInteger(value)));
  const sticky = effectiveStickyMode(draft);
  const algorithm = effectiveBalanceAlgorithm(draft);
  if (sticky !== 'source') {
    if (algorithm === 'random' && !inRange(draft.balanceRandomDraws, 1, 5)) add('balanceRandomDraws', 'Выборок — от 1 до 5.');
    if (algorithm === 'leastping') {
      if (!inRange(draft.leastpingTolerancePct, 0, 100)) add('leastpingTolerancePct', 'Толерантность — целое число от 0 до 100 %.');
      if (!inRange(draft.leastpingToleranceMs, 0, 1000)) add('leastpingToleranceMs', 'Толерантность в мс — от 0 до 1000.');
    }
    if (algorithm === 'leastconn' && !inRange(draft.leastpingTolerancePct, 0, 100)) {
      add('leastpingTolerancePct', 'Толерантность — целое число от 0 до 100 %.');
    }
  }
  if (sticky === 'source' && draft.stickyHash !== 'map-based' && !inRange(draft.stickyHashBalanceFactor, 101, 1000)) {
    add('stickyHashBalanceFactor', 'Лимит перегрузки — от 101 до 1000 % или пусто.');
  }
  if (draft.clientIPv6 && (sticky === 'source' || sticky === 'source_table')) {
    if (draft.stickyIPv6Prefix !== 0 && !inRange(draft.stickyIPv6Prefix, 32, 128)) add('stickyIPv6Prefix', 'Префикс IPv6 — от /32 до /128.');
  }
  if (sticky === 'source_table') {
    const ttl = haproxyTimeSeconds(draft.stickyTTL || DEFAULT_STICKY_TTL);
    if (ttl === null || ttl < STICKY_TTL_MIN_SECONDS || ttl > STICKY_TTL_MAX_SECONDS) add('stickyTTL', 'Помнить — от 1 минуты до 168 часов.');
    if (!validTableEntries(draft.stickyTableEntries)) add('stickyTableEntries', `Макс. клиентов — от 1 до ${formatCount(TABLE_MAX_CLIENTS)}.`);
  }
  return errors;
}

export function validateRouteDraft(draft: RouteDraft, peers: RouteRecord[], editingID?: string): RouteDraftError[] {
  const errors: RouteDraftError[] = [];
  const add = (field: RouteDraftError['field'], message: string) => errors.push({ field, message });
  const listenerPort = Number(draft.listenerPort);
  const listenerIP = draft.listenerIP.trim() || '*';
  const isWildcard = listenerIP === '*' || listenerIP === '0.0.0.0' || listenerIP === '::';
  const snis = draft.snis.map((value) => value.trim().replace(/\.$/, '').toLowerCase()).filter(Boolean);

  if (!draft.name.trim()) add('name', 'Укажите имя маршрута. Оно видно только оператору.');
  else if (draft.name.trim().length > 80) add('name', 'Имя маршрута не должно превышать 80 символов.');
  // Backend containsForbiddenControl: "route fields cannot contain control characters".
  if (/[\u0000-\u001f\u007f]/.test(draft.name)) add('name', 'Имя маршрута не должно содержать табуляцию и управляющие символы.');
  if (!isWildcard && !isIPAddress(listenerIP)) add('listenerIP', 'Адрес listener должен быть * или корректным IP.');
  if (!Number.isInteger(listenerPort) || listenerPort < 1 || listenerPort > 65535) add('listenerPort', 'Порт listener должен быть от 1 до 65535.');

  if (draft.matchMode === 'sni') {
    if (!snis.length) add('snis', 'Добавьте хотя бы один SNI.');
    if (snis.length > 64) add('snis', 'Можно указать не больше 64 SNI.');
    if (snis.some((sni) => !isDNSName(sni))) add('snis', 'Каждый SNI должен быть корректным DNS-именем.');
    if (new Set(snis).size !== snis.length) add('snis', 'SNI внутри маршрута не должны повторяться.');
  }

  // accept_proxy_from validation
  // An empty list with the toggle on means "expect PROXY from everyone".
  if (draft.acceptProxyEnabled) {
    if (draft.acceptProxyFrom.length > 256) {
      add('acceptProxyFrom', 'Список не должен превышать 256 записей.');
    } else {
      for (const entry of draft.acceptProxyFrom) {
        if (!isValidTrustedProxyEntry(entry)) {
          add('acceptProxyFrom', `Некорректный адрес, подсеть или домен: ${entry}`);
          break;
        }
      }
      const domains = new Set(draft.acceptProxyFrom.filter(isTrustedProxyHostname).map(normalizeTrustedProxyEntry));
      if (domains.size > MAX_ACCEPT_PROXY_DOMAINS) {
        add('acceptProxyFrom', `Не больше ${MAX_ACCEPT_PROXY_DOMAINS} доменов в списке.`);
      }
    }
  }

  // Server validation
  if (draft.servers.length === 0) {
    add('servers', 'Добавьте хотя бы один сервер назначения.');
  } else if (draft.servers.length > 16) {
    add('servers', 'Не более 16 серверов.');
  } else {
    const names = draft.servers.map((s) => s.name.trim()).filter(Boolean);
    const reserved = draft.servers.find((s) => /_pref$/i.test(s.name.trim()));
    if (reserved) add('servers', `Имя сервера ${reserved.name.trim()} не должно оканчиваться на _pref.`);
    if (new Set(names).size !== names.length) add('servers', 'Имена серверов не должны повторяться.');
    draft.servers.forEach((s, i) => {
      if (!s.name.trim()) { add('servers', `Сервер ${i + 1}: укажите имя.`); return; }
      if (!SERVER_NAME_RE.test(s.name.trim())) add('servers', `Сервер ${i + 1}: имя — буквы, цифры, _, - (до ${SERVER_NAME_MAX} символов).`);
      if (s.targetType === 'tcp') {
        const sPort = Number(s.port);
        const host = s.host.trim();
        if (!host) add('servers', `Сервер ${i + 1}: укажите адрес.`);
        else if (looksLikeNumericAddress(host) && !isIPAddress(host)) add('servers', `Сервер ${i + 1}: ${host} — некорректный IPv4-адрес.`);
        else if (!isValidTargetHost(host)) add('servers', `Сервер ${i + 1}: адрес должен быть IP или доменом.`);
        if (!Number.isInteger(sPort) || sPort < 1 || sPort > 65535) add('servers', `Сервер ${i + 1}: некорректный порт.`);
        if (s.dnsPool && isIPAddress(s.host.trim())) add('servers', `Сервер ${i + 1}: DNS-пул недоступен для IP-адресов.`);
        if (preferredIPApplies(draft, i) && s.preferredIP.trim() && !isIPAddress(s.preferredIP.trim())) add('servers', `Сервер ${i + 1}: основной IP — некорректный адрес.`);
      } else {
        if (!isCanonicalUnixSocketPath(s.unixSocketPath.trim())) add('servers', `Сервер ${i + 1}: некорректный путь Unix socket.`);
      }
      if (s.weight !== '' && (!Number.isInteger(Number(s.weight)) || Number(s.weight) < 1 || Number(s.weight) > 256)) {
        add('servers', `Сервер ${i + 1}: вес — целое число от 1 до 256.`);
      }
      if (costActive(draft) && s.cost !== '' && (!Number.isFinite(Number(s.cost)) || Number(s.cost) <= 0 || Number(s.cost) > 100)) {
        add('servers', `Сервер ${i + 1}: стоимость — число больше 0 и не больше 100.`);
      }
      if (s.ipWeights.length > 0) {
        const algorithm = effectiveBalanceAlgorithm(draft);
        const sticky = effectiveStickyMode(draft);
        if (!s.dnsPool) add('servers', `Сервер ${i + 1}: вес и стоимость IP доступны только для DNS-пула.`);
        if (s.ipWeights.length > 32) add('servers', `Сервер ${i + 1}: не более 32 отдельных IP.`);
        const ips = s.ipWeights.map((w) => canonicalIP(w.ip).toLowerCase());
        if (new Set(ips).size !== ips.length) add('servers', `Сервер ${i + 1}: IP не должны повторяться.`);
        if (s.ipWeights.some((w) => !isIPAddress(w.ip))) add('servers', `Сервер ${i + 1}: некорректный IP в списке отдельных IP.`);
        if (s.ipWeights.some((w) => w.weight !== '' && (!Number.isInteger(w.weight) || w.weight < 1 || w.weight > 256))) {
          add('servers', `Сервер ${i + 1}: вес IP — целое число от 1 до 256.`);
        }
        if (costActive(draft) && s.ipWeights.some((w) => w.cost !== '' && (!Number.isFinite(w.cost) || w.cost < 0.01 || w.cost > 100))) {
          add('servers', `Сервер ${i + 1}: стоимость IP — число от 0,01 до 100.`);
        }
        const withWeight = s.ipWeights.some((w) => w.weight !== '');
        if (withWeight && (algorithm === 'static-rr' || (sticky === 'source' && draft.stickyHash === 'map-based'))) {
          add('servers', `Сервер ${i + 1}: вес IP не работает с фиксированными весами — включите «Веса меняются на лету» или «Стабильное» распределение.`);
        }
      }
    });
    if (effectiveBalanceMode(draft) === 'failover' && draft.servers.length > 1) {
      if (draft.servers.every((s) => s.backup)) add('servers', 'Отметьте хотя бы один сервер как «Основной».');
      else if (!draft.servers.some((s) => s.backup)) add('servers', 'Отметьте хотя бы один сервер как «Резерв» или выберите режим «Пул».');
      else {
        // Backend resolveRouteServers: HAProxy has one backup tier, and a DNS-pool
        // reserve (or the pool behind a preferred IP) needs option allbackups,
        // which would switch on every other reserve at the same time.
        const preferredPool = preferredIPApplies(draft, 0) && draft.servers[0].preferredIP.trim() !== '';
        const reserves = draft.servers.filter((_, i) => serverIsBackup(draft, i));
        const poolReserve = preferredPool || reserves.some((s) => s.targetType === 'tcp' && s.dnsPool);
        if (poolReserve && reserves.length + (preferredPool ? 1 : 0) > 1) {
          add('servers', preferredPool
            ? 'С основным IP остальные IP домена уже служат резервом — уберите основной IP или резервные серверы.'
            : 'Резервный сервер с DNS-пулом должен быть единственным резервным.');
        }
      }
    }
    // HAProxy names DNS-pool slots <name>_1 … <name>_32 (backend validateRouteServers).
    for (const pool of draft.servers.filter((s) => s.targetType === 'tcp' && s.dnsPool && s.name.trim())) {
      const prefix = `${pool.name.trim()}_`;
      const clash = draft.servers.find((s) => s.name.trim().startsWith(prefix) && /^\d+$/.test(s.name.trim().slice(prefix.length)));
      if (clash) add('servers', `Имя сервера ${clash.name.trim()} совпадает с именами адресов DNS-пула ${pool.name.trim()}.`);
    }
  }


  const bytes = quotaBytes(draft);
  if (draft.quotaEnabled && (draft.quotaValue === '' || Number(draft.quotaValue) <= 0 || !Number.isFinite(bytes) || Number(bytes) > MAX_SAFE_BYTES)) {
    add('quota', 'Лимит должен быть положительным числом в безопасном диапазоне.');
  }
  if (draft.quotaAction === 'block_new' && !draft.quotaEnabled) add('quota', 'Блокировка новых соединений требует включённого лимита.');
  if (draft.quotaEnabled && draft.quotaAction === 'block_new' && draft.servers.length > 0) {
    const shape = payloadShape(draft);
    if (shape.kind === 'servers' || shape.dnsPool) {
      add('quota', 'Блокировка новых соединений доступна только для одного сервера без DNS-пула. Выберите «Только уведомить».');
    }
  }
  if (draft.clientBandwidthEnabled) {
    for (const [field, value, label] of [
      ['clientUploadMbps', draft.clientUploadMbps, 'Лимит upload'],
      ['clientDownloadMbps', draft.clientDownloadMbps, 'Лимит download'],
    ] as const) {
      if (value !== '' && (!Number.isSafeInteger(value) || value < 1 || value > 1_000_000)) {
        add(field, `${label} должен быть целым числом от 1 до 1 000 000 Mbps либо пустым.`);
      }
    }
  }
  if (draft.slowstart && !effectiveHealthCheck(draft)) add('servers', 'Плавный ввод требует включённой проверки здоровья.');
  const slowstart = draft.slowstart ? haproxyTimeSeconds(draft.slowstart) : 0;
  if (slowstart === null || slowstart > 600) add('slowstart', 'Плавный ввод — от 1 до 600 секунд.');
  errors.push(...validateDistribution(draft));
  errors.push(...validateExpertOverride(draft.expertOverride));
  // Backend dnsPoolFragmentConflict: DNS-pool templates own balancing and servers.
  if (hasDNSPoolServer(draft) && draft.expertOverride.split(/\r?\n/)
    .some((line) => ['balance', 'hash-type', 'server', 'server-template', 'default-server'].includes(line.trim().split(/\s+/)[0].toLowerCase()))) {
    add('expert', 'С DNS-пулом экспертный слой не может задавать balance, hash-type, server, server-template и default-server.');
  }

  for (const peer of peers.filter((route) => route.id !== editingID && !route.delete_pending && route.deployment_state !== 'deleting')) {
    if (!Number.isInteger(listenerPort) || !listenerOverlaps(listenerIP, listenerPort, peer.listener_ip || '*', peer.listener_port)) continue;
    const exact = sameListener(listenerIP, listenerPort, peer.listener_ip || '*', peer.listener_port);
    if (!exact) {
      add('listener', `Порт ${listenerPort} уже слушается ${peer.listener_ip || '*'}. Нельзя смешивать wildcard и конкретный IP.`);
      continue;
    }
    const fallback = draft.matchMode !== 'sni';
    if (fallback && peer.fallback) add('listener', `Для ${listenerIP}:${listenerPort} уже есть фолбэк-маршрут.`);
    if (!fallback && !peer.fallback) {
      const duplicate = snis.find((sni) => (peer.snis ?? []).map((value) => value.toLowerCase()).includes(sni));
      if (duplicate) add('snis', `SNI ${duplicate} уже назначен другому маршруту на ${listenerIP}:${listenerPort}.`);
    }
    // Kernel shaping classifies by listener + client IP (no SNI): every kernel-shaped
    // route on one listener must share limits, and kernel/HAProxy shaping cannot mix.
    const peerLimited = peer.enabled && (peer.client_upload_mbps !== null || peer.client_download_mbps !== null);
    if (hasClientBandwidth(draft) && peerLimited) {
      const peerKernel = peer.shaper_mode === 'kernel';
      const peerName = routeDisplayName(peer);
      if ((draft.shaperMode === 'kernel') !== peerKernel) {
        add('bandwidth', `На ${listenerIP}:${listenerPort} маршрут ${peerName} ограничивает скорость через ${peerKernel ? 'ядро' : 'HAProxy'}; режимы на одном listener смешивать нельзя.`);
      } else if (peerKernel) {
        const up = draft.clientUploadMbps === '' ? null : draft.clientUploadMbps;
        const down = draft.clientDownloadMbps === '' ? null : draft.clientDownloadMbps;
        if (up !== peer.client_upload_mbps || down !== peer.client_download_mbps) {
          add('bandwidth', `Лимиты ядра на ${listenerIP}:${listenerPort} должны совпадать с маршрутом ${peerName} (↑${peer.client_upload_mbps ?? '—'} / ↓${peer.client_download_mbps ?? '—'} Mbps).`);
        }
      }
    }
  }
  return errors;
}

/** accept_proxy_from as sent to the backend: empty list with the toggle on = from everyone. */
export function acceptProxyPayload(draft: Pick<RouteDraft, 'acceptProxyEnabled' | 'acceptProxyFrom'>): string[] {
  if (!draft.acceptProxyEnabled) return [];
  if (draft.acceptProxyFrom.length === 0) return [...ACCEPT_PROXY_FROM_ALL];
  return draft.acceptProxyFrom.filter(isValidTrustedProxyEntry).map(normalizeTrustedProxyEntry);
}

/**
 * Latency mode actually in effect: pool with several candidates, algorithm
 * leastping and no IP hash (the hash overrides any algorithm).
 */
export function leastpingActive(draft: RouteDraft): boolean {
  return effectiveBalanceMode(draft) === 'pool' && stickyApplicable(draft) && effectiveBalanceAlgorithm(draft) === 'leastping';
}

/**
 * Connections mode actually in effect: pool with several candidates,
 * algorithm leastconn and no IP hash (source_table uses it for the first pick).
 */
export function leastconnActive(draft: RouteDraft): boolean {
  return effectiveBalanceMode(draft) === 'pool' && stickyApplicable(draft) && effectiveBalanceAlgorithm(draft) === 'leastconn';
}

/**
 * «Стоимость» (server and per-IP) applies only in latency mode. With
 * «Меньше соединений» the share is weight ÷ cost, so the weight alone
 * expresses it and the editor offers no cost there.
 */
export function costActive(draft: RouteDraft): boolean {
  return leastpingActive(draft);
}

/** First Node Agent release that applies the leastconn tolerance. */
export const LEASTCONN_TOLERANCE_MIN_AGENT = '1.1.1';

/** Same rules as kernelShaperSupported, against LEASTCONN_TOLERANCE_MIN_AGENT. */
export function leastconnToleranceSupported(agentVersion: string | undefined | null): boolean {
  return agentVersionAtLeast(agentVersion, LEASTCONN_TOLERANCE_MIN_AGENT);
}

/**
 * Mirrors routeLeastConnCostWeights (haproxy_renderer.go): leastconn costs
 * become static weights base/cost × K, K = min(100, 256 / max(base/cost)),
 * clamped 1..256. null when nothing sets a cost (weights render unchanged).
 */
export function leastconnCostWeights(servers: { weight: number; cost: number; ip_weights: { ip: string; weight: number; cost?: number }[] }[]):
  { servers: number[]; ips: Record<string, number>[] } | null {
  const hasCost = servers.some((s) => s.cost > 0 || s.ip_weights.some((w) => (w.cost ?? 0) > 0));
  if (!hasCost) return null;
  const ratio = (base: number, cost: number) => (base > 0 ? base : 1) / (cost > 0 ? cost : 1);
  const ipRatio = (w: { weight: number; cost?: number }, s: { weight: number; cost: number }) => ratio(w.weight > 0 ? w.weight : s.weight, (w.cost ?? 0) > 0 ? (w.cost ?? 0) : s.cost);
  let max = 0;
  servers.forEach((s) => {
    max = Math.max(max, ratio(s.weight, s.cost));
    s.ip_weights.forEach((w) => { max = Math.max(max, ipRatio(w, s)); });
  });
  const scale = Math.min(100, 256 / max);
  const clamp = (v: number) => Math.min(256, Math.max(1, Math.round(v)));
  return {
    servers: servers.map((s) => clamp(ratio(s.weight, s.cost) * scale)),
    ips: servers.map((s) => Object.fromEntries(s.ip_weights.map((w) => [w.ip, clamp(ipRatio(w, s) * scale)]))),
  };
}

/**
 * ip_weights as sent: weight '' ⇒ 0 (the server weight); cost only travels in
 * latency/connections mode (it is inert otherwise), and an entry left with
 * neither is dropped. Entries without cost keep the pre-cost {ip, weight} shape.
 */
export function ipWeightsPayload(entries: IPWeightDraft[], leastping: boolean): { ip: string; weight: number; cost?: number }[] {
  return entries.flatMap((w) => {
    const weight = w.weight === '' ? 0 : Number(w.weight);
    const cost = leastping && w.cost !== '' ? Math.round(Number(w.cost) * 100) / 100 : 0;
    if (!weight && !cost) return [];
    return [cost ? { ip: w.ip.trim(), weight, cost } : { ip: w.ip.trim(), weight }];
  });
}

export function routePayload(draft: RouteDraft, enabled: boolean, expectedVersion?: number) {
  const fallback = draft.matchMode !== 'sni';
  const snis = fallback ? [] : draft.snis.map((value) => value.trim().replace(/\.$/, '').toLowerCase()).filter(Boolean);
  const acceptProxyFrom = acceptProxyPayload(draft);
  const balanceMode = effectiveBalanceMode(draft);
  const shape = payloadShape(draft);
  const poolMode = balanceMode === 'pool';
  // Two orthogonal pool-mode settings; failover ignores both and stores them
  // empty. In pool mode both keys always travel (never omitted), so a stale
  // choice from before a mode/server-count change never lingers server-side.
  const stickyMode: StickyMode | '' = poolMode ? effectiveStickyMode(draft) : '';
  const balanceApplicable = poolMode && stickyApplicable(draft);
  const balanceAlgorithm: BalanceAlgorithm | '' = balanceApplicable ? effectiveBalanceAlgorithm(draft) : '';
  const latency = leastpingActive(draft);
  const connections = leastconnActive(draft);
  const withCost = latency;

  // First server mirrors into the legacy target fields (canonical target for
  // older readers). Route-level dns_pool is never derived from servers[].
  const firstServer = draft.servers[0];
  const targetUnix = firstServer?.targetType === 'unix';
  const targetHost = targetUnix ? '' : (firstServer?.host.trim() ?? '');
  const targetPort = targetUnix ? 0 : Number(firstServer?.port ?? 443);
  const unixSocketPath = targetUnix ? (firstServer?.unixSocketPath.trim() ?? '') : '';

  // Canonical failover travels as balance_mode=failover; any other failover
  // layout (several primaries, reserve first) keeps explicit backup flags and
  // omits balance_mode so the backend stores them as sent.
  const explicitBackups = balanceMode === 'failover' && shape.kind === 'servers'
    && !isCanonicalFailover(draft.servers.map((_, i) => ({ backup: serverIsBackup(draft, i) })));
  const servers = shape.kind === 'legacy' ? [] : draft.servers.map((s, i) => ({
    position: i + 1,
    name: s.name.trim(),
    target_type: s.targetType,
    host: s.targetType === 'tcp' ? s.host.trim() : '',
    port: s.targetType === 'tcp' ? Number(s.port) : 0,
    unix_socket_path: s.targetType === 'unix' ? s.unixSocketPath.trim() : '',
    backup: serverIsBackup(draft, i),
    dns_pool: s.targetType === 'tcp' && s.dnsPool,
    preferred_ip: preferredIPApplies(draft, i) ? s.preferredIP.trim() : '',
    weight: s.weight === '' ? 0 : Number(s.weight),
    cost: withCost && s.cost !== '' ? Number(s.cost) : 0,
    ip_weights: s.targetType === 'tcp' && s.dnsPool ? ipWeightsPayload(s.ipWeights, withCost) : [],
  }));

  return {
    ...(expectedVersion ? { expected_version: expectedVersion } : {}),
    name: draft.name.trim(),
    hostname: snis[0] ?? '',
    listener_ip: draft.listenerIP.trim() || '*', listener_port: Number(draft.listenerPort),
    match_mode: draft.matchMode, snis, fallback,
    target_type: targetUnix ? 'unix' : 'tcp', target_host: targetHost,
    target_port: targetPort, dns_pool: shape.kind === 'legacy' && shape.dnsPool,
    unix_socket_path: unixSocketPath,
    health_check: effectiveHealthCheck(draft), proxy_protocol: draft.proxyProtocol, quota_bytes: quotaBytes(draft),
    quota_action: draft.quotaEnabled ? draft.quotaAction : 'observe', quota_period: draft.quotaPeriod,
    client_upload_mbps: !draft.clientBandwidthEnabled || draft.clientUploadMbps === '' ? null : draft.clientUploadMbps,
    client_download_mbps: !draft.clientBandwidthEnabled || draft.clientDownloadMbps === '' ? null : draft.clientDownloadMbps,
    enabled, custom_fragment: draft.expertOverride,
    accept_proxy_from: acceptProxyFrom,
    servers,
    sticky_enabled: stickyMode === 'source' || stickyMode === 'source_table',
    sticky_mode: stickyMode,
    ...(stickyMode === 'source_table' ? { sticky_ttl: draft.stickyTTL || DEFAULT_STICKY_TTL } : {}),
    sticky_hash: stickyMode === 'source' ? draft.stickyHash : '',
    sticky_hash_balance_factor: stickyMode === 'source' && draft.stickyHash !== 'map-based' ? Number(draft.stickyHashBalanceFactor || 0) : 0,
    sticky_table_entries: stickyMode === 'source_table' ? draft.stickyTableEntries : '',
    sticky_ipv6_prefix: draft.clientIPv6 && (stickyMode === 'source' || stickyMode === 'source_table') ? draft.stickyIPv6Prefix : 0,
    client_ipv6: draft.clientIPv6,
    balance_algorithm: balanceAlgorithm,
    balance_random_draws: balanceAlgorithm === 'random' ? Number(draft.balanceRandomDraws || 2) : 0,
    // balance_tolerance (alias of leastping_tolerance): latency default 20 %,
    // connections default 0 % (plain HAProxy leastconn).
    balance_tolerance: latency
      ? Math.round(draft.leastpingTolerancePct === '' ? DEFAULT_LEASTPING_TOLERANCE_PCT : Number(draft.leastpingTolerancePct)) / 100
      : connections && draft.leastpingTolerancePct !== '' ? Math.round(Number(draft.leastpingTolerancePct)) / 100 : 0,
    leastping_tolerance_ms: latency ? Number(draft.leastpingToleranceMs || 0) : 0,
    slowstart: draft.slowstart,
    ...(explicitBackups ? {} : { balance_mode: balanceMode }),
    shaper_mode: draft.shaperMode,
  };
}

function safeName(value: string): string {
  const normalized = value.toLowerCase().replace(/[^a-z0-9]+/g, '_').replace(/^_+|_+$/g, '');
  return normalized || 'route_preview';
}

/** Brackets IPv6 literals like the renderer's renderHostPort. */
function hostPort(host: string, port: number): string {
  return host.includes(':') ? `[${host}]:${port}` : `${host}:${port}`;
}

const DNS_TEMPLATE_OPTS = ' check inter 5s fall 3 rise 2 resolvers nf_dns init-addr last,none resolve-opts prevent-dup-ip hash-key addr';

/**
 * renderRoutePreview mirrors internal/panel/haproxy_renderer.go for the edited
 * route: legacy single target vs servers[] backend, pool/failover backup flags,
 * option allbackups, server-template for DNS pools, resolvers and the kernel
 * shaper annotation. Runtime IDs are placeholders derived from the route name.
 */
export function renderRoutePreview(draft: RouteDraft, peers: RouteRecord[], memoryTotalBytes?: number): { config: string; merged: number } {
  const listenerIP = draft.listenerIP.trim() || '*';
  const listenerPort = Number(draft.listenerPort) || 443;
  const activePeers = peers.filter((route) => route.enabled && sameListener(route.listener_ip || '*', route.listener_port, listenerIP, listenerPort));
  const current = routePayload(draft, true) as ReturnType<typeof routePayload> & { id?: string };
  const all = [...activePeers, current];
  const frontend = `nf_listener_${listenerPort}_${safeName(listenerIP === '*' ? 'any' : listenerIP)}`;
  const routeID = safeName(draft.name);
  // The backend stores a lone server without preferred IP/weight/cost/per-IP
  // overrides as the legacy single target (route_validation.go), whatever
  // shape the editor sent; render it that way too.
  const sentShape = payloadShape(draft);
  const lone = current.servers.length === 1 ? current.servers[0] : null;
  const shape: ReturnType<typeof payloadShape> = lone && !lone.preferred_ip && !lone.weight && !lone.cost && !lone.ip_weights.length
    ? { kind: 'legacy', dnsPool: lone.dns_pool }
    : sentShape;
  const healthCheck = current.health_check;
  const lines: string[] = ['# Managed by NodeFlow — preview'];

  const needsResolvers = draft.servers.some((s) => s.targetType === 'tcp'
    && (s.dnsPool || (s.host.trim() !== '' && !isIPAddress(s.host.trim()))));
  if (needsResolvers) {
    lines.push('resolvers nf_dns', '    nameserver adguard_local 127.0.0.1:53', '    # … остальные nameserver и hold-параметры', '');
  }
  // Emitted in `global`/before the first frontend once ANY backend uses source_table
  // (table survives a HAProxy reload); this preview only knows about the edited route.
  if (current.sticky_mode === 'source_table') {
    lines.push('peers nf_peers', '    bind unix@/run/haproxy/nf_peers.sock', '    server nf_local', '');
  }
  lines.push(`frontend ${frontend}`, `    bind ${listenerIP}:${listenerPort}`, '    mode tcp');

  const proxyFromCIDRs = acceptProxyPayload(draft);
  if (draft.acceptProxyEnabled && isAcceptProxyFromAll(proxyFromCIDRs)) {
    lines.push('    tcp-request connection expect-proxy layer4');
  } else if (proxyFromCIDRs.length > 0) {
    // Same order and shape as haproxy_renderer.go: sorted static IP/CIDRs,
    // hostnames through the Agent-maintained ACL file.
    const unique = [...new Set(proxyFromCIDRs)].sort();
    const domains = unique.filter(isTrustedProxyHostname);
    const statics = unique.filter((entry) => !isTrustedProxyHostname(entry));
    if (domains.length > 0) {
      const file = `/etc/haproxy/nodeflow/pp-trusted-${frontend}.acl`;
      lines.push(
        `    # nf-pp-trusted file=${file} domains=${domains.join(',')}`,
        `    acl nf_pp_trusted src -f ${file}`,
        `    tcp-request connection expect-proxy layer4 if ${statics.length ? `{ src ${statics.join(' ')} } || ` : ''}nf_pp_trusted`,
      );
    } else {
      lines.push(`    tcp-request connection expect-proxy layer4 if { src ${statics.join(' ')} }`);
    }
  }

  const downloadBytes = current.client_download_mbps !== null ? current.client_download_mbps * 125000 : null;
  const uploadBytes = current.client_upload_mbps !== null ? current.client_upload_mbps * 125000 : null;
  const hasLimit = downloadBytes !== null || uploadBytes !== null;
  const kernelShaper = draft.shaperMode === 'kernel';
  const haproxyShaper = !kernelShaper && hasLimit;
  if (kernelShaper && hasLimit) {
    lines.push(`    # nf-kernel-shaper listen=${listenerIP} port=${listenerPort} download_bps=${downloadBytes ?? 0} upload_bps=${uploadBytes ?? 0}`);
  }
  // client_ipv6 off: IPv4-only client tables (haproxy_renderer.go clientTableType/bandwidthLimitKey).
  const clientIPv6 = current.client_ipv6;
  const clientTableType = clientIPv6 ? 'ipv6' : 'ip';
  const bandwidthKey = clientIPv6 ? 'src,ipmask(32,64)' : 'src';
  if (haproxyShaper && downloadBytes !== null) lines.push(`    filter bwlim-out nf_bw_download_${routeID} limit ${downloadBytes} key ${bandwidthKey} table nf_bw_download_table_${routeID} min-size 2896`);
  if (haproxyShaper && uploadBytes !== null) lines.push(`    filter bwlim-in nf_bw_upload_${routeID} limit ${uploadBytes} key ${bandwidthKey} table nf_bw_upload_table_${routeID} min-size 2896`);
  const sniRoutes = all.filter((route) => !route.fallback);
  if (sniRoutes.length) lines.push('    tcp-request inspect-delay 5s', '    tcp-request content accept if { req.ssl_hello_type 1 }');
  const currentSNIIndex = sniRoutes.indexOf(current);
  const bandwidthCondition = currentSNIIndex >= 0
    ? ` if nf_sni_${currentSNIIndex + 1}`
    : sniRoutes.length
      ? ` if ${sniRoutes.map((_, index) => `!nf_sni_${index + 1}`).join(' ')}`
      : '';
  if (haproxyShaper && downloadBytes !== null) lines.push(`    tcp-request content set-bandwidth-limit nf_bw_download_${routeID}${bandwidthCondition}`);
  if (haproxyShaper && uploadBytes !== null) lines.push(`    tcp-request content set-bandwidth-limit nf_bw_upload_${routeID}${bandwidthCondition}`);
  sniRoutes.forEach((route, index) => {
    const values = (route.snis ?? []).length ? route.snis : ['sni.example.com'];
    lines.push(`    acl nf_sni_${index + 1} req.ssl_sni -i ${values.join(' ')}`);
    lines.push(`    use_backend nf_be_${safeName(route === current ? draft.name : values[0])} if nf_sni_${index + 1}`);
  });
  const fallbackRoute = all.find((route) => route.fallback);
  if (fallbackRoute) lines.push(`    default_backend nf_be_${safeName(fallbackRoute === current ? draft.name : fallbackRoute.snis?.[0] || `tcp_${fallbackRoute.listener_port}`)}`);

  lines.push('', `backend nf_be_${routeID}`, '    mode tcp');
  const legacyDNSPool = shape.kind === 'legacy' && shape.dnsPool;
  // routePayload already sends sticky_mode '' for failover (both settings are pool-only).
  const distribution = current.sticky_mode;
  if (distribution === 'source') {
    // balance source when the IPv6 grouping is untouched (0, byte-identical to
    // pre-migration rows), the full /128 address or IPv6 is off; anything else
    // masks explicitly.
    const prefix = current.sticky_ipv6_prefix;
    if (!clientIPv6 || prefix === 0 || prefix === 128) lines.push('    balance source');
    else lines.push(`    balance hash src,ipmask(32,${prefix})`);
    const hashType = current.sticky_hash === 'map-based' ? 'map-based' : 'consistent';
    lines.push(`    hash-type ${hashType} sdbm avalanche`);
    if (hashType === 'consistent' && current.sticky_hash_balance_factor) {
      lines.push(`    hash-balance-factor ${current.sticky_hash_balance_factor}`);
    }
  } else if (distribution === 'none' || distribution === 'source_table' || distribution === '') {
    const algorithm = current.balance_algorithm;
    if (algorithm === 'static-rr') lines.push('    balance static-rr');
    else if (algorithm === 'random') lines.push(`    balance random(${current.balance_random_draws || 2})`);
    // leastconn with a tolerance is Agent-managed weighted round-robin (no balance line).
    else if (algorithm === 'leastconn' && !(current.balance_tolerance > 0 && shape.kind === 'servers')) lines.push('    balance leastconn');
    // roundrobin, leastping and '' (not applicable) render nothing — HAProxy default.
    if (distribution === 'source_table') {
      // «Авто» shares node RAM with every enabled auto table on the node (all listeners).
      const entries = current.sticky_table_entries || draftAutoTableEntries(peers, memoryTotalBytes);
      lines.push(`    stick-table type ${clientTableType} size ${entries} expire ${current.sticky_ttl || DEFAULT_STICKY_TTL} peers nf_peers`);
      lines.push(clientIPv6 ? `    stick on src,ipmask(32,${current.sticky_ipv6_prefix || 64})` : '    stick on src');
      lines.push('    option redispatch');
    }
  }

  // Agent annotations: only when the algorithm actually applies (sticky != source)
  // and there's something for the weight controller to act on.
  const leastpingActive = current.balance_algorithm === 'leastping' && distribution !== 'source';
  const annotatable = shape.kind === 'servers' ? current.servers : [];
  const leastconnEffective = current.balance_algorithm === 'leastconn' && distribution !== 'source' && annotatable.length > 0;
  const leastconnAgent = leastconnEffective && current.balance_tolerance > 0;
  const dynamic = leastpingActive || leastconnAgent;
  // leastconn costs: static base/cost weights on server lines (mirrors routeLeastConnCostWeights).
  const costWeights = leastconnEffective ? leastconnCostWeights(annotatable) : null;
  // Mirrors writeWeightAnnotations: without a dynamic algorithm only per-IP weights matter.
  const managed = (s: (typeof annotatable)[number]) => (dynamic || costWeights
    ? (s.ip_weights?.length ?? 0) > 0
    : (s.ip_weights ?? []).some((w) => w.weight > 0));
  const anyIPWeights = annotatable.some(managed);
  if (dynamic || anyIPWeights) {
    const algoLabel = leastpingActive ? 'leastping' : leastconnAgent ? 'leastconn' : 'static';
    const tolerance = (dynamic ? current.balance_tolerance : 0).toFixed(2);
    const toleranceMs = leastpingActive ? current.leastping_tolerance_ms || 0 : 0;
    lines.push(`    # nf-weights backend=nf_be_${routeID} algo=${algoLabel} tolerance=${tolerance} tolerance_ms=${toleranceMs}`);
    annotatable.forEach((s, index) => {
      // algo=static manages only DNS pools with per-IP weights (mirrors the Go renderer).
      if (!dynamic && !managed(s)) return;
      const base = s.weight || 1;
      const cost = (s.cost || 0).toFixed(2);
      if (s.dns_pool) {
        if (s.preferred_ip && !s.backup && index === 0 && draft.balanceMode === 'failover') lines.push(`    # nf-weight server=${s.name}_pref base=${base} cost=${cost}`);
        if (!dynamic && costWeights) {
          const pinned = (s.ip_weights ?? []).map((w) => `${w.ip}=${costWeights.ips[index][w.ip]}`).join(',');
          lines.push(`    # nf-weight template=${s.name}_ slots=32 base=${costWeights.servers[index]} cost=0.00${pinned ? ` ips=${pinned}` : ''}`);
          return;
        }
        const ips = (s.ip_weights ?? []).filter((w) => w.weight > 0).map((w) => `${w.ip}=${w.weight}`).join(',');
        const ipCosts = dynamic ? (s.ip_weights ?? []).filter((w) => (w.cost ?? 0) > 0).map((w) => `${w.ip}=${(w.cost ?? 0).toFixed(2)}`).join(',') : '';
        lines.push(`    # nf-weight template=${s.name}_ slots=32 base=${base} cost=${cost}${ips ? ` ips=${ips}` : ''}${ipCosts ? ` ipcosts=${ipCosts}` : ''}`);
      } else {
        lines.push(`    # nf-weight server=${s.name} base=${base} cost=${cost}`);
      }
    });
  }

  if (healthCheck) lines.push('    option tcp-check');
  if (draft.quotaEnabled) lines.push(`    # quota ${formatBytes(quotaBytes(draft) ?? 0)} · ${draft.quotaPeriod} · ${draft.quotaAction}`);
  if (draft.expertOverride.trim()) {
    lines.push('    # ── expert override: backend directives only ──');
    draft.expertOverride.split('\n').forEach((line) => lines.push(`    ${line.trimStart()}`));
    lines.push('    # ── end expert override ──');
  }

  const proxy = draft.proxyProtocol === 'v1' ? ' send-proxy' : draft.proxyProtocol === 'v2' ? ' send-proxy-v2' : '';
  const checkProxy = draft.proxyProtocol !== 'none' && healthCheck ? ' check-send-proxy' : '';
  const health = healthCheck ? ' check inter 5s fall 3 rise 2' : '';
  const orPlaceholder = (host: string, placeholder: string) => host.trim() || placeholder;
  const portOf = (port: number | '') => Number(port) || 443;
  // Requires health_check (validated); no effect at all until a server has
  // been seen down, and no effect with static weights (static-rr / map-based hash).
  const slowstartSuffix = draft.slowstart && healthCheck ? ` slowstart ${draft.slowstart}` : '';
  const weightSuffix = (w: number) => (w ? ` weight ${w}` : '');

  if (shape.kind === 'legacy') {
    const s = draft.servers[0];
    if (legacyDNSPool) {
      lines.push(`    server-template nf_srv_${routeID}_ 32 ${hostPort(orPlaceholder(s.host, 'pool.example.com'), portOf(s.port))}${slowstartSuffix}${DNS_TEMPLATE_OPTS}${proxy}${checkProxy}`);
    } else if (s?.targetType === 'unix') {
      lines.push(`    server nf_srv_${routeID} ${s.unixSocketPath.trim() || '/run/service.sock'}${slowstartSuffix}${health}${proxy}${checkProxy}`);
    } else {
      const host = orPlaceholder(s?.host ?? '', '10.0.0.1');
      const resolvers = isIPAddress(host) ? '' : ' resolvers nf_dns init-addr last,none';
      lines.push(`    server nf_srv_${routeID} ${hostPort(host, portOf(s?.port ?? 443))}${slowstartSuffix}${health}${resolvers}${proxy}${checkProxy}`);
    }
  } else {
    const servers = current.servers;
    if (servers.some((s) => s.dns_pool && (s.backup || s.preferred_ip !== ''))) lines.push('    option allbackups');
    servers.forEach((s, i) => {
      const name = s.name || `srv${i + 1}`;
      const backup = s.backup ? ' backup' : '';
      const weight = weightSuffix(costWeights ? costWeights.servers[i] : s.weight);
      if (s.dns_pool) {
        const target = hostPort(orPlaceholder(s.host, 'pool.example.com'), portOf(s.port));
        if (s.preferred_ip && !s.backup) {
          lines.push(`    server ${name}_pref ${hostPort(s.preferred_ip, portOf(s.port))}${weight}${slowstartSuffix}${health}${proxy}${checkProxy}`);
          lines.push(`    server-template ${name}_ 32 ${target}${weight}${slowstartSuffix}${DNS_TEMPLATE_OPTS}${proxy}${checkProxy} backup`);
        } else {
          lines.push(`    server-template ${name}_ 32 ${target}${weight}${slowstartSuffix}${DNS_TEMPLATE_OPTS}${proxy}${checkProxy}${backup}`);
        }
      } else if (s.target_type === 'unix') {
        lines.push(`    server ${name} ${s.unix_socket_path || '/run/service.sock'}${weight}${slowstartSuffix}${health}${proxy}${checkProxy}${backup}`);
      } else {
        const host = orPlaceholder(s.host, '10.0.0.1');
        const resolvers = isIPAddress(host) ? '' : ' resolvers nf_dns init-addr last,none';
        lines.push(`    server ${name} ${hostPort(host, portOf(s.port))}${weight}${slowstartSuffix}${health}${resolvers}${proxy}${checkProxy}${backup}`);
      }
    });
  }

  if (haproxyShaper && downloadBytes !== null) lines.push('', `backend nf_bw_download_table_${routeID}`, `    stick-table type ${clientTableType} size 1m expire 1h store bytes_out_rate(1s)`);
  if (haproxyShaper && uploadBytes !== null) lines.push('', `backend nf_bw_upload_table_${routeID}`, `    stick-table type ${clientTableType} size 1m expire 1h store bytes_in_rate(1s)`);
  return { config: lines.join('\n'), merged: activePeers.length };
}
