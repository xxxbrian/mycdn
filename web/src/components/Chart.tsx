import { useEffect, useRef } from 'react';
import * as echarts from 'echarts/core';
import { BarChart, LineChart, MapChart } from 'echarts/charts';
import type { BarSeriesOption, LineSeriesOption, MapSeriesOption } from 'echarts/charts';
import {
  AriaComponent,
  DataZoomComponent,
  GeoComponent,
  GridComponent,
  LegendComponent,
  TooltipComponent,
  VisualMapComponent,
} from 'echarts/components';
import type {
  AriaComponentOption,
  DataZoomComponentOption,
  GeoComponentOption,
  GridComponentOption,
  LegendComponentOption,
  TooltipComponentOption,
  VisualMapComponentOption,
} from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';

echarts.use([
  BarChart,
  LineChart,
  MapChart,
  AriaComponent,
  DataZoomComponent,
  GeoComponent,
  GridComponent,
  LegendComponent,
  TooltipComponent,
  VisualMapComponent,
  CanvasRenderer,
]);
export type ChartOption = echarts.ComposeOption<
  | BarSeriesOption
  | LineSeriesOption
  | MapSeriesOption
  | AriaComponentOption
  | DataZoomComponentOption
  | GeoComponentOption
  | GridComponentOption
  | LegendComponentOption
  | TooltipComponentOption
  | VisualMapComponentOption
>;
export type ChartClick = { name: string; value?: unknown; data?: unknown };
export const palette = [
  '#ff5a36',
  '#8fa9bb',
  '#b5bd83',
  '#b49bd2',
  '#d2ae71',
  '#6caaa3',
  '#777d86',
];
export const chartBase: ChartOption = {
  animationDuration: 180,
  textStyle: {
    fontFamily: '"IBM Plex Mono", "SFMono-Regular", Consolas, monospace',
    fontSize: 11,
    color: '#9298a1',
  },
  color: palette,
  aria: { enabled: true },
  tooltip: {
    trigger: 'axis',
    renderMode: 'richText',
    backgroundColor: '#17191c',
    borderColor: '#383c42',
    borderWidth: 1,
    padding: 12,
    textStyle: { color: '#e7e9ed', fontSize: 11 },
    extraCssText: 'border-radius:0;box-shadow:none;',
    borderRadius: 0,
    shadowBlur: 0,
    shadowOffsetX: 0,
    shadowOffsetY: 0,
    order: 'seriesAsc',
    confine: true,
  },
  grid: { left: 14, right: 22, top: 44, bottom: 46, containLabel: true },
  legend: {
    top: 0,
    left: 8,
    itemWidth: 12,
    itemHeight: 2,
    textStyle: { color: '#9298a1', fontSize: 11 },
    icon: 'rect',
  },
};
export function Chart({
  option,
  height = 280,
  group,
  onClick,
  onZoom,
  zoomKey,
  label,
}: {
  option: ChartOption;
  height?: number;
  group?: string;
  onClick?: (event: ChartClick) => void;
  onZoom?: (start: number, end: number) => void;
  zoomKey?: string;
  label: string;
}) {
  const element = useRef<HTMLDivElement>(null);
  const instance = useRef<echarts.EChartsType | undefined>(undefined);
  const click = useRef(onClick);
  click.current = onClick;
  const zoom = useRef(onZoom);
  zoom.current = onZoom;
  const zoomSelection = useRef(zoomKey);
  zoomSelection.current = zoomKey;
  useEffect(() => {
    if (!element.current) return;
    const chart = echarts.init(element.current, undefined, { renderer: 'canvas' });
    instance.current = chart;
    let zoomTimer: ReturnType<typeof setTimeout> | undefined;
    if (group) {
      chart.group = group;
      echarts.connect(group);
    }
    chart.on('click', (params) => {
      const event = params as unknown as ChartClick;
      click.current?.(event);
    });
    chart.on('datazoom', (params) => {
      const event = params as {
        start?: number;
        end?: number;
        batch?: { start?: number; end?: number }[];
      };
      const range = event.batch?.[0] || event;
      if (
        typeof range.start === 'number' &&
        typeof range.end === 'number' &&
        range.start >= 0 &&
        range.end <= 100 &&
        range.end > range.start
      ) {
        clearTimeout(zoomTimer);
        const start = range.start;
        const end = range.end;
        const selection = zoomSelection.current;
        zoomTimer = setTimeout(() => {
          if (zoomSelection.current === selection) zoom.current?.(start, end);
        }, 400);
      }
    });
    const observer = new ResizeObserver(() => chart.resize());
    observer.observe(element.current);
    return () => {
      observer.disconnect();
      clearTimeout(zoomTimer);
      chart.dispose();
      instance.current = undefined;
    };
  }, [group]);
  useEffect(() => {
    // Merge series without resetting an in-progress view. Time zoom is controlled
    // explicitly by the analytics window, while map pan/zoom stays local.
    instance.current?.setOption(
      {
        ...chartBase,
        ...option,
        animation: !window.matchMedia('(prefers-reduced-motion: reduce)').matches,
        tooltip: { ...chartBase.tooltip, ...option.tooltip },
      },
      { replaceMerge: ['series'], silent: true },
    );
  }, [option, group]);
  return <div ref={element} className="chart" style={{ height }} role="img" aria-label={label} />;
}
export { echarts };
