import { Button } from '@mantine/core';
import { IconArrowDown, IconArrowUp, IconChartLine } from '@tabler/icons-react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import type { DashboardNode } from '../../lib/contracts';
import { formatBitrate, formatBytes, formatNumber, timeAgo } from '../../lib/format';
import { demoSuffix } from '../../lib/navigation';
import { isInteractiveRowTarget } from '../../lib/interaction';

export type TrafficNodeState = 'online' | 'degraded' | 'offline';

export function trafficNodeState(node: DashboardNode): TrafficNodeState {
  const status = String(node.node.status ?? '').toLowerCase();
  if (status === 'online') return 'online';
  if (status === 'degraded') return 'degraded';
  return 'offline';
}

const stateLabels: Record<TrafficNodeState, string> = {
  online: 'Работает',
  degraded: 'Деградирует',
  offline: 'Недоступна',
};

interface TrafficNodesTableProps {
  nodes: DashboardNode[];
  selectedNodeId?: string;
  totalRate: number | null;
  totalMonthBytes: number;
  onSelect: (nodeId?: string) => void;
}

function trafficLimit(node: DashboardNode) {
  const value = Number(node.node.metadata?.traffic_limit_bytes);
  return Number.isFinite(value) && value > 0 ? value : 0;
}

export function TrafficNodesTable({ nodes, selectedNodeId, totalRate, totalMonthBytes, onSelect }: TrafficNodesTableProps) {
  const location = useLocation();
  const navigate = useNavigate();
  const query = demoSuffix(location.search);
  return (
    <div className="nf-traffic-nodes-table">
      <table>
        <thead>
          <tr>
            <th>Статус</th>
            <th>Нода</th>
            <th>Маршруты</th>
            <th>Соединения</th>
            <th>RX / TX</th>
            <th>Трафик за месяц</th>
            <th>Последняя точка</th>
            <th><span className="nf-visually-hidden">Выбрать источник графика</span></th>
          </tr>
        </thead>
        <tbody>
          {nodes.map((item) => {
            const state = trafficNodeState(item);
            const rx = item.rate_sampled_at ? item.rx_bits_per_second : null;
            const tx = item.rate_sampled_at ? item.tx_bits_per_second : null;
            const currentRate = Number.isFinite(rx) && Number.isFinite(tx) ? Number(rx) + Number(tx) : null;
            const rateShare = currentRate !== null && totalRate !== null && totalRate > 0 ? currentRate / totalRate * 100 : null;
            const limit = trafficLimit(item);
            const monthShare = totalMonthBytes > 0 ? item.traffic_used_bytes / totalMonthBytes * 100 : 0;
            const quotaPercent = limit > 0 ? item.traffic_used_bytes / limit * 100 : 0;
            const meterPercent = Math.max(0, Math.min(100, limit > 0 ? quotaPercent : monthShare));
            const rawConnections = item.latest_heartbeat?.metrics.haproxy_runtime?.connections_current;
            const connections = state !== 'offline' && Number.isFinite(rawConnections) ? Number(rawConnections) : null;
            const selected = selectedNodeId === item.node.id;
            return (
              <tr
                key={item.node.id}
                className={`is-${state}${selected ? ' is-selected' : ''}`}
                tabIndex={0}
                aria-label={`Открыть ноду ${item.node.name}`}
                onClick={(event) => { if (!isInteractiveRowTarget(event.target, event.currentTarget)) navigate(`/nodes/${item.node.id}${query}`); }}
                onKeyDown={(event) => { if (event.key === 'Enter' && event.target === event.currentTarget) navigate(`/nodes/${item.node.id}${query}`); }}
              >
                <td data-label="Статус"><span className="nf-traffic-node-state"><i />{stateLabels[state]}</span></td>
                <td data-label="Нода"><Link className="nf-traffic-node-name" to={`/nodes/${item.node.id}${query}`}><strong>{item.node.name}</strong><span>{item.node.address}</span></Link></td>
                <td data-label="Маршруты"><strong>{item.routes_enabled}</strong><span className="nf-traffic-cell-muted"> из {item.routes_total}</span></td>
                <td data-label="Соединения"><strong>{formatNumber(connections)}</strong></td>
                <td data-label="RX / TX">
                  <div className="nf-traffic-node-rate"><span><IconArrowDown size={13} />{formatBitrate(rx)}</span><span><IconArrowUp size={13} />{formatBitrate(tx)}</span></div>
                </td>
                <td data-label="Трафик за месяц">
                  <div className="nf-traffic-node-volume">
                    <strong>{item.traffic_observed ? `${formatBytes(item.traffic_used_bytes)}${limit > 0 ? ` / ${formatBytes(limit)}` : ''}` : '—'}</strong>
                    {item.traffic_observed && <i aria-hidden="true"><em style={{ transform: `scaleX(${meterPercent / 100})` }} /></i>}
                    <span>{item.traffic_observed ? (limit > 0 ? `${Math.round(quotaPercent)}% квоты` : `${Math.round(monthShare)}% общего объёма`) : 'Нет данных за месяц'}</span>
                  </div>
                </td>
                <td data-label="Последняя точка">{item.rate_sampled_at ? <time dateTime={item.rate_sampled_at}>{timeAgo(item.rate_sampled_at)}</time> : 'Нет точки'}</td>
                <td>
                  <Button
                    variant={selected ? 'light' : 'subtle'}
                    color="nodeflow"
                    size="compact-sm"
                    leftSection={<IconChartLine size={15} />}
                    onClick={() => onSelect(selected ? undefined : item.node.id)}
                    aria-pressed={selected}
                    aria-label={selected
                      ? `Вернуть общий график вместо ${item.node.name}`
                      : rateShare === null
                        ? `Показать трафик ноды ${item.node.name}, текущая скорость недоступна`
                        : `Показать трафик ноды ${item.node.name}, сейчас ${Math.round(rateShare)}% общей скорости`}
                  >
                    {selected ? 'Общий' : 'На график'}
                  </Button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
