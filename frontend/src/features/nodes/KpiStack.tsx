import { IconArrowDown, IconArrowUp, IconInfoCircle } from '@tabler/icons-react';
import { useId } from 'react';
import { formatBitrate, formatBytes } from '../../lib/format';
import { t, locale } from '../../i18n';

interface KpiStackProps {
  rx: number | null;
  tx: number | null;
  rateComplete: boolean;
  aggregateScope: boolean;
  monthUsed: number;
  monthLimit: number;
  connections: number | null;
  health: { percent: number | null; up: number; degraded: number; down: number };
  trend: number[];
}

function Sparkline({ values }: { values: number[] }) {
  const gradientID = `nf-sparkline-${useId().replaceAll(':', '')}`;
  if (values.length < 2) return null;
  const max = Math.max(...values, 1);
  const min = Math.min(...values);
  const span = Math.max(1, max - min);
  const points = values.map((value, index) => ({
    x: (index / (values.length - 1)) * 100,
    y: 30 - ((value - min) / span) * 24,
  }));
  const polyline = points.map(({ x, y }) => `${x},${y}`).join(' ');
  const area = `M ${points.map(({ x, y }) => `${x} ${y}`).join(' L ')} L 100 36 L 0 36 Z`;
  return (
    <svg className="nf-sparkline" viewBox="0 0 100 36" preserveAspectRatio="none" aria-hidden="true">
      <defs>
        <linearGradient id={gradientID} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="var(--nf-accent-strong)" stopOpacity=".3" />
          <stop offset="100%" stopColor="var(--nf-accent-strong)" stopOpacity="0" />
        </linearGradient>
      </defs>
      <path className="nf-sparkline__area" d={area} fill={`url(#${gradientID})`} />
      <polyline points={polyline} />
    </svg>
  );
}

export function KpiStack({ rx, tx, rateComplete, aggregateScope, monthUsed, monthLimit, connections, health, trend }: KpiStackProps) {
  const quota = monthLimit > 0 ? Math.min(100, (monthUsed / monthLimit) * 100) : 0;
  return (
    <div className="nf-kpi-stack">
      <section className="nf-kpi nf-kpi--speed">
        <header>{t('nodes.totalThroughput')} <IconInfoCircle size={14} />{!rateComplete && <span className="nf-kpi-rate-state">{t('nodes.incompleteData')}</span>}</header>
        <div className="nf-speed-values">
          <div><strong><IconArrowDown /> {formatBitrate(rx)}</strong><span>{t('nodes.rxTotal')}</span></div>
          <div><strong><IconArrowUp /> {formatBitrate(tx)}</strong><span>{t('nodes.txTotal')}</span></div>
        </div>
        <Sparkline values={trend} />
      </section>
      <section className="nf-kpi nf-kpi--traffic">
        <header>{t('nodes.monthlyTraffic')} <IconInfoCircle size={14} /></header>
        <div className="nf-quota-copy"><strong>{formatBytes(monthUsed)}</strong><span>{monthLimit > 0 ? t('nodes.ofFormatbytes', { formatBytes: formatBytes(monthLimit) }) : aggregateScope ? t('nodes.noSharedLimit') : t('nodes.noLimit')}</span></div>
        <div
          className={`nf-progress${monthLimit > 0 ? '' : ' is-unlimited'}`}
          role="img"
          aria-label={monthLimit > 0 ? t('nodes.roundPercentOfQuotaUsed', { round: Math.round(quota) }) : t('nodes.trafficWithoutLimit')}
        >
          <i style={{ '--nf-progress': monthLimit > 0 ? quota / 100 : 1 } as React.CSSProperties} />
        </div>
        <small>{monthLimit > 0 ? t('nodes.roundUsed', { round: Math.round(quota) }) : aggregateScope ? t('nodes.noSharedLimitSet') : t('nodes.noQuotaSet')}</small>
      </section>
      <div className="nf-kpi-pair">
        <section className="nf-kpi nf-kpi--connections">
          <header>{t('nodes.connections')} <IconInfoCircle size={14} /></header>
          <div className="nf-kpi__readout">
            <strong>{connections === null ? '—' : connections.toLocaleString(locale())}</strong>
            <span>{connections === null ? t('nodes.noCompleteCurrentSample') : t('nodes.activeCurrentSnapshot')}</span>
          </div>
        </section>
        <section className="nf-kpi nf-kpi--health">
          <header>Health <IconInfoCircle size={14} /></header>
          <div className="nf-health-body">
            <div className="nf-health-ring" style={{ '--health': `${health.percent ?? 0}%` } as React.CSSProperties}><strong>{health.percent === null ? '—' : `${health.percent}%`}</strong></div>
            <div className="nf-health-legend">
              <span><i className="is-up" /><b>{health.up}</b> {t('nodes.online3')}</span>
              <span><i className="is-warn" /><b>{health.degraded}</b> {t('nodes.degraded2')}</span>
              <span><i className="is-down" /><b>{health.down}</b> {t('nodes.unavailable')}</span>
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}
