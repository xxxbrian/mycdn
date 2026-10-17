import { describe, expect, it } from 'vitest';
import {
  detailRangeAvailable,
  queryFromParams,
  resetTimeZoom,
  timeWindowFromParams,
  zoomWindow,
} from './analytics';

describe('shared analytics query', () => {
  const now = new Date('2026-10-16T14:00:00Z');
  it('preserves every linked dimension in one query', () => {
    const query = queryFromParams(
      new URLSearchParams(
        'range=7d&host=cdn.example.com&ip=192.0.2.0%2F24&site=assets&status=200,304&cache=hit,coalesced&method=get&type=image&country=us&asn=AS13335&path=%2Fassets%2F',
      ),
      now,
      '2026-10-02T00:00:00Z',
    );
    expect(query.filters).toEqual({
      hosts: ['cdn.example.com'],
      client_ips: ['192.0.2.0/24'],
      site_ids: ['assets'],
      statuses: [200, 304],
      cache_results: ['HIT', 'COALESCED'],
      methods: ['GET'],
      content_types: ['image'],
      countries: ['US'],
      asns: [13335],
      path_prefix: '/assets/',
    });
    expect(Date.parse(query.to) - Date.parse(query.from)).toBe(7 * 86400000);
    expect(query.group_by).toContain('client_ip');
    expect(query.group_by).toContain('path');
  });
  it('bounds point count without truncating the requested range', () => {
    for (const range of ['7d', '14d']) {
      const query = queryFromParams(new URLSearchParams({ range }), now);
      expect(
        (Date.parse(query.to) - Date.parse(query.from)) / 1000 / query.interval_seconds!,
      ).toBeLessThanOrEqual(720);
    }
  });
  it('rejects invalid custom time windows and invalid numeric filters', () => {
    expect(() => queryFromParams(new URLSearchParams('range=custom&from=bad&to=bad'), now)).toThrow(
      'valid time range',
    );
    expect(() => queryFromParams(new URLSearchParams('status=abc'), now)).toThrow('Status filters');
    expect(() => queryFromParams(new URLSearchParams('asn=-1'), now)).toThrow('ASNs');
  });
  it('keeps a 30-day window usable with low-cardinality rollups and no hidden window change', () => {
    const query = queryFromParams(
      new URLSearchParams({ range: 'custom', from: '2026-09-16T14:00:00Z', to: now.toISOString() }),
      now,
      '2026-10-02T00:00:00Z',
    );
    expect(query.group_by).toEqual([
      'host',
      'content_type',
      'method',
      'country',
      'status',
      'cache_result',
      'asn',
    ]);
    expect(Date.parse(query.to) - Date.parse(query.from)).toBe(30 * 86400000);
    expect(detailRangeAvailable(query.from, '2026-10-02T00:00:00Z')).toBe(false);
    expect(Object.keys(query)).not.toContain('detailsAvailable');
  });
  it('uses the running three-day horizon rather than assuming a seven-day selection is raw', () => {
    const query = queryFromParams(new URLSearchParams('range=7d'), now, '2026-10-13T00:00:00Z');
    expect(query.group_by).not.toContain('client_ip');
    expect(query.group_by).not.toContain('path');
    expect(detailRangeAvailable(query.from, '2026-10-13T00:00:00Z')).toBe(false);
  });
  it('treats the reported UTC detail floor as inclusive, not a rolling-hours approximation', () => {
    const detailFrom = '2026-10-02T00:00:00Z';
    const params = new URLSearchParams({
      range: 'custom',
      from: detailFrom,
      to: now.toISOString(),
    });
    expect(queryFromParams(params, now, detailFrom).group_by).toContain('client_ip');
    expect(detailRangeAvailable(detailFrom, detailFrom)).toBe(true);
    params.set('from', '2026-10-01T23:59:59.999Z');
    expect(queryFromParams(params, now, detailFrom).group_by).not.toContain('client_ip');
    expect(detailRangeAvailable(params.get('from')!, detailFrom)).toBe(false);
    expect(detailRangeAvailable(detailFrom)).toBe(false);
  });
  it('does not remove genuine old IP/path filters just because rankings are unavailable', () => {
    const query = queryFromParams(
      new URLSearchParams({
        range: 'custom',
        from: '2026-09-16T14:00:00Z',
        to: now.toISOString(),
        ip: '192.0.2.0/24',
        path: '/assets/',
      }),
      now,
      '2026-10-02T00:00:00Z',
    );
    expect(query.filters).toEqual({ client_ips: ['192.0.2.0/24'], path_prefix: '/assets/' });
    expect(query.group_by).not.toContain('client_ip');
    expect(query.group_by).not.toContain('path');
  });

  it('keeps the slider base while zooming globally, widening and restoring the range', () => {
    const params = new URLSearchParams('range=7d&country=US');
    const base = timeWindowFromParams(params, now);
    const selected = zoomWindow(base, 25, 50)!;
    expect(zoomWindow(base, 0, 100)).toEqual(base);
    expect(zoomWindow(base, 25, 100)!.to).toEqual(base.to);
    params.set('zoom_from', selected.from);
    params.set('zoom_to', selected.to);
    params.set('zoom_base_from', base.from);
    params.set('zoom_base_to', base.to);
    const later = new Date(now.getTime() + 60000);
    expect(timeWindowFromParams(params, later)).toEqual(base);
    const query = queryFromParams(params, later);
    expect({ from: query.from, to: query.to }).toEqual(selected);
    expect(query.filters.countries).toEqual(['US']);
    const wider = zoomWindow(base, 10, 90)!;
    params.set('zoom_from', wider.from);
    params.set('zoom_to', wider.to);
    expect(queryFromParams(params, later).from).toEqual(wider.from);
    for (const key of Object.keys(resetTimeZoom)) params.delete(key);
    expect(queryFromParams(params, now).from).toEqual(base.from);
    expect(params.get('country')).toBe('US');
  });

  it('rejects zoom outside its base and ignores no meaningful movement', () => {
    const params = new URLSearchParams('range=7d&zoom_from=bad&zoom_to=bad');
    expect(() => queryFromParams(params, now)).toThrow('time zoom');
    const base = timeWindowFromParams(new URLSearchParams('range=today'), now);
    expect(zoomWindow(base, 70, 60)).toBeUndefined();
    expect(zoomWindow(base, 100, 101)).toBeUndefined();
    expect(zoomWindow(base, NaN, 50)).toBeUndefined();
    expect(
      zoomWindow(
        { from: now.toISOString(), to: new Date(now.getTime() + 1000).toISOString() },
        0,
        50,
      ),
    ).toBeUndefined();
  });
});
