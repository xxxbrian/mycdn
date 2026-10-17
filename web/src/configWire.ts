import type { components } from './generated/api';
import type {
  Action,
  CacheKey,
  CachePolicy,
  ClientIPHeader,
  Condition,
  Configuration,
  CorsSettings,
  IdentitySettings,
  Origin,
  Step,
  UpstreamSettings,
} from './types';
import { stages } from './types';
import { enumValue, required } from './lib/narrow';
import { assertCurrentConfiguration } from './lib/configuration';

type Schema = components['schemas'];
export type WireConfiguration = Schema['Document'];
const protocols = ['auto', 'h1', 'h2', 'h2c', 'h3'] as const;
const cacheModes = ['respect', 'force', 'bypass'] as const;

export function cachePolicy(value: Schema['CachePolicy'] | Schema['CachePatch']): CachePolicy {
  return {
    ...value,
    mode: value.mode === undefined ? undefined : enumValue(value.mode, cacheModes, 'cache mode'),
    status_ttl: value.status_ttl ?? undefined,
  };
}
function cacheKey(value: Schema['CacheKey']): CacheKey {
  return {
    ...value,
    query_mode: enumValue(value.query_mode, ['all', 'ignore', 'include', 'exclude'], 'query mode'),
    query_names: value.query_names ?? undefined,
    headers: value.headers ?? undefined,
    cookies: value.cookies ?? undefined,
  };
}
function upstream(value: Schema['UpstreamPatch']): UpstreamSettings {
  return {
    ...value,
    protocol:
      value.protocol === undefined
        ? undefined
        : enumValue(value.protocol, protocols, 'upstream protocol'),
  };
}
function identity(value: Schema['IdentitySettings'] | Schema['IdentityPatch']): IdentitySettings {
  return {
    client_ip_source:
      value.client_ip_source === undefined
        ? undefined
        : enumValue(
            value.client_ip_source,
            ['peer', 'trusted_x_forwarded_for', 'trusted_forwarded'],
            'client IP source',
          ),
    scheme_source:
      value.scheme_source === undefined
        ? undefined
        : enumValue(
            value.scheme_source,
            ['connection', 'trusted_x_forwarded_proto', 'trusted_forwarded'],
            'scheme source',
          ),
    client_ip_headers: value.client_ip_headers?.map((header): ClientIPHeader => ({
      ...header,
      format: enumValue(header.format, ['single', 'xff', 'forwarded'], 'client IP header format'),
    })),
  };
}
function cors(value: Schema['CORS']): CorsSettings {
  return {
    ...value,
    origins: value.origins ?? [],
    methods: value.methods ?? undefined,
    headers: value.headers ?? undefined,
    expose: value.expose ?? undefined,
  };
}
function mutation(value: Schema['HeaderMutation']) {
  return {
    ...value,
    operation: enumValue(value.operation, ['set', 'add', 'delete'], 'mutation operation'),
  };
}
function action(value: Schema['Action']): Action {
  return {
    ...value,
    cache: value.cache && cachePolicy(value.cache),
    cache_key: value.cache_key && cacheKey(value.cache_key),
    upstream: value.upstream && upstream(value.upstream),
    identity: value.identity && identity(value.identity),
    cors: value.cors && cors(value.cors),
    header: value.header && mutation(value.header),
    cookie: value.cookie && mutation(value.cookie),
    query: value.query && mutation(value.query),
  };
}
function condition(value: Schema['Condition']): Condition {
  switch (value.type) {
    case 'all':
    case 'any':
    case 'not':
      return { type: value.type, children: (value.children ?? []).map(condition) };
    case 'match':
      return {
        type: 'match',
        field: required(value.field, 'condition.field'),
        key: value.key,
        op: required(value.op, 'condition.op'),
        values: value.values ?? [],
      };
    default:
      throw new Error(`Unsupported condition type: ${value.type}`);
  }
}
function step(value: Schema['Step']): Step {
  switch (value.type) {
    case 'action':
      return {
        id: value.id,
        type: 'action',
        action: action(required(value.action, 'step.action')),
      };
    case 'if':
      return {
        id: value.id,
        type: 'if',
        branches: (value.branches ?? []).map((branch) => ({
          id: branch.id,
          condition: branch.condition === null ? null : condition(branch.condition),
          steps: (branch.steps ?? []).map(step),
        })),
      };
    default:
      throw new Error(`Unsupported rule step type: ${value.type}`);
  }
}
function origin(value: Schema['Origin']): Origin {
  return {
    ...value,
    protocol:
      value.protocol === undefined
        ? undefined
        : enumValue(value.protocol, protocols, 'origin protocol'),
  };
}

export function normalizeConfiguration(document: WireConfiguration): Configuration {
  if (
    !document ||
    document.schema_version !== 1 ||
    !document.listen ||
    !document.defaults ||
    !document.storage ||
    !document.analytics
  )
    throw new Error('The API returned an unsupported or incomplete configuration document.');
  assertCurrentConfiguration(document);
  return {
    ...document,
    sites: (document.sites ?? []).map((site) => ({
      ...site,
      domains: site.domains ?? [],
      origins: (site.origins ?? []).map(origin),
    })),
    rules: (document.rules ?? []).map((rule) => ({
      ...rule,
      stage: enumValue(rule.stage, stages, 'rule stage'),
      steps: (rule.steps ?? []).map(step),
    })),
    trusted_proxies: document.trusted_proxies ?? [],
    defaults: {
      cache: cachePolicy(document.defaults.cache),
      cache_key: cacheKey(document.defaults.cache_key),
      upstream: upstream(document.defaults.upstream),
      identity: identity(document.defaults.identity),
      request: document.defaults.request,
      delivery: {
        ...document.defaults.delivery,
        headers: document.defaults.delivery.headers?.map(mutation),
        cors: document.defaults.delivery.cors && cors(document.defaults.delivery.cors),
      },
    },
  };
}

function wireStep(value: Step): Schema['Step'] {
  if (value.type === 'if')
    return {
      ...value,
      branches: value.branches.map((branch) => ({ ...branch, steps: branch.steps.map(wireStep) })),
    };
  return {
    ...value,
    action: {
      ...value.action,
      identity: value.action.identity && {
        ...value.action.identity,
        client_ip_headers: value.action.identity.client_ip_headers ?? undefined,
      },
    } satisfies Schema['Action'],
  };
}

// Editable fields can temporarily be blank. Require the generated schema's
// complete values at save; never fill missing values with guessed defaults.
function requireFields<T extends object, K extends keyof T>(
  value: T,
  fields: readonly K[],
  path: string,
): asserts value is T & { [P in K]-?: Exclude<T[P], undefined> } {
  for (const field of fields) required(value[field], `${path}.${String(field)}`);
}

export function toWireConfiguration(document: Configuration): WireConfiguration {
  assertCurrentConfiguration(document);
  const d = document.defaults;
  const cache = required(d.cache, 'defaults.cache');
  const upstream = required(d.upstream, 'defaults.upstream');
  const timeouts = required(upstream.timeouts, 'defaults.upstream.timeouts');
  const identity = required(d.identity, 'defaults.identity');
  const request = required(d.request, 'defaults.request');
  const storage = document.storage;
  const analytics = document.analytics;
  requireFields(
    cache,
    ['mode', 'ttl_seconds', 'stale_while_revalidate_seconds', 'stale_if_error_seconds', 'prefetch'],
    'defaults.cache',
  );
  requireFields(upstream, ['protocol'], 'defaults.upstream');
  requireFields(
    timeouts,
    ['connect_ms', 'tls_handshake_ms', 'response_header_ms', 'idle_ms', 'request_ms'],
    'defaults.upstream.timeouts',
  );
  requireFields(identity, ['client_ip_source', 'scheme_source'], 'defaults.identity');
  requireFields(request, ['max_upload_bytes', 'allow_upgrade'], 'defaults.request');
  requireFields(
    storage,
    [
      'max_bytes',
      'memory_bytes',
      'max_fills',
      'max_background',
      'max_object_bytes',
      'inline_bytes',
      'block_bytes',
      'max_readers',
      'fill_timeout_ms',
    ],
    'storage',
  );
  requireFields(
    analytics,
    [
      'enabled',
      'detail_days',
      'rollup_days',
      'max_bytes',
      'memory_bytes',
      'temp_bytes',
      'threads',
      'queue_size',
      'batch_size',
      'flush_interval_ms',
      'query_concurrency',
      'query_timeout_ms',
    ],
    'analytics',
  );
  return {
    ...document,
    rules: document.rules.map((rule) => ({ ...rule, steps: rule.steps.map(wireStep) })),
    defaults: {
      cache,
      cache_key: required(d.cache_key, 'defaults.cache_key'),
      upstream: { ...upstream, timeouts },
      identity,
      request,
      delivery: required(d.delivery, 'defaults.delivery'),
    },
    storage,
    analytics,
  } satisfies WireConfiguration;
}
