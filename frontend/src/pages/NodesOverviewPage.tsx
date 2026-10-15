import { ActionIcon, Alert, Button, Menu, Select, Skeleton, TextInput } from '@mantine/core';
import {
  IconAlertTriangle, IconChevronDown, IconFilter, IconLayoutList, IconPlus, IconRefresh, IconSearch, IconSettings,
} from '@tabler/icons-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { LoginPanel } from '../components/LoginPanel';
import { PageHeader } from '../components/PageHeader';
import { SegmentTabs } from '../components/SegmentTabs';
import { StateView, RetryButton } from '../components/StateView';
import { Surface } from '../components/Surface';
import { AddNodeDialog } from '../features/nodes/AddNodeDialog';
import { KpiStack } from '../features/nodes/KpiStack';
import { dashboardState } from '../features/nodes/model';
import { NodesTable } from '../features/nodes/NodesTable';
import { RoutesRanking } from '../features/nodes/RoutesRanking';
import { TrafficChart } from '../features/nodes/TrafficChart';
import { isNodesOverviewNotFound, useNodesOverview, type TrafficRange } from '../features/nodes/useNodesOverview';
import { api, isUnauthorized } from '../lib/api';
import type { DashboardNode } from '../lib/contracts';
import { t } from '../i18n';

type StatusFilter = 'all' | 'online' | 'degraded' | 'offline';
const pageSize = 20;

function metricTotals(nodes: DashboardNode[]) {
  let connections = 0; let monthUsed = 0; let monthLimit = 0;
  let connectionsComplete = nodes.length > 0;
  let everyNodeHasLimit = nodes.length > 0;
  let up = 0; let degraded = 0; let down = 0;
  for (const item of nodes) {
    const metrics = item.latest_heartbeat?.metrics;
    const currentConnections = metrics?.haproxy_runtime?.connections_current;
    if (dashboardState(item) === 'offline' || !Number.isFinite(currentConnections)) connectionsComplete = false;
    else connections += Number(currentConnections);
    monthUsed += item.traffic_observed ? item.traffic_used_bytes : 0;
    const metadata = item.node.metadata;
    const nodeLimit = metadata && typeof metadata === 'object' ? Number(metadata.traffic_limit_bytes) : 0;
    if (Number.isFinite(nodeLimit) && nodeLimit > 0) monthLimit += nodeLimit;
    else everyNodeHasLimit = false;
    const backends = Object.values(metrics?.haproxy_runtime?.backends ?? {});
    for (const backend of backends) {
      const state = String(backend.status ?? '').toUpperCase();
      if (dashboardState(item) === 'offline') down += 1;
      else if (state === 'UP' || state === 'OPEN') up += 1;
      else if (state === 'DOWN' || state === 'CLOSED' || state === 'MAINT') down += 1;
      else degraded += 1;
    }
  }
  const backendTotal = up + degraded + down;
  return {
    connections: connectionsComplete ? connections : null,
    monthUsed,
    monthLimit: everyNodeHasLimit ? monthLimit : 0,
    health: { percent: backendTotal ? Math.round(up / backendTotal * 100) : null, up, degraded, down },
  };
}

function OverviewSkeleton() {
  return <div className="nf-overview-skeleton"><Skeleton height={392} /><Skeleton height={392} /><Skeleton height={458} /></div>;
}

export function NodesOverviewPage() {
  const navigate = useNavigate();
  const [range, setRange] = useState<TrafficRange>('24h');
  const [scope, setScope] = useState('all');
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState<StatusFilter>('all');
  const [page, setPage] = useState(1);
  const [relaxedRows, setRelaxedRows] = useState(false);
  const [dialogOpened, setDialogOpened] = useState(false);
  const [recoveredScope, setRecoveredScope] = useState(false);
  const [nodeOrder, setNodeOrder] = useState<string[]>([]);
  const [orderingNodeID, setOrderingNodeID] = useState('');
  const [orderError, setOrderError] = useState('');
  const result = useNodesOverview(range, scope === 'all' ? undefined : scope);
  const queryOverview = result.data;
  const allOverviewRef = useRef(queryOverview);
  if (scope === 'all' && queryOverview) allOverviewRef.current = queryOverview;
  const selectedScopeInvalid = scope !== 'all' && (
    isNodesOverviewNotFound(result.error)
    || Boolean(queryOverview && !queryOverview.nodes.some((item) => item.node.id === scope))
  );
  const effectiveScope = selectedScopeInvalid ? 'all' : scope;
  const overview = selectedScopeInvalid ? (allOverviewRef.current ?? queryOverview) : queryOverview;
  const sourceNodes = useMemo(() => overview?.nodes ?? [], [overview?.nodes]);
  const nodes = useMemo(() => {
    if (!nodeOrder.length) return sourceNodes;
    const positions = new Map(nodeOrder.map((id, index) => [id, index]));
    return [...sourceNodes].sort((a, b) => (positions.get(a.node.id) ?? Number.MAX_SAFE_INTEGER) - (positions.get(b.node.id) ?? Number.MAX_SAFE_INTEGER));
  }, [nodeOrder, sourceNodes]);
  const scoped = effectiveScope === 'all' ? nodes : nodes.filter((item) => item.node.id === effectiveScope);
  const selectedScopeNode = effectiveScope === 'all' ? undefined : scoped[0];
  const baseTotals = metricTotals(scoped);
  let rx: number | null = null;
  let tx: number | null = null;
  if (selectedScopeNode?.rate_sampled_at) {
    if (Number.isFinite(selectedScopeNode.rx_bits_per_second)) rx = Number(selectedScopeNode.rx_bits_per_second);
    if (Number.isFinite(selectedScopeNode.tx_bits_per_second)) tx = Number(selectedScopeNode.tx_bits_per_second);
  } else if (
    effectiveScope === 'all'
    && overview?.totals.current_rate_complete === true
    && Number.isFinite(overview.totals.rx_bits_per_second)
    && Number.isFinite(overview.totals.tx_bits_per_second)
  ) {
    rx = Number(overview.totals.rx_bits_per_second);
    tx = Number(overview.totals.tx_bits_per_second);
  }
  const totals = { ...baseTotals, rx, tx, rateComplete: rx !== null && tx !== null };
  const samples = overview?.traffic_history.samples ?? [];
  const ranking = overview?.top_routes ?? [];
  const statusCounts = useMemo(() => nodes.reduce((counts, item) => {
    counts[dashboardState(item)] += 1; return counts;
  }, { online: 0, degraded: 0, offline: 0 }), [nodes]);
  const filtered = nodes.filter((item) => {
    const matchesStatus = status === 'all' || dashboardState(item) === status;
    const normalized = `${item.node.name} ${item.node.address}`.toLowerCase();
    return matchesStatus && normalized.includes(query.trim().toLowerCase());
  });
  const paginated = filtered.slice((page - 1) * pageSize, page * pageSize);
  const trend = samples.slice(-18).flatMap((sample) => (
    sample.rx_bps === null || sample.tx_bps === null ? [] : [sample.rx_bps + sample.tx_bps]
  ));
  const latestSample = [...samples].reverse().find((sample) => sample.rx_bps !== null || sample.tx_bps !== null);
  const latestSampleAt = latestSample?.timestamp ? Date.parse(latestSample.timestamp) : Number.NaN;
  const freshnessWindow = Math.max(45_000, (overview?.traffic_history.bucket_seconds ?? 15) * 2_500);
  const historyStale = Boolean(latestSample && (!Number.isFinite(latestSampleAt) || Date.now() - latestSampleAt > freshnessWindow));
  const chartStatus: { tone: 'online' | 'degraded' | 'offline' | 'stale'; label: string } = (() => {
    if ((overview && result.isError) || historyStale) return { tone: 'stale', label: t('nodes.dataIsStale') };
    if (!latestSample) return { tone: 'offline', label: t('nodes.noFreshData') };
    if (!totals.rateComplete) return { tone: 'degraded', label: t('nodes.currentDataIsIncomplete') };
    if (selectedScopeNode) {
      const selectedState = dashboardState(selectedScopeNode);
      if (selectedState === 'offline') return { tone: 'offline', label: t('nodes.nodeIsUnreachable') };
      if (selectedState === 'degraded') return { tone: 'degraded', label: t('nodes.nodeIsDegraded') };
      return { tone: 'online', label: t('nodes.online') };
    }
    if (statusCounts.online === 0 && statusCounts.degraded === 0) return { tone: 'offline', label: t('nodes.nodesAreUnreachable') };
    if (statusCounts.degraded > 0 || statusCounts.offline > 0) return { tone: 'degraded', label: t('nodes.partiallyAvailable') };
    return { tone: 'online', label: t('nodes.online') };
  })();
  const changeFilter = (value: StatusFilter) => { setStatus(value); setPage(1); };

  useEffect(() => {
    const incoming = sourceNodes.map((item) => item.node.id);
    setNodeOrder((current) => {
      if (current.length === incoming.length && current.every((id) => incoming.includes(id))) return current;
      return incoming;
    });
  }, [sourceNodes]);

  const reorderNodes = async (sourceID: string, targetID: string) => {
    if (sourceID === targetID || orderingNodeID) return;
    const previous = nodes.map((item) => item.node.id);
    const next = [...previous];
    const sourceIndex = next.indexOf(sourceID);
    const targetIndex = next.indexOf(targetID);
    if (sourceIndex < 0 || targetIndex < 0) return;
    next.splice(targetIndex, 0, next.splice(sourceIndex, 1)[0]);
    setNodeOrder(next);
    setOrderingNodeID(sourceID);
    setOrderError('');
    try {
      await api('/api/v1/nodes/order', { method: 'PUT', body: JSON.stringify({ node_ids: next }) });
      await result.refetch();
    } catch (error) {
      setNodeOrder(previous);
      setOrderError(error instanceof Error ? error.message : t('nodes.nodeOrderNotSaved'));
      void result.refetch();
    } finally {
      setOrderingNodeID('');
    }
  };

  useEffect(() => {
    if (!selectedScopeInvalid) return;
    setScope('all');
    setRecoveredScope(true);
  }, [selectedScopeInvalid]);

  if (result.isError && isUnauthorized(result.error)) return <LoginPanel onSuccess={() => result.refetch()} />;

  return (
    <main className="nf-page nf-nodes-page">
      <PageHeader
        title={t('app.nodes')}
        actions={<>
          <TextInput className="nf-top-search" placeholder={t('nodes.searchNodes')} value={query} onChange={(event) => { setQuery(event.currentTarget.value); setPage(1); }} leftSection={<IconSearch size={16} />} aria-label={t('nodes.searchNodes2')} />
          <StatusMenu value={status} onChange={changeFilter} />
          <ActionIcon variant="default" size="lg" onClick={() => result.refetch()} loading={result.isFetching} aria-label={t('nodes.refresh')}><IconRefresh size={18} /></ActionIcon>
          <ActionIcon variant="default" size="lg" onClick={() => navigate('/settings')} aria-label={t('app.settings')}><IconSettings size={18} /></ActionIcon>
          <Button className="nf-primary-action" leftSection={<IconPlus size={18} />} onClick={() => setDialogOpened(true)}>{t('nodes.addNode')}</Button>
        </>}
      />

      {recoveredScope && (
        <Alert
          mb="sm"
          color="yellow"
          icon={<IconAlertTriangle size={17} />}
          title={t('nodes.sourceReset')}
          role="status"
          withCloseButton
          closeButtonLabel={t('nodes.dismissNotice')}
          onClose={() => setRecoveredScope(false)}
        >
          {t('nodes.theSelectedNodeIsNo')}
        </Alert>
      )}

      {result.isPending && !overview ? <OverviewSkeleton /> : result.isError && !selectedScopeInvalid && !overview ? (
        <StateView title={t('nodes.failedToLoadNodes')} description={result.error instanceof Error ? result.error.message : t('nodes.panelApiIsUnreachable')} tone="error" action={<RetryButton onClick={() => result.refetch()} />} />
      ) : !nodes.length ? (
        <StateView title={t('nodes.noNodesYet')} description={t('nodes.addAServerOverSsh')} action={<Button leftSection={<IconPlus size={17} />} onClick={() => setDialogOpened(true)}>{t('nodes.addNode')}</Button>} />
      ) : (
        <div className="nf-nodes-content">
          <div className="nf-overview-grid">
            <Surface className="nf-traffic-module" title={t('app.traffic')} actions={<Select className="nf-scope-select" value={effectiveScope} onChange={(value) => { setRecoveredScope(false); setScope(value ?? 'all'); }} data={[{ value: 'all', label: t('nodes.allNodes') }, ...nodes.map(({ node }) => ({ value: node.id, label: node.name }))]} allowDeselect={false} aria-label={t('nodes.trafficSource')} />}>
              <TrafficChart samples={samples} range={range} onRangeChange={setRange} currentRx={totals.rx} currentTx={totals.tx} status={chartStatus} />
            </Surface>
            <Surface className="nf-ranking-module"><RoutesRanking routes={ranking} /></Surface>
            <KpiStack {...totals} aggregateScope={effectiveScope === 'all'} trend={trend} />
          </div>

          <Surface className={`nf-nodes-module${relaxedRows ? ' is-relaxed' : ''}`}>
            <div className="nf-table-toolbar">
              <SegmentTabs
                className="nf-status-tabs"
                value={status}
                onChange={changeFilter}
                label={t('nodes.statusFilter')}
                items={[
                  { value: 'all', label: t('nodes.allNodes'), count: nodes.length },
                  { value: 'online', label: t('nodes.online2'), count: statusCounts.online },
                  { value: 'degraded', label: t('nodes.degraded'), count: statusCounts.degraded },
                  { value: 'offline', label: t('nodes.unreachable'), count: statusCounts.offline },
                ]}
              />
              <div className="nf-table-actions">
                <TextInput placeholder={t('nodes.searchNodes')} value={query} onChange={(event) => { setQuery(event.currentTarget.value); setPage(1); }} leftSection={<IconSearch size={16} />} aria-label={t('nodes.searchInTable')} />
                <StatusMenu value={status} onChange={changeFilter} compact />
                <ActionIcon variant="default" size="lg" onClick={() => setRelaxedRows((value) => !value)} aria-label={t('nodes.toggleRowDensity')} aria-pressed={relaxedRows}><IconLayoutList size={18} /></ActionIcon>
              </div>
            </div>
            {orderError && <div className="nf-inline-error nf-order-error" role="alert">{orderError}<button type="button" onClick={() => setOrderError('')}>{t('nodes.close')}</button></div>}
            {filtered.length ? (
              <NodesTable
                nodes={paginated}
                page={page}
                pageSize={pageSize}
                total={filtered.length}
                onPageChange={setPage}
                reorderEnabled={!query.trim() && status === 'all' && page === 1}
                reorderDisabledReason={query.trim() || status !== 'all' ? t('nodes.clearSearchAndFiltersTo') : t('nodes.reorderingIsOnlyAvailableOn')}
                orderingNodeID={orderingNodeID}
                onReorder={reorderNodes}
              />
            ) : <StateView title={t('nodes.nothingFound')} description={t('nodes.changeTheSearchOrStatus')} />}
          </Surface>
        </div>
      )}
      <AddNodeDialog opened={dialogOpened} onClose={() => setDialogOpened(false)} onInstalled={() => result.refetch()} />
    </main>
  );
}

function StatusMenu({ value, onChange, compact = false }: { value: StatusFilter; onChange: (value: StatusFilter) => void; compact?: boolean }) {
  return (
    <Menu position="bottom-end" withinPortal>
      <Menu.Target>
        <Button className={`nf-status-menu-trigger${compact ? ' is-compact' : ''}`} variant="default" leftSection={<IconFilter size={17} />} rightSection={<IconChevronDown size={15} />}>{t('nodes.filters')}</Button>
      </Menu.Target>
      <Menu.Dropdown>
        <Menu.Label>{t('nodes.nodeStatus')}</Menu.Label>
        {([['all', t('nodes.allNodes')], ['online', t('nodes.online2')], ['degraded', t('nodes.degraded')], ['offline', t('nodes.unreachable')]] as const).map(([status, label]) => (
          <Menu.Item key={status} className={value === status ? 'is-selected' : ''} onClick={() => onChange(status)}>{label}</Menu.Item>
        ))}
      </Menu.Dropdown>
    </Menu>
  );
}
