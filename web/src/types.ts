import type { components } from './generated/api';

type Schema = components['schemas'];

export type Stage = 'ingress' | 'request' | 'origin_response' | 'delivery';
export const stages: Stage[] = ['ingress', 'request', 'origin_response', 'delivery'];
export type Protocol = 'auto' | 'h1' | 'h2' | 'h2c' | 'h3';
export type HTTPVersion = 'HTTP/1.0' | 'HTTP/1.1' | 'HTTP/2.0' | 'HTTP/3.0';

export interface Origin {
  id: string;
  name: string;
  url: string;
  host_header?: string;
  tls_server_name?: string;
  dial_address?: string;
  ca_file?: string;
  protocol?: Protocol;
  timeouts?: Timeouts;
}
export interface Site {
  id: string;
  name: string;
  enabled: boolean;
  domains: string[];
  origins: Origin[];
  default_origin: string;
}
export interface CachePolicy {
  mode?: 'respect' | 'force' | 'bypass';
  ttl_seconds?: number;
  stale_while_revalidate_seconds?: number;
  stale_if_error_seconds?: number;
  prefetch?: boolean;
  status_ttl?: { status: number; ttl_seconds: number }[];
}
export interface CacheKey {
  namespace?: string;
  include_host: boolean;
  query_mode: 'all' | 'ignore' | 'include' | 'exclude';
  query_names?: string[];
  headers?: string[];
  cookies?: string[];
}
export interface HeaderOperation {
  operation: 'set' | 'add' | 'delete';
  name: string;
  value?: string;
}
export interface Timeouts {
  connect_ms?: number;
  tls_handshake_ms?: number;
  response_header_ms?: number;
  idle_ms?: number;
  request_ms?: number;
}
export interface UpstreamSettings {
  protocol?: Protocol;
  timeouts?: Timeouts;
}
export interface IdentitySettings {
  client_ip_source?: 'peer' | 'trusted_x_forwarded_for' | 'trusted_forwarded';
  scheme_source?: 'connection' | 'trusted_x_forwarded_proto' | 'trusted_forwarded';
  client_ip_headers?: ClientIPHeader[] | null;
}
export interface ClientIPHeader {
  name: string;
  format: 'single' | 'xff' | 'forwarded';
}
export interface RequestSettings {
  max_upload_bytes?: number;
  allow_upgrade?: boolean;
}
export interface CorsSettings {
  origins: string[];
  methods?: string[];
  headers?: string[];
  expose?: string[];
  credentials: boolean;
  max_age_seconds?: number;
}
export interface DeliverySettings {
  headers?: HeaderOperation[];
  browser_ttl_seconds?: number;
  cors?: CorsSettings;
}
export interface Defaults {
  cache?: CachePolicy;
  cache_key?: CacheKey;
  upstream?: UpstreamSettings;
  identity?: IdentitySettings;
  request?: RequestSettings;
  delivery?: DeliverySettings;
}
export type Condition =
  | { type: 'all' | 'any' | 'not'; children: Condition[] }
  | { type: 'match'; field: string; key?: string; op: string; values: string[] };
export interface Action {
  type: string;
  cache?: CachePolicy;
  cache_key?: CacheKey;
  origin?: string;
  upstream?: UpstreamSettings;
  path?: string;
  query?: HeaderOperation;
  cookie?: HeaderOperation;
  header?: HeaderOperation;
  authority?: string;
  identity?: IdentitySettings;
  limits?: RequestSettings;
  browser_ttl_seconds?: number;
  cors?: CorsSettings;
  reply?: { status: number; content_type: string; body: string };
  redirect?: { status: number; location: string };
}
export type Step =
  { id: string; type: 'action'; action: Action } | { id: string; type: 'if'; branches: Branch[] };
export interface Branch {
  id: string;
  condition: Condition | null;
  steps: Step[];
}
export interface Rule {
  id: string;
  name: string;
  enabled: boolean;
  stage: Stage;
  steps: Step[];
}
export type StorageSettings = Partial<Schema['Storage']>;
export type AnalyticsSettings = Partial<Schema['Analytics']>;
export interface Configuration {
  schema_version: number;
  listen: { proxy: string; admin: string };
  data_dir: string;
  trusted_proxies: string[];
  sites: Site[];
  defaults: Defaults;
  rules: Rule[];
  storage: StorageSettings;
  analytics: AnalyticsSettings;
}
export interface FieldCapability extends Omit<
  Schema['FieldCapability'],
  'stages' | 'operators' | 'key' | 'type'
> {
  label?: string;
  type?: string;
  stages: Stage[];
  operators: string[];
  key?: boolean;
}
export interface ActionCapability extends Omit<Schema['ActionCapability'], 'stages'> {
  label?: string;
  stages: Stage[];
  description?: string;
}
export interface Capabilities extends Omit<
  Schema['Catalog'],
  'fields' | 'actions' | 'stages' | 'schemas'
> {
  fields: FieldCapability[];
  actions: ActionCapability[];
  stages?: Stage[];
  schemas?: Schema['Catalog']['schemas'];
}
export interface ValidationIssue {
  path: string;
  message: string;
}
export type RuntimeStatus = Omit<Schema['RuntimeStatus'], 'history' | 'restart_fields'> & {
  history: RuntimeInfo[];
  restart_fields: string[];
};
export type RuntimeInfo = Schema['RuntimeInfo'];
export interface AnalyticsFilters {
  site_ids?: string[];
  hosts?: string[];
  client_ips?: string[];
  methods?: string[];
  statuses?: number[];
  cache_results?: string[];
  content_types?: string[];
  countries?: string[];
  asns?: number[];
  path_prefix?: string;
}
export interface AnalyticsQuery {
  from: string;
  to: string;
  timezone?: string;
  interval_seconds?: number;
  filters: AnalyticsFilters;
  group_by?: string[];
  limit?: number;
}
export interface AnalyticsSummary {
  requests?: number;
  bytes_sent?: number;
  bytes_received?: number;
  origin_bytes?: number;
  peak_bandwidth_bps?: number;
  hit_ratio?: number;
  byte_hit_ratio?: number;
  p95_ttfb_ms?: number;
  p95_duration_ms?: number;
  p95_origin_ttfb_ms?: number;
  p95_origin_duration_ms?: number;
  unique_ips?: number | null;
  origin_requests?: number;
  errors?: number;
}
export interface SeriesPoint {
  time: string;
  requests?: number;
  bytes_sent?: number;
  origin_bytes?: number;
  bandwidth_bps?: number;
  origin_bandwidth_bps?: number;
  hit_ratio?: number;
  p95_ttfb_ms?: number;
  p95_duration_ms?: number;
  p95_origin_ttfb_ms?: number;
  p95_origin_duration_ms?: number;
  active_requests?: number;
  memory_bytes?: number;
}
export interface Breakdown {
  key: string;
  requests: number;
  bytes_sent?: number;
  hit_ratio?: number;
  p95_ttfb_ms?: number;
  p95_duration_ms?: number;
}
export interface GeoPoint {
  country: string;
  requests: number;
  bytes_sent?: number;
}
export interface Coverage {
  from?: string;
  to?: string;
  lag_ms?: number;
  dropped_events?: number;
  partial?: boolean;
  geoip_available?: boolean;
  notes?: string[];
  upstream_attributable?: boolean;
  latency_approximate?: boolean;
  interval_seconds?: number;
  unique_ips_available?: boolean;
}
export interface AnalyticsResult {
  summary: AnalyticsSummary;
  series: SeriesPoint[];
  breakdowns: Record<string, Breakdown[]>;
  geo: GeoPoint[];
  coverage: Coverage;
}
export interface RequestDetail {
  id?: string;
  time: string;
  host: string;
  path: string;
  client_ip: string;
  method: string;
  status: number;
  cache_result: string;
  bytes_sent?: number;
  duration_ms?: number;
  first_byte_ms?: number;
  country?: string;
}
export interface CacheEntry {
  key: string;
  site_id?: string;
  host?: string;
  path?: string;
  url?: string;
  status?: number;
  bytes?: number;
  created_at?: string;
  expires_at?: string;
  last_access?: string;
  state?: string;
}
export type CacheSelection = Schema['PurgeSelection'];
export type CacheRefresh = Omit<Schema['RefreshSelection'], 'urls'> & { urls: string[] };
export type RefreshResult = Schema['RefreshResult'];
export type RefreshBatch = Omit<Schema['RefreshBatch'], 'results'> & { results: RefreshResult[] };
export interface ExplainInput extends Omit<
  Schema['ExplainInput'],
  'headers' | 'http_version' | 'response'
> {
  headers: Record<string, string[]>;
  http_version?: HTTPVersion;
  response?: Omit<Schema['SimulatedResponse'], 'headers'> & { headers?: Record<string, string[]> };
}
export interface ExplainResult extends Omit<
  Schema['ExplainResult'],
  'cache_policy' | 'http_version' | 'trace' | 'response_headers'
> {
  cache_policy: CachePolicy;
  http_version: HTTPVersion;
  trace: Schema['Trace'][];
  response_headers: Record<string, string[]>;
}
