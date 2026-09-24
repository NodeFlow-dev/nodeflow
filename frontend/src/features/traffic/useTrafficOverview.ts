import { useQuery } from '@tanstack/react-query';
import { livePollingOptions } from '../../lib/polling';
import { api, APIError } from '../../lib/api';
import type { DashboardOverview } from '../../lib/contracts';
import type { TrafficRange } from '../nodes/useNodesOverview';

const demoMode = new URLSearchParams(window.location.search).get('demo') === '1'
  || import.meta.env.VITE_NODEFLOW_DEMO === 'true';

export async function loadOverview(range: TrafficRange, selectedNodeId?: string): Promise<DashboardOverview> {
  if (demoMode) {
    const { buildDemoOverview } = await import('../../fixtures/demo-overview');
    return buildDemoOverview(range, selectedNodeId);
  }
  const search = new URLSearchParams({ range });
  if (selectedNodeId) search.set('node_id', selectedNodeId);
  return api<DashboardOverview>(`/api/v1/overview?${search.toString()}`);
}

export function useTrafficOverview(range: TrafficRange, selectedNodeId?: string) {
  return useQuery({
    queryKey: ['overview', range, selectedNodeId ?? 'all', demoMode],
    queryFn: () => loadOverview(range, selectedNodeId),
    ...livePollingOptions,
    retry: (count, error: unknown) => {
      const status = 'status' in Object(error) ? (error as { status?: number }).status : undefined;
      return status !== 401 && status !== 404 && count < 2;
    },
  });
}

export function isTrafficOverviewNotFound(error: unknown) {
  return error instanceof APIError && error.status === 404;
}

export function isTrafficDemoMode() {
  return demoMode;
}
