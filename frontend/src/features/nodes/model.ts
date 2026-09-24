import type { DashboardNode, HeartbeatMetrics, NodeBundle, RouteRecord, TrafficHistorySample } from '../../lib/contracts';
import type { TrafficRange } from './useNodesOverview';

export type OperationalState = 'online' | 'degraded' | 'offline';

export function dashboardState(item: DashboardNode): OperationalState {
  const status = String(item.node.status).toLowerCase();
  if (status === 'online') return 'online';
  if (status === 'degraded' || status === 'pending') return 'degraded';
  return 'offline';
}

// Node detail still uses its richer bundle read model.  The overview page does
// not call these helpers: it consumes DashboardOverview rates directly.
export function effectiveState(bundle: NodeBundle): OperationalState {
  const { node, operational } = bundle;
  const lastSeen = Date.parse(node.last_seen_at ?? '');
  if (node.status === 'offline' || node.status === 'error' || (Number.isFinite(lastSeen) && Date.now() - lastSeen > 45_000)) {
    return 'offline';
  }
  if (node.status !== 'online' || operational?.latest_heartbeat?.metrics.haproxy_stats_available === false) return 'degraded';
  return 'online';
}

export function memoryUsage(metrics: HeartbeatMetrics | undefined) {
  const total = Number(metrics?.memory_total_bytes);
  const available = Number(metrics?.memory_available_bytes);
  const used = total > 0 && Number.isFinite(available) ? Math.max(0, total - available) : null;
  const percent = used !== null ? (used / total) * 100 : Number(metrics?.memory_percent);
  return { total: total > 0 ? total : null, used, percent: Number.isFinite(percent) ? percent : null };
}

export function filterSamples(samples: TrafficHistorySample[], range: TrafficRange): TrafficHistorySample[] {
  const ordered = [...samples]
    .filter((sample) => Number.isFinite(Date.parse(sample.timestamp)))
    .sort((a, b) => Date.parse(a.timestamp) - Date.parse(b.timestamp));
  const milliseconds = range === '1m' ? 60_000 : range === '5m' ? 300_000 : 0;
  if (!milliseconds || !ordered.length) return ordered;
  const newest = Date.parse(ordered.at(-1)!.timestamp);
  const filtered = ordered.filter((sample) => Date.parse(sample.timestamp) >= newest - milliseconds);
  return filtered.length > 1 ? filtered : ordered.slice(-2);
}

export function routeName(route: RouteRecord): string {
  return route.name || (route.fallback
    ? `Весь трафик · ${route.listener_ip || '*'}:${route.listener_port}`
    : route.snis[0] ?? route.hostname ?? 'Без имени');
}
