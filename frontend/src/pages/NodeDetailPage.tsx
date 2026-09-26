import { ActionIcon, Alert, Button, Group, Menu, Modal, Skeleton, Stack, Switch, Text, TextInput } from '@mantine/core';
import { useQueryClient } from '@tanstack/react-query';
import { IconDots, IconEdit, IconInfoCircle, IconPlus, IconPower, IconRefresh, IconServerCog, IconSettings, IconTrash } from '@tabler/icons-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useLocation, useNavigate, useParams } from 'react-router-dom';
import { LoginPanel } from '../components/LoginPanel';
import { PageHeader } from '../components/PageHeader';
import { RetryButton, StateView } from '../components/StateView';
import { Surface } from '../components/Surface';
import { HAProxyConfiguration } from '../features/node-detail/HAProxyConfiguration';
import { NodeKpiGrid } from '../features/node-detail/NodeKpiGrid';
import { NodeOperationalPanels } from '../features/node-detail/NodeOperationalPanels';
import { NodeRoutesTable } from '../features/node-detail/NodeRoutesTable';
import { nodeHAProxyLogs, nodeSettingsPayload } from '../features/node-detail/nodeSettings';
import { agentReleasesQueryKey } from '../lib/polling';
import { isNodeDetailDemoMode, useNodeDetail, type NodeDetailData } from '../features/node-detail/useNodeDetail';
import { AddNodeDialog } from '../features/nodes/AddNodeDialog';
import { effectiveState, filterSamples } from '../features/nodes/model';
import { TrafficChart } from '../features/nodes/TrafficChart';
import type { TrafficRange } from '../features/nodes/useNodesOverview';
import { api, isUnauthorized } from '../lib/api';
import type { AgentRelease, FirewallMode, HAProxyControlState, NodeAgentUpdateState, NodeFirewallPolicy, NodeRecord, RouteRecord } from '../lib/contracts';
import { timeAgo } from '../lib/format';

const stateCopy = { online: 'Работает', degraded: 'Требует внимания', offline: 'Недоступна', stopped: 'HAProxy выключен' } as const;

/**
 * Enable/disable goes through PATCH, which only flips `enabled` and keeps the
 * stored route spec (servers, balance mode, PROXY trust list, shaper...).
 * Never rebuild a full PUT body from a list row: absent fields reset to defaults.
 */
function routeTogglePatch(route: RouteRecord, enabled: boolean) {
  return { enabled, expected_version: route.version };
}

function NodeDetailSkeleton() {
  return (
    <main className="nf-page nf-node-detail-page">
      <div className="nf-detail-skeleton-head"><Skeleton height={16} width={180} /><Skeleton height={34} width={340} /></div>
      <div className="nf-detail-skeleton-top"><Skeleton height={340} /><Skeleton height={340} /></div>
      <Skeleton height={295} mt={12} />
      <div className="nf-detail-skeleton-bottom"><Skeleton height={206} /><Skeleton height={206} /><Skeleton height={206} /></div>
    </main>
  );
}

export function NodeDetailPage() {
  const { nodeId = '' } = useParams();
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const demo = isNodeDetailDemoMode();
  const demoQuery = new URLSearchParams(location.search).get('demo') === '1' ? '?demo=1' : '';
  const [range, setRange] = useState<TrafficRange>('24h');
  const [busyRouteID, setBusyRouteID] = useState('');
  const [busyAction, setBusyAction] = useState('');
  const [actionError, setActionError] = useState('');
  const [routeOverrides, setRouteOverrides] = useState<Record<string, RouteRecord | null>>({});
  const [firewallOverride, setFirewallOverride] = useState<NodeFirewallPolicy | null>(null);
  const [updateOverride, setUpdateOverride] = useState<NodeAgentUpdateState | null>(null);
  const [nodeOverride, setNodeOverride] = useState<NodeRecord | null>(null);
  const [nodeSettingsOpened, setNodeSettingsOpened] = useState(false);
  const [configOpened, setConfigOpened] = useState(false);
  const [configDirty, setConfigDirty] = useState(false);
  const [configBusy, setConfigBusy] = useState(false);
  const onConfigEditorState = useCallback((dirty: boolean, busy: boolean) => { setConfigDirty(dirty); setConfigBusy(busy); }, []);
  const [reinstallOpened, setReinstallOpened] = useState(false);
  const [nodeName, setNodeName] = useState('');
  const [nodeAddress, setNodeAddress] = useState('');
  const [nodeHAProxyLogsOn, setNodeHAProxyLogsOn] = useState(true);
  const [nodeEditBusy, setNodeEditBusy] = useState(false);
  const [nodeEditError, setNodeEditError] = useState('');
  const [nodeDeleteOpened, setNodeDeleteOpened] = useState(false);
  const [nodeDeleteConfirm, setNodeDeleteConfirm] = useState('');
  const [nodeDeleteBusy, setNodeDeleteBusy] = useState(false);
  const [nodeDeleteError, setNodeDeleteError] = useState('');
  const [routeOrder, setRouteOrder] = useState<string[]>([]);
  const [orderingRouteID, setOrderingRouteID] = useState('');
  const [haproxyOverride, setHAProxyOverride] = useState<HAProxyControlState | null>(null);
  const [haproxyConfirmOpened, setHAProxyConfirmOpened] = useState(false);
  const [haproxyRestartConfirmOpened, setHAProxyRestartConfirmOpened] = useState(false);
  const [haproxyBusy, setHAProxyBusy] = useState(false);
  const result = useNodeDetail(nodeId, range);
  const retainedDetail = useRef<{ nodeID: string; data: NodeDetailData } | null>(null);
  if (result.data) retainedDetail.current = { nodeID: nodeId, data: result.data };
  const detail = result.data ?? (retainedDetail.current?.nodeID === nodeId ? retainedDetail.current.data : undefined);
  const bundle = detail?.bundle;
  const sourceRoutes = useMemo(() => (bundle?.routes ?? []).map((route) => routeOverrides[route.id] === undefined ? route : routeOverrides[route.id]).filter((route): route is RouteRecord => route !== null), [bundle?.routes, routeOverrides]);
  const routes = useMemo(() => {
    if (!routeOrder.length) return sourceRoutes;
    const positions = new Map(routeOrder.map((id, index) => [id, index]));
    return [...sourceRoutes].sort((a, b) => (positions.get(a.id) ?? Number.MAX_SAFE_INTEGER) - (positions.get(b.id) ?? Number.MAX_SAFE_INTEGER));
  }, [routeOrder, sourceRoutes]);
  const samples = useMemo(() => filterSamples(bundle?.history?.samples ?? [], range), [bundle?.history?.samples, range]);
  const refresh = async () => {
    setRouteOverrides({}); setFirewallOverride(null); setUpdateOverride(null); setNodeOverride(null); setHAProxyOverride(null); setActionError('');
    await queryClient.invalidateQueries({ queryKey: agentReleasesQueryKey, refetchType: 'none' });
    await result.refetch();
  };
  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['node-detail', nodeId] });

  useEffect(() => {
    const incoming = sourceRoutes.map((route) => route.id);
    setRouteOrder((current) => {
      if (current.length === incoming.length && current.every((id) => incoming.includes(id))) return current;
      return incoming;
    });
  }, [sourceRoutes]);

  if (result.isPending && !detail) return <NodeDetailSkeleton />;
  if (result.isError && isUnauthorized(result.error)) return <LoginPanel onSuccess={() => result.refetch()} />;
  if (!detail || !bundle) {
    return <main className="nf-page"><StateView title="Нода не загрузилась" description={result.error instanceof Error ? result.error.message : 'Нода не найдена'} tone="error" action={<><Button variant="default" onClick={() => navigate(`/nodes${demoQuery}`)}>К списку нод</Button><RetryButton onClick={() => result.refetch()} /></>} /></main>;
  }

  const metrics = bundle.operational?.latest_heartbeat?.metrics;
  const heartbeat = bundle.operational?.latest_heartbeat;
  const status = effectiveState({ ...bundle, routes });
  const heartbeatAt = heartbeat?.received_at ? Date.parse(heartbeat.received_at) : Number.NaN;
  const heartbeatFresh = Number.isFinite(heartbeatAt) && Date.now() - heartbeatAt <= 45_000;
  const mtlsConnected = Boolean(heartbeat && heartbeatFresh && status !== 'offline');
  const rateSampledAt = bundle.operational?.rate_sampled_at;
  const rateAt = rateSampledAt ? Date.parse(rateSampledAt) : Number.NaN;
  const rateFresh = status !== 'offline' && Number.isFinite(rateAt) && Date.now() - rateAt <= 45_000;
  const currentRx = rateFresh && Number.isFinite(bundle.operational?.rx_bits_per_second) ? Number(bundle.operational?.rx_bits_per_second) : null;
  const currentTx = rateFresh && Number.isFinite(bundle.operational?.tx_bits_per_second) ? Number(bundle.operational?.tx_bits_per_second) : null;
  const latestSample = [...samples].reverse().find((sample) => sample.rx_bps !== null || sample.tx_bps !== null);
  const latestSampleAt = latestSample?.timestamp ? Date.parse(latestSample.timestamp) : Number.NaN;
  const freshnessWindow = Math.max(45_000, (bundle.history?.bucket_seconds ?? 15) * 2_500);
  const historyStale = Boolean(latestSample && (!Number.isFinite(latestSampleAt) || Date.now() - latestSampleAt > freshnessWindow));
  const chartStatus = result.isError || historyStale
    ? { tone: 'stale' as const, label: 'Данные устарели' }
    : !latestSample
      ? { tone: 'offline' as const, label: 'Нет свежих данных' }
      : status === 'offline'
        ? { tone: 'offline' as const, label: 'Нода недоступна' }
        : status === 'degraded' || !rateFresh
          ? { tone: 'degraded' as const, label: rateFresh ? 'Нода деградирует' : 'Нет текущей точки' }
          : { tone: 'online' as const, label: 'Онлайн' };
  const firewall = firewallOverride ?? detail.firewall;
  const update = updateOverride ?? detail.update;
  const node = nodeOverride ?? bundle.node;
  const agentPort = typeof node.metadata?.agent_port === 'number' ? node.metadata.agent_port : 4200;
  const haproxyControl = haproxyOverride ?? bundle.operational?.haproxy_control;
  const haproxyEnabled = haproxyControl?.desired_enabled !== false;
  const displayStatus = !haproxyEnabled && status !== 'offline' ? 'stopped' : status;

  const openNodeSettings = () => {
    setNodeName(node.name);
    setNodeAddress(node.address);
    setNodeHAProxyLogsOn(nodeHAProxyLogs(node));
    setNodeEditError('');
    setNodeSettingsOpened(true);
  };

  const openReinstall = () => {
    setNodeSettingsOpened(false);
    setReinstallOpened(true);
  };

  const openNodeDelete = () => {
    setNodeDeleteConfirm('');
    setNodeDeleteError('');
    setNodeDeleteOpened(true);
  };

  const deleteNode = async () => {
    if (nodeDeleteConfirm !== node.name) return;
    setNodeDeleteBusy(true);
    setNodeDeleteError('');
    try {
      if (!demo) {
        await api(`/api/v1/nodes/${nodeId}`, { method: 'DELETE' });
        await queryClient.invalidateQueries({ queryKey: ['overview'] });
      }
      setNodeDeleteOpened(false);
      navigate(`/nodes${demoQuery}`, { replace: true });
    } catch (error) {
      setNodeDeleteError(error instanceof Error ? error.message : 'Нода не удалена');
    } finally {
      setNodeDeleteBusy(false);
    }
  };

  const saveNode = async () => {
    const name = nodeName.trim();
    const address = nodeAddress.trim();
    if (!name || !address) {
      setNodeEditError('Укажите название и IP-адрес ноды.');
      return;
    }
    setNodeEditBusy(true);
    setNodeEditError('');
    try {
      const updated = demo
        ? { ...node, ...nodeSettingsPayload(node, name, address, nodeHAProxyLogsOn), updated_at: new Date().toISOString() }
        : await api<NodeRecord>(`/api/v1/nodes/${nodeId}`, {
          method: 'PUT',
          body: JSON.stringify(nodeSettingsPayload(node, name, address, nodeHAProxyLogsOn)),
        });
      setNodeOverride(updated);
      if (!demo) await invalidate();
      setNodeSettingsOpened(false);
    } catch (error) {
      setNodeEditError(error instanceof Error ? error.message : 'Параметры ноды не сохранены');
    } finally {
      setNodeEditBusy(false);
    }
  };

  const toggleRoute = async (route: RouteRecord, enabled: boolean) => {
    setBusyRouteID(route.id); setActionError('');
    try {
      if (demo) {
        const toggled: RouteRecord = { ...route, enabled, deployed: enabled, deployment_state: enabled ? 'active' : 'disabled', version: route.version + 1, updated_at: new Date().toISOString() };
        const { upsertDemoRoute } = await import('../fixtures/demo');
        upsertDemoRoute(nodeId, toggled);
        setRouteOverrides((current) => ({ ...current, [route.id]: toggled }));
      } else {
        const updated = await api<RouteRecord>(`/api/v1/nodes/${nodeId}/routes/${route.id}`, { method: 'PATCH', body: JSON.stringify(routeTogglePatch(route, enabled)) });
        setRouteOverrides((current) => ({ ...current, [route.id]: updated }));
        await invalidate();
        setRouteOverrides((current) => { const { [route.id]: _omit, ...rest } = current; return rest; });
      }
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Маршрут не изменён');
    } finally {
      setBusyRouteID('');
    }
  };

  const deleteRoute = async (route: RouteRecord) => {
    setBusyRouteID(route.id); setActionError('');
    try {
      if (demo) setRouteOverrides((current) => ({ ...current, [route.id]: null }));
      else {
        await api(`/api/v1/nodes/${nodeId}/routes/${route.id}?expected_version=${route.version}`, { method: 'DELETE' });
        await invalidate();
      }
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Маршрут не удалён');
      throw error;
    } finally {
      setBusyRouteID('');
    }
  };

  const reorderRoutes = async (sourceID: string, targetID: string) => {
    if (sourceID === targetID || orderingRouteID) return;
    const previous = routes.map((route) => route.id);
    const next = [...previous];
    const sourceIndex = next.indexOf(sourceID);
    const targetIndex = next.indexOf(targetID);
    if (sourceIndex < 0 || targetIndex < 0) return;
    next.splice(targetIndex, 0, next.splice(sourceIndex, 1)[0]);
    setRouteOrder(next);
    setOrderingRouteID(sourceID);
    setActionError('');
    try {
      if (!demo) {
        await api(`/api/v1/nodes/${nodeId}/routes/order`, { method: 'PUT', body: JSON.stringify({ route_ids: next }) });
        await invalidate();
      }
    } catch (error) {
      setRouteOrder(previous);
      setActionError(error instanceof Error ? error.message : 'Порядок маршрутов не сохранён');
      if (!demo) void invalidate();
    } finally {
      setOrderingRouteID('');
    }
  };

  const setHAProxyEnabled = async (enabled: boolean) => {
    if (!haproxyControl || haproxyBusy) return;
    setHAProxyBusy(true);
    setActionError('');
    try {
      const updated = demo
        ? {
          ...haproxyControl,
          supported: true,
          desired_enabled: enabled,
          generation: haproxyControl.generation + 1,
          actual_enabled: enabled,
          active_state: enabled ? 'active' : 'inactive',
          report_generation: haproxyControl.generation + 1,
          updated_at: new Date().toISOString(),
          reported_at: new Date().toISOString(),
        }
        : await api<HAProxyControlState>(`/api/v1/nodes/${nodeId}/haproxy`, {
          method: 'PUT',
          body: JSON.stringify({ enabled, expected_generation: haproxyControl.generation }),
        });
      setHAProxyOverride(updated);
      setHAProxyConfirmOpened(false);
      if (!demo) {
        await invalidate();
        setHAProxyOverride(null);
      }
    } catch (error) {
      setActionError(error instanceof Error ? error.message : `HAProxy не ${enabled ? 'включён' : 'выключен'}`);
    } finally {
      setHAProxyBusy(false);
    }
  };

  const restartHAProxy = async () => {
    if (!haproxyControl || haproxyBusy || !haproxyEnabled) return;
    setHAProxyBusy(true);
    setActionError('');
    try {
      const updated = demo
        ? {
          ...haproxyControl,
          supported: true,
          desired_enabled: true,
          restart_generation: haproxyControl.restart_generation + 1,
          actual_enabled: true,
          active_state: 'reloading',
          report_generation: haproxyControl.generation,
          updated_at: new Date().toISOString(),
        }
        : await api<HAProxyControlState>(`/api/v1/nodes/${nodeId}/haproxy/restart`, {
          method: 'POST',
          body: JSON.stringify({ expected_generation: haproxyControl.restart_generation }),
        });
      setHAProxyOverride(updated);
      setHAProxyRestartConfirmOpened(false);
      if (!demo) {
        await invalidate();
        setHAProxyOverride(null);
      }
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'HAProxy не перезагружен');
    } finally {
      setHAProxyBusy(false);
    }
  };

  const changeFirewall = async (mode: FirewallMode) => {
    if (!firewall) return;
    setBusyAction('firewall'); setActionError('');
    try {
      if (demo) setFirewallOverride({ ...firewall, mode, updated_at: new Date().toISOString() });
      else {
        const value = await api<NodeFirewallPolicy>(`/api/v1/nodes/${nodeId}/firewall`, { method: 'PUT', body: JSON.stringify({ mode }) });
        setFirewallOverride(value);
      }
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Режим UFW не изменён');
    } finally {
      setBusyAction('');
    }
  };

  const assignRelease = async (release: AgentRelease) => {
    if (!update) {
      setActionError('Состояние Node Agent недоступно. Обновите данные и повторите.');
      return;
    }
    setBusyAction('update'); setActionError('');
    try {
      if (demo && update) setUpdateOverride({ ...update, desired_release: release, state: 'assigned', updated_at: new Date().toISOString() });
      else {
        const value = await api<NodeAgentUpdateState>(`/api/v1/nodes/${nodeId}/agent-update`, {
          method: 'PUT',
          body: JSON.stringify({
            release_id: release.id,
            expected_actual_sequence: update.actual_sequence,
            expected_desired_sequence: update.desired_release?.sequence ?? 0,
          }),
        });
        setUpdateOverride(value);
      }
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Релиз не назначен');
    } finally {
      setBusyAction('');
    }
  };

  const rollbackRelease = async (release: AgentRelease, actualSequence: number, desiredSequence: number) => {
    setBusyAction('rollback'); setActionError('');
    try {
      if (demo && update) setUpdateOverride({ ...update, desired_release: release, state: 'assigned', updated_at: new Date().toISOString() });
      else {
        const value = await api<NodeAgentUpdateState>(`/api/v1/nodes/${nodeId}/agent-update/rollback`, {
          method: 'POST',
          body: JSON.stringify({
            target_release_id: release.id,
            expected_actual_sequence: actualSequence,
            expected_desired_sequence: desiredSequence,
          }),
        });
        setUpdateOverride(value);
      }
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Откат не назначен');
      throw error;
    } finally {
      setBusyAction('');
    }
  };

  return (
    <main className="nf-page nf-node-detail-page">
      <PageHeader
        className="nf-detail-header"
        breadcrumb={<><Link to={`/nodes${demoQuery}`}>Ноды</Link><span>/</span><span aria-current="page">{node.name}</span></>}
        title={node.name}
        meta={<><code>{node.address}</code><span className={`nf-detail-status is-${displayStatus}`}><i />{stateCopy[displayStatus]}</span><span className="nf-detail-heartbeat">Последний сигнал: {timeAgo(node.last_seen_at)}</span></>}
        actions={<>
          <Button component={Link} to={`/nodes/${nodeId}/routes/new${demoQuery}`} className="nf-primary-action" leftSection={<IconPlus size={18} />}>Добавить маршрут</Button>
          <Button variant="default" onClick={() => { setConfigDirty(false); setConfigBusy(false); setConfigOpened(true); }}>Конфигурация HAProxy</Button>
          <ActionIcon variant="default" size="lg" onClick={openNodeSettings} aria-label="Настройки ноды"><IconSettings size={18} /></ActionIcon>
          <Menu position="bottom-end" withinPortal>
            <Menu.Target><ActionIcon variant="default" size="lg" aria-label="Действия ноды"><IconDots size={19} /></ActionIcon></Menu.Target>
            <Menu.Dropdown>
              <Menu.Item leftSection={<IconRefresh size={16} />} onClick={refresh}>Обновить данные</Menu.Item>
              <Menu.Item leftSection={<IconEdit size={16} />} onClick={openNodeSettings}>Редактировать ноду</Menu.Item>
              <Menu.Item leftSection={<IconServerCog size={16} />} onClick={openReinstall}>Переустановить Node Agent</Menu.Item>
              <Menu.Divider />
              <Menu.Item
                color="red"
                leftSection={<IconRefresh size={16} />}
                disabled={!haproxyControl?.supported || !haproxyEnabled || haproxyBusy}
                onClick={() => setHAProxyRestartConfirmOpened(true)}
              >Жёстко перезагрузить HAProxy</Menu.Item>
              <Menu.Item
                color={haproxyEnabled ? 'red' : 'nodeflow'}
                leftSection={<IconPower size={16} />}
                disabled={!haproxyControl?.supported || haproxyBusy}
                onClick={() => { if (haproxyEnabled) setHAProxyConfirmOpened(true); else void setHAProxyEnabled(true); }}
              >{haproxyEnabled ? 'Выключить HAProxy' : 'Включить HAProxy'}</Menu.Item>
              <Menu.Item color="red" leftSection={<IconTrash size={16} />} onClick={openNodeDelete}>Удалить ноду</Menu.Item>
            </Menu.Dropdown>
          </Menu>
        </>}
      />

      {status !== 'online' && haproxyEnabled && <div className={`nf-detail-state-banner is-${status}`} role="status"><strong>{status === 'offline' ? 'Нода не отвечает.' : 'Нода работает с ограничениями.'}</strong><span>{status === 'offline' ? 'Показаны последние сохранённые значения; управляющие действия могут ждать восстановления связи.' : 'Проверьте HAProxy runtime, backend health и состояние применения маршрутов.'}</span></div>}
      {!haproxyEnabled && <div className="nf-detail-state-banner is-haproxy-off" role="status"><strong>HAProxy выключен.</strong><span>Node Agent остаётся на связи, но listener-порты и все маршруты остановлены.</span></div>}
      {haproxyControl && !haproxyControl.supported && <div className="nf-detail-state-banner" role="status"><strong>Управление HAProxy недоступно.</strong><span>Обновите Node Agent до версии с поддержкой включения и выключения сервиса.</span></div>}
      {actionError && <div className="nf-inline-error nf-detail-action-error" role="alert">{actionError}<button type="button" onClick={() => setActionError('')}>Закрыть</button></div>}
      {detail.partialErrors.operational && <div className="nf-inline-error nf-detail-action-error" role="alert">Системная статистика временно недоступна: {detail.partialErrors.operational}</div>}

      <div className="nf-detail-top-grid">
        <Surface
          className="nf-detail-chart"
          title="Трафик ноды"
          description={detail.partialErrors.history
            ? `История недоступна: ${detail.partialErrors.history}`
            : result.isError
              ? `Новый диапазон не загружен: ${result.error instanceof Error ? result.error.message : 'данные недоступны'}`
              : !result.data && result.isFetching
                ? 'Обновляем диапазон без перезагрузки страницы…'
                : undefined}
        >
          <TrafficChart samples={samples} range={range} onRangeChange={setRange} currentRx={currentRx} currentTx={currentTx} status={chartStatus} />
        </Surface>
        <NodeKpiGrid metrics={metrics} history={samples} summary={bundle.operational?.metrics_summary} currentRx={currentRx} currentTx={currentTx} rateSampledAt={rateSampledAt} live={status !== 'offline'} mtlsConnected={mtlsConnected} />
      </div>

      <NodeRoutesTable nodeID={nodeId} routes={routes} traffic={bundle.traffic} metrics={metrics} busyRouteID={busyRouteID} orderingRouteID={orderingRouteID} error={detail.partialErrors.routes || detail.partialErrors.traffic} onToggle={toggleRoute} onDelete={deleteRoute} onReorder={reorderRoutes} />

      <NodeOperationalPanels
        metrics={metrics}
        agentVersion={heartbeat?.agent_version}
        agentPort={agentPort}
        mtlsReady={mtlsConnected}
        credentialLastUsed={bundle.operational?.credential_last_used_at}
        routes={routes}
        firewall={firewall}
        update={update}
        releases={detail.releases}
        audit={detail.audit}
        partialErrors={detail.partialErrors}
        busyAction={busyAction}
        onFirewallMode={changeFirewall}
        onAssignRelease={assignRelease}
        onRollback={rollbackRelease}
        onOpenNodeSettings={openNodeSettings}
      />

      <Modal
        opened={haproxyConfirmOpened}
        onClose={() => !haproxyBusy && setHAProxyConfirmOpened(false)}
        title="Выключить HAProxy?"
        size="sm"
        closeOnClickOutside={!haproxyBusy}
        closeOnEscape={!haproxyBusy}
        classNames={{ content: 'nf-dialog nf-haproxy-control-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}
      >
        <Stack gap="md">
          <div className="nf-haproxy-control-warning"><IconPower size={20} /><div><strong>Все маршруты этой ноды остановятся.</strong><span>Node Agent останется на связи. HAProxy можно будет снова включить из этого же меню.</span></div></div>
          {haproxyControl?.last_error && <div className="nf-inline-error" role="alert">Последняя ошибка: {haproxyControl.last_error}</div>}
          <Group justify="flex-end">
            <Button variant="default" disabled={haproxyBusy} onClick={() => setHAProxyConfirmOpened(false)}>Отмена</Button>
            <Button color="red" leftSection={<IconPower size={16} />} loading={haproxyBusy} onClick={() => { void setHAProxyEnabled(false); }}>Выключить HAProxy</Button>
          </Group>
        </Stack>
      </Modal>
      <Modal
        opened={haproxyRestartConfirmOpened}
        onClose={() => !haproxyBusy && setHAProxyRestartConfirmOpened(false)}
        title="Жёстко перезагрузить HAProxy?"
        size="sm"
        closeOnClickOutside={!haproxyBusy}
        closeOnEscape={!haproxyBusy}
        classNames={{ content: 'nf-dialog nf-haproxy-control-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}
      >
        <Stack gap="md">
          <div className="nf-haproxy-control-warning"><IconPower size={20} /><div><strong>Все активные соединения будут немедленно оборваны.</strong><span>Это именно systemd restart, а не безопасный reload. Клиентам потребуется переподключиться.</span></div></div>
          {haproxyControl?.last_error && <div className="nf-inline-error" role="alert">Последняя ошибка: {haproxyControl.last_error}</div>}
          <Group justify="flex-end">
            <Button variant="default" disabled={haproxyBusy} onClick={() => setHAProxyRestartConfirmOpened(false)}>Отмена</Button>
            <Button color="red" leftSection={<IconRefresh size={16} />} loading={haproxyBusy} onClick={() => { void restartHAProxy(); }}>Жёстко перезагрузить</Button>
          </Group>
        </Stack>
      </Modal>
      <Modal
        opened={nodeSettingsOpened}
        onClose={() => !nodeEditBusy && setNodeSettingsOpened(false)}
        title="Настройки ноды"
        size="lg"
        closeOnClickOutside={!nodeEditBusy}
        closeOnEscape={!nodeEditBusy}
        classNames={{ content: 'nf-dialog nf-node-settings-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}
      >
        <Stack gap="lg">
          <Stack gap="sm">
            <TextInput label="Название ноды" value={nodeName} onChange={(event) => setNodeName(event.currentTarget.value)} autoFocus maxLength={200} required />
            <TextInput label="IP-адрес ноды" value={nodeAddress} onChange={(event) => setNodeAddress(event.currentTarget.value)} required />
            <Switch
              mt={4}
              checked={nodeHAProxyLogsOn}
              onChange={(event) => setNodeHAProxyLogsOn(event.currentTarget.checked)}
              disabled={nodeEditBusy}
              label="Логи соединений HAProxy"
              description="Каждое соединение пишется в syslog. На маленьких дисках выключите."
            />
          </Stack>
          {nodeEditError && <div className="nf-inline-error" role="alert">{nodeEditError}</div>}
          <Alert color="gray" icon={<IconInfoCircle size={18} />} title="Переустановка Node Agent">
            <Stack gap={8}>
              <Text size="sm">NodeFlow повторно проверит отпечаток SSH-ключа хоста, установит Agent и дождётся нового mTLS-сигнала. ID ноды и маршруты не меняются; при ошибке прежние учётные данные будут автоматически восстановлены.</Text>
              <Button variant="default" size="xs" leftSection={<IconServerCog size={15} />} onClick={openReinstall}>Переустановить Agent</Button>
            </Stack>
          </Alert>
          <Group justify="flex-end">
            <Button variant="default" disabled={nodeEditBusy} onClick={() => setNodeSettingsOpened(false)}>Отмена</Button>
            <Button leftSection={<IconEdit size={16} />} loading={nodeEditBusy} onClick={saveNode}>Сохранить</Button>
          </Group>
        </Stack>
      </Modal>
      <Modal opened={configOpened} onClose={() => { if (!configBusy && (!configDirty || window.confirm('Закрыть редактор? Несохранённые изменения будут потеряны.'))) setConfigOpened(false); }} title="Конфигурация HAProxy" size="xl" closeOnClickOutside={false}>
        {configOpened && <HAProxyConfiguration node={node} demo={demo} onEditorState={onConfigEditorState} onSaved={(updated) => { setNodeOverride(updated); void invalidate(); }} />}
      </Modal>
      <AddNodeDialog
        opened={reinstallOpened}
        onClose={() => setReinstallOpened(false)}
        onInstalled={() => { void refresh(); }}
        demo={demo}
        reinstallTarget={{
          id: node.id,
          name: node.name,
          address: node.address,
          sshPort: typeof node.metadata?.ssh_port === 'number' ? node.metadata.ssh_port : 22,
          agentPort,
          allowFirewallApply: firewall?.mode === 'apply',
          os: typeof metrics?.os === 'string' ? metrics.os : undefined,
          arch: typeof metrics?.arch === 'string' ? metrics.arch : undefined,
        }}
      />
      <Modal
        opened={nodeDeleteOpened}
        onClose={() => !nodeDeleteBusy && setNodeDeleteOpened(false)}
        title="Удалить ноду?"
        closeOnClickOutside={!nodeDeleteBusy}
        closeOnEscape={!nodeDeleteBusy}
        classNames={{ content: 'nf-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}
      >
        <Stack gap="md">
          <Text size="sm" c="dimmed">
            Запись <Text component="span" inherit fw={600} c="var(--nf-text)">{node.name}</Text> и связанные данные будут удалены из Panel. Node Agent и HAProxy на сервере останутся установленными.
          </Text>
          <TextInput
            label={<>Для подтверждения введите <strong>{node.name}</strong></>}
            value={nodeDeleteConfirm}
            onChange={(event) => setNodeDeleteConfirm(event.currentTarget.value)}
            disabled={nodeDeleteBusy}
            autoComplete="off"
            autoFocus
          />
          {nodeDeleteError && <div className="nf-inline-error" role="alert">{nodeDeleteError}</div>}
          <Group justify="flex-end">
            <Button variant="default" disabled={nodeDeleteBusy} onClick={() => setNodeDeleteOpened(false)}>Отмена</Button>
            <Button color="red" leftSection={<IconTrash size={16} />} loading={nodeDeleteBusy} disabled={nodeDeleteConfirm !== node.name} onClick={deleteNode}>Удалить ноду</Button>
          </Group>
        </Stack>
      </Modal>
    </main>
  );
}
