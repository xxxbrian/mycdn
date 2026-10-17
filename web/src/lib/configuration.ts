import type { Configuration } from '../types';

const removedAnalyticsFields = [
  'traffic_days',
  'query_max_rows',
  'query_memory_bytes',
  'geoip_file',
] as const;

// Fail at import/save rather than quietly forwarding retired options or guessing
// replacement values for them. The rest of the config is validated by the API.
export function assertCurrentConfiguration(
  document: Pick<Configuration, 'analytics' | 'storage'>,
): void {
  for (const field of ['memory_bytes', 'max_background', 'inline_bytes'] as const) {
    const value = document.storage[field];
    if (value !== undefined && value < 1) {
      throw new Error(`storage.${field} must be at least 1; zero selects cache defaults.`);
    }
  }
  for (const field of removedAnalyticsFields) {
    if (Object.hasOwn(document.analytics, field)) {
      throw new Error(
        `analytics.${field} is no longer supported. Remove it and configure the current analytics settings before saving.`,
      );
    }
  }
}
