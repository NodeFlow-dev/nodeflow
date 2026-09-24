import { APIError } from '../lib/api';
import { isAssignedHAProxyServer } from '../lib/haproxy';
import type {
  DashboardNode,
  DashboardOverview,
  DashboardOverviewTotal,
  DashboardRoute,
  NodeBundle,
  TrafficHistorySample,
} from '../lib/contracts';
import type { TrafficRange } from '../features/nodes/useNodesOverview';
import { demoNodeBundles } from './demo';

// Demo-only overview builder. Loaded via dynamic import so production bundles
// never fetch the fixture chunk unless ?demo=1 / VITE_NODEFLOW_DEMO is set.
const demoStepMs: Record<TrafficRange, number> = {
  '1m': 1_250,
  '5m': 6_250,
  '1h': 75_000,
  '24h': 30 * 60_000,
  '7d': 3.5 * 60 * 60_000,
  '30d': 15 * 60 * 60_000,
};

function latestRate(bundle: NodeBundle) {
  const sample = bundle.history?.samples.at(-1);
  return {
    rx: Number.isFinite(sample?.rx_bps) ? Number(sample?.rx_bps) : null,
    tx: Number.isFinite(sample?.tx_bps) ? Number(sample?.tx_bps) : null,
  };
}

function dashboardNode(bundle: NodeBundle, now: number, index: number): DashboardNode {
  const receivedAt = new Date(now - 7_000 - index * 900).toISOString();
  const rate = latestRate(bundle);
  const node = structuredClone(bundle.node);
  node.last_seen_at = receivedAt;
  const heartbeat = bundle.operational?.latest_heartbeat
    ? structuredClone(bundle.operational.latest_heartbeat)
    : undefined;
  if (heartbeat) heartbeat.received_at = receivedAt;

  return {
    node,
    latest_heartbeat: heartbeat,
    routes_total: bundle.operational?.routes_total ?? bundle.routes.length,
    routes_enabled: bundle.operational?.routes_enabled ?? bundle.routes.filter((route) => route.enabled).length,
    traffic_month: bundle.traffic?.month ?? bundle.operational?.traffic_month ?? '',
    traffic_bytes_in: bundle.traffic?.bytes_in ?? bundle.operational?.traffic_bytes_in ?? 0,
    traffic_bytes_out: bundle.traffic?.bytes_out ?? bundle.operational?.traffic_bytes_out ?? 0,
    traffic_used_bytes: bundle.traffic?.used_bytes ?? bundle.operational?.traffic_used_bytes ?? 0,
    traffic_observed: bundle.operational?.traffic_observed ?? Boolean(bundle.traffic),
    rx_bits_per_second: rate.rx,
    tx_bits_per_second: rate.tx,
    rate_sampled_at: rate.rx !== null || rate.tx !== null ? new Date(now).toISOString() : undefined,
  };
}

function demoHistory(bundles: NodeBundle[], range: TrafficRange, now: number): TrafficHistorySample[] {
  const count = Math.max(0, ...bundles.map((bundle) => bundle.history?.samples.length ?? 0));
  const step = demoStepMs[range];
  return Array.from({ length: count }, (_, index) => {
    let rx = 0;
    let tx = 0;
    for (const bundle of bundles) {
      const samples = bundle.history?.samples ?? [];
      const sample = samples[Math.max(0, samples.length - count + index)];
      rx += Number(sample?.rx_bps) || 0;
      tx += Number(sample?.tx_bps) || 0;
    }
    return {
      timestamp: new Date(now - (count - 1 - index) * step).toISOString(),
      rx_bps: rx,
      tx_bps: tx,
    };
  });
}

function demoRoutes(bundles: NodeBundle[]): DashboardRoute[] {
  const routes = bundles.flatMap((bundle) => {
    const { rx: nodeRX, tx: nodeTX } = latestRate(bundle);
    if (nodeRX === null || nodeTX === null) return [];
    const trafficByRoute = new Map((bundle.traffic?.routes ?? []).map((item) => [item.route_id, item]));
    const observedTotal = (bundle.traffic?.routes ?? []).reduce((sum, item) => sum + Number(item.used_bytes || 0), 0);
    return bundle.routes.map((route) => {
      const traffic = trafficByRoute.get(route.id);
      const routeWeight = observedTotal > 0 ? Number(traffic?.used_bytes || 0) / observedTotal : 1 / Math.max(1, bundle.routes.length);
      const rx = nodeRX * routeWeight;
      const tx = nodeTX * routeWeight;
      return {
        route_id: route.id,
        node_id: bundle.node.id,
        node_name: bundle.node.name,
        name: route.name || route.hostname || route.snis[0] || (route.fallback ? 'Весь TCP-трафик' : 'Маршрут'),
        listener_ip: route.listener_ip,
        listener_port: route.listener_port,
        snis: route.snis,
        fallback: route.fallback,
        bytes_in: traffic?.bytes_in ?? 0,
        bytes_out: traffic?.bytes_out ?? 0,
        used_bytes: traffic?.used_bytes ?? 0,
        rx_bits_per_second: rx,
        tx_bits_per_second: tx,
        bits_per_second: rx + tx,
        share_percent: 0,
      } satisfies DashboardRoute;
    });
  });
  const total = routes.reduce((sum, route) => sum + route.bits_per_second, 0);
  return routes
    .map((route) => ({ ...route, share_percent: total > 0 ? route.bits_per_second / total * 100 : 0 }))
    .sort((left, right) => right.bits_per_second - left.bits_per_second)
    .slice(0, 5);
}

function backendHealth(bundle: NodeBundle) {
  const runtime = bundle.operational?.latest_heartbeat?.metrics.haproxy_runtime;
  const serverStatuses = Object.values(runtime?.servers ?? {}).flatMap((servers) => (
    Object.values(servers).filter(isAssignedHAProxyServer).map((server) => server.status)
  ));
  const statuses = serverStatuses.length
    ? serverStatuses
    : Object.values(runtime?.backends ?? {}).map((backend) => backend.status);
  return statuses.reduce((result, value) => {
    const status = String(value ?? '').toUpperCase();
    if (status === 'UP' || status === 'OPEN') result.healthy += 1;
    else if (status === 'DOWN' || status === 'CLOSED' || status === 'MAINT') result.unavailable += 1;
    else result.degraded += 1;
    return result;
  }, { healthy: 0, degraded: 0, unavailable: 0 });
}

function demoTotals(bundles: NodeBundle[]): DashboardOverviewTotal {
  const totals: DashboardOverviewTotal = {
    nodes_total: bundles.length,
    nodes_online: 0,
    nodes_degraded: 0,
    nodes_offline: 0,
    routes_total: 0,
    connections_current: 0,
    rx_bits_per_second: null,
    tx_bits_per_second: null,
    current_rate_complete: false,
    traffic_month_bytes: 0,
    backends_healthy: 0,
    backends_degraded: 0,
    backends_unavailable: 0,
  };
  let rx = 0;
  let tx = 0;
  let rateNodes = 0;
  let rateComplete = true;
  for (const bundle of bundles) {
    const status = String(bundle.node.status).toLowerCase();
    if (status === 'online') totals.nodes_online += 1;
    else if (status === 'degraded') totals.nodes_degraded += 1;
    else totals.nodes_offline += 1;
    const rate = latestRate(bundle);
    const health = backendHealth(bundle);
    totals.routes_total += bundle.operational?.routes_total ?? bundle.routes.length;
    totals.connections_current += Number(bundle.operational?.latest_heartbeat?.metrics.haproxy_runtime?.connections_current) || 0;
    if (status !== 'offline') {
      rateNodes += 1;
      if (rate.rx === null || rate.tx === null) rateComplete = false;
      else {
        rx += rate.rx;
        tx += rate.tx;
      }
    }
    totals.traffic_month_bytes += bundle.operational?.traffic_used_bytes ?? bundle.traffic?.used_bytes ?? 0;
    totals.backends_healthy += health.healthy;
    totals.backends_degraded += health.degraded;
    totals.backends_unavailable += health.unavailable;
  }
  totals.current_rate_complete = rateNodes > 0 && rateComplete;
  if (totals.current_rate_complete) {
    totals.rx_bits_per_second = rx;
    totals.tx_bits_per_second = tx;
  }
  return totals;
}

export function buildDemoOverview(range: TrafficRange, selectedNodeId?: string): DashboardOverview {
  const now = Date.now();
  const bundles = structuredClone(demoNodeBundles);
  if (selectedNodeId && !bundles.some((bundle) => bundle.node.id === selectedNodeId)) {
    throw new APIError('Выбранная нода не найдена', 404, 'not_found');
  }
  const scoped = selectedNodeId ? bundles.filter((bundle) => bundle.node.id === selectedNodeId) : bundles;
  return {
    range,
    selected_node_id: selectedNodeId,
    nodes: bundles.map((bundle, index) => dashboardNode(bundle, now, index)),
    traffic_history: {
      range,
      bucket_seconds: Math.max(1, Math.round(demoStepMs[range] / 1000)),
      samples: demoHistory(scoped, range, now),
    },
    top_routes: demoRoutes(scoped),
    totals: demoTotals(bundles),
  };
}
