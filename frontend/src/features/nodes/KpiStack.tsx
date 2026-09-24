import { IconArrowDown, IconArrowUp, IconInfoCircle } from '@tabler/icons-react';
import { useId } from 'react';
import { formatBitrate, formatBytes } from '../../lib/format';

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
        <header>Общая скорость <IconInfoCircle size={14} />{!rateComplete && <span className="nf-kpi-rate-state">Неполные данные</span>}</header>
        <div className="nf-speed-values">
          <div><strong><IconArrowDown /> {formatBitrate(rx)}</strong><span>RX суммарно</span></div>
          <div><strong><IconArrowUp /> {formatBitrate(tx)}</strong><span>TX суммарно</span></div>
        </div>
        <Sparkline values={trend} />
      </section>
      <section className="nf-kpi nf-kpi--traffic">
        <header>Трафик за месяц <IconInfoCircle size={14} /></header>
        <div className="nf-quota-copy"><strong>{formatBytes(monthUsed)}</strong><span>{monthLimit > 0 ? `из ${formatBytes(monthLimit)}` : aggregateScope ? 'без общего лимита' : 'без лимита'}</span></div>
        <div
          className={`nf-progress${monthLimit > 0 ? '' : ' is-unlimited'}`}
          role="img"
          aria-label={monthLimit > 0 ? `Использовано ${Math.round(quota)} процентов квоты` : 'Трафик без лимита'}
        >
          <i style={{ '--nf-progress': monthLimit > 0 ? quota / 100 : 1 } as React.CSSProperties} />
        </div>
        <small>{monthLimit > 0 ? `${Math.round(quota)}% использовано` : aggregateScope ? 'Общий лимит не задан' : 'Квота не задана'}</small>
      </section>
      <div className="nf-kpi-pair">
        <section className="nf-kpi nf-kpi--connections">
          <header>Соединения <IconInfoCircle size={14} /></header>
          <div className="nf-kpi__readout">
            <strong>{connections === null ? '—' : connections.toLocaleString('ru-RU')}</strong>
            <span>{connections === null ? 'нет полной текущей точки' : 'активных · текущий срез'}</span>
          </div>
        </section>
        <section className="nf-kpi nf-kpi--health">
          <header>Health <IconInfoCircle size={14} /></header>
          <div className="nf-health-body">
            <div className="nf-health-ring" style={{ '--health': `${health.percent ?? 0}%` } as React.CSSProperties}><strong>{health.percent === null ? '—' : `${health.percent}%`}</strong></div>
            <div className="nf-health-legend">
              <span><i className="is-up" /><b>{health.up}</b> Работает</span>
              <span><i className="is-warn" /><b>{health.degraded}</b> Деградирует</span>
              <span><i className="is-down" /><b>{health.down}</b> Недоступно</span>
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}
