import { useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query';
import { agentReleasesQueryKey, livePollingOptions } from '../../lib/polling';
import { api } from '../../lib/api';
import type {
  AgentRelease,
  AuditEntry,
  NodeAgentUpdateState,
  NodeBundle,
  NodeFirewallPolicy,
  NodeOperational,
  NodeRecord,
  NodeTraffic,
  RouteRecord,
  TrafficHistory,
} from '../../lib/contracts';
import type { TrafficRange } from '../nodes/useNodesOverview';

export interface NodeDetailData {
  bundle: NodeBundle;
  firewall: NodeFirewallPolicy | null;
  update: NodeAgentUpdateState | null;
  releases: AgentRelease[];
  audit: AuditEntry[];
  partialErrors: Partial<Record<'operational' | 'routes' | 'traffic' | 'history' | 'firewall' | 'update' | 'releases' | 'audit', string>>;
}

const explicitDemo = new URLSearchParams(window.location.search).get('demo') === '1'
  || import.meta.env.VITE_NODEFLOW_DEMO === 'true';

function errorMessage(reason: unknown): string {
  return reason instanceof Error ? reason.message : 'Данные недоступны';
}

function settledValue<T>(result: PromiseSettledResult<T>, fallback: T, key: keyof NodeDetailData['partialErrors'], errors: NodeDetailData['partialErrors']): T {
  if (result.status === 'fulfilled') return result.value;
  errors[key] = errorMessage(result.reason);
  return fallback;
}


// Releases are static between explicit uploads/deletes, so they are cached
// with staleTime: Infinity and shared across polls instead of being refetched
// every 15 s. Refresh/upload/delete invalidate agentReleasesQueryKey.
function loadReleases(queryClient: QueryClient): Promise<AgentRelease[]> {
  return queryClient.fetchQuery({
    queryKey: agentReleasesQueryKey,
    queryFn: () => api<AgentRelease[]>('/api/v1/agent-releases'),
    staleTime: Infinity,
  });
}

async function loadNodeDetail(queryClient: QueryClient, nodeID: string, range: TrafficRange): Promise<NodeDetailData> {
  if (explicitDemo) {
    const { createDemoDetail } = await import('../../fixtures/demo-node-detail');
    return createDemoDetail(nodeID, range);
  }
  const results = await Promise.allSettled([
    api<NodeRecord>(`/api/v1/nodes/${nodeID}`),
    api<NodeOperational>(`/api/v1/nodes/${nodeID}/operational`),
    api<RouteRecord[]>(`/api/v1/nodes/${nodeID}/routes`),
    api<NodeTraffic>(`/api/v1/nodes/${nodeID}/traffic`),
    api<TrafficHistory>(`/api/v1/nodes/${nodeID}/traffic/history?range=${encodeURIComponent(range)}`),
    api<NodeFirewallPolicy>(`/api/v1/nodes/${nodeID}/firewall`),
    api<NodeAgentUpdateState>(`/api/v1/nodes/${nodeID}/agent-update`),
    loadReleases(queryClient),
    api<AuditEntry[]>(`/api/v1/nodes/${nodeID}/audit?limit=40`),
  ] as const);
  const nodeResult = results[0];
  if (nodeResult.status === 'rejected') throw nodeResult.reason;
  const partialErrors: NodeDetailData['partialErrors'] = {};
  const operational = settledValue(results[1], null as NodeOperational | null, 'operational', partialErrors);
  const routes = settledValue(results[2], [], 'routes', partialErrors);
  const traffic = settledValue(results[3], null as NodeTraffic | null, 'traffic', partialErrors);
  const history = settledValue(results[4], null as TrafficHistory | null, 'history', partialErrors);
  return {
    bundle: { node: nodeResult.value, operational, routes, traffic, history },
    firewall: settledValue(results[5], null as NodeFirewallPolicy | null, 'firewall', partialErrors),
    update: settledValue(results[6], null as NodeAgentUpdateState | null, 'update', partialErrors),
    releases: settledValue(results[7], [], 'releases', partialErrors),
    audit: settledValue(results[8], [], 'audit', partialErrors),
    partialErrors,
  };
}

export function useNodeDetail(nodeID: string, range: TrafficRange) {
  const queryClient = useQueryClient();
  return useQuery({
    queryKey: ['node-detail', nodeID, range, explicitDemo],
    queryFn: () => loadNodeDetail(queryClient, nodeID, range),
    enabled: Boolean(nodeID),
    ...livePollingOptions,
    retry: (count, error: unknown) => !('status' in Object(error) && (error as { status?: number }).status === 401) && count < 2,
  });
}

export function isNodeDetailDemoMode(): boolean {
  return explicitDemo;
}
