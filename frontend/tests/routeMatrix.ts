// UI route-editor draft matrix for the frontend↔backend contract test.
//
// Every draft here is a state the route editor can reach through its
// controls. Only drafts the editor itself accepts (validateRouteDraft without
// errors) are exported: the backend must accept each of their payloads.
// internal/panel/route_ui_contract_test.go replays the JSON written by
// tests/routeContract.test.ts (testdata/ui_route_payloads.json.gz).
import { registerHooks } from 'node:module';

// src/ uses extensionless relative imports (Vite/Bundler resolution); map
// them to the .ts files so node --experimental-strip-types can load model.ts.
registerHooks({
  resolve(specifier, context, nextResolve) {
    try {
      return nextResolve(specifier, context);
    } catch (error) {
      if (specifier.startsWith('.') && !/\.[cm]?[jt]sx?$/.test(specifier)) return nextResolve(`${specifier}.ts`, context);
      throw error;
    }
  },
});

export const model = await import('../src/features/routes/model.ts');
type RouteDraft = import('../src/features/routes/model.ts').RouteDraft;
type ServerDraft = import('../src/features/routes/model.ts').ServerDraft;
type RouteRecord = import('../src/lib/contracts.ts').RouteRecord;

export interface UIPayloadCase {
  name: string;
  /** 'create' = POST body (enabled false), 'enable' = PUT with enabled true. */
  method: 'POST' | 'PUT';
  payload: Record<string, unknown>;
  /** Loaded cases: the stored route (GET shape) the editor started from. */
  stored?: RouteRecord;
}

let seq = 0;
function server(patch: Partial<ServerDraft>): ServerDraft {
  seq += 1;
  return { ...model.emptyServerDraft(`srv${seq}`), _key: `k${seq}`, ...patch };
}

type ServersKind = 'ip' | 'domain' | 'unix' | 'dnspool' | 'dnspool-pref' | 'ip+ip' | 'ip+dnspool' | 'dnspool+ip' | '3-mixed' | 'ip+unix';
const serverKinds: ServersKind[] = ['ip', 'domain', 'unix', 'dnspool', 'dnspool-pref', 'ip+ip', 'ip+dnspool', 'dnspool+ip', '3-mixed', 'ip+unix'];

function servers(kind: ServersKind): ServerDraft[] {
  seq = 0;
  const ip = (host = '192.0.2.10') => server({ host, port: 443 });
  const domain = () => server({ host: 'backend.example.com', port: 8443 });
  const unix = () => server({ targetType: 'unix', host: '', port: '', unixSocketPath: '/run/app.sock' });
  const pool = (preferredIP = '') => server({ host: 'pool.example.com', port: 443, dnsPool: true, preferredIP });
  switch (kind) {
    case 'ip': return [ip()];
    case 'domain': return [domain()];
    case 'unix': return [unix()];
    case 'dnspool': return [pool()];
    case 'dnspool-pref': return [pool('192.0.2.50')];
    case 'ip+ip': return [ip(), ip('2001:db8::20')];
    case 'ip+dnspool': return [ip(), pool()];
    case 'dnspool+ip': return [pool(), ip()];
    case '3-mixed': return [ip(), domain(), ip('192.0.2.30')];
    case 'ip+unix': return [ip(), unix()];
  }
}

type Layout = 'pool' | 'failover' | 'failover-2primaries';
type Sticky = null | 'none' | 'source' | 'source_table';
type Algo = null | 'roundrobin' | 'static-rr' | 'random' | 'leastconn' | 'leastping';

interface Axes {
  match: 'sni' | 'fallback';
  listener: '*' | 'ip';
  acceptProxy: 'off' | 'all' | 'list';
  kind: ServersKind;
  layout: Layout;
  sticky: Sticky;
  algo: Algo;
  tuned: boolean;
  health: boolean;
  proxy: 'none' | 'v1' | 'v2';
  slowstart: boolean;
  quota: 'off' | 'observe' | 'block_new';
  bandwidth: 'off' | 'haproxy' | 'kernel';
  expert: boolean;
  ipv6: boolean;
}

function buildDraft(a: Axes): RouteDraft {
  const draft = model.emptyRouteDraft();
  draft.name = 'matrix';
  draft.matchMode = a.match;
  draft.snis = a.match === 'sni' ? ['app.example.com', 'www.example.com'] : [];
  draft.listenerIP = a.listener === '*' ? '*' : '203.0.113.1';
  draft.listenerPort = 443;
  draft.acceptProxyEnabled = a.acceptProxy !== 'off';
  draft.acceptProxyFrom = a.acceptProxy === 'list' ? ['10.0.0.0/8', '2001:db8::/32', 'proxy.example.com'] : [];
  draft.servers = servers(a.kind);
  draft.balanceMode = a.layout === 'pool' ? 'pool' : 'failover';
  if (a.layout === 'failover') draft.servers = model.withCanonicalFailoverRoles(draft.servers);
  if (a.layout === 'failover-2primaries') draft.servers = draft.servers.map((s, i) => ({ ...s, backup: i === draft.servers.length - 1 && i > 0 }));
  draft.stickyMode = a.sticky;
  draft.balanceAlgorithm = a.algo;
  if (a.tuned) {
    draft.servers = draft.servers.map((s, i) => ({
      ...s, weight: i + 2, cost: a.algo === 'leastping' ? 1.5 : '',
      ipWeights: s.dnsPool ? [{ ip: '192.0.2.61', weight: 3, cost: a.algo === 'leastping' ? 2 : '' }, { ip: '192.0.2.62', weight: '', cost: '' }] : [],
    }));
    draft.balanceRandomDraws = 3;
    draft.leastpingTolerancePct = 35;
    draft.leastpingToleranceMs = 40;
    draft.stickyHash = a.sticky === 'source' && !a.ipv6 ? 'map-based' : '';
    draft.stickyHashBalanceFactor = 150;
    draft.stickyTableEntries = '200k';
    draft.stickyTTL = '30m';
    draft.stickyIPv6Prefix = 56;
  }
  draft.clientIPv6 = a.ipv6;
  draft.healthCheck = a.health;
  draft.proxyProtocol = a.proxy;
  draft.slowstart = a.slowstart ? '10s' : '';
  draft.quotaEnabled = a.quota !== 'off';
  draft.quotaValue = a.quota !== 'off' ? 100 : '';
  draft.quotaAction = a.quota === 'block_new' ? 'block_new' : 'observe';
  draft.quotaPeriod = a.quota === 'observe' ? 'monthly_from_creation' : 'calendar_month';
  draft.clientBandwidthEnabled = a.bandwidth !== 'off';
  draft.clientUploadMbps = a.bandwidth !== 'off' ? 50 : '';
  draft.clientDownloadMbps = a.bandwidth === 'haproxy' ? 200 : '';
  draft.shaperMode = a.bandwidth === 'kernel' ? 'kernel' : 'haproxy';
  draft.expertOverride = a.expert ? 'timeout connect 5s\nmaxconn 2000' : '';
  return draft;
}

function axesName(a: Axes): string {
  return [a.match, a.listener === '*' ? 'all-ip' : 'one-ip', `pp-in:${a.acceptProxy}`, a.kind, a.layout,
    `sticky:${a.sticky ?? 'auto'}`, `algo:${a.algo ?? 'auto'}`, a.tuned ? 'tuned' : 'defaults',
    a.health ? 'hc' : 'no-hc', `pp-out:${a.proxy}`, a.slowstart ? 'slowstart' : '', `quota:${a.quota}`,
    `bw:${a.bandwidth}`, a.expert ? 'expert' : '', a.ipv6 ? '' : 'ipv4-only'].filter(Boolean).join('/');
}

/**
 * Core axes are a full product (servers × layout × distribution × algorithm ×
 * tuning × health × slowstart); the independent ones rotate with the index so
 * each value is paired with every core shape family.
 */
export function draftMatrix(): { name: string; draft: RouteDraft }[] {
  const out: { name: string; draft: RouteDraft }[] = [];
  const seen = new Set<string>();
  const stickies: Sticky[] = [null, 'none', 'source', 'source_table'];
  const algos: Algo[] = [null, 'roundrobin', 'static-rr', 'random', 'leastconn', 'leastping'];
  const layouts: Layout[] = ['pool', 'failover', 'failover-2primaries'];
  let i = 0;
  for (const kind of serverKinds) for (const layout of layouts) for (const sticky of stickies) for (const algo of algos)
    for (const tuned of [false, true]) for (const health of [true, false]) for (const slowstart of [false, true]) {
      i += 1;
      const a: Axes = {
        kind, layout, sticky, algo, tuned, health, slowstart,
        match: i % 2 ? 'sni' : 'fallback',
        listener: i % 3 ? '*' : 'ip',
        acceptProxy: (['off', 'all', 'list'] as const)[i % 3],
        proxy: (['none', 'v1', 'v2'] as const)[Math.floor(i / 3) % 3],
        quota: (['off', 'observe', 'block_new'] as const)[Math.floor(i / 2) % 3],
        bandwidth: (['off', 'haproxy', 'kernel'] as const)[Math.floor(i / 5) % 3],
        expert: i % 7 === 0,
        ipv6: i % 4 !== 1,
      };
      const draft = buildDraft(a);
      if (model.validateRouteDraft(draft, []).length > 0) continue;
      // Identical payloads (inert controls) are kept once.
      const key = JSON.stringify(model.routePayload(draft, true));
      if (seen.has(key)) continue;
      seen.add(key);
      out.push({ name: axesName(a), draft });
    }
  return out;
}

const loadedBase: RouteRecord = {
  id: '44444444-4444-4444-8444-444444444401', node_id: '55555555-5555-4555-8555-555555555501', name: 'loaded', version: 7,
  listener_ip: '*', listener_port: 443, match_mode: 'sni', snis: ['app.example.com'], fallback: false, hostname: 'app.example.com',
  target_type: 'tcp', target_host: '192.0.2.10', target_port: 443, dns_pool: false, unix_socket_path: '',
  health_check: true, proxy_protocol: 'none', accept_proxy_from: [], quota_bytes: null, quota_action: 'observe',
  quota_period: 'calendar_month', client_upload_mbps: null, client_download_mbps: null, enabled: true, deployed: true,
  deployment_state: 'applied', desired_fingerprint: '', deployed_fingerprint: '', delete_pending: false, custom_fragment: '',
  sticky_enabled: false, balance_mode: 'pool', shaper_mode: 'haproxy', sticky_mode: '', sticky_ttl: '',
  balance_algorithm: '', balance_random_draws: 0, leastping_tolerance: 0, balance_tolerance: 0, leastping_tolerance_ms: 0,
  sticky_hash: '', sticky_hash_balance_factor: 0, sticky_table_entries: '', sticky_ipv6_prefix: 0, slowstart: '',
  client_ipv6: true, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
} as unknown as RouteRecord;

/**
 * Stored routes as GET returns them (flat legacy rows from every era, and
 * servers[] rows), loaded into the editor and saved unchanged.
 */
export function loadedRoutes(): { name: string; route: RouteRecord }[] {
  const r = (patch: Record<string, unknown>) => ({ ...loadedBase, ...patch } as unknown as RouteRecord);
  const srv = (patch: Record<string, unknown>) => ({
    id: '', route_id: loadedBase.id, position: 1, name: 'srv1', target_type: 'tcp', host: '192.0.2.10', port: 443,
    unix_socket_path: '', backup: false, dns_pool: false, preferred_ip: '', weight: 0, cost: 0, ip_weights: [], ...patch,
  });
  return [
    { name: 'flat pre-051 (sticky_mode empty)', route: r({}) },
    { name: 'flat pre-051 sticky_enabled', route: r({ sticky_enabled: true }) },
    { name: 'flat roundrobin (000051 default)', route: r({ sticky_mode: 'roundrobin' }) },
    { name: 'flat leastconn legacy', route: r({ sticky_mode: 'leastconn' }) },
    { name: 'flat source', route: r({ sticky_mode: 'source', sticky_enabled: true }) },
    { name: 'flat source_table', route: r({ sticky_mode: 'source_table', sticky_enabled: true, sticky_ttl: '1h' }) },
    { name: 'flat sni (removed mode)', route: r({ sticky_mode: 'sni' }) },
    { name: 'flat fallback all-ip', route: r({ match_mode: 'fallback', fallback: true, snis: [], hostname: '', sticky_mode: 'roundrobin' }) },
    { name: 'flat fallback one-ip', route: r({ match_mode: 'fallback', fallback: true, snis: [], hostname: '', listener_ip: '203.0.113.1', sticky_mode: 'roundrobin' }) },
    { name: 'flat legacy dns_pool', route: r({ target_host: 'pool.example.com', dns_pool: true, sticky_mode: 'source', sticky_enabled: true }) },
    { name: 'flat legacy dns_pool pre-051', route: r({ target_host: 'pool.example.com', dns_pool: true }) },
    { name: 'flat unix', route: r({ target_type: 'unix', target_host: '', target_port: 0, unix_socket_path: '/run/app.sock', sticky_mode: 'roundrobin' }) },
    { name: 'flat block_new quota', route: r({ quota_bytes: 1073741824, quota_action: 'block_new', quota_period: 'daily' }) },
    { name: 'flat kernel shaper', route: r({ client_upload_mbps: 10, client_download_mbps: 20, shaper_mode: 'kernel' }) },
    { name: 'flat accept proxy all', route: r({ accept_proxy_from: ['0.0.0.0/0', '::/0'] }) },
    { name: 'flat accept proxy v4 all only', route: r({ accept_proxy_from: ['0.0.0.0/0'] }) },
    { name: 'flat no health v2', route: r({ health_check: false, proxy_protocol: 'v2' }) },
    { name: 'flat failover mode single', route: r({ balance_mode: 'failover' }) },
    { name: 'flat slowstart stored', route: r({ slowstart: '10s' }) },
    { name: 'flat ipv4-only', route: r({ client_ipv6: false }) },
    { name: 'flat custom fragment', route: r({ custom_fragment: '    timeout connect 5s' }) },
    { name: 'servers pool 2', route: r({ sticky_mode: 'none', balance_algorithm: 'roundrobin', servers: [srv({}), srv({ position: 2, name: 'srv2', host: '192.0.2.11' })] }) },
    { name: 'servers rc-era backup flags', route: r({ balance_mode: 'failover', sticky_mode: '', servers: [srv({}), srv({ position: 2, name: 'srv2', host: '192.0.2.11' }), srv({ position: 3, name: 'srv3', host: '192.0.2.12', backup: true })] }) },
    { name: 'servers failover canonical', route: r({ balance_mode: 'failover', servers: [srv({}), srv({ position: 2, name: 'srv2', host: '192.0.2.11', backup: true })] }) },
    { name: 'servers single dns pool pref', route: r({ balance_mode: 'failover', target_host: 'pool.example.com', servers: [srv({ host: 'pool.example.com', dns_pool: true, preferred_ip: '192.0.2.50' })] }) },
    { name: 'servers single dns pool leastping', route: r({ target_host: 'pool.example.com', sticky_mode: 'none', balance_algorithm: 'leastping', leastping_tolerance: 0.2, balance_tolerance: 0.2, servers: [srv({ host: 'pool.example.com', dns_pool: true })] }) },
    { name: 'servers leastconn costs', route: r({ sticky_mode: 'none', balance_algorithm: 'leastconn', servers: [srv({ cost: 2 }), srv({ position: 2, name: 'srv2', host: '192.0.2.11', weight: 3 })] }) },
    { name: 'servers leastconn tolerance', route: r({ sticky_mode: 'none', balance_algorithm: 'leastconn', leastping_tolerance: 0.3, balance_tolerance: 0.3, servers: [srv({}), srv({ position: 2, name: 'srv2', host: '192.0.2.11' })] }) },
    { name: 'servers source_table legacy sticky leastconn', route: r({ sticky_mode: 'leastconn', servers: [srv({}), srv({ position: 2, name: 'srv2', host: '192.0.2.11' })] }) },
    { name: 'servers ip weights', route: r({ sticky_mode: 'source', sticky_enabled: true, servers: [srv({ host: 'pool.example.com', dns_pool: true, ip_weights: [{ ip: '192.0.2.61', weight: 3 }] }), srv({ position: 2, name: 'srv2', host: '192.0.2.11' })] }) },
    { name: 'servers table entries invalid legacy', route: r({ sticky_mode: 'source_table', sticky_ttl: '7d', sticky_table_entries: '10k', servers: [srv({}), srv({ position: 2, name: 'srv2', host: '192.0.2.11' })] }) },
  ];
}

/**
 * Boundary values typed into single fields of a known-good multi-server
 * draft. Only values the editor accepts are exported.
 */
export function edgeDrafts(): { name: string; draft: RouteDraft }[] {
  const base = () => {
    const d = buildDraft({
      match: 'sni', listener: '*', acceptProxy: 'off', kind: 'ip+dnspool', layout: 'pool', sticky: 'none', algo: 'roundrobin',
      tuned: false, health: true, proxy: 'none', slowstart: false, quota: 'off', bandwidth: 'off', expert: false, ipv6: true,
    });
    d.name = 'edge';
    return d;
  };
  const out: { name: string; draft: RouteDraft }[] = [];
  const add = (name: string, mutate: (d: RouteDraft) => void) => {
    const d = base();
    mutate(d);
    if (model.validateRouteDraft(d, []).length === 0) out.push({ name, draft: d });
  };
  const hosts = ['01.2.3.4', '1.2.3.04', '192.0.2.1', 'backend.1', 'host.0x1f', 'a.b1', 'EXAMPLE.COM', 'example.com.', 'xn--80ak6aa92e.com',
    '1:::2', '::1', '::ffff:192.0.2.1', '2001:db8::1', '2001:DB8::1', 'fe80::1', '1::2::3', ':::', '1:2:3:4:5:6:7:8:9', 'a'.repeat(63) + '.com',
    `${'a'.repeat(63)}.${'b'.repeat(63)}.${'c'.repeat(63)}.${'d'.repeat(61)}`, '-a.com', 'a-.com', 'a_b.com', 'localhost', '1.2.3', '300.1.1.1'];
  for (const h of hosts) {
    add(`listener_ip=${h}`, (d) => { d.listenerIP = h; });
    add(`sni=${h}`, (d) => { d.snis = [h]; });
    add(`server host=${h}`, (d) => { d.servers[0].host = h; });
    add(`dns pool host=${h}`, (d) => { d.servers[1].host = h; });
    add(`accept_proxy_from=${h}`, (d) => { d.acceptProxyEnabled = true; d.acceptProxyFrom = [h]; });
    add(`preferred_ip=${h}`, (d) => { d.balanceMode = 'failover'; d.servers = [d.servers[1]]; d.servers[0].preferredIP = h; });
    add(`ip_weight ip=${h}`, (d) => { d.servers[1].ipWeights = [{ ip: h, weight: 2, cost: '' }]; });
  }
  for (const cidr of ['10.0.0.0/8', '10.0.0.1/8', '10.0.0.0/0', '1.2.3.4/0', '0.0.0.0/0', '::/0', '2001:db8::/0', '::ffff:0:0/96',
    '2001:db8::/129', '2001:db8::/32', '10.0.0.0/33', '10.0.0.0/08', '01.0.0.0/8', 'fe80::/10', '0.0.0.0/0 ', ' ::/0']) {
    add(`accept_proxy_from=${cidr}`, (d) => { d.acceptProxyEnabled = true; d.acceptProxyFrom = [cidr]; });
  }
  add('accept_proxy_from v4+v6 duplicates', (d) => { d.acceptProxyEnabled = true; d.acceptProxyFrom = ['2001:db8::1', '2001:DB8:0::1']; });
  add('ip_weights equal after normalisation', (d) => { d.servers[1].ipWeights = [{ ip: '2001:db8::1', weight: 2, cost: '' }, { ip: '2001:DB8:0::1', weight: 3, cost: '' }]; });
  for (const name of ['srv', 'a'.repeat(58), 'x_pref', 'X_PREF', 'srv1_pref2', 'a-b_c']) {
    add(`server name=${name}`, (d) => { d.servers[0].name = name; });
  }
  add('pool named x, static named x_1', (d) => { d.servers[1].name = 'x'; d.servers[0].name = 'x_1'; });
  add('pool named srv2, static named srv2_10', (d) => { d.servers[1].name = 'srv2'; d.servers[0].name = 'srv2_10'; });
  for (const p of ['/a', '/run/app.sock', '/' + 'a'.repeat(106), '/' + 'a'.repeat(107), '/run/../x', '/run/./x', '/run//x', '/run/x/', '/run/x y', '/run/ü']) {
    add(`unix path=${p}`, (d) => { d.servers[0] = { ...d.servers[0], targetType: 'unix', host: '', port: '', unixSocketPath: p }; });
  }
  for (const port of [1, 65535]) add(`ports=${port}`, (d) => { d.listenerPort = port; d.servers[0].port = port; });
  for (const w of [1, 256]) add(`weight=${w}`, (d) => { d.servers[0].weight = w; });
  for (const cost of [0.01, 0.015, 0.001, 99.999, 100]) {
    add(`leastping cost=${cost}`, (d) => { d.balanceAlgorithm = 'leastping'; d.servers[0].cost = cost; });
    add(`leastping ip cost=${cost}`, (d) => { d.balanceAlgorithm = 'leastping'; d.servers[1].ipWeights = [{ ip: '192.0.2.61', weight: '', cost }]; });
  }
  for (const pct of [0, 1, 99, 100]) {
    add(`leastping tolerance=${pct}`, (d) => { d.balanceAlgorithm = 'leastping'; d.leastpingTolerancePct = pct; d.leastpingToleranceMs = 1000; });
    add(`leastconn tolerance=${pct}`, (d) => { d.balanceAlgorithm = 'leastconn'; d.leastpingTolerancePct = pct; });
  }
  for (const draws of [1, 5]) add(`random draws=${draws}`, (d) => { d.balanceAlgorithm = 'random'; d.balanceRandomDraws = draws; });
  for (const f of [101, 1000]) add(`hash factor=${f}`, (d) => { d.stickyMode = 'source'; d.stickyHashBalanceFactor = f; });
  for (const prefix of [32, 64, 127, 128]) {
    add(`hash prefix=${prefix}`, (d) => { d.stickyMode = 'source'; d.stickyIPv6Prefix = prefix; });
    add(`table prefix=${prefix}`, (d) => { d.stickyMode = 'source_table'; d.stickyIPv6Prefix = prefix; });
  }
  for (const ttl of ['1m', '60s', '59s', '168h', '7d', '10080m', '169h', '9999999s', '1s']) add(`ttl=${ttl}`, (d) => { d.stickyMode = 'source_table'; d.stickyTTL = ttl; });
  for (const count of [1, 1024, 1025, 102400, 10239999, 10240000, 10485760, 5_000_000]) {
    add(`table clients=${count}`, (d) => { d.stickyMode = 'source_table'; d.stickyTableEntries = model.tableEntriesToken(count); });
  }
  for (const s of [1, 599, 600]) add(`slowstart=${s}s`, (d) => { d.slowstart = `${s}s`; });
  for (const [v, unit] of [[0.001, 'GiB'], [1, 'GiB'], [8191.999, 'TiB'], [8192, 'TiB'], [0.001, 'TiB']] as const) {
    add(`quota=${v}${unit}`, (d) => { d.quotaEnabled = true; d.quotaValue = v; d.quotaUnit = unit; });
  }
  for (const mbps of [1, 1_000_000]) add(`bandwidth=${mbps}`, (d) => { d.clientBandwidthEnabled = true; d.clientUploadMbps = mbps; d.clientDownloadMbps = mbps; });
  add('bandwidth enabled but empty', (d) => { d.clientBandwidthEnabled = true; });
  add('kernel shaper upload only', (d) => { d.clientBandwidthEnabled = true; d.clientUploadMbps = 5; d.shaperMode = 'kernel'; });
  for (const frag of ['balance roundrobin', 'server extra 192.0.2.99:443', 'default-server inter 3s', 'hash-type consistent', 'server-template x 2 a.example.com:443',
    'timeout connect 5s\r\nmaxconn 10', '\ttimeout server 30s', 'a'.repeat(512), `${'é'.repeat(256)}`, 'option tcp-check', 'x'.repeat(100) + '\n'.repeat(3) + 'y']) {
    add(`expert=${JSON.stringify(frag.slice(0, 30))}`, (d) => { d.expertOverride = frag; });
    add(`expert single-ip=${JSON.stringify(frag.slice(0, 30))}`, (d) => { d.expertOverride = frag; d.servers = [d.servers[0]]; });
  }
  add('name 80 chars', (d) => { d.name = 'n'.repeat(80); });
  add('name 80 emoji-ish', (d) => { d.name = '😀'.repeat(40); });
  add('name surrogate half', (d) => { d.name = '\ud800x'; });
  add('name with tab', (d) => { d.name = 'edge\tmsk'; });
  add('name trailing tab', (d) => { d.name = 'edge\t'; });
  add('64 snis', (d) => { d.snis = Array.from({ length: 64 }, (_, i) => `s${i}.example.com`); });
  add('16 servers', (d) => { d.servers = Array.from({ length: 16 }, (_, i) => ({ ...d.servers[0], _key: `e${i}`, name: `srv${i + 1}`, host: `192.0.2.${i + 1}` })); });
  add('sni uppercase + trailing dot duplicate', (d) => { d.snis = ['App.Example.com.', 'app.example.com']; });
  add('failover with dns reserve and 2 reserves', (d) => { d.balanceMode = 'failover'; d.servers = model.withCanonicalFailoverRoles([...d.servers, { ...d.servers[0], _key: 'z', name: 'srv9', host: '192.0.2.99' }]); });
  add('failover single dns pool pref + reserve static', (d) => {
    d.balanceMode = 'failover';
    d.servers = model.withCanonicalFailoverRoles([{ ...d.servers[1], preferredIP: '192.0.2.50' }, d.servers[0]]);
  });
  add('failover 2 primaries dns reserve', (d) => {
    d.balanceMode = 'failover';
    d.servers = [{ ...d.servers[0], backup: false }, { ...d.servers[0], _key: 'q', name: 'srvq', host: '192.0.2.77', backup: false }, { ...d.servers[1], backup: true }];
  });
  add('failover reserve first', (d) => { d.balanceMode = 'failover'; d.servers = [{ ...d.servers[0], backup: true }, { ...d.servers[1], backup: false }]; });
  add('block_new single dns pool legacy', (d) => { d.servers = [d.servers[1]]; d.quotaEnabled = true; d.quotaValue = 1; d.quotaAction = 'block_new'; });
  add('ip weights with static-rr', (d) => { d.balanceAlgorithm = 'static-rr'; d.servers[1].ipWeights = [{ ip: '192.0.2.61', weight: 2, cost: '' }]; });
  add('ip weights with map-based', (d) => { d.stickyMode = 'source'; d.stickyHash = 'map-based'; d.servers[1].ipWeights = [{ ip: '192.0.2.61', weight: 2, cost: '' }]; });
  add('ip weights cost-only outside leastping', (d) => { d.servers[1].ipWeights = [{ ip: '192.0.2.61', weight: '', cost: 2 }]; });
  add('ip weights 32', (d) => { d.servers[1].ipWeights = Array.from({ length: 32 }, (_, i) => ({ ip: `192.0.2.${i + 1}`, weight: 2, cost: '' })); });
  add('slowstart with no health, static only', (d) => { d.servers = [d.servers[0], { ...d.servers[0], _key: 'w', name: 'srvw', host: '192.0.2.88' }]; d.healthCheck = false; d.slowstart = '5s'; });
  add('listener "::" wildcard', (d) => { d.listenerIP = '::'; });
  add('listener "0.0.0.0" wildcard', (d) => { d.listenerIP = '0.0.0.0'; });
  add('listener "" (empty)', (d) => { d.listenerIP = ''; });
  add('listener " 203.0.113.1 " spaces', (d) => { d.listenerIP = ' 203.0.113.1 '; });
  return out;
}

export function uiPayloadCases(): UIPayloadCase[] {
  const cases: UIPayloadCase[] = [];
  for (const { name, draft } of draftMatrix()) {
    cases.push({ name: `new/${name}/draft`, method: 'POST', payload: model.routePayload(draft, false) });
    cases.push({ name: `new/${name}/enable`, method: 'PUT', payload: model.routePayload(draft, true, 1) });
  }
  for (const { name, draft } of edgeDrafts()) {
    cases.push({ name: `edge/${name}/enable`, method: 'PUT', payload: model.routePayload(draft, true, 1) });
  }
  for (const { name, route } of loadedRoutes()) {
    const draft = model.routeToDraft(route);
    cases.push({ name: `loaded/${name}/save`, method: 'PUT', payload: model.routePayload(draft, route.enabled, route.version), stored: route });
  }
  return cases;
}
