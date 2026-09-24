import type { AgentRelease, AuditEntry, TrafficHistory } from '../lib/contracts';
import type { NodeDetailData } from '../features/node-detail/useNodeDetail';
import type { TrafficRange } from '../features/nodes/useNodesOverview';
import { demoNodeBundles, mergeDemoRoutes } from './demo';

// Demo-only node detail builder, loaded via dynamic import in demo mode only.
function createDemoHistory(range: TrafficRange, baseRX: number, baseTX: number): TrafficHistory {
  const windows: Record<TrafficRange, number> = {
    '1m': 60_000,
    '5m': 5 * 60_000,
    '1h': 60 * 60_000,
    '24h': 24 * 60 * 60_000,
    '7d': 7 * 24 * 60 * 60_000,
    '30d': 30 * 24 * 60 * 60_000,
  };
  const sampleCount = range === '1m' ? 13 : range === '5m' ? 21 : 49;
  const windowMS = windows[range];
  const bucketMS = windowMS / (sampleCount - 1);
  const end = Date.now();
  const samples = Array.from({ length: sampleCount }, (_, index) => {
    const progress = index / (sampleCount - 1);
    const pulse = Math.sin(index * .86) * .08 + Math.sin(index * .23 + .7) * .11;
    const crest = Math.exp(-Math.pow((progress - .52) / .2, 2)) * .56;
    return {
      timestamp: new Date(end - windowMS + index * bucketMS).toISOString(),
      rx_bps: Math.max(0, baseRX * (.65 + crest + pulse)),
      tx_bps: Math.max(0, baseTX * (.58 + crest * .28 + pulse * .42)),
      cpu_percent: 19 + crest * 10 + pulse * 8,
      memory_percent: 38 + crest * 4 + pulse * 2,
    };
  });
  return { range, bucket_seconds: Math.max(1, Math.round(bucketMS / 1000)), samples };
}

export function createDemoDetail(nodeID: string, range: TrafficRange): NodeDetailData {
  const source = structuredClone(demoNodeBundles.find(({ node }) => node.id === nodeID) ?? demoNodeBundles[0]);
  const receivedAt = new Date(Date.now() - 8_000).toISOString();
  source.node.id = nodeID;
  source.node.status = 'online';
  source.node.last_seen_at = receivedAt;
  const latestHistory = source.history?.samples.at(-1);
  source.history = createDemoHistory(range, latestHistory?.rx_bps ?? 412e6, latestHistory?.tx_bps ?? 256e6);
  if (source.operational) {
    source.operational.node = source.node;
    source.operational.credential_prefix = 'nf_demo';
    source.operational.credential_last_used_at = new Date(Date.now() - 12_000).toISOString();
    source.operational.credential_expires_at = new Date(Date.now() + 720 * 86_400_000).toISOString();
    if (source.operational.latest_heartbeat) source.operational.latest_heartbeat.received_at = receivedAt;
    const metrics = source.operational.latest_heartbeat?.metrics;
    if (metrics) {
      metrics.uptime_seconds = 7 * 86_400 + 4 * 3_600 + 21 * 60;
      metrics.process_count = 138;
      metrics.process_names = ['haproxy', 'nodeflow-node-agent', 'sshd', 'systemd'];
      Object.values(metrics.haproxy_runtime?.backends ?? {}).forEach((backend, index) => {
        backend.connections_current = 1284 - index * 168;
        backend.tcp_sessions_current = 1261 - index * 164;
        backend.rx_bps = Math.max(0, 198e6 - index * 46e6);
        backend.tx_bps = Math.max(0, 124e6 - index * 38e6);
      });
      if (metrics.haproxy_runtime) {
        metrics.haproxy_runtime.servers = Object.fromEntries(
          Object.keys(metrics.haproxy_runtime.backends ?? {}).map((backendKey, index) => {
            const count = index < 2 ? 2 + (index === 1 ? 1 : 0) : 1;
            return [backendKey, Object.fromEntries(Array.from({ length: count }, (_, serverIndex) => [
              `srv_${serverIndex + 1}`,
              {
                status: 'UP', check_status: 'L7OK', active: true,
                sessions_current: Math.max(0, 620 - index * 110 - serverIndex * 34),
              },
            ]))];
          }),
        );
      }
    }
  }
  source.routes = source.routes.map((route, index) => ({
    ...route,
    node_id: nodeID,
    listener_port: index === 2 ? 10065 : index === 3 ? 8443 : index === 4 ? 9443 : 443,
    enabled: index !== 4,
    deployed: index !== 4,
    deployment_state: index === 4 ? 'draft' : 'active',
    quota_bytes: index === 0 ? 6 * 1024 ** 4 : index === 1 ? 3 * 1024 ** 4 : null,
  }));
  source.routes = mergeDemoRoutes(nodeID, source.routes);
  if (source.traffic) {
    source.traffic.node_id = nodeID;
    source.traffic.routes = source.traffic.routes.map((traffic, index) => ({
      ...traffic,
      limit_bytes: source.routes[index]?.quota_bytes ?? null,
      quota_used_bytes: source.routes[index]?.quota_bytes ? traffic.used_bytes : traffic.quota_used_bytes,
      quota_period: source.routes[index]?.quota_period,
      quota_action: source.routes[index]?.quota_action,
      applied: source.routes[index]?.enabled ?? false,
    }));
  }

  const now = Date.now();
  const audit: AuditEntry[] = [
    ['route.enabled', 'Маршрут api.internal включён'],
    ['agent.update.installed', 'Node Agent обновлён до 0.4.4-dev'],
    ['firewall.port.opened', 'Открыт listener 10065/TCP'],
    ['route.updated', 'Лимит connections изменён'],
    ['route.created', 'Создан черновик draft.new-service'],
    ['firewall.policy.updated', 'UFW переведён в автоматический режим'],
  ].map(([action, summary], index) => ({
    id: index + 1,
    actor_type: index === 1 ? 'agent' : 'browser_session',
    actor_id: index === 1 ? 'node-agent' : 'operator',
    action,
    resource_type: action.startsWith('route') ? 'route' : action.startsWith('firewall') ? 'firewall' : 'agent_release',
    details: { summary },
    source_ip: '192.0.2.2',
    created_at: new Date(now - index * 21 * 60_000).toISOString(),
  }));
  const releases: AgentRelease[] = [
    { id: '00000000-0000-4000-8000-000000000005', version: '0.4.5-dev', os: 'linux', arch: 'amd64', sha256: '5d0d4c6b', size_bytes: 7_340_032, sequence: 5, signature: 'verified', created_at: new Date(now - 3_600_000).toISOString() },
    { id: '00000000-0000-4000-8000-000000000004', version: '0.4.4-dev', os: 'linux', arch: 'amd64', sha256: '8d19bd20', size_bytes: 7_286_784, sequence: 4, signature: 'verified', created_at: new Date(now - 86_400_000).toISOString() },
    { id: '00000000-0000-4000-8000-000000000003', version: '0.4.3-dev', os: 'linux', arch: 'amd64', sha256: 'e73e7d79', size_bytes: 7_233_536, sequence: 3, signature: 'verified', created_at: new Date(now - 2 * 86_400_000).toISOString() },
  ];
  return {
    bundle: source,
    firewall: { node_id: nodeID, mode: 'apply', tcp_ports: [443, 8443, 10065], plan_complete: true, updated_at: new Date(now - 30_000).toISOString() },
    update: { node_id: nodeID, actual_sequence: 4, state: 'installed', last_report_at: new Date(now - 18_000).toISOString(), updated_at: new Date(now - 18_000).toISOString() },
    releases,
    audit,
    partialErrors: {},
  };
}
