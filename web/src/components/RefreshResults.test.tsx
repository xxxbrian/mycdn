import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import RefreshResults, { refreshCounts } from './RefreshResults';

describe('refresh operation evidence', () => {
  it('renders exact server statuses and per-URL reasons/errors', () => {
    const results = [
      { url: 'http://cdn.example.com/a', status: 'refreshed' },
      {
        url: 'http://cdn.example.com/b',
        status: 'skipped',
        reason: 'current rules bypass the cache',
      },
      { url: 'http://cdn.example.com/c', status: 'error', error: 'origin TLS verification failed' },
    ];
    expect(refreshCounts(results)).toEqual({ refreshed: 1, skipped: 1, failed: 1, other: 0 });
    const html = renderToStaticMarkup(<RefreshResults result={{ results }} />);
    expect(html).toContain('1 refreshed · 1 skipped · 1 failed');
    expect(html).toContain('current rules bypass the cache');
    expect(html).toContain('origin TLS verification failed');
    expect(html).toContain('http://cdn.example.com/c');
  });
  it('does not invent success or statuses for empty or unknown results', () => {
    expect(renderToStaticMarkup(<RefreshResults result={{ results: [] }} />)).toContain(
      'No URL results returned',
    );
    const results = [{ url: 'http://cdn.example.com/a', status: 'pending-review' }];
    expect(refreshCounts(results)).toEqual({ refreshed: 0, skipped: 0, failed: 0, other: 1 });
    expect(renderToStaticMarkup(<RefreshResults result={{ results }} />)).toContain(
      'pending-review',
    );
  });
});
