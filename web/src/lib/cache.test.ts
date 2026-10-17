import { describe, expect, it } from 'vitest';
import { cacheSelection, entryRefreshUrl, refreshInput } from './cache';

describe('bounded cache operations', () => {
  it('keeps exact purge selectors distinct from prefixes and explicit all', () => {
    expect(
      cacheSelection({ site_id: ' site-a ', host: '', path: '/assets/app.js', path_prefix: '' }),
    ).toEqual({ site_id: 'site-a', path: '/assets/app.js' });
    expect(cacheSelection({ site_id: ' ', host: '', path: '', path_prefix: '' })).toEqual({});
  });
  it('preserves explicit URLs and query punctuation, without accepting filters as a refresh', () => {
    expect(
      refreshInput(
        'https://cdn.example.com/a?v=1,2\nhttp://cdn.example.com/b\nhttps://cdn.example.com/a?v=1,2',
        'site-a',
      ),
    ).toEqual({
      urls: ['https://cdn.example.com/a?v=1,2', 'http://cdn.example.com/b'],
      site_id: 'site-a',
    });
    expect(refreshInput('http://cdn.example.com/', '')).toEqual({
      urls: ['http://cdn.example.com/'],
    });
  });
  it('rejects empty, relative, non-HTTP, credentialed or fragmented refresh URLs', () => {
    for (const value of [
      '',
      '/assets/app.js',
      'http:cdn.example.com/a',
      'ftp://cdn.example.com/a',
      'https://user:secret@cdn.example.com/a',
      'https://cdn.example.com/a#x',
      'https://*.example.com/a',
      'https://cdn.example.com/a b',
    ])
      expect(() => refreshInput(value, '')).toThrow();
    expect(() =>
      refreshInput(
        Array.from({ length: 33 }, (_, index) => `http://cdn.example.com/${index}`).join('\n'),
        '',
      ),
    ).toThrow('up to 32');
  });
  it('prepares an entry URL without treating a double-slash path as another host', () => {
    expect(entryRefreshUrl({ host: 'cdn.example.com', path: '//another.example/a%2Fb' })).toBe(
      'http://cdn.example.com//another.example/a%2Fb',
    );
    expect(entryRefreshUrl({ host: '[2001:db8::1]:8080', path: '/' })).toBe(
      'http://[2001:db8::1]:8080/',
    );
    for (const host of [
      'user@cdn.example.com',
      'cdn.example.com/path',
      'cdn.example.com?x=1',
      '*.example.com',
    ]) {
      expect(entryRefreshUrl({ host, path: '/' })).toBeUndefined();
    }
    expect(
      entryRefreshUrl({ host: 'cdn.example.com', path: '/a?missing-query=1' }),
    ).toBeUndefined();
    expect(entryRefreshUrl({ host: 'cdn.example.com' })).toBeUndefined();
  });
});
