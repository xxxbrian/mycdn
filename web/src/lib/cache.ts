import type { CacheEntry, CacheRefresh, CacheSelection } from '../types';

export function cacheSelection(filters: Omit<CacheSelection, 'all'>): Omit<CacheSelection, 'all'> {
  return Object.fromEntries(
    Object.entries(filters)
      .map(([key, value]) => [key, value?.trim()])
      .filter(([, value]) => value),
  );
}

export function refreshInput(text: string, siteId: string): CacheRefresh {
  const urls = [
    ...new Set(
      text
        .split(/\r?\n/)
        .map((value) => value.trim())
        .filter(Boolean),
    ),
  ];
  if (!urls.length) throw new Error('Enter at least one absolute client URL to refresh.');
  if (urls.length > 32) throw new Error('Refresh supports up to 32 client URLs per batch.');
  for (const value of urls) {
    let url: URL;
    try {
      url = new URL(value);
    } catch {
      throw new Error(`Invalid absolute client URL: ${value}`);
    }
    if (
      !/^https?:\/\//i.test(value) ||
      !['http:', 'https:'].includes(url.protocol) ||
      !url.hostname ||
      url.hostname.includes('*') ||
      url.username ||
      url.password ||
      url.hash ||
      /\s/.test(value)
    ) {
      throw new Error(
        `Refresh URLs must use HTTP or HTTPS, without credentials or a fragment: ${value}`,
      );
    }
  }
  return { urls, ...(siteId ? { site_id: siteId } : {}) };
}

// Entry metadata only carries host/path, not the original query or scheme. Show
// this suggestion in the explicit URL form before executing a refresh.
export function entryRefreshUrl(entry: Pick<CacheEntry, 'host' | 'path'>): string | undefined {
  if (
    !entry.host ||
    !entry.path?.startsWith('/') ||
    /[\s/\\?#@*]/.test(entry.host) ||
    /[\r\n?#\\]/.test(entry.path)
  )
    return;
  try {
    const base = new URL(`http://${entry.host}`);
    const target = new URL(`http://${entry.host}${entry.path}`);
    if (base.host !== target.host || base.pathname !== '/' || base.username || base.password)
      return;
    return `http://${entry.host}${entry.path}`;
  } catch {
    return;
  }
}
