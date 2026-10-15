import { Tooltip } from '@mantine/core';
import { IconArrowDown, IconArrowUp, IconInfoCircle, IconShieldCheck } from '@tabler/icons-react';
import type { HeartbeatMetrics, NodeMetricSummary, TrafficHistorySample } from '../../lib/contracts';
import { formatBitrate, formatBytes, formatDuration, formatNumber, timeAgo } from '../../lib/format';
import { memoryUsage } from '../nodes/model';
import { t } from '../../i18n';

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
  if (!sampleCount || average == null) return t('node.labelHistoryIsStillBeing', { label });
  const deltaCopy = delta == null ? t('node.deltaUnavailable') : t('node.deltaP0Formatter', { p0: delta >= 0 ? '+' : '', formatter: formatter(delta) });
  return t('node.labelAverageFormatterDeltacopySamples', { label, formatter: formatter(average), deltaCopy, sampleCount });
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
    ? t('node.processesFormatnumberP1', { formatNumber: formatNumber(processCount), p1: processNames.length ? t('misc.mainP0', { p0: processNames.join(', ') }) : '' })
    : processNames.length ? t('node.mainProcessesP0', { p0: processNames.join(', ') }) : t('node.processListNotReceivedYet');

  return (
    <section className="nf-surface nf-detail-kpis" aria-label={t('node.systemNetworkAndConnections')}>
      <div className="nf-detail-kpi">
        <MetricLabel hint={summaryHint('CPU', cpuSummary?.average, cpuSummary?.delta, cpuSummary?.sample_count, (value) => `${formatNumber(value, 1)}%`)}>CPU</MetricLabel>
        <strong>{Number.isFinite(Number(metrics?.cpu_percent)) ? `${formatNumber(Number(metrics?.cpu_percent), 1)}%` : '—'}</strong>
        <MiniSparkline values={cpuSeries} />
      </div>
      <div className="nf-detail-kpi is-memory">
        <MetricLabel hint={summaryHint('RAM', memorySummary?.average, memorySummary?.delta, memorySummary?.sample_count, (value) => `${formatNumber(value, 1)}%`)}>RAM</MetricLabel>
        <strong>{formatBytes(memory.used)} <small>/ {formatBytes(memory.total)}</small></strong>
        <span className="nf-detail-kpi__sub">{memory.percent == null ? t('node.noData') : t('node.p0Used', { p0: formatNumber(memory.percent, 1) })}</span>
        <MiniSparkline values={memorySeries} />
      </div>
      <div className="nf-detail-kpi is-load">
        <MetricLabel hint={t('node.normalizedLinuxLoadAverageOver')}>{t('node.load1m5m15m')}</MetricLabel>
        <strong>{loads.length ? loads.map((value) => formatNumber(value, 2)).join(' / ') : '—'}</strong>
        <span className="nf-detail-kpi__sub">{t('node.currentSnapshot')}</span>
        <MiniSparkline values={[]} />
      </div>

      <div className="nf-detail-kpi is-network">
        <MetricLabel hint={summaryHint('RX', rxSummary?.average, rxSummary?.delta, rxSummary?.sample_count, formatBitrate)}><IconArrowDown size={17} /> RX</MetricLabel>
        <strong>{formatBitrate(currentRx)}</strong>
        <span className="nf-detail-kpi__sub">{currentRx === null ? rateSampledAt ? t('node.latestSampleTimeago', { timeAgo: timeAgo(rateSampledAt) }) : t('node.noFreshSample') : t('node.haproxyNow')}</span>
        <MiniSparkline values={rxSeries} />
      </div>
      <div className="nf-detail-kpi is-network">
        <MetricLabel hint={summaryHint('TX', txSummary?.average, txSummary?.delta, txSummary?.sample_count, formatBitrate)}><IconArrowUp size={17} /> TX</MetricLabel>
        <strong>{formatBitrate(currentTx)}</strong>
        <span className="nf-detail-kpi__sub">{currentTx === null ? rateSampledAt ? t('node.latestSampleTimeago', { timeAgo: timeAgo(rateSampledAt) }) : t('node.noFreshSample') : t('node.haproxyNow')}</span>
        <MiniSparkline values={txSeries} />
      </div>
      <div className="nf-detail-kpi">
        <MetricLabel hint={runtime ? `${connectionRate == null ? t('node.rateNotReceived') : t('node.newPerSecond', { rate: formatNumber(connectionRate) })} · ${connectionsTotal == null ? t('node.totalNotReceivedLower') : t('node.totalCount', { total: formatNumber(connectionsTotal) })}` : t('node.haproxyRuntimeNotReceivedYet')}>{t('nodes.connections')}</MetricLabel>
        <strong>{formatNumber(connectionsCurrent)}</strong>
        <span className="nf-detail-kpi__sub">{connectionRate == null && connectionsTotal == null ? t('node.rateAndTotalNotReceived') : `${t('node.newPerSecond', { rate: connectionRate == null ? '—' : formatNumber(connectionRate) })} · ${t('node.totalCount', { total: connectionsTotal == null ? '—' : formatNumber(connectionsTotal) })}`}</span>
        <MiniSparkline values={[]} />
      </div>

      <div className="nf-detail-kpi">
        <MetricLabel hint={runtime ? t('node.formatnumberTcpSessionsTotal', { formatNumber: formatNumber(sessionsTotal) }) : t('node.haproxyRuntimeNotReceivedYet')}>{t('node.tcpSessions')}</MetricLabel>
        <strong>{formatNumber(sessionsCurrent)}</strong>
        <span className="nf-detail-kpi__sub">{sessionsTotal == null ? t('node.totalNotReceived') : t('node.formatnumberTotal', { formatNumber: formatNumber(sessionsTotal) })}</span>
        <MiniSparkline values={[]} />
      </div>
      <div className="nf-detail-kpi is-uptime">
        <MetricLabel hint={processHint}>{t('node.uptime')}</MetricLabel>
        <strong>{formatDuration(Number(metrics?.uptime_seconds))}</strong>
        <span className="nf-detail-kpi__sub">{Number.isFinite(processCount) ? t('node.formatnumberProcesses', { formatNumber: formatNumber(processCount) }) : t('node.withoutAgentRestart')}</span>
      </div>
      <div className={`nf-detail-kpi nf-detail-kpi--mtls${mtlsConnected ? ' is-ready' : ''}`}>
        <MetricLabel>{t('node.mtlsConnection')}</MetricLabel>
        <strong><IconShieldCheck size={21} /> {mtlsConnected ? t('node.connected') : t('node.noFreshHeartbeat')}</strong>
        <span className="nf-detail-kpi__sub">{t('node.lastHeartbeatOverTheSecure')}</span>
      </div>
    </section>
  );
}
