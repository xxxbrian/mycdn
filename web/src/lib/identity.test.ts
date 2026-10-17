import { describe, expect, it } from 'vitest';
import { withClientIPHeaders } from './identity';
import type { ClientIPHeader, IdentitySettings } from '../types';

describe('ordered client-IP source overrides', () => {
  const preset: IdentitySettings = {
    client_ip_source: 'trusted_x_forwarded_for',
    scheme_source: 'connection',
  };
  it('keeps an unset preset distinct from an explicit peer-only list', () => {
    const peerOnly = withClientIPHeaders(preset, []);
    expect(JSON.parse(JSON.stringify(peerOnly))).toEqual({ ...preset, client_ip_headers: [] });
    expect(withClientIPHeaders(peerOnly, undefined)).toEqual(preset);
    expect(preset).not.toHaveProperty('client_ip_headers');
  });
  it('preserves source order, parser formats, and sibling identity settings', () => {
    const headers: ClientIPHeader[] = [
      { name: 'X-Real-IP', format: 'single' },
      { name: 'X-Forwarded-For', format: 'xff' },
      { name: 'Forwarded', format: 'forwarded' },
    ];
    const initial = withClientIPHeaders(preset, headers);
    const reordered = withClientIPHeaders(initial, [headers[2], headers[0], headers[1]]);
    expect(JSON.parse(JSON.stringify(reordered)).client_ip_headers).toEqual([
      headers[2],
      headers[0],
      headers[1],
    ]);
    expect(reordered.scheme_source).toBe('connection');
    expect(reordered.client_ip_source).toBe('trusted_x_forwarded_for');
    expect(initial.client_ip_headers).toEqual(headers);
  });
  it('does not conflate null/unset with an empty list', () => {
    expect(withClientIPHeaders({ ...preset, client_ip_headers: null }, undefined)).toEqual(preset);
    expect(withClientIPHeaders({ ...preset, client_ip_headers: null }, [])).toEqual({
      ...preset,
      client_ip_headers: [],
    });
  });
});
