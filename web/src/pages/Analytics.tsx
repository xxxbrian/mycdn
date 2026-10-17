import { lazy, Suspense, useMemo, useState } from 'react';
import {
  Activity,
  ArrowDownToLine,
  ArrowUpRight,
  ChartNoAxesCombined,
  Clock3,
  Gauge,
  RefreshCw,
  RotateCcw,
  X,
} from 'lucide-react';
import { api } from '../api';
import { useAsync } from '../lib/useAsync';
import { useNavigation, useReadingPosition } from '../lib/navigation';
import {
  detailRangeAvailable,
  filterKeys,
  filterLabels,
  queryFromParams,
  resetTimeZoom,
  timeWindowFromParams,
  zoomWindow,
} from '../lib/analytics';
import type { TimeWindow } from '../lib/analytics';
import type { FilterKey } from '../lib/analytics';
import { bandwidth, bytes, dateTime, milliseconds, number, percent } from '../lib/format';
import { countryName } from '../lib/world';
import {
  Badge,
  Button,
  Empty,
  Loading,
  LoadError,
  Metric,
  PageHeading,
  Panel,
  Tabs,
} from '../components/ui';
import AnalyticsFilters from '../components/AnalyticsFilters';
import { Chart, palette } from '../components/Chart';
import type { ChartOption } from '../components/Chart';
import type { AnalyticsQuery, Breakdown, SeriesPoint } from '../types';
import Runtime from './Runtime';

const WorldMap = lazy(() => import('../components/WorldMap'));
const tabs = ['overview', 'traffic', 'cache', 'visitors', 'runtime'].map((id) => ({
  id,
  label: id[0].toUpperCase() + id.slice(1),
}));
const axisStyle = {
  axisLabel: { color: '#858c96', fontSize: 10 },
  axisLine: { lineStyle: { color: '#33373d' } },
  splitLine: { lineStyle: { color: '#24282e', type: 'dashed' as const } },
  axisTick: { show: false },
};
type AnalyticsView = {
  query: AnalyticsQuery;
  window: TimeWindow;
  detailFrom?: string;
};

function TimeSeries({
  points,
  kind,
  interval,
  from,
  to,
  window,
  group,
}: {
  points: SeriesPoint[];
  kind: 'requests' | 'traffic' | 'latency' | 'hits';
  interval: number;
  from: string;
  to: string;
  window: TimeWindow;
  group: string;
}) {
  const { params, update } = useNavigation();
  function zoomRange(start: number, end: number) {
    if (start < 0.01 && end > 99.99) {
      update(resetTimeZoom);
      return;
    }
    const selected = zoomWindow(window, start, end);
    if (
      !selected ||
      (selected.from === params.get('zoom_from') && selected.to === params.get('zoom_to'))
    )
      return;
    update({
      zoom_from: selected.from,
      zoom_to: selected.to,
      zoom_base_from: window.from,
      zoom_base_to: window.to,
    });
  }
  const config = useMemo(() => {
    const series: {
      name: string;
      field: keyof SeriesPoint;
      convert?: (value: number) => number;
    }[] =
      kind === 'traffic'
        ? [
            { name: 'Delivered', field: 'bandwidth_bps' },
            { name: 'Origin', field: 'origin_bandwidth_bps' },
          ]
        : kind === 'latency'
          ? [
              { name: 'p95 first response', field: 'p95_ttfb_ms' },
              { name: 'p95 full duration', field: 'p95_duration_ms' },
            ]
          : kind === 'hits'
            ? [{ name: 'Cache hit ratio', field: 'hit_ratio', convert: (value) => value * 100 }]
            : [{ name: 'Requests', field: 'requests' }];
    const visibleSeries = series.filter((item) =>
      points.some((point) => typeof point[item.field] === 'number'),
    );
    const zoom = {
      startValue: Date.parse(from),
      endValue: Date.parse(to),
      rangeMode: ['value', 'value'] as ['value', 'value'],
    };
    return {
      series: visibleSeries,
      option: {
        xAxis: {
          type: 'time',
          min: window.from,
          max: window.to,
          ...axisStyle,
          splitLine: { show: false },
          axisLabel: { ...axisStyle.axisLabel, hideOverlap: true },
        },
        yAxis: {
          type: 'value',
          ...axisStyle,
          min: 0,
          ...(kind === 'hits' ? { max: 100 } : {}),
          axisLabel: {
            ...axisStyle.axisLabel,
            formatter: (value: number) =>
              kind === 'traffic'
                ? bandwidth(value)
                : kind === 'hits'
                  ? `${value}%`
                  : kind === 'latency'
                    ? `${number(value)}ms`
                    : number(value),
          },
        },
        tooltip: {
          trigger: 'axis',
          valueFormatter: (value) => {
            if (value === null || value === undefined || value === '-') return 'Unavailable';
            const numeric = Number(value);
            return kind === 'traffic'
              ? bandwidth(numeric)
              : kind === 'hits'
                ? `${number(numeric, 1)}%`
                : kind === 'latency'
                  ? milliseconds(numeric)
                  : `${number(numeric)} requests`;
          },
        },
        dataZoom: [
          { id: 'time-inside', type: 'inside', filterMode: 'none', throttle: 250, ...zoom },
          {
            id: 'time-slider',
            type: 'slider',
            ...zoom,
            realtime: false,
            height: 16,
            bottom: 2,
            borderColor: '#33373d',
            backgroundColor: 'transparent',
            fillerColor: '#ff5a3612',
            handleStyle: { color: '#9298a1', borderColor: '#9298a1' },
            handleIcon: 'path://M0,0L0,10L3,10L3,0Z',
            moveHandleIcon: 'path://M0,0L10,0L10,2L0,2Z',
            moveHandleStyle: { color: '#4b515a', shadowBlur: 0 },
            dataBackground: { lineStyle: { color: '#535962' }, areaStyle: { color: '#282d34' } },
            textStyle: { color: '#858c96', fontSize: 9 },
            brushSelect: false,
          },
        ],
        series: visibleSeries.map((item, i) => ({
          id: item.name,
          type: 'line' as const,
          name: item.name,
          showSymbol: points.length <= 30,
          symbolSize: 4,
          symbol: 'rect',
          sampling: 'lttb' as const,
          connectNulls: false,
          lineStyle: { width: i === 0 ? 1.8 : 1.2 },
          areaStyle: kind === 'traffic' && i === 0 ? { opacity: 0.05 } : undefined,
          data: points.flatMap((point, index) => {
            const value = point[item.field];
            const datum = [
              point.time,
              typeof value === 'number' ? (item.convert ? item.convert(value) : value) : null,
            ];
            const previous = points[index - 1];
            return previous && Date.parse(point.time) - Date.parse(previous.time) > interval * 1500
              ? [[new Date(Date.parse(previous.time) + interval * 1000).toISOString(), null], datum]
              : [datum];
          }),
        })),
      } satisfies ChartOption,
    };
  }, [points, kind, interval, from, to, window]);
  const empty =
    !points.length ||
    !config.series.some((series) =>
      points.some((point) => typeof point[series.field] === 'number'),
    );
  return (
    <div className="analytics-chart-slot">
      <Chart
        option={config.option}
        group={group}
        onZoom={zoomRange}
        zoomKey={`${params.toString()}|${window.from}|${window.to}`}
        label={`${kind === 'latency' ? 'p95 first response and p95 full duration' : kind} over time; zoom linked to other trends`}
      />
      {empty && <div className="analytics-chart-empty">No observations in this selection</div>}
    </div>
  );
}
function Distribution({
  entries,
  onSelect,
  label,
}: {
  entries: Breakdown[];
  onSelect: (key: string) => void;
  label: string;
}) {
  const option = useMemo<ChartOption>(
    () => ({
      grid: { left: 10, right: 45, top: 8, bottom: 8, containLabel: true },
      legend: { show: false },
      tooltip: { trigger: 'item', valueFormatter: (value) => `${number(Number(value))} requests` },
      xAxis: { type: 'value', show: false },
      yAxis: {
        type: 'category',
        inverse: true,
        data: entries.map((item) => item.key || 'Unknown'),
        axisLine: { show: false },
        axisTick: { show: false },
        axisLabel: { color: '#a3a9b2', fontSize: 10 },
      },
      series: [
        {
          id: label,
          type: 'bar',
          barMaxWidth: 13,
          data: entries.map((item, i) => ({
            name: item.key,
            value: item.requests,
            itemStyle: { color: palette[i % palette.length] },
          })),
          label: {
            show: true,
            position: 'right',
            color: '#b8bec8',
            fontSize: 10,
            formatter: (params) => number(Number(params.value)),
          },
        },
      ],
    }),
    [entries],
  );
  return (
    <>
      <div className="analytics-chart-slot">
        <Chart
          option={option}
          height={220}
          label={label}
          onClick={(event) => onSelect(event.name)}
        />
        {!entries.length && (
          <div className="analytics-chart-empty">No observations in this selection</div>
        )}
      </div>
      <div className="distribution-filters" aria-label={`${label} filters`}>
        {entries.map((entry) => (
          <Button key={entry.key} disabled={!entry.key} onClick={() => onSelect(entry.key)}>
            {entry.key || 'Unknown'}
            <span className="mono">{number(entry.requests)}</span>
          </Button>
        ))}
      </div>
    </>
  );
}
function Ranking({
  title,
  entries,
  onSelect,
  countries = false,
  unavailable,
}: {
  title: string;
  entries: Breakdown[];
  onSelect: (key: string) => void;
  countries?: boolean;
  unavailable?: string;
}) {
  const max = Math.max(1, ...entries.map((entry) => entry.requests));
  return (
    <Panel
      title={title}
      subtitle={
        unavailable
          ? 'Available within retained detail only'
          : 'Ranked by delivered traffic · select to filter'
      }
      action={<Badge>{unavailable ? 'UNAVAILABLE' : `TOP ${Math.min(20, entries.length)}`}</Badge>}
      className="ranking-panel"
    >
      <div className="analytics-ranking-viewport">
        {unavailable ? (
          <Empty title="Outside retained detail" description={unavailable} />
        ) : !entries.length ? (
          <Empty title="No ranked observations" />
        ) : (
          <div className="table-wrap">
            <table className="ranking-table">
              <thead>
                <tr>
                  <th>{countries ? 'Country' : 'Dimension'}</th>
                  <th className="numeric">Requests</th>
                  <th className="numeric">Traffic</th>
                </tr>
              </thead>
              <tbody>
                {entries.slice(0, 20).map((entry) => (
                  <tr key={entry.key}>
                    <td>
                      <button
                        type="button"
                        className="ranking-key"
                        disabled={!entry.key}
                        onClick={() => onSelect(entry.key)}
                        title={`Filter by ${entry.key}`}
                      >
                        <span
                          className="rank-bar"
                          style={{ width: `${(entry.requests / max) * 100}%` }}
                        />
                        <span className="truncate">
                          {countries ? countryName(entry.key) : entry.key || 'Unknown'}
                        </span>
                        <ArrowUpRight size={11} />
                      </button>
                    </td>
                    <td className="numeric mono">{number(entry.requests)}</td>
                    <td className="numeric mono muted">{bytes(entry.bytes_sent)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </Panel>
  );
}
function Details({ query, unavailable }: { query: AnalyticsQuery; unavailable?: string }) {
  const result = useAsync(
    (signal) => api.details({ ...query, limit: 100 }, signal),
    JSON.stringify(query),
    0,
    !unavailable,
    true,
  );
  return (
    <Panel
      title="Request explorer"
      subtitle="Completed request records · up to 100 results · no request bodies or sensitive headers"
      action={
        <Button onClick={result.reload}>
          <RefreshCw size={13} />
          Reload
        </Button>
      }
    >
      <div
        className={`analytics-details-viewport ${result.loading || result.error ? 'is-updating' : ''}`}
        aria-busy={result.loading}
      >
        {result.loading && !unavailable && (
          <span className="analytics-local-status" role="status">
            Updating records…
          </span>
        )}
        {result.error && !unavailable && <LoadError error={result.error} retry={result.reload} />}
        {unavailable ? (
          <Empty title="Request details unavailable" description={unavailable} />
        ) : result.loading && !result.data ? (
          <Loading />
        ) : !result.data?.entries?.length ? (
          <Empty
            title="No request details"
            description="No retained requests match the shared filters."
          />
        ) : (
          <>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    {[
                      'Time',
                      'Client IP',
                      'Method / host / path',
                      'Status',
                      'Cache',
                      'Traffic',
                      'First response',
                      'Full duration',
                    ].map((label) => (
                      <th key={label}>{label}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {result.data.entries.map((entry, i) => (
                    <tr key={entry.id || i}>
                      <td className="mono nowrap muted">{dateTime(entry.time)}</td>
                      <td className="mono">{entry.client_ip}</td>
                      <td>
                        <span className="mono text-small">
                          {entry.method} {entry.host}
                        </span>
                        <div className="muted truncate path-cell">{entry.path}</div>
                      </td>
                      <td>
                        <Badge tone={entry.status >= 500 ? 'error' : ''}>{entry.status}</Badge>
                      </td>
                      <td className="mono text-small">{entry.cache_result}</td>
                      <td className="mono nowrap">{bytes(entry.bytes_sent)}</td>
                      <td className="mono nowrap">{milliseconds(entry.first_byte_ms)}</td>
                      <td className="mono nowrap">{milliseconds(entry.duration_ms)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {result.data.truncated && (
              <p className="footnote">
                Result limit reached. Narrow the filters to inspect more precise records.
              </p>
            )}
          </>
        )}
      </div>
    </Panel>
  );
}
function QuerySummary({
  query,
  updating,
  error,
  retry,
}: {
  query?: AnalyticsQuery;
  updating: boolean;
  error?: string;
  retry: () => void;
}) {
  const { params, update } = useNavigation();
  const active = filterKeys.filter((key) => params.get(key));
  return (
    <div className="analytics-query-summary" aria-label="Current analysis selection">
      <div className="analytics-query-range">
        <span className={`square-dot ${error ? 'error-dot' : ''}`} />
        <span className="mono">
          {query ? `${dateTime(query.from)} — ${dateTime(query.to)}` : 'Select a valid range'}
        </span>
        <Button
          disabled={!params.has('zoom_from')}
          onClick={() => update(resetTimeZoom)}
          aria-label="Reset time zoom"
        >
          <RotateCcw size={12} />
          Restore range
        </Button>
        <span className="analytics-query-status" role="status" title={error}>
          {updating ? (
            <>
              <RefreshCw size={12} className="spin" />
              Updating · previous results
            </>
          ) : error ? (
            'Update failed · previous results'
          ) : (
            'Up to date'
          )}
        </span>
        {error && <Button onClick={retry}>Retry</Button>}
      </div>
      <div className="analytics-query-filters">
        {active.length === 0 && <span className="muted">All requests</span>}
        {active.map((key) => (
          <Button
            key={key}
            onClick={() => update({ [key]: null })}
            aria-label={`Remove ${filterLabels[key]} filter`}
          >
            <span>{filterLabels[key]}:</span>
            <strong className="mono">{params.get(key)}</strong>
            <X size={12} />
          </Button>
        ))}
      </div>
    </div>
  );
}
export default function Analytics() {
  const { params, update } = useNavigation();
  const tab = tabs.some((item) => item.id === params.get('tab')) ? params.get('tab')! : 'overview';
  const [revision, setRevision] = useState(0);
  // The live collector horizon is authoritative; saved retention settings may
  // still be waiting for a restart. Wait for it before issuing the one query.
  const retention = useAsync(
    (signal) => api.status(signal),
    `analytics-retention-${revision}`,
    0,
    true,
    true,
  );
  const detailFrom = retention.data?.analytics_status?.detail_from;
  const queryKey = JSON.stringify(
    [...params.entries()].filter(([key]) => !['page', 'tab'].includes(key)).sort(),
  );
  const selection = useMemo(() => {
    try {
      const now = new Date();
      return {
        view: {
          query: queryFromParams(params, now, detailFrom),
          window: timeWindowFromParams(params, now),
          detailFrom,
        } satisfies AnalyticsView,
      };
    } catch (error) {
      return { error: error instanceof Error ? error.message : String(error) };
    }
  }, [queryKey, revision, detailFrom]);
  const result = useAsync(
    async (signal) => {
      const view = selection.view!;
      const data = await api.analytics(view.query, signal);
      return { ...view, data };
    },
    JSON.stringify([selection, revision]),
    0,
    tab !== 'runtime' && !!selection.view && !!retention.data,
    true,
  );
  // A displayed result always carries the exact query that produced it. Pending
  // controls may change immediately without relabeling the previous chart data.
  const displayed = result.data;
  const data = displayed?.data;
  const updating = tab !== 'runtime' && (result.loading || retention.loading);
  const error = selection.error || retention.error || result.error;
  const retry = retention.error ? retention.reload : result.reload;
  useReadingPosition(!updating);
  const noTraffic = data && data.summary.requests === 0 && data.summary.bytes_sent === 0;
  const select = (key: FilterKey) => (value: string) => update({ [key]: value });
  const breakdown = (name: string) => data?.breakdowns?.[name] || [];
  const detailsAvailable =
    !!displayed && detailRangeAvailable(displayed.query.from, displayed.detailFrom);
  const detailUnavailable = detailsAvailable
    ? undefined
    : displayed?.detailFrom
      ? `Choose a range starting ${dateTime(displayed.detailFrom)} or later.`
      : 'The running collector did not report a retained-detail horizon.';
  return (
    <>
      <PageHeading
        title="Analytics"
        eyebrow="OBSERVABILITY / TRAFFIC"
        description="Request volume, transfer rates, cache results, and response timing."
        action={
          <Button onClick={() => setRevision((r) => r + 1)} disabled={updating}>
            <RefreshCw size={14} className={updating ? 'spin' : ''} />
            Refresh
          </Button>
        }
      />
      <Tabs
        items={tabs}
        value={tab}
        onChange={(id) => update({ tab: id })}
        label="Analytics views"
      />
      <AnalyticsFilters />
      {tab !== 'runtime' && (
        <QuerySummary
          query={selection.view?.query}
          updating={updating}
          error={error}
          retry={retry}
        />
      )}
      {tab === 'runtime' ? (
        <Runtime embedded />
      ) : (
        <>
          {error && !data && (
            <div className="analytics-query-error">
              <LoadError error={error} retry={retry} />
            </div>
          )}
          {updating && !data ? (
            <div className="analytics-loading">
              <Loading
                label={
                  retention.loading ? 'Reading analytics retention…' : 'Querying local analytics…'
                }
              />
              <div className="metric-grid skeleton-grid">
                {Array.from({ length: 5 }, (_, i) => (
                  <div className="metric skeleton" key={i} />
                ))}
              </div>
            </div>
          ) : (
            data && (
              <div
                key="results"
                className={`analytics-results ${updating || error ? 'is-updating' : ''}`}
                aria-busy={updating}
              >
                <div className="coverage-line">
                  <span>
                    <span className="square-dot" />
                    {data.coverage?.partial ? 'PARTIAL COVERAGE' : 'RECORDED OBSERVATIONS'}
                  </span>
                  <span className="mono">
                    {dateTime(displayed!.query.from)} — {dateTime(displayed!.query.to)}
                  </span>
                  <span>
                    bucket{' '}
                    {number(data.coverage.interval_seconds || displayed!.query.interval_seconds)}s
                  </span>
                  {!!data.coverage.notes?.length && (
                    <span
                      className="badge"
                      tabIndex={0}
                      title={data.coverage.notes.join(' ')}
                      aria-label={data.coverage.notes.join(' ')}
                    >
                      DATA NOTES
                    </span>
                  )}
                </div>
                <div className="metric-grid">
                  <Metric
                    label="Total requests"
                    value={number(data.summary.requests)}
                    note="Completed request outcomes"
                    icon={<Activity size={14} />}
                    accent
                  />
                  <Metric
                    label="Delivered traffic"
                    value={bytes(data.summary.bytes_sent)}
                    note={
                      data.coverage.upstream_attributable
                        ? `Origin ${bytes(data.summary.origin_bytes)}`
                        : 'Origin unavailable for these filters'
                    }
                    icon={<ArrowDownToLine size={14} />}
                  />
                  <Metric
                    label="Peak bandwidth"
                    value={bandwidth(data.summary.peak_bandwidth_bps)}
                    note="10-second base traffic buckets"
                    icon={<ChartNoAxesCombined size={14} />}
                  />
                  <Metric
                    label="Cache hit ratio"
                    value={percent(data.summary.hit_ratio)}
                    note={`Byte hit ratio ${percent(data.summary.byte_hit_ratio)}`}
                    icon={<Gauge size={14} />}
                  />
                  <Metric
                    label="p95 first response"
                    value={milliseconds(data.summary.p95_ttfb_ms)}
                    note={`Full duration p95 ${milliseconds(data.summary.p95_duration_ms)}`}
                    icon={<Clock3 size={14} />}
                  />
                </div>
                <div className="analytics-notice" role="status">
                  {noTraffic
                    ? 'No traffic in this selection. Try a wider time range or remove a filter.'
                    : data.coverage.partial ||
                        !!data.coverage.dropped_events ||
                        (data.coverage.lag_ms || 0) > 10000
                      ? `Partial coverage · lag ${milliseconds(data.coverage.lag_ms)} · dropped events ${number(data.coverage.dropped_events)}.`
                      : ''}
                </div>
                {tab === 'overview' && (
                  <>
                    <div className="two-column trends-grid">
                      <Panel
                        title="Request volume"
                        subtitle="Completion time · zoom applies to all views"
                        action={<Badge>REQUESTS</Badge>}
                      >
                        <TimeSeries
                          points={data.series || []}
                          kind="requests"
                          interval={displayed!.query.interval_seconds!}
                          from={displayed!.query.from}
                          to={displayed!.query.to}
                          window={displayed!.window}
                          group="overview"
                        />
                      </Panel>
                      <Panel
                        title="Traffic throughput"
                        subtitle="Actual byte transfer · shared-range zoom"
                        action={<Badge>BIT / S</Badge>}
                      >
                        <TimeSeries
                          points={data.series || []}
                          kind="traffic"
                          interval={displayed!.query.interval_seconds!}
                          from={displayed!.query.from}
                          to={displayed!.query.to}
                          window={displayed!.window}
                          group="overview"
                        />
                      </Panel>
                    </div>
                    <div className="two-column">
                      <Panel title="Cache decisions" subtitle="Request outcomes · select to filter">
                        <Distribution
                          label="Cache results"
                          entries={breakdown('cache_result')}
                          onSelect={select('cache')}
                        />
                      </Panel>
                      <Panel
                        title="Response status"
                        subtitle="Select a status to inspect matching requests"
                      >
                        <Distribution
                          label="HTTP statuses"
                          entries={breakdown('status')}
                          onSelect={select('status')}
                        />
                      </Panel>
                    </div>
                    <div className="geo-grid">
                      <Panel
                        title="Geographic distribution"
                        subtitle="Requests by country · select to filter"
                        action={<Badge>WORLD</Badge>}
                      >
                        <Suspense fallback={<Loading />}>
                          <WorldMap
                            points={data.geo || []}
                            onCountry={select('country')}
                            available={data.coverage?.geoip_available}
                          />
                        </Suspense>
                      </Panel>
                      <Ranking
                        title="Countries"
                        entries={breakdown('country')}
                        onSelect={select('country')}
                        countries
                      />
                    </div>
                    <div className="three-column">
                      <Ranking
                        title="Client IPs"
                        entries={breakdown('client_ip')}
                        onSelect={select('ip')}
                        unavailable={detailUnavailable}
                      />
                      <Ranking
                        title="Hosts"
                        entries={breakdown('host')}
                        onSelect={select('host')}
                      />
                      <Ranking
                        title="Paths"
                        entries={breakdown('path')}
                        onSelect={select('path')}
                        unavailable={detailUnavailable}
                      />
                    </div>
                  </>
                )}
                {tab === 'traffic' && (
                  <>
                    <Panel
                      title="Delivered vs. upstream traffic"
                      subtitle="Transfer deltas throughout downloads, not only at completion"
                    >
                      <TimeSeries
                        points={data.series || []}
                        kind="traffic"
                        interval={displayed!.query.interval_seconds!}
                        from={displayed!.query.from}
                        to={displayed!.query.to}
                        window={displayed!.window}
                        group="traffic"
                      />
                    </Panel>
                    <div className="three-column">
                      <Ranking
                        title="Hosts"
                        entries={breakdown('host')}
                        onSelect={select('host')}
                      />
                      <Ranking
                        title="Paths"
                        entries={breakdown('path')}
                        onSelect={select('path')}
                        unavailable={detailUnavailable}
                      />
                      <Ranking
                        title="Content types"
                        entries={breakdown('content_type')}
                        onSelect={select('type')}
                      />
                    </div>
                    <div className="two-column">
                      <Ranking
                        title="Methods"
                        entries={breakdown('method')}
                        onSelect={select('method')}
                      />
                      <Panel
                        title="Response timing"
                        subtitle="p95 first response (TTFB) vs. full duration · separate distributions"
                      >
                        <TimeSeries
                          points={data.series || []}
                          kind="latency"
                          interval={displayed!.query.interval_seconds!}
                          from={displayed!.query.from}
                          to={displayed!.query.to}
                          window={displayed!.window}
                          group="traffic"
                        />
                        <p className="footnote">
                          Origin p95 first response: {milliseconds(data.summary.p95_origin_ttfb_ms)}{' '}
                          · full duration: {milliseconds(data.summary.p95_origin_duration_ms)}.
                        </p>
                      </Panel>
                    </div>
                  </>
                )}
                {tab === 'cache' && (
                  <>
                    <p
                      className="footnote"
                      title="HIT, STALE, and REVALIDATED count as cache hits. COALESCED joins an active fill; BYPASS is excluded from the hit ratio."
                    >
                      Hit ratio excludes bypassed requests.
                    </p>
                    <div className="two-column">
                      <Panel title="Cache efficiency over time">
                        <TimeSeries
                          points={data.series || []}
                          kind="hits"
                          interval={displayed!.query.interval_seconds!}
                          from={displayed!.query.from}
                          to={displayed!.query.to}
                          window={displayed!.window}
                          group="cache"
                        />
                      </Panel>
                      <Panel title="Cache result distribution">
                        <Distribution
                          label="Cache results"
                          entries={breakdown('cache_result')}
                          onSelect={select('cache')}
                        />
                      </Panel>
                    </div>
                    <div className="three-column">
                      <Ranking
                        title="Paths"
                        entries={breakdown('path')}
                        onSelect={select('path')}
                        unavailable={detailUnavailable}
                      />
                      <Ranking
                        title="Content types"
                        entries={breakdown('content_type')}
                        onSelect={select('type')}
                      />
                      <Ranking
                        title="Hosts"
                        entries={breakdown('host')}
                        onSelect={select('host')}
                      />
                    </div>
                  </>
                )}
                {tab === 'visitors' && (
                  <>
                    <div className="metric-grid compact-metrics">
                      <Metric
                        label="Unique client IPs"
                        value={number(data.summary.unique_ips ?? undefined)}
                        note={
                          data.summary.unique_ips == null
                            ? 'Unavailable outside detail retention'
                            : 'Exact distinct IPs on completed requests'
                        }
                      />
                      <Metric
                        label="Origin attempts"
                        value={number(data.summary.origin_requests)}
                        note="Shared fill cost counted once"
                      />
                      <Metric
                        label="Errors"
                        value={number(data.summary.errors)}
                        note="Observed failures"
                      />
                    </div>
                    <div className="geo-grid">
                      <Panel
                        title="Where requests originate"
                        subtitle="Country distribution from the local GeoIP database"
                      >
                        <Suspense fallback={<Loading />}>
                          <WorldMap
                            points={data.geo || []}
                            onCountry={select('country')}
                            available={data.coverage?.geoip_available}
                          />
                        </Suspense>
                      </Panel>
                      <Ranking
                        title="Countries"
                        entries={breakdown('country')}
                        onSelect={select('country')}
                        countries
                      />
                    </div>
                    <div className="two-column">
                      <Ranking
                        title="Client IPs"
                        entries={breakdown('client_ip')}
                        onSelect={select('ip')}
                        unavailable={detailUnavailable}
                      />
                      <Ranking
                        title="Autonomous systems"
                        entries={breakdown('asn')}
                        onSelect={select('asn')}
                      />
                    </div>
                  </>
                )}
                <Details
                  query={displayed!.query}
                  unavailable={detailsAvailable ? undefined : detailUnavailable}
                />
              </div>
            )
          )}
        </>
      )}
    </>
  );
}
