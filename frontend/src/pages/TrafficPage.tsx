import { ActionIcon, Select, Skeleton, TextInput } from '@mantine/core';
import {
  IconActivityHeartbeat,
  IconArrowDown,
  IconArrowUp,
  IconPlugConnected,
  IconRefresh,
  IconRoute,
  IconSearch,
  IconServer,
} from '@tabler/icons-react';
import { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { LoginPanel } from '../components/LoginPanel';
import { RetryButton, StateView } from '../components/StateView';
import { Surface } from '../components/Surface';
import { TrafficChart } from '../features/nodes/TrafficChart';
import type { TrafficRange } from '../features/nodes/useNodesOverview';
import { TrafficNodesTable, trafficNodeState, type TrafficNodeState } from '../features/traffic/TrafficNodesTable';
import { TrafficRouteRanking } from '../features/traffic/TrafficRouteRanking';
import { isTrafficDemoMode, isTrafficOverviewNotFound, useTrafficOverview } from '../features/traffic/useTrafficOverview';
import { isUnauthorized } from '../lib/api';
import type { DashboardNode } from '../lib/contracts';
import { formatBitrate, formatBytes, formatNumber } from '../lib/format';
import '../styles/traffic.css';

type StatusFilter = 'all' | TrafficNodeState;

const supportedRanges = new Set<TrafficRange>(['1m', '5m', '1h', '24h', '7d', '30d']);

function healthFromNode(node: DashboardNode) {
  const runtime = node.latest_heartbeat?.metrics.haproxy_runtime;
  const serverStatuses = Object.values(runtime?.servers ?? {}).flatMap((servers) => Object.values(servers).filter((server) => server.address === undefined || Boolean(server.address)).map((server) => server.status));
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

function trafficLimit(node: DashboardNode | undefined) {
  const value = Number(node?.node.metadata?.traffic_limit_bytes);
  return Number.isFinite(value) && value > 0 ? value : 0;
}

function TrafficPageSkeleton() {
  return (
    <div className="nf-traffic-page__skeleton" aria-label="Загрузка статистики трафика">
      <Skeleton height={392} /><Skeleton height={88} /><Skeleton height={300} />
    </div>
  );
}

export function TrafficPage() {
  const [params, setParams] = useSearchParams();
  const rawRange = params.get('range') as TrafficRange | null;
  const range: TrafficRange = rawRange && supportedRanges.has(rawRange) ? rawRange : '24h';
  const selectedNodeId = params.get('node') || undefined;
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState<StatusFilter>('all');
  const [recoveredNode, setRecoveredNode] = useState(false);
  const result = useTrafficOverview(range, selectedNodeId);
  const overview = result.data;
  const nodes = overview?.nodes ?? [];
  const selectedNode = nodes.find((node) => node.node.id === selectedNodeId);

  const missingSelectedNode = Boolean(selectedNodeId && isTrafficOverviewNotFound(result.error));
  useEffect(() => {
    if (!missingSelectedNode) return;
    setRecoveredNode(true);
    setParams((current) => {
      const next = new URLSearchParams(current);
      next.delete('node');
      if (!next.has('range')) next.set('range', range);
      return next;
    }, { replace: true });
  }, [missingSelectedNode, range, setParams]);

  const updateQuery = (key: 'range' | 'node', value?: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value); else next.delete(key);
    if (!next.has('range')) next.set('range', range);
    setParams(next, { replace: true });
  };

  const statusCounts = useMemo(() => nodes.reduce((counts, node) => {
    counts[trafficNodeState(node)] += 1;
    return counts;
  }, { online: 0, degraded: 0, offline: 0 }), [nodes]);

  const filteredNodes = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase();
    return nodes.filter((node) => {
      const stateMatches = status === 'all' || trafficNodeState(node) === status;
      const queryMatches = !normalizedQuery || `${node.node.name} ${node.node.address}`.toLowerCase().includes(normalizedQuery);
      return stateMatches && queryMatches;
    });
  }, [nodes, query, status]);

  const totals = overview?.totals;
  const selectedHealth = selectedNode ? healthFromNode(selectedNode) : null;
  const health = selectedHealth ?? {
    healthy: totals?.backends_healthy ?? 0,
    degraded: totals?.backends_degraded ?? 0,
    unavailable: totals?.backends_unavailable ?? 0,
  };
  const healthTotal = health.healthy + health.degraded + health.unavailable;
  const healthPercent = healthTotal ? Math.round(health.healthy / healthTotal * 100) : null;
  const selectedRateObserved = Boolean(selectedNode?.rate_sampled_at);
  const aggregateRateComplete = totals?.current_rate_complete === true
    && Number.isFinite(totals.rx_bits_per_second)
    && Number.isFinite(totals.tx_bits_per_second);
  const rx = selectedNode
    ? selectedRateObserved && Number.isFinite(selectedNode.rx_bits_per_second) ? Number(selectedNode.rx_bits_per_second) : null
    : aggregateRateComplete ? Number(totals?.rx_bits_per_second) : null;
  const tx = selectedNode
    ? selectedRateObserved && Number.isFinite(selectedNode.tx_bits_per_second) ? Number(selectedNode.tx_bits_per_second) : null
    : aggregateRateComplete ? Number(totals?.tx_bits_per_second) : null;
  const currentRateComplete = rx !== null && tx !== null;
  const currentRateNote = currentRateComplete
    ? 'из HAProxy counters'
    : selectedNode && !selectedRateObserved ? 'нет текущей точки' : 'неполные данные';
  const monthUsed = selectedNode?.traffic_used_bytes ?? totals?.traffic_month_bytes ?? 0;
  const nodeLimits = nodes.map(trafficLimit);
  const monthLimit = selectedNode
    ? trafficLimit(selectedNode)
    : nodeLimits.length && nodeLimits.every((limit) => limit > 0)
      ? nodeLimits.reduce((sum, limit) => sum + limit, 0)
      : 0;
  const selectedConnections = selectedNode?.latest_heartbeat?.metrics.haproxy_runtime?.connections_current;
  const aggregateConnectionsComplete = nodes.length > 0 && nodes.every((node) => (
    trafficNodeState(node) !== 'offline'
    && Number.isFinite(node.latest_heartbeat?.metrics.haproxy_runtime?.connections_current)
  ));
  const connections = selectedNode
    ? trafficNodeState(selectedNode) !== 'offline' && Number.isFinite(selectedConnections) ? Number(selectedConnections) : null
    : aggregateConnectionsComplete
      ? nodes.reduce((sum, node) => sum + Number(node.latest_heartbeat?.metrics.haproxy_runtime?.connections_current), 0)
      : null;
  const routes = selectedNode?.routes_total ?? totals?.routes_total ?? 0;
  const scopeLabel = selectedNode?.node.name ?? 'Все ноды';
  const totalCurrentRate = aggregateRateComplete
    ? Number(totals?.rx_bits_per_second) + Number(totals?.tx_bits_per_second)
    : null;
  const latestSample = [...(overview?.traffic_history.samples ?? [])].reverse().find((sample) => (
    sample.rx_bps !== null || sample.tx_bps !== null
  ));
  const latestSampleAt = latestSample?.timestamp ? Date.parse(latestSample.timestamp) : Number.NaN;
  const freshnessWindow = Math.max(45_000, (overview?.traffic_history.bucket_seconds ?? 15) * 2_500);
  const historyStale = Boolean(latestSample && (
    !Number.isFinite(latestSampleAt) || Date.now() - latestSampleAt > freshnessWindow
  ));
  const backgroundError = Boolean(overview && result.isError);
  const lastSuccessLabel = result.dataUpdatedAt
    ? new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(result.dataUpdatedAt)
    : '—';
  const chartStatus: { tone: 'online' | 'degraded' | 'offline' | 'stale'; label: string } = (() => {
    if (backgroundError || historyStale) return { tone: 'stale', label: 'Данные устарели' };
    if (!latestSample) return { tone: 'offline', label: 'Нет свежих данных' };
    if (!currentRateComplete) return {
      tone: 'degraded',
      label: selectedNode ? 'Нет полной текущей точки' : 'Текущие данные неполные',
    };
    if (selectedNode) {
      const nodeState = trafficNodeState(selectedNode);
      if (nodeState === 'offline') return { tone: 'offline', label: 'Нода недоступна' };
      if (nodeState === 'degraded') return { tone: 'degraded', label: 'Нода деградирует' };
      return { tone: 'online', label: 'Онлайн' };
    }
    if ((totals?.nodes_online ?? 0) === 0) return { tone: 'offline', label: 'Ноды недоступны' };
    if ((totals?.nodes_degraded ?? 0) > 0 || (totals?.nodes_offline ?? 0) > 0) return { tone: 'degraded', label: 'Частично доступно' };
    return { tone: 'online', label: 'Онлайн' };
  })();
  const refreshLabel = backgroundError
    ? `Нет связи · последние данные ${lastSuccessLabel}`
    : result.isFetching
      ? 'Обновление…'
      : `Обновлено ${lastSuccessLabel}`;

  if (result.isError && isUnauthorized(result.error)) return <LoginPanel onSuccess={() => result.refetch()} />;

  return (
    <main className="nf-page nf-traffic-page">
      <header className="nf-page-header nf-traffic-page__header">
        <div>
          <h1>Трафик</h1>
          {isTrafficDemoMode() && <span className="nf-demo-badge">Демо-данные</span>}
        </div>
        <div className="nf-page-tools nf-traffic-page__tools">
          <Select
            className="nf-traffic-page__scope"
            value={selectedNodeId ?? 'all'}
            onChange={(value) => updateQuery('node', value === 'all' ? undefined : value ?? undefined)}
            data={[{ value: 'all', label: 'Все ноды' }, ...nodes.map((node) => ({ value: node.node.id, label: node.node.name }))]}
            allowDeselect={false}
            searchable
            aria-label="Источник статистики трафика"
            leftSection={<IconServer size={16} />}
            disabled={!nodes.length}
          />
          <ActionIcon variant="default" size="lg" onClick={() => result.refetch()} loading={result.isFetching} aria-label="Обновить статистику трафика"><IconRefresh size={18} /></ActionIcon>
        </div>
      </header>

      {recoveredNode && (
        <div className="nf-traffic-page__recovery" role="status">
          Выбранная нода больше недоступна. Показан общий трафик всех нод.
          <button type="button" onClick={() => setRecoveredNode(false)} aria-label="Скрыть уведомление">Скрыть</button>
        </div>
      )}

      {result.isPending || missingSelectedNode ? <TrafficPageSkeleton /> : result.isError && !overview ? (
        <StateView title="Не удалось загрузить трафик" description={result.error instanceof Error ? result.error.message : 'Panel API недоступен'} tone="error" action={<RetryButton onClick={() => result.refetch()} />} />
      ) : !overview || !nodes.length ? (
        <StateView title="Нет данных о трафике" description="Сначала добавьте ноду и дождитесь первого сигнала Node Agent." />
      ) : (
        <>
          <Surface className="nf-traffic-page__workspace">
            <section className="nf-traffic-page__chart" aria-labelledby="traffic-chart-title">
              <header>
                <div><h2 id="traffic-chart-title">Трафик HAProxy</h2><p>{scopeLabel} · RX и TX</p></div>
                <span className={`nf-traffic-page__live is-${backgroundError ? 'stale' : 'fresh'}`} role="status" aria-live="polite"><i />{refreshLabel}</span>
              </header>
              <TrafficChart
                samples={overview.traffic_history.samples}
                range={range}
                onRangeChange={(value) => updateQuery('range', value)}
                currentRx={rx}
                currentTx={tx}
                status={chartStatus}
              />
            </section>
            <TrafficRouteRanking routes={overview.top_routes} scopeLabel={scopeLabel} range={range} />
          </Surface>

          <Surface className="nf-traffic-page__summary" aria-label={`Сводка: ${scopeLabel}`}>
            <div className="nf-traffic-summary-cell is-scope"><IconServer /><span>Источник</span><strong>{scopeLabel}</strong><small>{selectedNode ? selectedNode.node.address : `${nodes.length} нод`}</small></div>
            <div className="nf-traffic-summary-cell"><IconArrowDown /><span>RX сейчас</span><strong>{formatBitrate(rx)}</strong><small>{currentRateNote}</small></div>
            <div className="nf-traffic-summary-cell"><IconArrowUp /><span>TX сейчас</span><strong>{formatBitrate(tx)}</strong><small>{currentRateNote}</small></div>
            <div className="nf-traffic-summary-cell"><IconRoute /><span>Трафик за месяц</span><strong>{formatBytes(monthUsed)}</strong><small>{monthLimit > 0 ? `лимит ${formatBytes(monthLimit)}` : selectedNode ? 'без лимита' : 'без общего лимита'}</small></div>
            <div className="nf-traffic-summary-cell"><IconPlugConnected /><span>Соединения</span><strong>{formatNumber(connections)}</strong><small>{formatNumber(routes)} маршрутов</small></div>
            <div className="nf-traffic-summary-cell"><IconActivityHeartbeat /><span>Backend health</span><strong>{healthPercent === null ? '—' : `${healthPercent}%`}</strong><small>{health.healthy} работают · {health.degraded + health.unavailable} требуют внимания</small></div>
          </Surface>

          <Surface className="nf-traffic-page__nodes" title="Трафик по нодам" description="Сравнение текущей скорости и месячного объёма">
            <div className="nf-traffic-page__node-toolbar">
              <div className="nf-traffic-page__status-tabs" role="group" aria-label="Фильтр состояния нод">
                {([
                  ['all', 'Все', nodes.length],
                  ['online', 'Работают', statusCounts.online],
                  ['degraded', 'Деградируют', statusCounts.degraded],
                  ['offline', 'Недоступны', statusCounts.offline],
                ] as const).map(([value, label, count]) => (
                  <button key={value} type="button" className={status === value ? 'is-active' : ''} onClick={() => setStatus(value)} aria-pressed={status === value}>{label}<span>{count}</span></button>
                ))}
              </div>
              <TextInput value={query} onChange={(event) => setQuery(event.currentTarget.value)} placeholder="Имя или IP ноды..." leftSection={<IconSearch size={16} />} aria-label="Поиск ноды в статистике" />
            </div>
            {filteredNodes.length ? (
              <TrafficNodesTable
                nodes={filteredNodes}
                selectedNodeId={selectedNodeId}
                totalRate={totalCurrentRate}
                totalMonthBytes={totals?.traffic_month_bytes ?? 0}
                onSelect={(nodeId) => updateQuery('node', nodeId)}
              />
            ) : (
              <StateView title="Ноды не найдены" description="Измените строку поиска или фильтр состояния." />
            )}
          </Surface>
        </>
      )}
    </main>
  );
}
