import { describe, expect, it } from 'vitest';
import { normalizeAnalytics, normalizeCache, normalizeDetails } from './wire';
import type { WireAnalytics, WireMetrics } from './wire';

const metrics: WireMetrics = {
  cache_hits: 80,
  cached_bytes: 512,
  coalesced_requests: 10,
  duration_sum_ms: 4000,
  error_rate: 0.02,
  origin_duration_sum_ms: 300,
  origin_errors: 0,
  origin_request_bytes: 32,
  peak_origin_response_bytes_per_second: 1024,
  requests_per_second: 2,
  status_4xx: 0,
  status_5xx: 2,
  requests: 100,
  unique_ips: 42,
  request_errors: 2,
  response_bytes: 1024,
  request_bytes: 64,
  origin_response_bytes: 512,
  origin_attempts: 7,
  cache_hit_rate: 0.8,
  cache_eligible_requests: 100,
  byte_hit_rate: 0.5,
  peak_response_bytes_per_second: 2048,
  response_bytes_per_second: 128,
  origin_response_bytes_per_second: 64,
  latency: { p50_ms: 10, p95_ms: 90, p99_ms: 120 },
  origin_latency: { p50_ms: 8, p95_ms: 80, p99_ms: 110 },
  ttfb: { p50_ms: 2, p95_ms: 15, p99_ms: 25 },
  origin_ttfb: { p50_ms: 3, p95_ms: 20, p99_ms: 30 },
  ttfb_samples: 99,
  origin_ttfb_samples: 7,
};
function wire(upstream = true): WireAnalytics {
  return {
    summary: metrics,
    series: [{ time: '2026-10-16T00:00:00Z', ...metrics }],
    breakdowns: { host: [{ value: 'cdn.example.com', ...metrics }] },
    geo: [{ country: 'US', ...metrics }],
    coverage: {
      base_bucket_seconds: 10,
      data_from: null,
      data_to: null,
      detail_from: '2026-10-02T00:00:00Z',
      rollup_from: '2026-07-18T00:00:00Z',
      queued_events: 0,
      requested_from: '2026-10-16T00:00:00Z',
      requested_to: '2026-10-17T00:00:00Z',
      partial: false,
      upstream_attributable: upstream,
      latency_approximate: false,
      uses_rollups: false,
      unique_ips_available: true,
      interval_seconds: 60,
      dropped: { queue: 1, invalid: 2, expired: 3, storage: 4 },
      ingest_lag_ms: 30,
      geo_country_available: true,
      geo_asn_available: false,
      disk_evicted_before: null,
    },
  };
}
describe('real analytics wire adapter', () => {
  it('converts byte rates to bits without recomputing bucket-edge rates or peak', () => {
    const result = normalizeAnalytics(wire());
    expect(result.summary.peak_bandwidth_bps).toBe(2048 * 8);
    expect(result.series[0].bandwidth_bps).toBe(128 * 8);
    expect(result.summary.hit_ratio).toBe(0.8);
    expect(result.summary.p95_duration_ms).toBe(90);
    expect(result.summary.p95_ttfb_ms).toBe(15);
    expect(result.summary.p95_origin_ttfb_ms).toBe(20);
    expect(result.summary.p95_origin_duration_ms).toBe(80);
    expect(result.series[0].p95_ttfb_ms).toBe(15);
    expect(result.series[0].p95_duration_ms).toBe(90);
    expect(result.summary.unique_ips).toBe(42);
    expect(result.breakdowns.host).toHaveLength(1);
    expect(result.breakdowns.host[0].key).toBe('cdn.example.com');
    expect(result.coverage.dropped_events).toBe(10);
  });
  it('does not present unattributable origin zeros as measured traffic', () => {
    const result = normalizeAnalytics(wire(false));
    expect(result.summary.origin_bytes).toBeUndefined();
    expect(result.summary.origin_requests).toBeUndefined();
    expect(result.series[0].origin_bandwidth_bps).toBeUndefined();
    expect(result.summary.p95_origin_ttfb_ms).toBeUndefined();
    expect(result.summary.p95_origin_duration_ms).toBeUndefined();
    expect(result.series[0].p95_origin_ttfb_ms).toBeUndefined();
    expect(result.coverage.notes?.join()).toContain('Origin metrics unavailable');
  });
  it('keeps empty results honest and ratios undefined without a denominator', () => {
    const input = wire();
    input.series = null;
    input.geo = null;
    input.breakdowns = {};
    input.summary = {
      ...metrics,
      requests: 0,
      unique_ips: 0,
      response_bytes: 0,
      cache_eligible_requests: 0,
      ttfb_samples: 0,
      origin_attempts: 0,
      origin_ttfb_samples: 0,
    };
    const result = normalizeAnalytics(input);
    expect(result.series).toEqual([]);
    expect(result.geo).toEqual([]);
    expect(result.breakdowns).toEqual({});
    expect(result.summary.hit_ratio).toBeUndefined();
    expect(result.summary.byte_hit_ratio).toBeUndefined();
    expect(result.summary.p95_duration_ms).toBeUndefined();
    expect(result.summary.p95_ttfb_ms).toBeUndefined();
    expect(result.summary.p95_origin_ttfb_ms).toBeUndefined();
    expect(result.summary.unique_ips).toBe(0);
  });
  it('maps retained completion timestamps and published cache metadata', () => {
    const detail = normalizeDetails({
      rows: [
        {
          request_id: 'r',
          asn: 0,
          cached_bytes: 128,
          content_type: 'text/html',
          request_bytes: 0,
          site_id: 's',
          started_at: 1792108799995,
          completed_at: 1792108800000,
          host: 'cdn.example.com',
          client_ip: '192.0.2.1',
          path: '/',
          method: 'GET',
          status: 200,
          cache_result: 'HIT',
          response_bytes: 128,
          duration_ms: 5,
          first_byte_ms: 2,
          country: 'US',
        },
      ],
      has_more: true,
      next_offset: 100,
      coverage: wire().coverage,
    });
    expect(detail.entries[0].time).toBe('2026-10-16T00:00:00.000Z');
    expect(detail.truncated).toBe(true);
    expect(detail.entries[0].first_byte_ms).toBe(2);
    expect(
      normalizeCache({
        key: 'k',
        site_id: 's',
        host: 'cdn.example.com',
        path: '/',
        size: 12,
        status: 200,
        stored_at: 'a',
        accessed_at: 'b',
        fresh_until: 'c',
        complete: true,
      }),
    ).toMatchObject({ bytes: 12, created_at: 'a', expires_at: 'c', state: 'COMPLETE' });
  });
  it('never substitutes full duration for missing historical first-byte samples', () => {
    const input = wire();
    const historical = { ...metrics, ttfb_samples: 0, origin_ttfb_samples: 0 };
    input.summary = historical;
    input.series = [{ time: '2026-10-16T00:00:00Z', ...historical }];
    input.breakdowns = { host: [{ value: 'cdn.example.com', ...historical }] };
    const result = normalizeAnalytics(input);
    expect(result.summary.p95_ttfb_ms).toBeUndefined();
    expect(result.summary.p95_origin_ttfb_ms).toBeUndefined();
    expect(result.summary.p95_duration_ms).toBe(90);
    expect(result.series[0].p95_ttfb_ms).toBeUndefined();
    expect(result.series[0].p95_duration_ms).toBe(90);
    expect(result.breakdowns.host[0].p95_ttfb_ms).toBeUndefined();
  });
  it('keeps nullable unique-IP history unavailable rather than zero or a sum of buckets', () => {
    const input = wire();
    input.summary = { ...metrics, unique_ips: null };
    input.coverage.unique_ips_available = false;
    input.coverage.uses_rollups = true;
    const result = normalizeAnalytics(input);
    expect(result.summary.unique_ips).toBeNull();
    expect(result.coverage.unique_ips_available).toBe(false);
    expect(result.coverage.notes?.join()).toContain('Unique IPs unavailable');
  });
  it('shows unavailable rather than zero milliseconds in old request-detail rows', () => {
    const result = normalizeDetails({
      rows: [
        {
          request_id: 'old',
          asn: 0,
          cached_bytes: 12,
          content_type: 'text/html',
          request_bytes: 0,
          site_id: 's',
          started_at: 1792108799980,
          completed_at: 1792108800000,
          host: 'cdn.example.com',
          path: '/',
          client_ip: '192.0.2.1',
          method: 'GET',
          status: 200,
          cache_result: 'HIT',
          response_bytes: 12,
          duration_ms: 20,
          first_byte_ms: 0,
          country: '',
        },
      ],
      has_more: false,
      next_offset: null,
      coverage: wire().coverage,
    });
    expect(result.entries[0].first_byte_ms).toBeUndefined();
    expect(result.entries[0].duration_ms).toBe(20);
  });
});
