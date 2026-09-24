import { ActionIcon, Menu, Pagination, Tooltip } from '@mantine/core';
import { IconChevronDown, IconChevronUp, IconDotsVertical, IconEye, IconGripVertical, IconRoute, IconSettings } from '@tabler/icons-react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { memo, useCallback, useRef, useState } from 'react';
import type { CSSProperties, DragEvent, KeyboardEvent, MouseEvent } from 'react';
import type { DashboardNode } from '../../lib/contracts';
import { formatBitrate, formatBytes, formatNumber, shortHAProxy, timeAgo } from '../../lib/format';
import { demoSuffix } from '../../lib/navigation';
import { dashboardState, memoryUsage } from './model';

interface NodesTableProps {
  nodes: DashboardNode[];
  page: number;
  pageSize: number;
  total: number;
  onPageChange: (page: number) => void;
  reorderEnabled: boolean;
  reorderDisabledReason: string;
  orderingNodeID?: string;
  onReorder: (sourceID: string, targetID: string) => Promise<void>;
}

const stateLabel = { online: 'Работает', degraded: 'Деградирует', offline: 'Недоступна' } as const;

function numericMetadata(item: DashboardNode, key: string): number {
  const metadata = item.node.metadata;
  const value = metadata && typeof metadata === 'object' ? Number(metadata[key]) : 0;
  return Number.isFinite(value) ? value : 0;
}

interface NodeRowProps {
  item: DashboardNode;
  previousID?: string;
  nextID?: string;
  reorderEnabled: boolean;
  reorderDisabledReason: string;
  ordering: boolean;
  dragging: boolean;
  dropTarget: boolean;
  onDragStart: (event: DragEvent<HTMLButtonElement>, nodeID: string) => void;
  onDragEnd: () => void;
  onDragOver: (event: DragEvent<HTMLDivElement>, nodeID: string) => void;
  onDrop: (event: DragEvent<HTMLDivElement>, nodeID: string) => void;
  onMove: (sourceID: string, targetID: string) => void;
}

const NodeRow = memo(function NodeRow({ item, previousID, nextID, reorderEnabled, reorderDisabledReason, ordering, dragging, dropTarget, onDragStart, onDragEnd, onDragOver, onDrop, onMove }: NodeRowProps) {
  const location = useLocation();
  const navigate = useNavigate();
  const query = demoSuffix(location.search);
  const nodeURL = `/nodes/${item.node.id}${query}`;
  const newRouteURL = `/nodes/${item.node.id}/routes/new${query}`;
  const state = dashboardState(item);
  const heartbeat = item.latest_heartbeat;
  const metrics = heartbeat?.metrics;
  const runtime = metrics?.haproxy_runtime;
  const loads = metrics?.load ?? [];
  const cpu = Number(metrics?.cpu_percent);
  const memory = memoryUsage(metrics);
  const rx = item.rate_sampled_at ? item.rx_bits_per_second : null;
  const tx = item.rate_sampled_at ? item.tx_bits_per_second : null;
  const used = item.traffic_observed ? item.traffic_used_bytes : null;
  const limit = numericMetadata(item, 'traffic_limit_bytes');
  const quota = used !== null && limit > 0 ? Math.min(100, used / limit * 100) : 0;
  const open = () => navigate(nodeURL);
  const stop = (event: MouseEvent) => event.stopPropagation();
  const isInteractiveTarget = (target: EventTarget | null) => (
    target instanceof Element
    && Boolean(target.closest('a, button, input, select, textarea, [role="button"], [role="menuitem"]'))
  );
  const openFromRow = (event: MouseEvent<HTMLDivElement>) => {
    if (!isInteractiveTarget(event.target)) open();
  };
  const openFromKeyboard = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== 'Enter' || event.target !== event.currentTarget) return;
    event.preventDefault();
    open();
  };

  return (
    <div
      className={`nf-node-row is-${state}${dragging ? ' is-dragging' : ''}${dropTarget ? ' is-drop-target' : ''}${ordering ? ' is-ordering' : ''}`}
      role="row"
      tabIndex={0}
      aria-label={`Открыть ноду ${item.node.name}`}
      onClick={openFromRow}
      onKeyDown={openFromKeyboard}
      onDragOver={(event) => onDragOver(event, item.node.id)}
      onDrop={(event) => onDrop(event, item.node.id)}
    >
      <div className="nf-node-status" role="cell"><i /><span>{stateLabel[state]}</span></div>
      <div className="nf-node-identity-cell" role="cell">
        <div className="nf-sort-controls" data-row-action>
          <Tooltip label={reorderEnabled ? 'Перетащить ноду' : reorderDisabledReason} openDelay={250}>
            <span className="nf-sort-grip-wrap">
              <ActionIcon
                className="nf-sort-grip"
                variant="subtle"
                color="gray"
                size="sm"
                draggable={reorderEnabled && !ordering}
                disabled={!reorderEnabled || ordering}
                aria-label={`Перетащить ноду ${item.node.name}`}
                onDragStart={(event) => onDragStart(event, item.node.id)}
                onDragEnd={onDragEnd}
                onClick={stop}
              ><IconGripVertical size={15} /></ActionIcon>
            </span>
          </Tooltip>
          <div className="nf-sort-step-actions">
            <ActionIcon variant="subtle" color="gray" size="xs" disabled={!reorderEnabled || !previousID || ordering} aria-label={`Переместить ${item.node.name} выше`} onClick={(event) => { stop(event); if (previousID) onMove(item.node.id, previousID); }}><IconChevronUp size={12} /></ActionIcon>
            <ActionIcon variant="subtle" color="gray" size="xs" disabled={!reorderEnabled || !nextID || ordering} aria-label={`Переместить ${item.node.name} ниже`} onClick={(event) => { stop(event); if (nextID) onMove(item.node.id, nextID); }}><IconChevronDown size={12} /></ActionIcon>
          </div>
        </div>
        <Link className="nf-node-identity" to={nodeURL}><strong>{item.node.name}</strong><span>{item.node.address}</span><small>ответ {timeAgo(item.node.last_seen_at)}</small></Link>
      </div>
      <div className="nf-node-number" role="cell"><strong>{formatNumber(item.routes_total)}</strong><span>{item.routes_enabled} включено</span></div>
      <div className="nf-node-number" role="cell"><strong>{formatNumber(runtime?.connections_current)}</strong><span>{runtime?.connection_rate == null ? 'нет rate' : `${formatNumber(runtime.connection_rate)} новых/с`}</span></div>
      <Tooltip label={`Load 1 / 5 / 15: ${loads.map((value) => formatNumber(value, 2)).join(' · ')} · RAM ${formatBytes(memory.used)} / ${formatBytes(memory.total)}`} openDelay={350}>
        <div className="nf-node-system" role="cell" tabIndex={0}>
          <div><small>CPU</small><strong>{Number.isFinite(cpu) ? `${formatNumber(cpu)}%` : '—'}</strong></div>
          <div><small>RAM</small><strong>{(memory.total ?? 0) > 0 ? `${formatBytes(memory.used)} / ${formatBytes(memory.total)}` : '—'}</strong></div>
        </div>
      </Tooltip>
      <div className="nf-node-network" role="cell"><strong>↓ {formatBitrate(rx)}</strong><span>↑ {formatBitrate(tx)}</span></div>
      <div className="nf-node-traffic" role="cell">
        <div><strong>{formatBytes(used)}</strong><span>{limit > 0 ? `/ ${formatBytes(limit)}` : 'без лимита'}</span></div>
        <div
          className={`nf-progress${limit > 0 ? '' : ' is-unlimited'}`}
          role="img"
          aria-label={limit > 0 ? `Использовано ${Math.round(quota)} процентов квоты` : 'Трафик без лимита'}
        >
          <i style={{ '--nf-progress': limit > 0 ? quota / 100 : 1 } as CSSProperties} />
        </div>
        <small>{limit > 0 ? `${Math.round(quota)}%` : item.traffic_month || 'нет данных'}</small>
      </div>
      <div className="nf-node-version" role="cell" title={metrics?.haproxy_version}><strong>HAProxy {shortHAProxy(metrics?.haproxy_version)}</strong><span>Agent {heartbeat?.agent_version ?? '—'}</span></div>
      <div className="nf-node-actions" role="cell">
        <Menu position="bottom-end" withinPortal shadow="md">
          <Menu.Target>
            <ActionIcon variant="subtle" color="gray" aria-label={`Действия: ${item.node.name}`} onClick={stop}>
              <IconDotsVertical size={18} />
            </ActionIcon>
          </Menu.Target>
          <Menu.Dropdown onClick={stop}>
            <Menu.Item leftSection={<IconEye size={16} />} onClick={open}>Открыть ноду</Menu.Item>
            <Menu.Item leftSection={<IconRoute size={16} />} onClick={() => navigate(newRouteURL)}>Добавить маршрут</Menu.Item>
            <Menu.Item leftSection={<IconSettings size={16} />} onClick={() => navigate(`${nodeURL}#operations`)}>Управление нодой</Menu.Item>
          </Menu.Dropdown>
        </Menu>
      </div>
    </div>
  );
});

export function NodesTable({ nodes, page, pageSize, total, onPageChange, reorderEnabled, reorderDisabledReason, orderingNodeID = '', onReorder }: NodesTableProps) {
  const pages = Math.max(1, Math.ceil(total / pageSize));
  const [draggingID, setDraggingID] = useState('');
  const [dropTargetID, setDropTargetID] = useState('');
  // Handlers read the latest props/state through a ref so their identity stays
  // stable across renders; this lets React.memo(NodeRow) skip rows whose data
  // did not change on each 15 s poll.
  const latest = useRef({ reorderEnabled, orderingNodeID, draggingID, onReorder });
  latest.current = { reorderEnabled, orderingNodeID, draggingID, onReorder };
  const startDrag = useCallback((event: DragEvent<HTMLButtonElement>, nodeID: string) => {
    const { reorderEnabled: enabled, orderingNodeID: ordering } = latest.current;
    if (!enabled || ordering) return;
    event.stopPropagation();
    event.dataTransfer.effectAllowed = 'move';
    event.dataTransfer.setData('text/plain', nodeID);
    latest.current.draggingID = nodeID;
    setDraggingID(nodeID);
  }, []);
  const dragOver = useCallback((event: DragEvent<HTMLDivElement>, nodeID: string) => {
    const { reorderEnabled: enabled, draggingID: dragging } = latest.current;
    if (!enabled || !dragging || nodeID === dragging) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = 'move';
    setDropTargetID(nodeID);
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
  const finishDrag = useCallback(() => {
    latest.current.draggingID = '';
    setDraggingID('');
    setDropTargetID('');
  }, []);
  const move = useCallback((sourceID: string, targetID: string) => {
    void latest.current.onReorder(sourceID, targetID);
  }, []);
  return (
    <div className="nf-nodes-table" role="table" aria-label="Ноды" aria-colcount={9} aria-rowcount={nodes.length + 1}>
      <div className="nf-nodes-head" role="row">
        <span role="columnheader">Статус</span><span role="columnheader">Нода</span><span role="columnheader">Маршруты</span><span role="columnheader">Соединения</span><span role="columnheader">CPU / RAM</span><span role="columnheader">RX / TX</span><span role="columnheader">Трафик за месяц</span><span role="columnheader">HAProxy / Agent</span><span role="columnheader" aria-label="Действия" />
      </div>
      <div className="nf-nodes-body" role="rowgroup">
        {nodes.map((item, index) => (
          <NodeRow
            key={item.node.id}
            item={item}
            previousID={nodes[index - 1]?.node.id}
            nextID={nodes[index + 1]?.node.id}
            reorderEnabled={reorderEnabled}
            reorderDisabledReason={reorderDisabledReason}
            ordering={orderingNodeID === item.node.id}
            dragging={draggingID === item.node.id}
            dropTarget={dropTargetID === item.node.id}
            onDragStart={startDrag}
            onDragEnd={finishDrag}
            onDragOver={dragOver}
            onDrop={drop}
            onMove={move}
          />
        ))}
      </div>
      <footer className="nf-table-footer">
        <span>Показано {total ? (page - 1) * pageSize + 1 : 0}–{Math.min(total, page * pageSize)} из {total} нод</span>
        <Pagination total={pages} value={page} onChange={onPageChange} size="sm" withEdges siblings={1} boundaries={1} />
        <span>{pageSize} / стр.</span>
      </footer>
    </div>
  );
}
