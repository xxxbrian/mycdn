import { describe, expect, it } from 'vitest';
import { normalizeConfiguration, toWireConfiguration } from './configWire';
import { configurationFixture } from './test/fixtures';
import { normalizeCapabilities, normalizeExplain, normalizeRefresh } from './wire';

describe('generated-schema boundary', () => {
  it('normalizes nullable collections once and keeps discriminated branches and empty overrides', () => {
    const wire = configurationFixture();
    wire.sites = [
      {
        id: 's',
        name: 'Assets',
        enabled: true,
        default_origin: 'main',
        domains: null,
        origins: [{ id: 'main', name: 'Main', url: 'http://origin.example.com', protocol: 'h2c' }],
      },
    ];
    wire.rules = [
      {
        id: 'r',
        name: 'Identity',
        enabled: true,
        stage: 'ingress',
        steps: [
          {
            id: 'if',
            type: 'if',
            branches: [
              {
                id: 'branch',
                condition: { type: 'all' },
                steps: [
                  {
                    id: 'identity',
                    type: 'action',
                    action: { type: 'identity', identity: { client_ip_headers: [] } },
                  },
                ],
              },
              { id: 'else', condition: null, steps: null },
            ],
          },
        ],
      },
    ];
    wire.trusted_proxies = null;
    wire.defaults.identity.client_ip_headers = [];
    const domain = normalizeConfiguration(wire);
    expect(domain.sites[0].domains).toEqual([]);
    expect(domain.sites[0].origins[0].protocol).toBe('h2c');
    expect(domain.trusted_proxies).toEqual([]);
    const step = domain.rules[0].steps[0];
    if (step.type !== 'if') throw new Error('expected if');
    expect(step.branches[0].condition).toEqual({ type: 'all', children: [] });
    expect(step.branches[1].steps).toEqual([]);
    const saved = toWireConfiguration(domain);
    expect(saved.defaults.identity.client_ip_headers).toEqual([]);
    expect(
      saved.rules?.[0].steps?.[0].branches?.[0].steps?.[0].action?.identity?.client_ip_headers,
    ).toEqual([]);
  });
  it('rejects unknown narrowed values instead of casting generated strings into enums', () => {
    const protocol = configurationFixture();
    protocol.defaults.upstream.protocol = 'experimental';
    expect(() => normalizeConfiguration(protocol)).toThrow('Unsupported upstream protocol');
    const identity = configurationFixture();
    identity.defaults.identity.client_ip_headers = [{ name: 'X-IP', format: 'guess' }];
    expect(() => normalizeConfiguration(identity)).toThrow('Unsupported client IP header format');
  });
  it('requires complete outgoing fields without filling in guessed zero/false defaults', () => {
    const document = normalizeConfiguration(configurationFixture());
    document.defaults.request!.allow_upgrade = false;
    document.defaults.upstream!.timeouts!.request_ms = 0;
    expect(toWireConfiguration(document).defaults.request.allow_upgrade).toBe(false);
    expect(toWireConfiguration(document).defaults.upstream.timeouts.request_ms).toBe(0);
    document.storage.max_readers = undefined;
    expect(() => toWireConfiguration(document)).toThrow('storage.max_readers is required');
  });
  it('normalizes nullable catalog, explain headers/trace and refresh results', () => {
    expect(
      normalizeCapabilities({ actions: null, fields: null, stages: null, schemas: {} }),
    ).toEqual({ actions: [], fields: [], stages: [], schemas: {} });
    const explain = normalizeExplain({
      authority: 'origin.example.com',
      cache_key: 'k',
      cache_policy: configurationFixture().defaults.cache,
      client_ip: '192.0.2.1',
      http_version: 'HTTP/2.0',
      origin_id: 'o',
      origin_url: 'http://origin.example.com/',
      peer_ip: '127.0.0.1',
      protocol: 'h2c',
      scheme: 'http',
      site_id: 's',
      trace: null,
      response_headers: { 'X-Empty': null },
    });
    expect(explain.trace).toEqual([]);
    expect(explain.response_headers).toEqual({ 'X-Empty': [] });
    expect(normalizeRefresh({ results: null }).results).toEqual([]);
  });
});
