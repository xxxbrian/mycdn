import type {
  AnalyticsQuery,
  CacheRefresh,
  CacheSelection,
  Configuration,
  ExplainInput,
  ValidationIssue,
} from './types';
import {
  normalizeAnalytics,
  normalizeCache,
  normalizeConfiguration,
  normalizeDetails,
  normalizeCapabilities,
  normalizeExplain,
  normalizeRefresh,
  normalizeRuntime,
} from './wire';
import type { WireAnalytics, WireDetails, WireRuntimeStatus } from './wire';
import { toWireConfiguration } from './configWire';
import type { WireConfiguration } from './configWire';
import type { components } from './generated/api';

type Schema = components['schemas'];

export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
    public issues: ValidationIssue[] = [],
  ) {
    super(message);
  }
}

async function request<T>(
  path: string,
  init: RequestInit = {},
): Promise<{ data: T; etag: string }> {
  const response = await fetch(path, {
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init.body ? { 'Content-Type': 'application/json' } : {}),
      ...init.headers,
    },
  });
  const text = await response.text();
  let value: unknown;
  try {
    value = text ? JSON.parse(text) : undefined;
  } catch {
    throw new ApiError(
      response.ok
        ? 'The server returned a non-JSON response. Check the admin API address.'
        : `Request failed (${response.status}). ${text.slice(0, 200)}`,
      response.status,
    );
  }
  if (!response.ok) {
    const problem = value as
      | {
          error?: string | { message?: string; path?: string; field?: string };
          message?: string;
          detail?: string;
          title?: string;
          path?: string;
          field?: string;
          issues?: ValidationIssue[];
          errors?: { path?: string; location?: string; message: string }[];
        }
      | undefined;
    const message =
      typeof problem?.error === 'string'
        ? problem.error
        : problem?.error?.message || problem?.message || problem?.detail || problem?.title;
    throw new ApiError(
      message || `Request failed (${response.status})`,
      response.status,
      problem?.issues ||
        problem?.errors?.map((issue) => ({
          path: issue.path || issue.location || '',
          message: issue.message,
        })) ||
        (problem?.path || problem?.field
          ? [{ path: problem.path || problem.field || '', message: message || 'Invalid value' }]
          : problem?.error &&
              typeof problem.error === 'object' &&
              (problem.error.path || problem.error.field)
            ? [
                {
                  path: problem.error.path || problem.error.field || '',
                  message: message || 'Invalid value',
                },
              ]
            : []),
    );
  }
  if (value === undefined)
    throw new ApiError(
      'The API returned an empty response where JSON was required.',
      response.status,
    );
  return { data: value as T, etag: response.headers.get('ETag') || '' };
}

const post = <T>(path: string, body: unknown, signal?: AbortSignal) =>
  request<T>(path, { method: 'POST', body: JSON.stringify(body), signal }).then((r) => r.data);

export const api = {
  config: (signal?: AbortSignal) =>
    request<WireConfiguration>('/api/config', { signal }).then((result) => ({
      ...result,
      data: normalizeConfiguration(result.data),
    })),
  saveConfig: async (config: Configuration, etag: string) => {
    const body = toWireConfiguration(config);
    const result = await request<WireConfiguration>('/api/config', {
      method: 'PUT',
      headers: { 'If-Match': etag },
      body: JSON.stringify(body),
    });
    return { ...result, data: normalizeConfiguration(result.data) };
  },
  capabilities: (signal?: AbortSignal) =>
    request<Schema['Catalog']>('/api/capabilities', { signal }).then((r) =>
      normalizeCapabilities(r.data),
    ),
  status: (signal?: AbortSignal) =>
    request<WireRuntimeStatus>('/api/status', { signal }).then((r) => normalizeRuntime(r.data)),
  analytics: (query: AnalyticsQuery, signal?: AbortSignal) =>
    post<WireAnalytics>('/api/analytics/query', query satisfies Schema['Query'], signal).then(
      normalizeAnalytics,
    ),
  details: (query: AnalyticsQuery, signal?: AbortSignal) =>
    post<WireDetails>(
      '/api/analytics/details',
      {
        from: query.from,
        to: query.to,
        filters: query.filters,
        limit: query.limit,
      } satisfies Schema['DetailsQuery'],
      signal,
    ).then(normalizeDetails),
  cacheEntries: (search: URLSearchParams, signal?: AbortSignal) =>
    request<Schema['EntriesResult']>(`/api/cache/entries?${search}`, {
      signal,
    }).then((r) => ({
      entries: (r.data.entries ?? []).map(normalizeCache),
      limit: r.data.limit,
      truncated: r.data.truncated,
    })),
  purge: (selection: CacheSelection) =>
    post<Schema['PurgeResult']>('/api/cache/purge', selection satisfies Schema['PurgeSelection']),
  refresh: (input: CacheRefresh) =>
    post<Schema['RefreshBatch']>(
      '/api/cache/refresh',
      input satisfies Schema['RefreshSelection'],
    ).then(normalizeRefresh),
  explain: (input: ExplainInput, signal?: AbortSignal) =>
    post<Schema['ExplainResult']>(
      '/api/explain',
      input satisfies Schema['ExplainInput'],
      signal,
    ).then(normalizeExplain),
};

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
