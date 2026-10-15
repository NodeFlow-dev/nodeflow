import { IconArrowDown, IconArrowUp } from '@tabler/icons-react';
import { useState } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import type { DashboardRoute } from '../../lib/contracts';
import { formatBitrate, formatBytes } from '../../lib/format';
import { demoSuffix } from '../../lib/navigation';
import type { TrafficRange } from '../nodes/useNodesOverview';
import { isInteractiveRowTarget } from '../../lib/interaction';
import { t } from '../../i18n';

function matchRule(route: DashboardRoute) {
  if (route.fallback) return t('traffic.anyTcp');
  if (route.snis.length) return `SNI ${route.snis.join(', ')}`;
  return t('traffic.destinationIp');
}

const rangeLabels: Record<TrafficRange, string> = {
  '1m': t('traffic.n1Min'), '5m': t('traffic.n5Min'), '1h': t('traffic.n1H'), '24h': t('traffic.n24H'), '7d': t('traffic.n7D'), '30d': t('traffic.n30D'),
};

export function TrafficRouteRanking({ routes, scopeLabel, range }: { routes: DashboardRoute[]; scopeLabel: string; range: TrafficRange }) {
  const location = useLocation();
  const navigate = useNavigate();
  const query = demoSuffix(location.search);
  const [mobileExpanded, setMobileExpanded] = useState(false);
  return (
    <section className="nf-traffic-routes" aria-labelledby="traffic-routes-title">
      <header>
        <div>
          <h2 id="traffic-routes-title">{t('nodes.topRoutes')}</h2>
          <p>{t('traffic.averageRxTx')} {scopeLabel} · {rangeLabels[range]}</p>
        </div>
        <span>{routes.length || '—'}</span>
      </header>

      <div className="nf-traffic-routes__head" aria-hidden="true">
        <span>#</span><span>{t('nodes.route')}</span><span>{t('nodes.node')}</span><span>{t('traffic.rate')}</span><span>{t('traffic.share')}</span>
      </div>
      {routes.length ? (
        <ol>
          {routes.map((route, index) => (
            <li
              key={route.route_id}
              className={index >= 3 && !mobileExpanded ? 'is-mobile-collapsed' : ''}
              tabIndex={0}
              aria-label={t('node.editRouteName', { name: route.name })}
              onClick={(event) => { if (!isInteractiveRowTarget(event.target, event.currentTarget)) navigate(`/nodes/${route.node_id}/routes/${route.route_id}/edit${query}`); }}
              onKeyDown={(event) => { if (event.key === 'Enter' && event.target === event.currentTarget) navigate(`/nodes/${route.node_id}/routes/${route.route_id}/edit${query}`); }}
            >
              <span className="nf-traffic-routes__index">{index + 1}</span>
              <div className="nf-traffic-routes__identity">
                <Link to={`/nodes/${route.node_id}/routes/${route.route_id}/edit${query}`} title={route.name}>{route.name}</Link>
                <small title={`${route.node_name} · ${matchRule(route)} · ${route.listener_ip || '*'}:${route.listener_port}`}>
                  {route.node_name} · {matchRule(route)} · {route.listener_ip || '*'}:{route.listener_port}
                </small>
                <i aria-hidden="true"><em style={{ transform: `scaleX(${Math.max(3, Math.min(100, route.share_percent)) / 100})` }} /></i>
              </div>
              <Link className="nf-traffic-routes__node" to={`/nodes/${route.node_id}${query}`}>{route.node_name}</Link>
              <div className="nf-traffic-routes__rate" title={t('traffic.currentMonthRxTxUsed', { used_bytes: formatBytes(route.used_bytes) })}>
                <strong>{formatBitrate(route.bits_per_second)}</strong>
                <small><span><IconArrowDown size={11} />{formatBitrate(route.rx_bits_per_second)}</span><span><IconArrowUp size={11} />{formatBitrate(route.tx_bits_per_second)}</span></small>
              </div>
              <b>{Math.round(route.share_percent)}%</b>
            </li>
          ))}
          {routes.length > 3 && (
            <li className="nf-traffic-routes__mobile-more">
              <button type="button" onClick={() => setMobileExpanded((value) => !value)}>
                {mobileExpanded ? t('traffic.collapseList') : t('traffic.p0MoreRoutes', { p0: routes.length - 3 })}
              </button>
            </li>
          )}
        </ol>
      ) : (
        <div className="nf-traffic-routes__empty" role="status">
          <strong>{t('traffic.noRouteDataPoints')}</strong>
          <span>{t('traffic.rateAppearsAfterTheNext')}</span>
        </div>
      )}
    </section>
  );
}
