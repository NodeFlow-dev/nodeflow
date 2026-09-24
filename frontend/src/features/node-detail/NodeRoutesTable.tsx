import { ActionIcon, Button, Menu, Modal, Pagination, Switch, TextInput, Tooltip } from '@mantine/core';
import {
  IconAlertTriangle, IconChartLine, IconChevronDown, IconChevronUp, IconDotsVertical, IconEdit, IconFilter, IconGripVertical, IconLayoutList, IconPlus, IconSearch, IconTrash,
} from '@tabler/icons-react';
import { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { DragEvent } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import type {
  HAProxyProxyStats, HAProxyServerStats, HeartbeatMetrics, NodeTraffic, RouteRecord, TrafficRoute, TrafficHistory, TrafficHistorySample,
} from '../../lib/contracts';
import { formatBitrate, formatBytes, formatNumber } from '../../lib/format';
import { isAssignedHAProxyServer } from '../../lib/haproxy';
import { StateView } from '../../components/StateView';
import { SegmentTabs } from '../../components/SegmentTabs';
import { routeName } from '../nodes/model';
import { TrafficChart } from '../nodes/TrafficChart';
import type { TrafficRange } from '../nodes/useNodesOverview';
import { api } from '../../lib/api';
import { isInteractiveRowTarget } from '../../lib/interaction';

type RouteFilter = 'all' | 'enabled' | 'draft' | 'failed';

interface NodeRoutesTableProps {
  nodeID: string;
  routes: RouteRecord[];
  traffic: NodeTraffic | null;
  metrics?: HeartbeatMetrics;
  busyRouteID?: string;
  orderingRouteID?: string;
  error?: string;
  onToggle: (route: RouteRecord, enabled: boolean) => Promise<void>;
  onDelete: (route: RouteRecord) => Promise<void>;
  onReorder: (sourceID: string, targetID: string) => Promise<void>;
}

const deploymentCopy: Record<string, string> = {
  active: 'Активен', pending: 'Применяется', failed: 'Ошибка', draft: 'Черновик', disabled: 'Выключен', deleting: 'Удаляется',
};

function routeMatch(route: RouteRecord) {
  if (route.match_mode === 'sni' || (!route.fallback && route.snis.length)) return `SNI  ${route.snis.join(', ')}`;
  if (route.fallback) return 'Фолбэк';
  // legacy: any_tcp / destination_ip treated as fallback
  if (route.match_mode === 'any_tcp' || route.match_mode === 'destination_ip') return 'Фолбэк';
  return route.snis.length ? `SNI  ${route.snis.join(', ')}` : 'Фолбэк';
}

function routeTarget(route: RouteRecord) {
  if (route.target_type === 'unix') return route.unix_socket_path || 'Unix socket';
  const target = `${route.target_host || '—'}:${route.target_port || '—'}`;
  return route.dns_pool ? `DNS-пул · ${target}` : target;
}

function routeRuntime(route: RouteRecord, traffic: NodeTraffic | null, metrics?: HeartbeatMetrics, reports?: Map<string, TrafficRoute>) {
  const report = reports ? reports.get(route.id) : traffic?.routes.find((item) => item.route_id === route.id);
  const backend = report ? metrics?.haproxy_runtime?.backends?.[report.backend_key] : undefined;
  const servers = report ? metrics?.haproxy_runtime?.servers?.[report.backend_key] : undefined;
  return { report, backend, servers };
}

function backendHealth(backend?: HAProxyProxyStats, servers?: Record<string, HAProxyServerStats>) {
  const serverValues = Object.values(servers ?? {}).filter(isAssignedHAProxyServer);
  const healthy = serverValues.filter((server) => ['UP', 'OPEN', 'READY'].includes(String(server.status ?? '').trim().toUpperCase())).length;
  const backendStatus = String(backend?.status ?? '').trim();
  const normalizedStatus = backendStatus.toUpperCase();

  if (serverValues.length) {
    return {
      tone: healthy === serverValues.length ? 'online' : healthy === 0 ? 'offline' : 'degraded',
      label: `${healthy}/${serverValues.length}`,
      detail: backendStatus || 'Backend status: UNKNOWN',
      tooltip: `HAProxy backend: ${backendStatus || 'UNKNOWN'} · серверов ${healthy}/${serverValues.length}`,
    };
  }

  if (!backend) {
    return { tone: 'degraded', label: '—', detail: 'Backend ещё не наблюдался', tooltip: 'Backend ещё не наблюдался' };
  }

  const tone = ['UP', 'OPEN', 'READY'].includes(normalizedStatus)
    ? 'online'
    : ['DOWN', 'CLOSED', 'MAINT', 'STOPPED'].some((status) => normalizedStatus.startsWith(status))
      ? 'offline'
      : 'degraded';
  const label = backendStatus || 'UNKNOWN';
  return {
    tone,
    label,
    detail: 'Карта серверов не получена',
    tooltip: `HAProxy backend: ${label} · карта серверов не получена`,
  };
}

function routeMatchesFilter(route: RouteRecord, filter: RouteFilter) {
  if (filter === 'enabled') return route.enabled;
  if (filter === 'draft') return !route.enabled && route.deployment_state !== 'failed';
  if (filter === 'failed') return route.deployment_state === 'failed';
  return true;
}

const demoHistoryStep: Record<TrafficRange, number> = {
  '1m': 2_000, '5m': 10_000, '1h': 120_000, '24h': 2_400_000, '7d': 16_800_000, '30d': 72_000_000,
};

function createDemoRouteHistory(route: RouteRecord, range: TrafficRange, metrics?: HeartbeatMetrics): TrafficHistorySample[] {
  const key = route.id.split('').reduce((sum, char) => sum + char.charCodeAt(0), 0);
  const report = metrics?.haproxy_runtime?.backends;
  const observed = Object.values(report ?? {}).find((backend) => Number(backend.rx_bps) > 0 || Number(backend.tx_bps) > 0);
  const baseRX = Number(observed?.rx_bps) || 198_000_000;
  const baseTX = Number(observed?.tx_bps) || 124_000_000;
  const step = demoHistoryStep[range];
  const now = Date.now();
  return Array.from({ length: 60 }, (_, index) => {
    const drift = 0.7 + index / 170;
    const wave = 1 + Math.sin((index + key % 13) / 4.8) * 0.13 + Math.cos(index / 8.5) * 0.06;
    return {
      timestamp: new Date(now - (59 - index) * step).toISOString(),
      rx_bps: Math.max(0, Math.round(baseRX * drift * wave)),
      tx_bps: Math.max(0, Math.round(baseTX * drift * (1 + Math.sin((index + 5) / 6.4) * 0.1))),
    };
  });
}

// Routes per page. Pagination only kicks in above this size, so typical nodes
// render exactly as before; nodes with hundreds of routes stay responsive.
const ROUTES_PAGE_SIZE = 100;

function listenerKey(route: RouteRecord) { return `${route.listener_ip || '*'}:${route.listener_port}`; }
function isFallbackRoute(route: RouteRecord) { return route.fallback || route.match_mode === 'any_tcp' || route.match_mode === 'destination_ip'; }

interface ListenerGroup { key: string; routes: RouteRecord[]; hasFallback: boolean; hasSNI: boolean }

// F6: group visible routes by listener (ip:port), preserving first-seen order;
// within a group SNI routes come first and fallback routes last.
function groupByListener(visible: RouteRecord[]): ListenerGroup[] {
  const seenKeys: string[] = [];
  const groupMap = new Map<string, RouteRecord[]>();
  for (const route of visible) {
    const key = listenerKey(route);
    let group = groupMap.get(key);
    if (!group) { group = []; seenKeys.push(key); groupMap.set(key, group); }
    group.push(route);
  }
  return seenKeys.map((key) => {
    const group = groupMap.get(key)!;
    const sniRoutes = group.filter((route) => !isFallbackRoute(route));
    const fallbackRoutes = group.filter(isFallbackRoute);
    return { key, routes: [...sniRoutes, ...fallbackRoutes], hasFallback: fallbackRoutes.length > 0, hasSNI: sniRoutes.length > 0 };
  });
}

interface RouteRowProps {
  route: RouteRecord;
  report?: TrafficRoute;
  backend?: HAProxyProxyStats;
  servers?: Record<string, HAProxyServerStats>;
  previousID?: string;
  nextID?: string;
  pending: boolean;
  dragging: boolean;
  dropTarget: boolean;
  ordering: boolean;
  reorderEnabled: boolean;
  reorderDisabledReason: string;
  onEdit: (route: RouteRecord) => void;
  onToggle: (route: RouteRecord, enabled: boolean) => void;
  onStats: (route: RouteRecord) => void;
  onRequestDelete: (route: RouteRecord) => void;
  onMove: (sourceID: string, targetID: string) => void;
  onDragStart: (event: DragEvent<HTMLButtonElement>, routeID: string) => void;
  onDragEnd: () => void;
  onDragOver: (event: DragEvent<HTMLDivElement>, routeID: string) => void;
  onDrop: (event: DragEvent<HTMLDivElement>, routeID: string) => void;
}

const RouteRow = memo(function RouteRow({
  route, report, backend, servers, previousID, nextID, pending, dragging, dropTarget, ordering, reorderEnabled, reorderDisabledReason,
  onEdit, onToggle, onStats, onRequestDelete, onMove, onDragStart, onDragEnd, onDragOver, onDrop,
}: RouteRowProps) {
  const health = backendHealth(backend, servers);
  const limit = report?.limit_bytes ?? route.quota_bytes;
  const used = report?.limit_bytes ? (report.quota_used_bytes ?? report.used_bytes) : report?.used_bytes;
  const quota = limit && used != null ? Math.min(100, (used / limit) * 100) : null;
  const name = routeName(route);
  const match = routeMatch(route);
  const target = routeTarget(route);
  return (
    <div
      className={`nf-detail-route-row is-${route.deployment_state}${pending ? ' is-busy' : ''}${dragging ? ' is-dragging' : ''}${dropTarget ? ' is-drop-target' : ''}${ordering ? ' is-ordering' : ''}`}
      role="row"
      tabIndex={0}
      aria-label={`Редактировать маршрут ${name}`}
      onClick={(event) => { if (!isInteractiveRowTarget(event.target, event.currentTarget)) onEdit(route); }}
      onKeyDown={(event) => { if (event.key === 'Enter' && event.target === event.currentTarget) onEdit(route); }}
      onDragOver={(event) => onDragOver(event, route.id)}
      onDrop={(event) => onDrop(event, route.id)}
    >
      <div role="cell" className="nf-route-toggle" data-row-action>
        <Switch size="sm" checked={route.enabled} disabled={pending || route.delete_pending} onChange={(event) => onToggle(route, event.currentTarget.checked)} aria-label={`${route.enabled ? 'Выключить' : 'Включить'} маршрут ${name}`} />
      </div>
      <div role="cell" className="nf-route-name-cell">
        <div className="nf-sort-controls" data-row-action>
          <Tooltip label={reorderEnabled ? 'Перетащить маршрут' : reorderDisabledReason} openDelay={250}>
            <span className="nf-sort-grip-wrap">
              <ActionIcon
                className="nf-sort-grip"
                variant="subtle"
                color="gray"
                size="sm"
                draggable={reorderEnabled}
                disabled={!reorderEnabled}
                aria-label={`Перетащить маршрут ${name}`}
                onDragStart={(event) => onDragStart(event, route.id)}
                onDragEnd={onDragEnd}
              ><IconGripVertical size={15} /></ActionIcon>
            </span>
          </Tooltip>
          <div className="nf-sort-step-actions">
            <ActionIcon variant="subtle" color="gray" size="xs" disabled={!reorderEnabled || !previousID} aria-label={`Переместить ${name} выше`} onClick={() => { if (previousID) onMove(route.id, previousID); }}><IconChevronUp size={12} /></ActionIcon>
            <ActionIcon variant="subtle" color="gray" size="xs" disabled={!reorderEnabled || !nextID} aria-label={`Переместить ${name} ниже`} onClick={() => { if (nextID) onMove(route.id, nextID); }}><IconChevronDown size={12} /></ActionIcon>
          </div>
        </div>
        <button className="nf-route-name" onClick={() => onEdit(route)} title={name}>
          <i className={route.enabled ? 'is-online' : ''} />
          <span><strong>{name}</strong><small className={`is-${route.deployment_state}`}>{deploymentCopy[route.deployment_state] ?? route.deployment_state}</small></span>
        </button>
      </div>
      <span role="cell" className="nf-route-mono">{route.listener_ip || '*'}:{route.listener_port}</span>
      <Tooltip label={match} openDelay={350}><span role="cell" tabIndex={0} className="nf-route-ellipsis">{match}</span></Tooltip>
      <Tooltip label={target} openDelay={350}><span role="cell" tabIndex={0} className="nf-route-ellipsis">{target}</span></Tooltip>
      <span role="cell">{formatNumber(backend?.connections_current)}</span>
      <span role="cell">{formatNumber(backend?.tcp_sessions_current ?? backend?.sessions_current)}</span>
      <span role="cell" className="nf-route-rate"><b>↓ {formatBitrate(backend?.rx_bps)}</b><small>↑ {formatBitrate(backend?.tx_bps)}</small></span>
      <span role="cell" className="nf-route-quota">
        <b>{formatBytes(used)}</b>
        <small>{limit ? `/ ${formatBytes(limit)}` : 'без лимита'}</small>
        <i className={quota == null ? 'is-unlimited' : undefined}><em style={{ width: `${quota ?? 100}%` }} /></i>
      </span>
      <Tooltip label={health.tooltip} openDelay={300}>
        <span role="cell" className={`nf-route-health is-${health.tone}`}><i /> {health.label}</span>
      </Tooltip>
      <span role="cell" data-row-action>
        <Menu position="bottom-end" withinPortal>
          <Menu.Target><ActionIcon variant="subtle" color="gray" size="md" aria-label={`Действия маршрута ${name}`}><IconDotsVertical size={16} /></ActionIcon></Menu.Target>
          <Menu.Dropdown>
            <Menu.Item leftSection={<IconChartLine size={16} />} onClick={() => onStats(route)}>Статистика</Menu.Item>
            <Menu.Item leftSection={<IconEdit size={16} />} onClick={() => onEdit(route)}>Редактировать</Menu.Item>
            <Menu.Divider />
            <Menu.Item color="red" leftSection={<IconTrash size={16} />} onClick={() => onRequestDelete(route)}>Удалить</Menu.Item>
          </Menu.Dropdown>
        </Menu>
      </span>
    </div>
  );
});

export function NodeRoutesTable({ nodeID, routes, traffic, metrics, busyRouteID, orderingRouteID = '', error, onToggle, onDelete, onReorder }: NodeRoutesTableProps) {
  const navigate = useNavigate();
  const location = useLocation();
  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState<RouteFilter>('all');
  const [deleteRoute, setDeleteRoute] = useState<RouteRecord | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState('');
  const [relaxedRows, setRelaxedRows] = useState(false);
  const [statsRoute, setStatsRoute] = useState<RouteRecord | null>(null);
  const [statsRange, setStatsRange] = useState<TrafficRange>('24h');
  const [statsHistory, setStatsHistory] = useState<TrafficHistorySample[]>([]);
  const [statsLoading, setStatsLoading] = useState(false);
  const [statsError, setStatsError] = useState('');
  const [draggingID, setDraggingID] = useState('');
  const [dropTargetID, setDropTargetID] = useState('');
  const statsRequestID = useRef(0);
  const counts = useMemo(() => ({
    all: routes.length,
    enabled: routes.filter((route) => route.enabled).length,
    draft: routes.filter((route) => !route.enabled && route.deployment_state !== 'failed').length,
    failed: routes.filter((route) => route.deployment_state === 'failed').length,
  }), [routes]);
  const normalizedQuery = query.trim().toLowerCase();
  const visible = useMemo(() => routes.filter((route) => {
    if (!routeMatchesFilter(route, filter)) return false;
    if (!normalizedQuery) return true;
    const copy = `${routeName(route)} ${route.listener_ip}:${route.listener_port} ${routeMatch(route)} ${routeTarget(route)}`.toLowerCase();
    return copy.includes(normalizedQuery);
  }), [routes, filter, normalizedQuery]);
  const demo = new URLSearchParams(location.search).get('demo') === '1';
  const demoQuery = demo ? '?demo=1' : '';
  const reorderEnabled = filter === 'all' && !query.trim() && !busyRouteID && !orderingRouteID;
  const reorderDisabledReason = query.trim() || filter !== 'all'
    ? 'Сбросьте поиск и фильтры, чтобы менять порядок.'
    : 'Дождитесь завершения текущего изменения.';
  const listenerGroups = useMemo(() => groupByListener(visible), [visible]);
  // Display order (grouped) drives both pagination and up/down neighbours.
  const displayRoutes = useMemo(() => listenerGroups.flatMap((group) => group.routes), [listenerGroups]);
  const reports = useMemo(() => new Map((traffic?.routes ?? []).map((item) => [item.route_id, item])), [traffic?.routes]);
  const [page, setPage] = useState(1);
  const pages = Math.max(1, Math.ceil(displayRoutes.length / ROUTES_PAGE_SIZE));
  const currentPage = Math.min(page, pages);
  useEffect(() => { setPage(1); }, [filter, normalizedQuery]);
  const paged = pages > 1;
  const pageStart = paged ? (currentPage - 1) * ROUTES_PAGE_SIZE : 0;
  const pageEnd = paged ? pageStart + ROUTES_PAGE_SIZE : displayRoutes.length;
  const pageGroups = useMemo(() => {
    if (!paged) return listenerGroups.map((group) => ({ ...group, lastOnPage: true }));
    const result: Array<ListenerGroup & { lastOnPage: boolean }> = [];
    let offset = 0;
    for (const group of listenerGroups) {
      const start = Math.max(pageStart, offset);
      const end = Math.min(pageEnd, offset + group.routes.length);
      if (start < end) {
        result.push({ ...group, routes: group.routes.slice(start - offset, end - offset), lastOnPage: end === offset + group.routes.length });
      }
      offset += group.routes.length;
      if (offset >= pageEnd) break;
    }
    return result;
  }, [listenerGroups, paged, pageStart, pageEnd]);
  const neighbours = useMemo(() => {
    const map = new Map<string, { previousID?: string; nextID?: string }>();
    displayRoutes.forEach((route, index) => map.set(route.id, { previousID: displayRoutes[index - 1]?.id, nextID: displayRoutes[index + 1]?.id }));
    return map;
  }, [displayRoutes]);

  // Handlers read current values through a ref so their identity is stable and
  // memoized RouteRow instances skip re-rendering when their data is unchanged.
  const latest = useRef({ reorderEnabled, draggingID, onReorder, onToggle, nodeID, demoQuery, navigate });
  latest.current = { reorderEnabled, draggingID, onReorder, onToggle, nodeID, demoQuery, navigate };
  const edit = useCallback((route: RouteRecord) => {
    const { navigate: go, nodeID: id, demoQuery: suffix } = latest.current;
    go(`/nodes/${id}/routes/${route.id}${suffix}`);
  }, []);
  const toggle = useCallback((route: RouteRecord, enabled: boolean) => { void latest.current.onToggle(route, enabled); }, []);
  const move = useCallback((sourceID: string, targetID: string) => { void latest.current.onReorder(sourceID, targetID); }, []);
  const requestDelete = useCallback((route: RouteRecord) => { setDeleteError(''); setDeleteRoute(route); }, []);
  const startDrag = useCallback((event: DragEvent<HTMLButtonElement>, routeID: string) => {
    if (!latest.current.reorderEnabled) return;
    event.stopPropagation();
    event.dataTransfer.effectAllowed = 'move';
    event.dataTransfer.setData('text/plain', routeID);
    latest.current.draggingID = routeID;
    setDraggingID(routeID);
  }, []);
  const dragOver = useCallback((event: DragEvent<HTMLDivElement>, routeID: string) => {
    const { reorderEnabled: enabled, draggingID: dragging } = latest.current;
    if (!enabled || !dragging || routeID === dragging) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = 'move';
    setDropTargetID(routeID);
  }, []);
  const drop = useCallback((event: DragEvent<HTMLDivElement>, targetID: string) => {
    event.preventDefault();
    event.stopPropagation();
    const { reorderEnabled: enabled, draggingID: dragging, onReorder: reorder } = latest.current;
    const sourceID = dragging || event.dataTransfer.getData('text/plain');
    latest.current.draggingID = '';
    setDraggingID('');
    setDropTargetID('');
    if (enabled && sourceID && sourceID !== targetID) void reorder(sourceID, targetID);
  }, []);
  const finishDrag = useCallback(() => { latest.current.draggingID = ''; setDraggingID(''); setDropTargetID(''); }, []);
  useEffect(() => {
    if (!statsRoute) return undefined;
    const requestID = ++statsRequestID.current;
    setStatsHistory([]);
    setStatsError('');
    if (demo) {
      setStatsHistory(createDemoRouteHistory(statsRoute, statsRange, metrics));
      setStatsLoading(false);
      return undefined;
    }
    const controller = new AbortController();
    setStatsLoading(true);
    void api<TrafficHistory>(`/api/v1/nodes/${encodeURIComponent(nodeID)}/routes/${encodeURIComponent(statsRoute.id)}/traffic/history?range=${encodeURIComponent(statsRange)}`, { signal: controller.signal })
      .then((history) => {
        if (statsRequestID.current === requestID) setStatsHistory(history.samples ?? []);
      })
      .catch((reason) => {
        if (!controller.signal.aborted && statsRequestID.current === requestID) setStatsError(reason instanceof Error ? reason.message : 'История маршрута недоступна');
      })
      .finally(() => {
        if (!controller.signal.aborted && statsRequestID.current === requestID) setStatsLoading(false);
      });
    return () => controller.abort();
  }, [demo, metrics, nodeID, statsRange, statsRoute]);
  const openStats = useCallback((route: RouteRecord) => {
    statsRequestID.current += 1;
    setStatsHistory([]);
    setStatsError('');
    setStatsLoading(true);
    setStatsRange('24h');
    setStatsRoute(route);
  }, []);
  const changeStatsRange = (nextRange: TrafficRange) => {
    if (nextRange === statsRange) return;
    statsRequestID.current += 1;
    setStatsHistory([]);
    setStatsError('');
    setStatsLoading(true);
    setStatsRange(nextRange);
  };
  const closeStats = () => {
    statsRequestID.current += 1;
    setStatsRoute(null);
    setStatsHistory([]);
    setStatsError('');
    setStatsLoading(false);
  };
  const confirmDelete = async () => {
    if (!deleteRoute) return;
    setDeleting(true);
    setDeleteError('');
    try {
      await onDelete(deleteRoute);
      setDeleteRoute(null);
    } catch (reason) {
      setDeleteError(reason instanceof Error ? reason.message : 'Маршрут не удалён');
    } finally {
      setDeleting(false);
    }
  };

  return (
    <section className={`nf-surface nf-detail-routes${relaxedRows ? ' is-relaxed' : ''}`} aria-labelledby="node-routes-title">
      <header className="nf-detail-routes__toolbar">
        <div className="nf-detail-routes__title"><h2 id="node-routes-title">Маршруты</h2></div>
        <SegmentTabs
          className="nf-detail-route-tabs"
          value={filter}
          onChange={setFilter}
          label="Фильтр маршрутов"
          items={[
            { value: 'all', label: 'Все', count: counts.all },
            { value: 'enabled', label: 'Включены', count: counts.enabled },
            { value: 'draft', label: 'Черновики', count: counts.draft },
          ]}
        />
        <div className="nf-detail-routes__tools">
          <TextInput value={query} onChange={(event) => setQuery(event.currentTarget.value)} placeholder="Поиск маршрутов..." leftSection={<IconSearch size={16} />} aria-label="Поиск маршрутов" />
          <Menu position="bottom-end" withinPortal>
            <Menu.Target><Button variant="default" leftSection={<IconFilter size={16} />}>Фильтры</Button></Menu.Target>
            <Menu.Dropdown>
              <Menu.Label>Состояние</Menu.Label>
              {([['all', 'Все маршруты'], ['enabled', 'Только включённые'], ['draft', 'Только черновики'], ['failed', `С ошибкой (${counts.failed})`]] as const).map(([value, label]) => (
                <Menu.Item key={value} className={filter === value ? 'is-selected' : ''} onClick={() => setFilter(value)}>{label}</Menu.Item>
              ))}
            </Menu.Dropdown>
          </Menu>
          <ActionIcon variant="default" size="lg" onClick={() => setRelaxedRows((value) => !value)} aria-label="Переключить плотность строк" aria-pressed={relaxedRows}><IconLayoutList size={17} /></ActionIcon>
        </div>
      </header>

      {error && <div className="nf-inline-error" role="alert"><IconAlertTriangle size={16} /><span>{error}</span></div>}
      <div className="nf-route-table-scroll" tabIndex={0} aria-label="Маршруты ноды">
        <div className="nf-detail-route-table" role="table" aria-rowcount={visible.length + 1}>
          <div className="nf-detail-route-head" role="row">
            {['Статус', 'Маршрут', 'Слушает', 'Правило соответствия', 'Назначение', 'Соединения', 'TCP-сессии', 'RX / TX', 'Трафик', 'Бэкенды', 'Действия'].map((label) => <span role="columnheader" key={label}>{label}</span>)}
          </div>
          {pageGroups.map(({ key, routes: groupRoutes, hasFallback, hasSNI, lastOnPage }, groupIndex) => (
            <div className="nf-route-listener-group" key={key} role="rowgroup" aria-label={`Listener ${key}`}>
              {(listenerGroups.length > 1 || groupIndex === 0) && (
                <div className="nf-route-listener-group__header" role="presentation" aria-hidden>
                  <span className="nf-route-listener-group__key">{key}</span>
                </div>
              )}
              {groupRoutes.map((route) => {
                const { report, backend, servers } = routeRuntime(route, traffic, metrics, reports);
                const neighbour = neighbours.get(route.id);
                return (
                  <RouteRow
                    key={route.id}
                    route={route}
                    report={report}
                    backend={backend}
                    servers={servers}
                    previousID={neighbour?.previousID}
                    nextID={neighbour?.nextID}
                    pending={busyRouteID === route.id}
                    dragging={draggingID === route.id}
                    dropTarget={dropTargetID === route.id}
                    ordering={orderingRouteID === route.id}
                    reorderEnabled={reorderEnabled}
                    reorderDisabledReason={reorderDisabledReason}
                    onEdit={edit}
                    onToggle={toggle}
                    onStats={openStats}
                    onRequestDelete={requestDelete}
                    onMove={move}
                    onDragStart={startDrag}
                    onDragEnd={finishDrag}
                    onDragOver={dragOver}
                    onDrop={drop}
                  />
                );
              })}
              {/* F6: no-fallback hint */}
              {hasSNI && !hasFallback && lastOnPage && (
                <div className="nf-route-listener-group__no-fallback" role="presentation">
                  <span>несовпавшие соединения сбрасываются</span>
                </div>
              )}
            </div>
          ))}
        </div>
      </div>
      {paged && (
        <footer className="nf-table-footer">
          <span>Показано {pageStart + 1}–{Math.min(displayRoutes.length, pageEnd)} из {displayRoutes.length} маршрутов</span>
          <Pagination total={pages} value={currentPage} onChange={setPage} size="sm" withEdges siblings={1} boundaries={1} />
          <span>{ROUTES_PAGE_SIZE} / стр.</span>
        </footer>
      )}
      {!visible.length && (
        <StateView
          title={routes.length ? 'Ничего не найдено' : 'Маршрутов пока нет'}
          description={routes.length ? 'Измените поиск или фильтр состояния.' : 'Создайте listener → target. Новый маршрут сохранится выключенным черновиком.'}
          action={!routes.length ? <Button leftSection={<IconPlus size={17} />} onClick={() => navigate(`/nodes/${nodeID}/routes/new${demoQuery}`)}>Добавить маршрут</Button> : undefined}
        />
      )}

      <Modal opened={Boolean(deleteRoute)} onClose={() => !deleting && setDeleteRoute(null)} title="Удалить маршрут?" classNames={{ content: 'nf-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}>
        <div className="nf-confirm-dialog">
          <p>Маршрут <strong>{deleteRoute ? routeName(deleteRoute) : ''}</strong> будет сначала безопасно исключён из HAProxy, затем удалён после подтверждения Agent.</p>
          {deleteError && <div className="nf-inline-error" role="alert">{deleteError}</div>}
          <div><Button variant="default" onClick={() => setDeleteRoute(null)} disabled={deleting}>Отмена</Button><Button color="red" leftSection={<IconTrash size={16} />} onClick={confirmDelete} loading={deleting}>Удалить</Button></div>
        </div>
      </Modal>

      <Modal
        opened={Boolean(statsRoute)}
        onClose={closeStats}
        title={statsRoute ? `Статистика · ${routeName(statsRoute)}` : 'Статистика маршрута'}
        size="xl"
        classNames={{ content: 'nf-dialog nf-route-stats-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}
      >
        {statsRoute && (() => {
          const { report, backend, servers } = routeRuntime(statsRoute, traffic, metrics);
          const health = backendHealth(backend, servers);
          return (
            <div className="nf-route-stats">
              <div className="nf-route-stats__context"><span>{statsRoute.listener_ip || '*'}:{statsRoute.listener_port}</span><span>{routeMatch(statsRoute)}</span><span>→ {routeTarget(statsRoute)}</span></div>
              <div className="nf-route-stats__rail">
                <div><span>Соединения</span><strong>{formatNumber(backend?.connections_current)}</strong><small>{formatNumber(backend?.session_rate)} новых/с</small></div>
                <div><span>TCP-сессии</span><strong>{formatNumber(backend?.tcp_sessions_current ?? backend?.sessions_current)}</strong><small>{formatNumber(backend?.sessions_total)} всего</small></div>
                <div><span>Трафик за месяц</span><strong>{formatBytes(report?.used_bytes)}</strong><small>RX {formatBytes(report?.bytes_in)} · TX {formatBytes(report?.bytes_out)}</small></div>
                <div><span>Backend health</span><strong>{health.label === '—' ? 'Нет данных' : health.label}</strong><small>{health.detail}</small></div>
              </div>
              {statsError && <div className="nf-inline-error" role="alert">{statsError}</div>}
              <div className={`nf-route-stats__chart${statsLoading ? ' is-loading' : ''}`} aria-busy={statsLoading}>
                <TrafficChart
                  samples={statsHistory}
                  range={statsRange}
                  onRangeChange={changeStatsRange}
                  emptyTitle={statsLoading ? 'Загружаем историю…' : statsError ? 'История недоступна' : 'Нет точек за выбранный период'}
                  emptyDescription={statsLoading ? 'Новая выборка заменит предыдущий период.' : statsError ? 'Повторите запрос или выберите другой период.' : 'HAProxy не передал точки для этого маршрута и диапазона.'}
                  emptyFooterLabel={statsLoading ? 'Запрос выполняется' : statsError ? 'Запрос завершился ошибкой' : 'Точек нет'}
                  status={statsLoading
                    ? { tone: 'stale', label: 'Загрузка' }
                    : statsError
                      ? { tone: 'offline', label: 'Ошибка загрузки' }
                      : undefined}
                />
                {statsLoading && <div className="nf-route-stats__loading">Загружаем историю…</div>}
              </div>
            </div>
          );
        })()}
      </Modal>
    </section>
  );
}
