import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError } from './api';
import { normalizeConfiguration } from './wire';
import type { Configuration } from './types';
import { configurationFixture, runtimeFixture } from './test/fixtures';
import { withResponseStatus } from './lib/explain';
import { queryFromParams } from './lib/analytics';

afterEach(() => vi.unstubAllGlobals());
const wireConfig = configurationFixture();
const config = normalizeConfiguration(wireConfig);
describe('whole-document management boundary', () => {
  it('sends exact If-Match and preserves explicit false/zero patches', async () => {
    const value = {
      ...config,
      defaults: { ...config.defaults, request: { allow_upgrade: false, max_upload_bytes: 0 } },
    };
    const fetch = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify(value), { headers: { ETag: '"next"' } }));
    vi.stubGlobal('fetch', fetch);
    const result = await api.saveConfig(value, '"current"');
    const [path, init] = fetch.mock.calls[0];
    expect(path).toBe('/api/config');
    expect(init.headers['If-Match']).toBe('"current"');
    expect(JSON.parse(init.body).defaults.request).toEqual({
      allow_upgrade: false,
      max_upload_bytes: 0,
    });
    expect(result.etag).toBe('"next"');
  });
  it('rejects conflicts rather than overwriting the server document', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(
          new Response(JSON.stringify({ error: 'etag mismatch' }), { status: 412 }),
        ),
    );
    await expect(api.saveConfig(config, '"old"')).rejects.toMatchObject({
      status: 412,
      message: 'etag mismatch',
    });
  });
  it('retains field paths from structured validation errors', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            detail: 'Invalid configuration',
            errors: [{ location: 'body.sites[0].domains', message: 'invalid domain' }],
          }),
          { status: 422 },
        ),
      ),
    );
    await expect(api.saveConfig(config, '"v"')).rejects.toMatchObject({
      issues: [{ path: 'body.sites[0].domains', message: 'invalid domain' }],
    });
  });
  it('does not turn an HTML fallback into fake empty analytics', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response('<!doctype html><html>SPA</html>')),
    );
    await expect(api.status()).rejects.toBeInstanceOf(ApiError);
  });
  it('sends the current budgets, ordered identity, h2c and explicit false/zero without retired fields', async () => {
    const value: Configuration = {
      ...config,
      defaults: {
        ...config.defaults,
        identity: { client_ip_source: 'peer', scheme_source: 'connection', client_ip_headers: [] },
        upstream: {
          ...config.defaults.upstream,
          protocol: 'h2c',
          timeouts: { ...config.defaults.upstream?.timeouts, request_ms: 0 },
        },
        request: { allow_upgrade: false, max_upload_bytes: 0 },
      },
      storage: {
        ...config.storage,
        max_readers: 4096,
        fill_timeout_ms: 1800000,
        max_background: 1,
      },
      analytics: {
        enabled: false,
        detail_days: 14,
        rollup_days: 90,
        max_bytes: 2147483648,
        memory_bytes: 268435456,
        temp_bytes: 134217728,
        threads: 2,
        query_concurrency: 2,
        query_timeout_ms: 15000,
        queue_size: 8192,
        batch_size: 1024,
        flush_interval_ms: 1000,
        country_mmdb: '/data/country.mmdb',
        asn_mmdb: '/data/asn.mmdb',
      },
    };
    const fetch = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify(value), { headers: { ETag: '"next"' } }));
    vi.stubGlobal('fetch', fetch);
    const result = await api.saveConfig(value, '"current"');
    const body = JSON.parse(fetch.mock.calls[0][1].body);
    expect(body).toEqual(value);
    expect(result.data.defaults.identity?.client_ip_headers).toEqual([]);
    expect(result.data.storage.fill_timeout_ms).toBe(1800000);
    for (const field of ['traffic_days', 'query_max_rows', 'query_memory_bytes', 'geoip_file'])
      expect(body.analytics).not.toHaveProperty(field);
  });
  it('rejects retired analytics options before they can be sent', async () => {
    const fetch = vi.fn();
    vi.stubGlobal('fetch', fetch);
    for (const field of ['traffic_days', 'query_max_rows', 'query_memory_bytes', 'geoip_file']) {
      const value = { ...config, analytics: { ...config.analytics, [field]: 0 } };
      expect(() =>
        normalizeConfiguration({
          ...wireConfig,
          analytics: { ...wireConfig.analytics, [field]: 0 },
        }),
      ).toThrow(`analytics.${field} is no longer supported`);
      await expect(api.saveConfig(value, '"current"')).rejects.toThrow(
        `analytics.${field} is no longer supported`,
      );
    }
    expect(fetch).not.toHaveBeenCalled();
  });
  it('passes client HTTP version to explain and exposes socket peer/version results', async () => {
    const fetch = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          authority: 'origin.example.com',
          cache_key: 'key',
          cache_policy: wireConfig.defaults.cache,
          origin_id: 'o',
          origin_url: 'http://origin.example.com/',
          scheme: 'http',
          site_id: 's',
          peer_ip: '127.0.0.1',
          client_ip: '192.0.2.1',
          http_version: 'HTTP/2.0',
          protocol: 'h2c',
          trace: [],
        }),
      ),
    );
    vi.stubGlobal('fetch', fetch);
    const result = await api.explain({
      method: 'GET',
      url: 'http://cdn.example.com/',
      peer: '127.0.0.1:1234',
      http_version: 'HTTP/2.0',
      headers: {},
    });
    expect(JSON.parse(fetch.mock.calls[0][1].body).http_version).toBe('HTTP/2.0');
    expect(result).toMatchObject({
      peer_ip: '127.0.0.1',
      http_version: 'HTTP/2.0',
      protocol: 'h2c',
    });
  });
  it('returns actual top-level runtime measurements and normalizes only nullable lists', async () => {
    const status = runtimeFixture();
    status.analytics_status = {
      accepted: 0,
      closed: false,
      detail_from: '2026-10-13T00:00:00Z',
      disk_budget_bytes: 2147483648,
      disk_bytes: 0,
      disk_evicted_before: null,
      dropped: { queue: 0, invalid: 0, expired: 0, storage: 0 },
      geo_asn_available: false,
      geo_country_available: false,
      ingest_lag_ms: 0,
      ingested: 0,
      last_ingest_at: 0,
      pending_events: 0,
      queue_capacity: 8192,
      queue_depth: 0,
      rollup_from: '2026-07-18T00:00:00Z',
      since: 1792108800000,
      storage_pressure: false,
    };
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(status))));
    expect(await api.status()).toMatchObject({
      cpu_percent: 12.5,
      memory_bytes: 1048576,
      heap_bytes: 524288,
      history: [],
      restart_fields: [],
      analytics_status: { detail_from: '2026-10-13T00:00:00Z' },
    });
  });
  it('sends MISS in the real explain body after enabling a simulated response by entering status only', async () => {
    const fetch = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          authority: 'origin.example.com',
          cache_key: 'key',
          cache_policy: wireConfig.defaults.cache,
          origin_id: 'o',
          origin_url: 'http://origin.example.com/',
          scheme: 'http',
          site_id: 's',
          peer_ip: '127.0.0.1',
          client_ip: '127.0.0.1',
          http_version: 'HTTP/1.1',
          protocol: 'auto',
          trace: [],
        }),
      ),
    );
    vi.stubGlobal('fetch', fetch);
    const model = withResponseStatus(
      { method: 'GET', url: 'http://cdn.example.com/', peer: '127.0.0.1:1234', headers: {} },
      200,
    );
    await api.explain(model);
    expect(JSON.parse(fetch.mock.calls[0][1].body).response).toEqual({
      status: 200,
      cache_result: 'MISS',
    });
  });
  it('preserves old user IP/path filters and surfaces the retention error without retrying a broader query', async () => {
    const fetch = vi
      .fn()
      .mockResolvedValue(
        new Response(
          JSON.stringify({ detail: 'query requires detail history outside its retention window' }),
          { status: 422 },
        ),
      );
    vi.stubGlobal('fetch', fetch);
    const query = queryFromParams(
      new URLSearchParams({
        range: 'custom',
        from: '2026-09-16T14:00:00Z',
        to: '2026-10-16T14:00:00Z',
        ip: '192.0.2.0/24',
        path: '/assets/',
      }),
      new Date('2026-10-16T14:00:00Z'),
      '2026-10-02T00:00:00Z',
    );
    await expect(api.analytics(query)).rejects.toMatchObject({
      status: 422,
      message: 'query requires detail history outside its retention window',
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    const body = JSON.parse(fetch.mock.calls[0][1].body);
    expect(body.from).toBe('2026-09-16T14:00:00.000Z');
    expect(body.filters).toEqual({ client_ips: ['192.0.2.0/24'], path_prefix: '/assets/' });
    expect(body.group_by).not.toContain('client_ip');
    expect(body.group_by).not.toContain('path');
  });
  it('rejects ambiguous zero cache budgets on import/save before network submission', async () => {
    const fetch = vi.fn();
    vi.stubGlobal('fetch', fetch);
    for (const field of ['memory_bytes', 'max_background', 'inline_bytes'] as const) {
      const wire = configurationFixture();
      wire.storage[field] = 0;
      expect(() => normalizeConfiguration(wire)).toThrow(`storage.${field} must be at least 1`);
      const document = { ...config, storage: { ...config.storage, [field]: 0 } };
      await expect(api.saveConfig(document, '"current"')).rejects.toThrow(
        `storage.${field} must be at least 1`,
      );
    }
    expect(fetch).not.toHaveBeenCalled();
  });
});

describe('actual cache management contracts', () => {
  it('uses a bounded listing with truncation and no invented cursor', async () => {
    const fetch = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          entries: [
            {
              key: 'k',
              site_id: 'site-a',
              host: 'cdn.example.com',
              path: '/a',
              size: 12,
              status: 200,
              stored_at: 'a',
              accessed_at: 'b',
              fresh_until: 'c',
              complete: true,
            },
          ],
          limit: 50,
          truncated: true,
        }),
      ),
    );
    vi.stubGlobal('fetch', fetch);
    const result = await api.cacheEntries(
      new URLSearchParams({ site_id: 'site-a', path: '/a', limit: '50' }),
    );
    expect(fetch.mock.calls[0][0]).toBe('/api/cache/entries?site_id=site-a&path=%2Fa&limit=50');
    expect(result).toMatchObject({
      limit: 50,
      truncated: true,
      entries: [{ key: 'k', bytes: 12 }],
    });
    expect(result).not.toHaveProperty('next_cursor');
  });
  it('purges only the exact selected site/host/path with no key selector', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ purged: 1 })));
    vi.stubGlobal('fetch', fetch);
    expect(await api.purge({ site_id: 'site-a', host: 'cdn.example.com', path: '/a' })).toEqual({
      purged: 1,
    });
    expect(fetch.mock.calls[0][0]).toBe('/api/cache/purge');
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({
      site_id: 'site-a',
      host: 'cdn.example.com',
      path: '/a',
    });
  });
  it('refreshes explicit absolute client URLs rather than a listing filter or entry key', async () => {
    const fetch = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          results: [{ url: 'http://cdn.example.com/a?width=640', status: 'refreshed' }],
        }),
      ),
    );
    vi.stubGlobal('fetch', fetch);
    const urls = ['http://cdn.example.com/a?width=640'];
    await api.refresh({ urls, site_id: 'site-a' });
    expect(fetch.mock.calls[0][0]).toBe('/api/cache/refresh');
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ urls, site_id: 'site-a' });
  });
  it('preserves mixed per-URL refresh outcomes without treating HTTP 200 as full success', async () => {
    const results = [
      { url: 'http://cdn.example.com/a', status: 'refreshed' },
      {
        url: 'http://cdn.example.com/b',
        status: 'skipped',
        reason: 'current rules bypass the cache',
      },
      { url: 'http://cdn.example.com/c', status: 'error', error: 'origin TLS verification failed' },
    ];
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ results }))));
    const response = await api.refresh({ urls: results.map((result) => result.url) });
    expect(response.results).toEqual(results);
    expect(response).not.toHaveProperty('queued');
  });
});
