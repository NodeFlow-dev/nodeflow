import { useQuery } from '@tanstack/react-query';
import { livePollingOptions } from '../../lib/polling';
import type { DashboardOverview } from '../../lib/contracts';
import { APIError } from '../../lib/api';
import { loadOverview, isTrafficDemoMode } from '../traffic/useTrafficOverview';

export type TrafficRange = '1m' | '5m' | '1h' | '24h' | '7d' | '30d';

/**
 * The Nodes screen deliberately consumes the dashboard read model in one
 * request.  Do not fan this back out into per-node operational/routes/traffic
 * calls: current rates and route shares are sampled atomically by the Panel.
 */
export function useNodesOverview(range: TrafficRange, selectedNodeID?: string) {
  const demo = isTrafficDemoMode();
  return useQuery<DashboardOverview>({
    queryKey: ['overview', range, selectedNodeID ?? 'all', demo],
    queryFn: () => loadOverview(range, selectedNodeID),
    ...livePollingOptions,
    retry: (count, error: unknown) => {
      const status = 'status' in Object(error) ? (error as { status?: number }).status : undefined;
      return status !== 401 && status !== 404 && count < 2;
    },
  });
}

export function isNodesOverviewNotFound(error: unknown): boolean {
  return error instanceof APIError && error.status === 404;
}

export function isDemoMode(): boolean {
  return isTrafficDemoMode();
}
