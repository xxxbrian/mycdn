import { describe, expect, it } from 'vitest';
import { withResponseStatus } from './explain';
import type { ExplainInput } from '../types';

const request: ExplainInput = {
  method: 'GET',
  url: 'http://cdn.example.com/',
  peer: '127.0.0.1:1234',
  headers: {},
};
describe('simulated response form model', () => {
  it('stores the visible MISS default as soon as a response status is entered', () => {
    expect(withResponseStatus(request, 200).response).toEqual({
      status: 200,
      cache_result: 'MISS',
    });
    expect(request.response).toBeUndefined();
  });
  it('keeps explicit cache result and headers while status changes, and drops only a disabled response', () => {
    const input = {
      ...request,
      response: { status: 200, cache_result: 'HIT', headers: { 'Content-Type': ['text/css'] } },
    };
    expect(withResponseStatus(input, 304).response).toEqual({ ...input.response, status: 304 });
    expect(withResponseStatus(input, undefined).response).toBeUndefined();
    expect(input.response.cache_result).toBe('HIT');
  });
});
