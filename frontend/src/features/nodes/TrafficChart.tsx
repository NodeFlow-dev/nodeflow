import { SegmentedControl } from '@mantine/core';
import { useMediaQuery, useReducedMotion } from '@mantine/hooks';
import ReactEChartsCore from 'echarts-for-react/lib/core';
import * as echarts from 'echarts/core';
import { LineChart } from 'echarts/charts';
import { AriaComponent, GridComponent, TooltipComponent } from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';
import { useEffect, useMemo, useState } from 'react';
import { nodeFlowChartTheme } from '../../lib/appearance';
import type { TrafficHistorySample } from '../../lib/contracts';
import { formatBitrate } from '../../lib/format';
import type { TrafficRange } from './useNodesOverview';

interface TrafficChartProps {
  samples: TrafficHistorySample[];
  range: TrafficRange;
  onRangeChange: (range: TrafficRange) => void;
  currentRx?: number | null;
  currentTx?: number | null;
  emptyTitle?: string;
  emptyDescription?: string;
  emptyFooterLabel?: string;
  status?: {
    tone: 'online' | 'degraded' | 'offline' | 'stale';
    label: string;
  };
}

const ranges = [
  { label: '1м', value: '1m' },
  { label: '5м', value: '5m' },
  { label: '1ч', value: '1h' },
  { label: '24ч', value: '24h' },
  { label: '7д', value: '7d' },
  { label: '30д', value: '30d' },
];

type TooltipParam = { axisValueLabel?: string; marker?: string; seriesName?: string; value?: [number, number | null] };

function formatAxisTick(value: number | string, range: TrafficRange) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  if (range === '1m') return new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(date);
  if (range === '5m' || range === '1h' || range === '24h') return new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit' }).format(date);
  return new Intl.DateTimeFormat('ru-RU', { day: '2-digit', month: '2-digit' }).format(date);
}

echarts.use([LineChart, GridComponent, TooltipComponent, AriaComponent, CanvasRenderer]);

export function TrafficChart({
  samples, range, onRangeChange, currentRx, currentTx, status,
  emptyTitle = 'История трафика собирается',
  emptyDescription = 'Первая точка появится после сигнала Node Agent.',
  emptyFooterLabel = 'Ожидаем первую точку',
}: TrafficChartProps) {
  const reducedMotion = useReducedMotion();
  const compactChart = useMediaQuery('(max-width: 479px)') ?? false;
  const [appearanceVersion, setAppearanceVersion] = useState(0);
  useEffect(() => {
    const update = () => setAppearanceVersion((value) => value + 1);
    window.addEventListener('nodeflow:appearance', update);
    return () => window.removeEventListener('nodeflow:appearance', update);
  }, []);
  const chartTheme = useMemo(() => nodeFlowChartTheme(), [appearanceVersion]);
  const latest = [...samples].reverse().find((sample) => Number.isFinite(sample.rx_bps) || Number.isFinite(sample.tx_bps));
  const pointCounts = useMemo(() => samples.reduce((counts, sample) => {
    if (Number.isFinite(sample.rx_bps)) counts.rxObserved += 1; else counts.rxGaps += 1;
    if (Number.isFinite(sample.tx_bps)) counts.txObserved += 1; else counts.txGaps += 1;
    return counts;
  }, { rxObserved: 0, rxGaps: 0, txObserved: 0, txGaps: 0 }), [samples]);
  const timeExtent = useMemo(() => {
    const values = samples.map((sample) => new Date(sample.timestamp).getTime()).filter(Number.isFinite);
    if (!values.length) return undefined;
    const first = Math.min(...values);
    const last = Math.max(...values);
    return [first, last > first ? last : first + 1_000] as const;
  }, [samples]);
  const option = useMemo(() => ({
    animation: !reducedMotion,
    animationDuration: 220,
    animationDurationUpdate: 180,
    animationEasing: 'cubicOut',
    animationEasingUpdate: 'cubicOut',
    aria: { enabled: true, decal: { show: false } },
    grid: { top: 18, right: compactChart ? 6 : 10, bottom: 34, left: compactChart ? 50 : 72 },
    textStyle: { fontFamily: 'Inter, system-ui, sans-serif' },
    tooltip: {
      trigger: 'axis',
      confine: true,
      className: 'nf-chart-tooltip',
      axisPointer: { type: 'line', lineStyle: { color: chartTheme.pointer, width: 1 } },
      formatter: (params: TooltipParam[]) => {
        const items = Array.isArray(params) ? params : [];
        const heading = items[0]?.axisValueLabel ?? '';
        return `<strong>${heading}</strong>${items.map((item) => `<span>${item.marker ?? ''}${item.seriesName} <b>${formatBitrate(item.value?.[1])}</b></span>`).join('')}`;
      },
    },
    xAxis: {
      type: 'time',
      boundaryGap: false,
      min: timeExtent?.[0],
      max: timeExtent?.[1],
      splitNumber: compactChart ? 3 : 6,
      axisLine: { lineStyle: { color: chartTheme.axisLine } },
      axisTick: { show: false },
      axisLabel: { color: chartTheme.axisText, fontSize: compactChart ? 10 : 11, hideOverlap: true, margin: 14, formatter: (value: number | string) => formatAxisTick(value, range) },
      splitLine: { show: false },
    },
    yAxis: {
      type: 'value',
      min: 0,
      axisLine: { show: false },
      axisTick: { show: false },
      axisLabel: {
        color: chartTheme.axisText,
        fontSize: compactChart ? 10 : 11,
        formatter: (value: number) => value > 0 && value < 1 ? '' : formatBitrate(value),
      },
      splitLine: { lineStyle: { color: chartTheme.splitLine, type: 'dashed' } },
    },
    series: [
      {
        name: 'RX',
        type: 'line',
        data: samples.map((sample) => [sample.timestamp, Number.isFinite(sample.rx_bps) ? sample.rx_bps : null]),
        connectNulls: false,
        showSymbol: false,
        smooth: 0.14,
        lineStyle: { width: 1.7, color: chartTheme.primary },
        areaStyle: {
          color: {
            type: 'linear', x: 0, y: 0, x2: 0, y2: 1,
            colorStops: [{ offset: 0, color: chartTheme.primaryAreaTop }, { offset: 1, color: chartTheme.primaryAreaBottom }],
          },
        },
      },
      {
        name: 'TX',
        type: 'line',
        data: samples.map((sample) => [sample.timestamp, Number.isFinite(sample.tx_bps) ? sample.tx_bps : null]),
        connectNulls: false,
        showSymbol: false,
        smooth: 0.14,
        lineStyle: { width: 1.5, color: chartTheme.secondary },
        areaStyle: {
          color: {
            type: 'linear', x: 0, y: 0, x2: 0, y2: 1,
            colorStops: [{ offset: 0, color: chartTheme.secondaryAreaTop }, { offset: 1, color: chartTheme.secondaryAreaBottom }],
          },
        },
      },
    ],
  }), [samples, range, reducedMotion, compactChart, timeExtent, chartTheme]);
  const updatedAt = latest?.timestamp ? new Date(latest.timestamp) : null;
  const updatedLabel = updatedAt && !Number.isNaN(updatedAt.getTime())
    ? `Обновлено: ${new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(updatedAt)}`
    : emptyFooterLabel;
  const explicitCurrent = currentRx !== undefined || currentTx !== undefined;
  const actualRx = currentRx === undefined ? latest?.rx_bps : currentRx;
  const actualTx = currentTx === undefined ? latest?.tx_bps : currentTx;
  const valueScope = explicitCurrent ? 'Сейчас' : 'Последняя точка';
  const chartLabel = `Трафик за период ${ranges.find((item) => item.value === range)?.label ?? range}. ${valueScope} RX ${formatBitrate(actualRx)}, TX ${formatBitrate(actualTx)}.`;
  const currentStatus = status ?? {
    tone: latest ? 'stale' as const : 'offline' as const,
    label: latest ? 'Исторические данные' : 'Нет свежих данных',
  };

  return (
    <div className="nf-traffic-chart">
      <div className="nf-chart-head">
        <div className="nf-chart-legend" aria-label={explicitCurrent ? 'Текущая скорость' : 'Последняя точка истории'}>
          <span><i className="is-rx" /> RX <b>{formatBitrate(actualRx)}</b></span>
          <span><i className="is-tx" /> TX <b>{formatBitrate(actualTx)}</b></span>
        </div>
        <SegmentedControl
          className="nf-range-control"
          value={range}
          onChange={(value) => onRangeChange(value as TrafficRange)}
          data={ranges}
          size="sm"
          aria-label="Период графика"
        />
      </div>
      {latest ? (
        <div
          role="img"
          aria-label={chartLabel}
          data-rx-observed-points={pointCounts.rxObserved}
          data-rx-gap-points={pointCounts.rxGaps}
          data-tx-observed-points={pointCounts.txObserved}
          data-tx-gap-points={pointCounts.txGaps}
        >
          <ReactEChartsCore echarts={echarts} option={option} notMerge lazyUpdate className="nf-chart-canvas" />
          <p className="nf-visually-hidden">{chartLabel}</p>
        </div>
      ) : (
        <div className="nf-chart-empty"><strong>{emptyTitle}</strong><span>{emptyDescription}</span></div>
      )}
      <footer className="nf-chart-footer">
        <span className={`is-${currentStatus.tone}`}><i />{currentStatus.label}</span>
        <time dateTime={latest?.timestamp}>{updatedLabel}</time>
      </footer>
    </div>
  );
}
