import { IconArrowRight } from '@tabler/icons-react';
import { Link, useLocation } from 'react-router-dom';
import type { DashboardRoute } from '../../lib/contracts';
import { formatBitrate, formatBytes } from '../../lib/format';
import { demoSuffix } from '../../lib/navigation';

function routeLabel(route: DashboardRoute): string {
  if (route.name) return route.name;
  if (route.fallback) return `Весь трафик · ${route.listener_ip || '*'}:${route.listener_port}`;
  return route.snis[0] ?? `Маршрут · ${route.listener_ip || '*'}:${route.listener_port}`;
}

export function RoutesRanking({ routes }: { routes: DashboardRoute[] }) {
  const location = useLocation();
  const visible = routes.slice(0, 5);
  return (
    <div className="nf-ranking">
      <header>
        <h2>Топ маршрутов</h2>
        <span>RX + TX</span>
      </header>
      <div className="nf-ranking__columns" aria-hidden="true"><span>#</span><span>Маршрут</span><span>Нода</span><span>Трафик</span><span>%</span></div>
      <ol>
        {visible.length ? visible.map((route, index) => (
          <li key={route.route_id}>
            <span className="nf-ranking__index">{index + 1}</span>
            <div className="nf-ranking__route">
              <strong title={routeLabel(route)}>{routeLabel(route)}</strong>
              <i><em style={{ width: `${Math.max(4, Math.min(100, route.share_percent))}%` }} /></i>
            </div>
            <span className="nf-ranking__node" title={route.node_name}>{route.node_name}</span>
            <b title={`За месяц: ${formatBytes(route.used_bytes)}`}>{formatBitrate(route.bits_per_second)}</b>
            <span>{Math.round(route.share_percent)}%</span>
          </li>
        )) : <li className="nf-ranking__empty">Маршрутов пока нет</li>}
      </ol>
      <Link to={`/traffic${demoSuffix(location.search)}`} className="nf-ranking__more">Посмотреть все маршруты <IconArrowRight size={16} /></Link>
    </div>
  );
}
