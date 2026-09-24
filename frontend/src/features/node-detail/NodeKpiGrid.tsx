import { Tooltip } from '@mantine/core';
import { IconArrowDown, IconArrowUp, IconInfoCircle, IconShieldCheck } from '@tabler/icons-react';
import type { HeartbeatMetrics, NodeMetricSummary, TrafficHistorySample } from '../../lib/contracts';
import { formatBitrate, formatBytes, formatDuration, formatNumber, timeAgo } from '../../lib/format';
import { memoryUsage } from '../nodes/model';

interface NodeKpiGridProps {
  metrics?: HeartbeatMetrics;
  history: TrafficHistorySample[];
  summary?: NodeMetricSummary;
  currentRx: number | null;
  currentTx: number | null;
  rateSampledAt?: string;
  live: boolean;
  mtlsConnected: boolean;
}

function MiniSparkline({ values }: { values: number[] }) {
  const clean = values.filter(Number.isFinite).slice(-28);
  if (clean.length < 2) return <span className="nf-mini-sparkline is-empty" aria-hidden="true" />;
  const min = Math.min(...clean);
  const max = Math.max(...clean);
  const span = Math.max(1, max - min);
  const points = clean.map((value, index) => `${(index / (clean.length - 1)) * 100},${20 - ((value - min) / span) * 16}`).join(' ');
  return <svg className="nf-mini-sparkline" viewBox="0 0 100 22" preserveAspectRatio="none" aria-hidden="true"><polyline points={points} /></svg>;
}

function summaryHint(label: string, average?: number, delta?: number, sampleCount?: number, formatter: (value: number) => string = (value) => formatNumber(value, 1)) {
  if (!sampleCount || average == null) return `${label}: история ещё собирается`;
  const deltaCopy = delta == null ? 'дельта недоступна' : `дельта ${delta >= 0 ? '+' : ''}${formatter(delta)}`;
  return `${label}: среднее ${formatter(average)}, ${deltaCopy}, точек ${sampleCount}`;
}

function seriesSummary(values: number[]) {
  const clean = values.filter(Number.isFinite);
  if (!clean.length) return undefined;
  return {
    average: clean.reduce((sum, value) => sum + value, 0) / clean.length,
    delta: clean.at(-1)! - clean[0],
    sample_count: clean.length,
  };
}

function MetricLabel({ children, hint }: { children: React.ReactNode; hint?: string }) {
  return (
    <span className="nf-detail-kpi__label">
      {children}
      {hint && <Tooltip label={hint} multiline w={260} openDelay={250}><IconInfoCircle size={14} tabIndex={0} aria-label={hint} /></Tooltip>}
    </span>
  );
}

export function NodeKpiGrid({ metrics, history, summary, currentRx, currentTx, rateSampledAt, live, mtlsConnected }: NodeKpiGridProps) {
  const memory = memoryUsage(metrics);
  const loads = (metrics?.load ?? []).slice(0, 3);
  const runtime = metrics?.haproxy_runtime;
  const backends = Object.values(runtime?.backends ?? {});
  const sessionsCurrent = runtime ? backends.reduce((total, backend) => total + (Number(backend.sessions_current) || 0), 0) : undefined;
  const sessionsTotal = runtime ? backends.reduce((total, backend) => total + (Number(backend.sessions_total) || 0), 0) : undefined;
  const connectionsCurrent = live && Number.isFinite(runtime?.connections_current) ? Number(runtime?.connections_current) : undefined;
  const connectionsTotal = Number.isFinite(runtime?.connections_total) ? Number(runtime?.connections_total) : undefined;
  const connectionRate = live && Number.isFinite(runtime?.connection_rate) ? Number(runtime?.connection_rate) : undefined;
  const rxSeries = history.flatMap((sample) => sample.rx_bps === null ? [] : [sample.rx_bps]);
  const txSeries = history.flatMap((sample) => sample.tx_bps === null ? [] : [sample.tx_bps]);
  const cpuSeries = history.map((sample) => Number(sample.cpu_percent)).filter(Number.isFinite);
  const memorySeries = history.map((sample) => Number(sample.memory_percent)).filter(Number.isFinite);
  const cpuSummary = seriesSummary(cpuSeries) ?? summary?.cpu_percent;
  const memorySummary = seriesSummary(memorySeries) ?? summary?.memory_percent;
  const rxSummary = seriesSummary(rxSeries) ?? summary?.rx_bps;
  const txSummary = seriesSummary(txSeries) ?? summary?.tx_bps;
  const processCount = Number(metrics?.process_count);
  const processNames = (metrics?.process_names ?? []).filter(Boolean);
  const processHint = Number.isFinite(processCount) && processCount >= 0
    ? `Процессов: ${formatNumber(processCount)}${processNames.length ? `. Основные: ${processNames.join(', ')}` : ''}`
    : processNames.length ? `Основные процессы: ${processNames.join(', ')}` : 'Список процессов ещё не получен';

  return (
    <section className="nf-surface nf-detail-kpis" aria-label="Система, сеть и соединения">
      <div className="nf-detail-kpi">
        <MetricLabel hint={summaryHint('CPU', cpuSummary?.average, cpuSummary?.delta, cpuSummary?.sample_count, (value) => `${formatNumber(value, 1)}%`)}>CPU</MetricLabel>
        <strong>{Number.isFinite(Number(metrics?.cpu_percent)) ? `${formatNumber(Number(metrics?.cpu_percent), 1)}%` : '—'}</strong>
        <MiniSparkline values={cpuSeries} />
      </div>
      <div className="nf-detail-kpi is-memory">
        <MetricLabel hint={summaryHint('RAM', memorySummary?.average, memorySummary?.delta, memorySummary?.sample_count, (value) => `${formatNumber(value, 1)}%`)}>RAM</MetricLabel>
        <strong>{formatBytes(memory.used)} <small>/ {formatBytes(memory.total)}</small></strong>
        <span className="nf-detail-kpi__sub">{memory.percent == null ? 'Нет данных' : `${formatNumber(memory.percent, 1)}% занято`}</span>
        <MiniSparkline values={memorySeries} />
      </div>
      <div className="nf-detail-kpi is-load">
        <MetricLabel hint="Нормализованные значения Linux load average за 1, 5 и 15 минут">Load (1м / 5м / 15м)</MetricLabel>
        <strong>{loads.length ? loads.map((value) => formatNumber(value, 2)).join(' / ') : '—'}</strong>
        <span className="nf-detail-kpi__sub">текущий срез</span>
        <MiniSparkline values={[]} />
      </div>

      <div className="nf-detail-kpi is-network">
        <MetricLabel hint={summaryHint('RX', rxSummary?.average, rxSummary?.delta, rxSummary?.sample_count, formatBitrate)}><IconArrowDown size={17} /> RX</MetricLabel>
        <strong>{formatBitrate(currentRx)}</strong>
        <span className="nf-detail-kpi__sub">{currentRx === null ? rateSampledAt ? `последняя точка ${timeAgo(rateSampledAt)}` : 'нет свежей точки' : 'HAProxy сейчас'}</span>
        <MiniSparkline values={rxSeries} />
      </div>
      <div className="nf-detail-kpi is-network">
        <MetricLabel hint={summaryHint('TX', txSummary?.average, txSummary?.delta, txSummary?.sample_count, formatBitrate)}><IconArrowUp size={17} /> TX</MetricLabel>
        <strong>{formatBitrate(currentTx)}</strong>
        <span className="nf-detail-kpi__sub">{currentTx === null ? rateSampledAt ? `последняя точка ${timeAgo(rateSampledAt)}` : 'нет свежей точки' : 'HAProxy сейчас'}</span>
        <MiniSparkline values={txSeries} />
      </div>
      <div className="nf-detail-kpi">
        <MetricLabel hint={runtime ? `${connectionRate == null ? 'Rate не получен' : `${formatNumber(connectionRate)} новых/с`} · ${connectionsTotal == null ? 'total не получен' : `${formatNumber(connectionsTotal)} всего`}` : 'HAProxy runtime ещё не получен'}>Соединения</MetricLabel>
        <strong>{formatNumber(connectionsCurrent)}</strong>
        <span className="nf-detail-kpi__sub">{connectionRate == null && connectionsTotal == null ? 'Rate и total не получены' : `${connectionRate == null ? '— новых/с' : `${formatNumber(connectionRate)} новых/с`} · ${connectionsTotal == null ? '— всего' : `${formatNumber(connectionsTotal)} всего`}`}</span>
        <MiniSparkline values={[]} />
      </div>

      <div className="nf-detail-kpi">
        <MetricLabel hint={runtime ? `${formatNumber(sessionsTotal)} TCP-сессий всего` : 'HAProxy runtime ещё не получен'}>TCP-сессии</MetricLabel>
        <strong>{formatNumber(sessionsCurrent)}</strong>
        <span className="nf-detail-kpi__sub">{sessionsTotal == null ? 'Total не получен' : `${formatNumber(sessionsTotal)} всего`}</span>
        <MiniSparkline values={[]} />
      </div>
      <div className="nf-detail-kpi is-uptime">
        <MetricLabel hint={processHint}>Время работы</MetricLabel>
        <strong>{formatDuration(Number(metrics?.uptime_seconds))}</strong>
        <span className="nf-detail-kpi__sub">{Number.isFinite(processCount) ? `${formatNumber(processCount)} процессов` : 'без перезапуска Agent'}</span>
      </div>
      <div className={`nf-detail-kpi nf-detail-kpi--mtls${mtlsConnected ? ' is-ready' : ''}`}>
        <MetricLabel>mTLS соединение</MetricLabel>
        <strong><IconShieldCheck size={21} /> {mtlsConnected ? 'На связи' : 'Нет свежего сигнала'}</strong>
        <span className="nf-detail-kpi__sub">последний heartbeat по защищённому каналу</span>
      </div>
    </section>
  );
}
