import { useMemo } from 'react';
import type { RuntimeInfo } from '../types';
import { bytes, number } from '../lib/format';
import { Chart } from './Chart';
import type { ChartOption } from './Chart';
import { Badge, Empty, Panel } from './ui';

export function runtimeTrendOption(samples: RuntimeInfo[], kind: 'cpu' | 'memory'): ChartOption {
  const fields: { name: string; field: 'cpu_percent' | 'go_bytes' | 'heap_bytes' }[] =
    kind === 'cpu'
      ? [{ name: 'CPU', field: 'cpu_percent' }]
      : [
          { name: 'Go memory', field: 'go_bytes' },
          { name: 'Heap allocated', field: 'heap_bytes' },
        ];
  const axis = {
    axisLine: { lineStyle: { color: '#33373d' } },
    axisTick: { show: false },
    axisLabel: { color: '#858c96', fontSize: 10 },
    splitLine: { lineStyle: { color: '#24282e', type: 'dashed' as const } },
  };
  return {
    xAxis: { type: 'time', ...axis, splitLine: { show: false } },
    yAxis: {
      type: 'value',
      min: 0,
      ...axis,
      axisLabel: {
        ...axis.axisLabel,
        formatter: (value) => (kind === 'cpu' ? `${number(value, 1)}%` : bytes(value)),
      },
    },
    tooltip: {
      trigger: 'axis',
      valueFormatter: (value) =>
        value === null || value === undefined || value === '-'
          ? 'Unavailable'
          : kind === 'cpu'
            ? `${number(Number(value), 1)}%`
            : bytes(Number(value)),
    },
    dataZoom: [{ type: 'inside', filterMode: 'none' }],
    grid: { left: 14, right: 18, top: 40, bottom: 18, containLabel: true },
    series: fields.map((item) => ({
      name: item.name,
      type: 'line' as const,
      showSymbol: samples.length <= 15,
      symbol: 'rect',
      symbolSize: 4,
      sampling: 'lttb' as const,
      connectNulls: false,
      lineStyle: { width: 1.5 },
      data: samples.flatMap((sample, index) => {
        const datum = [sample.time, sample[item.field]];
        const previous = samples[index - 1];
        return previous && Date.parse(sample.time) - Date.parse(previous.time) > 10000
          ? [[new Date(Date.parse(previous.time) + 5000).toISOString(), null], datum]
          : [datum];
      }),
    })),
  };
}

export default function RuntimeTrends({ samples }: { samples: RuntimeInfo[] }) {
  const cpu = useMemo(() => runtimeTrendOption(samples, 'cpu'), [samples]);
  const memory = useMemo(() => runtimeTrendOption(samples, 'memory'), [samples]);
  return (
    <div className="two-column">
      <Panel
        title="CPU usage"
        subtitle="Five-second process samples · up to one hour"
        action={<Badge>{samples.length} SAMPLES</Badge>}
      >
        {samples.length ? (
          <Chart
            option={cpu}
            group="runtime-history"
            height={235}
            label="Real process CPU usage over time"
          />
        ) : (
          <Empty
            title="No CPU samples yet"
            description="History appears after the runtime monitor samples this process."
          />
        )}
      </Panel>
      <Panel
        title="Go memory"
        subtitle="Go runtime memory and allocated heap · not process RSS"
        action={<Badge>BYTES</Badge>}
      >
        {samples.length ? (
          <Chart
            option={memory}
            group="runtime-history"
            height={235}
            label="Real Go runtime memory and allocated heap over time"
          />
        ) : (
          <Empty
            title="No memory samples yet"
            description="History appears after the runtime monitor samples this process."
          />
        )}
      </Panel>
    </div>
  );
}
