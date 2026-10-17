// Generated Go API schemas are the wire source of truth. Keep unit conversion
// and nullable response normalization here; pages consume display-domain types.
import type {
  AnalyticsResult,
  AnalyticsSummary,
  Breakdown,
  CacheEntry,
  Capabilities,
  Coverage,
  RequestDetail,
  ExplainResult,
  RefreshBatch,
  RuntimeStatus,
  SeriesPoint,
} from './types';
import { stages } from './types';
import type { components } from './generated/api';
import { cachePolicy } from './configWire';
import { enumValue } from './lib/narrow';
export { normalizeConfiguration } from './configWire';

type Schema = components['schemas'];
export type WirePercentiles = Schema['Percentiles'];
export type WireMetrics = Schema['Metrics'];
export type WireCoverage = Schema['Coverage'];
export type WireAnalytics = Schema['Result'];
export type WireDetail = Schema['RequestDetail'];
export type WireDetails = Schema['DetailsResult'];
export type WireCacheEntry = Schema['EntryInfo'];
export type WireRuntimeStatus = Schema['RuntimeStatus'];

function summary(metrics: WireMetrics, upstream: boolean): AnalyticsSummary {
  return {
    requests: metrics.requests,
    bytes_sent: metrics.response_bytes,
    bytes_received: metrics.request_bytes,
    origin_bytes: upstream ? metrics.origin_response_bytes : undefined,
    peak_bandwidth_bps: metrics.peak_response_bytes_per_second * 8,
    hit_ratio: metrics.cache_eligible_requests > 0 ? metrics.cache_hit_rate : undefined,
    byte_hit_ratio: metrics.response_bytes > 0 ? metrics.byte_hit_rate : undefined,
    p95_duration_ms: metrics.requests > 0 ? metrics.latency?.p95_ms : undefined,
    p95_ttfb_ms: metrics.ttfb_samples > 0 ? metrics.ttfb.p95_ms : undefined,
    p95_origin_ttfb_ms:
      upstream && metrics.origin_ttfb_samples > 0 ? metrics.origin_ttfb.p95_ms : undefined,
    p95_origin_duration_ms:
      upstream && metrics.origin_attempts > 0 ? metrics.origin_latency.p95_ms : undefined,
    unique_ips: metrics.unique_ips,
    origin_requests: upstream ? metrics.origin_attempts : undefined,
    errors: metrics.request_errors,
  };
}
export function normalizeAnalytics(wire: WireAnalytics): AnalyticsResult {
  const upstream = wire.coverage.upstream_attributable;
  const coverage: Coverage = {
    from: wire.coverage.requested_from,
    to: wire.coverage.requested_to,
    partial: wire.coverage.partial,
    lag_ms: wire.coverage.ingest_lag_ms,
    dropped_events: Object.values(wire.coverage.dropped || {}).reduce(
      (sum, count) => sum + count,
      0,
    ),
    geoip_available: wire.coverage.geo_country_available,
    upstream_attributable: upstream,
    latency_approximate: wire.coverage.latency_approximate,
    interval_seconds: wire.coverage.interval_seconds,
    unique_ips_available: wire.coverage.unique_ips_available,
    notes: [
      ...(!upstream ? ['Origin metrics unavailable for these filters.'] : []),
      ...(wire.coverage.latency_approximate ? ['Approximate historical latency.'] : []),
      ...(wire.coverage.uses_rollups ? ['Includes historical rollups.'] : []),
      ...(!wire.coverage.unique_ips_available
        ? ['Unique IPs unavailable outside detail retention.']
        : []),
      ...(wire.coverage.disk_evicted_before
        ? [`Capacity-limited coverage before ${wire.coverage.disk_evicted_before}.`]
        : []),
    ],
  };
  const series: SeriesPoint[] = (wire.series ?? []).map((point) => ({
    time: point.time,
    requests: point.requests,
    bytes_sent: point.response_bytes,
    origin_bytes: upstream ? point.origin_response_bytes : undefined,
    bandwidth_bps: point.response_bytes_per_second * 8,
    origin_bandwidth_bps: upstream ? point.origin_response_bytes_per_second * 8 : undefined,
    hit_ratio: point.cache_eligible_requests > 0 ? point.cache_hit_rate : undefined,
    p95_duration_ms: point.requests > 0 ? point.latency?.p95_ms : undefined,
    p95_ttfb_ms: point.ttfb_samples > 0 ? point.ttfb.p95_ms : undefined,
    p95_origin_ttfb_ms:
      upstream && point.origin_ttfb_samples > 0 ? point.origin_ttfb.p95_ms : undefined,
    p95_origin_duration_ms:
      upstream && point.origin_attempts > 0 ? point.origin_latency.p95_ms : undefined,
  }));
  const breakdowns: Record<string, Breakdown[]> = Object.fromEntries(
    Object.entries(wire.breakdowns).map(([name, entries]) => [
      name,
      (entries ?? []).map((entry) => ({
        key: entry.value,
        requests: entry.requests,
        bytes_sent: entry.response_bytes,
        hit_ratio: entry.cache_hit_rate,
        p95_duration_ms: entry.latency?.p95_ms,
        p95_ttfb_ms: entry.ttfb_samples > 0 ? entry.ttfb.p95_ms : undefined,
      })),
    ]),
  );
  return {
    summary: summary(wire.summary, upstream),
    series,
    breakdowns,
    geo: (wire.geo ?? []).map((entry) => ({
      country: entry.country,
      requests: entry.requests,
      bytes_sent: entry.response_bytes,
    })),
    coverage,
  };
}

export function normalizeCapabilities(value: Schema['Catalog']): Capabilities {
  const stageList = (values: string[] | null) =>
    (values ?? []).map((value) => enumValue(value, stages, 'catalog stage'));
  return {
    stages: stageList(value.stages),
    schemas: value.schemas,
    fields: (value.fields ?? []).map((field) => ({
      ...field,
      stages: stageList(field.stages),
      operators: field.operators ?? [],
    })),
    actions: (value.actions ?? []).map((action) => ({
      ...action,
      stages: stageList(action.stages),
    })),
  };
}
export function normalizeExplain(value: Schema['ExplainResult']): ExplainResult {
  return {
    ...value,
    cache_policy: cachePolicy(value.cache_policy),
    http_version: enumValue(
      value.http_version,
      ['HTTP/1.0', 'HTTP/1.1', 'HTTP/2.0', 'HTTP/3.0'],
      'client HTTP version',
    ),
    trace: value.trace ?? [],
    response_headers: Object.fromEntries(
      Object.entries(value.response_headers ?? {}).map(([name, values]) => [name, values ?? []]),
    ),
  };
}
export function normalizeRefresh(value: Schema['RefreshBatch']): RefreshBatch {
  return { ...value, results: value.results ?? [] };
}
export function normalizeRuntime(value: WireRuntimeStatus): RuntimeStatus {
  return { ...value, history: value.history ?? [], restart_fields: value.restart_fields ?? [] };
}
export function normalizeDetails(wire: WireDetails): {
  entries: RequestDetail[];
  truncated: boolean;
} {
  return {
    entries: (wire.rows ?? []).map((row) => ({
      id: row.request_id,
      time: new Date(row.completed_at).toISOString(),
      host: row.host,
      path: row.path,
      client_ip: row.client_ip,
      method: row.method,
      status: row.status,
      cache_result: row.cache_result,
      bytes_sent: row.response_bytes,
      duration_ms: row.duration_ms,
      first_byte_ms: row.first_byte_ms > 0 ? row.first_byte_ms : undefined,
      country: row.country,
    })),
    truncated: wire.has_more,
  };
}
export function normalizeCache(entry: WireCacheEntry): CacheEntry {
  return {
    key: entry.key,
    site_id: entry.site_id,
    host: entry.host,
    path: entry.path,
    status: entry.status,
    bytes: entry.size,
    created_at: entry.stored_at,
    last_access: entry.accessed_at,
    expires_at: entry.fresh_until,
    state: entry.complete ? 'COMPLETE' : 'PARTIAL',
  };
}
