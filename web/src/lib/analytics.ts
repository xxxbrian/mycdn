import type { AnalyticsFilters, AnalyticsQuery } from '../types';
import { splitLines } from './format';

export const filterKeys = [
  'site',
  'host',
  'ip',
  'path',
  'status',
  'cache',
  'method',
  'type',
  'country',
  'asn',
] as const;
export type FilterKey = (typeof filterKeys)[number];
export const filterLabels: Record<FilterKey, string> = {
  site: 'Site',
  host: 'Host',
  ip: 'Client IP / CIDR',
  path: 'Path prefix',
  status: 'Status',
  cache: 'Cache result',
  method: 'Method',
  type: 'Content type',
  country: 'Country',
  asn: 'ASN',
};
export function detailRangeAvailable(from: string, detailFrom?: string): boolean {
  return detailFrom !== undefined && Date.parse(from) >= Date.parse(detailFrom);
}

export type TimeWindow = { from: string; to: string };
export const resetTimeZoom = {
  zoom_from: null,
  zoom_to: null,
  zoom_base_from: null,
  zoom_base_to: null,
};

// The slider keeps this base window even when a narrower global zoom is active.
export function timeWindowFromParams(params: URLSearchParams, now = new Date()): TimeWindow {
  const range = params.get('range') || 'today';
  const from = new Date(now);
  const to = new Date(now);
  if (range === 'today') from.setHours(0, 0, 0, 0);
  else if (range === 'yesterday') {
    from.setDate(from.getDate() - 1);
    from.setHours(0, 0, 0, 0);
    to.setHours(0, 0, 0, 0);
  } else if (range === '7d' || range === '14d')
    from.setTime(now.getTime() - Number(range.slice(0, -1)) * 86400000);
  else if (range === 'custom') {
    from.setTime(Date.parse(params.get('from') || ''));
    to.setTime(Date.parse(params.get('to') || ''));
  } else
    throw new Error('Unknown time range. Choose today, yesterday, 7 days, 14 days, or custom.');
  if (params.has('zoom_from') && params.has('zoom_base_from') && params.has('zoom_base_to')) {
    from.setTime(Date.parse(params.get('zoom_base_from')!));
    to.setTime(Date.parse(params.get('zoom_base_to')!));
  }
  if (Number.isNaN(from.getTime()) || Number.isNaN(to.getTime()) || from >= to)
    throw new Error('Select a valid time range. The end must be after the start.');
  return { from: from.toISOString(), to: to.toISOString() };
}

export function zoomWindow(window: TimeWindow, start: number, end: number): TimeWindow | undefined {
  if (!Number.isFinite(start) || !Number.isFinite(end) || start < 0 || end > 100 || end <= start)
    return;
  const first = Date.parse(window.from);
  const last = Date.parse(window.to);
  const from =
    start === 0
      ? first
      : Math.max(first, Math.round((first + ((last - first) * start) / 100) / 1000) * 1000);
  const to =
    end === 100
      ? last
      : Math.min(last, Math.round((first + ((last - first) * end) / 100) / 1000) * 1000);
  if (to - from < 10000) return;
  return { from: new Date(from).toISOString(), to: new Date(to).toISOString() };
}

export function queryFromParams(
  params: URLSearchParams,
  now = new Date(),
  detailFrom?: string,
): AnalyticsQuery {
  const base = timeWindowFromParams(params, now);
  const from = new Date(base.from);
  const to = new Date(base.to);
  if (params.has('zoom_from') || params.has('zoom_to')) {
    const lower = Date.parse(params.get('zoom_from') || '');
    const upper = Date.parse(params.get('zoom_to') || '');
    if (
      !Number.isFinite(lower) ||
      !Number.isFinite(upper) ||
      lower < from.getTime() ||
      upper > to.getTime() ||
      upper - lower < 10000
    )
      throw new Error(
        'The selected time zoom must fit within the time range. Reset the zoom to continue.',
      );
    from.setTime(lower);
    to.setTime(upper);
  }
  const filters: AnalyticsFilters = {};
  const list = (key: FilterKey) => splitLines(params.get(key) || '');
  if (list('site').length) filters.site_ids = list('site');
  if (list('host').length) filters.hosts = list('host');
  if (list('ip').length) filters.client_ips = list('ip');
  if (list('method').length) filters.methods = list('method').map((v) => v.toUpperCase());
  if (list('status').length) {
    filters.statuses = list('status').map(Number);
    if (filters.statuses.some((v) => !Number.isInteger(v) || v < 100 || v > 599))
      throw new Error('Status filters must be HTTP status codes between 100 and 599.');
  }
  if (list('cache').length) filters.cache_results = list('cache').map((v) => v.toUpperCase());
  if (list('type').length) filters.content_types = list('type');
  if (list('country').length) filters.countries = list('country').map((v) => v.toUpperCase());
  if (list('asn').length) {
    filters.asns = list('asn').map((v) => Number(v.replace(/^AS/i, '')));
    if (filters.asns.some((v) => !Number.isSafeInteger(v) || v < 0 || v > 4294967295))
      throw new Error('ASNs must be integers between 0 and 4294967295.');
  }
  if (params.get('path')) filters.path_prefix = params.get('path')!;
  // Keep at most 720 buckets without silently truncating the selected range.
  const interval = Math.max(10, Math.ceil((to.getTime() - from.getTime()) / 1000 / 720 / 10) * 10);
  return {
    from: from.toISOString(),
    to: to.toISOString(),
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    interval_seconds: interval,
    filters,
    group_by: [
      ...(detailRangeAvailable(from.toISOString(), detailFrom) ? ['client_ip', 'path'] : []),
      'host',
      'content_type',
      'method',
      'country',
      'status',
      'cache_result',
      'asn',
    ],
    limit: 20,
  };
}
