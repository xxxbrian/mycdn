import type { WireConfiguration } from '../configWire';
import type { components } from '../generated/api';

export function runtimeFixture(): components['schemas']['RuntimeStatus'] {
  return {
    active_fills: 0,
    active_requests: 0,
    admin_listen: '127.0.0.1:8081',
    analytics: {
      dropped_events: 0,
      enabled: true,
      geoip_available: false,
      lag_ms: 0,
      queue_depth: 0,
    },
    cache: {
      active_fills: 0,
      bypasses: 0,
      bytes: 0,
      coalesced: 0,
      entries: 0,
      evictions: 0,
      hits: 0,
      misses: 0,
      readers: 0,
      resident_bytes: 0,
      storage_errors: 0,
    },
    cpu_percent: 12.5,
    cpus: 8,
    data_dir: 'data',
    gateway: {
      active_requests: 0,
      active_upstreams: 0,
      max_upstreams: 64,
      requests: 0,
      started_at: 1792108800000,
    },
    go_version: 'go1.26',
    goroutines: 40,
    heap_bytes: 524288,
    history: null,
    memory_bytes: 1048576,
    proxy_listen: ':8080',
    restart_fields: null,
    restart_required: false,
    started_at: '2026-10-16T00:00:00Z',
    uptime_seconds: 5,
    version: 'test',
  };
}

// Complete API documents, not permissive partials: generated-schema drift must
// fail compilation even when a test exercises only one setting.
export function configurationFixture(): WireConfiguration {
  return {
    schema_version: 1,
    listen: { proxy: ':8080', admin: ':8081' },
    data_dir: 'data',
    trusted_proxies: [],
    sites: [],
    rules: [],
    defaults: {
      cache: {
        mode: 'respect',
        ttl_seconds: 0,
        stale_while_revalidate_seconds: 0,
        stale_if_error_seconds: 0,
        prefetch: false,
      },
      cache_key: { include_host: true, query_mode: 'all' },
      upstream: {
        protocol: 'auto',
        timeouts: {
          connect_ms: 10000,
          tls_handshake_ms: 10000,
          response_header_ms: 30000,
          idle_ms: 90000,
          request_ms: 0,
        },
      },
      identity: {
        client_ip_source: 'trusted_x_forwarded_for',
        scheme_source: 'trusted_x_forwarded_proto',
      },
      request: { max_upload_bytes: 67108864, allow_upgrade: true },
      delivery: {},
    },
    storage: {
      max_bytes: 10737418240,
      memory_bytes: 67108864,
      max_fills: 64,
      max_background: 4,
      max_object_bytes: 2147483648,
      inline_bytes: 32768,
      block_bytes: 1048576,
      max_readers: 4096,
      fill_timeout_ms: 1800000,
    },
    analytics: {
      enabled: true,
      detail_days: 14,
      rollup_days: 90,
      max_bytes: 2147483648,
      queue_size: 8192,
      batch_size: 1024,
      flush_interval_ms: 1000,
      query_concurrency: 2,
      query_timeout_ms: 15000,
      memory_bytes: 268435456,
      temp_bytes: 134217728,
      threads: 2,
    },
  };
}
