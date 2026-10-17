import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import ConditionEditor from './ConditionEditor';
import StepEditor from './StepEditor';
import { IdentityForm, OptionalBoolean, OptionalNumber, UpstreamForm } from './SettingsForms';
import { fieldsAtStage } from '../lib/rules';
import type { Capabilities, FieldCapability, Step } from '../types';

const fields: FieldCapability[] = [
  {
    name: 'header',
    type: 'string',
    key: true,
    stages: ['request'],
    operators: ['eq', 'in', 'exists', 'not_exists'],
  },
];
describe('graphical editor contracts', () => {
  it('distinguishes an empty value from missing and does not allow multi-value equals', () => {
    const html = renderToStaticMarkup(
      <ConditionEditor
        condition={{ type: 'match', field: 'header', key: 'X-Flag', op: 'eq', values: [''] }}
        onChange={() => {}}
        fields={fields}
      />,
    );
    expect(html).toContain('value="X-Flag"');
    expect(html).toContain('aria-label="Condition value 1"');
    expect(html).toContain('Empty string is a value');
    expect(html).not.toContain('>Value</button>');
    const missing = renderToStaticMarkup(
      <ConditionEditor
        condition={{ type: 'match', field: 'header', key: 'X-Flag', op: 'not_exists', values: [] }}
        onChange={() => {}}
        fields={fields}
      />,
    );
    expect(missing).not.toContain('aria-label="Condition value 1"');
  });
  it('renders all-request conditions omitted by Go and recursive branch/action scopes', () => {
    const steps: Step[] = [
      {
        id: 'if',
        type: 'if',
        branches: [
          {
            id: 'b',
            condition: { type: 'all', children: [] },
            steps: [
              {
                id: 'nested',
                type: 'if',
                branches: [
                  {
                    id: 'nested-b',
                    condition: {
                      type: 'not',
                      children: [
                        {
                          type: 'match',
                          field: 'header',
                          key: 'X-Bypass',
                          op: 'exists',
                          values: [],
                        },
                      ],
                    },
                    steps: [
                      {
                        id: 'action',
                        type: 'action',
                        action: {
                          type: 'cache',
                          cache: { mode: 'force', ttl_seconds: 0, prefetch: false },
                        },
                      },
                    ],
                  },
                ],
              },
            ],
          },
          { id: 'else', condition: null, steps: [] },
        ],
      },
    ];
    const html = renderToStaticMarkup(
      <StepEditor
        steps={steps}
        onChange={() => {}}
        fields={fields}
        actions={[{ type: 'cache', parameter: 'cache', stages: ['request'] }]}
        sites={[]}
      />,
    );
    expect(html).toContain('All requests · explicit match-all');
    expect(html).toContain('Move branch 1 up');
    expect(html).toContain('Remove ELSE');
    expect(html).toContain('Force freshness TTL');
    expect(html).not.toContain('<form');
  });
  it('renders explicit false and zero instead of the inherit option', () => {
    expect(
      renderToStaticMarkup(<OptionalBoolean label="Prefetch" value={false} onChange={() => {}} />),
    ).toContain('value="false" selected=""');
    expect(
      renderToStaticMarkup(<OptionalNumber label="TTL" value={0} onChange={() => {}} />),
    ).toContain('value="0"');
  });
  it('renders preset, unset patch, and explicit peer-only identity modes distinctly', () => {
    const preset = renderToStaticMarkup(
      <IdentityForm
        patch={false}
        value={{ client_ip_source: 'trusted_x_forwarded_for', scheme_source: 'connection' }}
        onChange={() => {}}
      />,
    );
    expect(preset).toContain('Header list is unset; the selected preset is used.');
    const unset = renderToStaticMarkup(<IdentityForm value={{}} onChange={() => {}} />);
    expect(unset).toContain('Header-list override is unset');
    const peer = renderToStaticMarkup(
      <IdentityForm value={{ client_ip_headers: [] }} onChange={() => {}} />,
    );
    expect(peer).toContain('Peer-only');
    expect(peer).toContain('aria-pressed="true"');
    expect(peer).toContain('Inherit header list');
    const nullPreset = renderToStaticMarkup(
      <IdentityForm
        patch={false}
        value={{ client_ip_source: 'peer', scheme_source: 'connection', client_ip_headers: null }}
        onChange={() => {}}
      />,
    );
    expect(nullPreset).toContain('Header list is unset; the selected preset is used.');
  });
  it('renders ordered custom headers with every parser and keyboard reorder controls', () => {
    const html = renderToStaticMarkup(
      <IdentityForm
        value={{
          client_ip_headers: [
            { name: 'X-Real-IP', format: 'single' },
            { name: 'X-Forwarded-For', format: 'xff' },
            { name: 'Forwarded', format: 'forwarded' },
          ],
        }}
        onChange={() => {}}
      />,
    );
    expect(html.indexOf('value="X-Real-IP"')).toBeLessThan(html.indexOf('value="X-Forwarded-For"'));
    expect(html).toContain('value="single" selected=""');
    expect(html).toContain('value="xff" selected=""');
    expect(html).toContain('value="forwarded" selected=""');
    expect(html).toContain('Move IP header 2 up');
    expect(html).toContain('Move IP header 2 down');
  });
  it('offers HTTP-only h2c without substituting an upstream request deadline for zero', () => {
    const html = renderToStaticMarkup(
      <UpstreamForm value={{ protocol: 'h2c', timeouts: { request_ms: 0 } }} onChange={() => {}} />,
    );
    expect(html).toContain('value="h2c" selected=""');
    expect(html).toContain('HTTP/2 cleartext · HTTP only');
    expect(html).toContain('0 disables the request deadline');
    expect(html).toContain('value="0"');
  });
  it('uses the catalog for ingress peer/protocol fields and excludes circular client-IP fields', () => {
    const catalog: Capabilities = {
      actions: [],
      fields: [
        {
          name: 'peer_ip',
          type: 'ip',
          key: false,
          stages: ['ingress', 'request'],
          operators: ['eq', 'cidr'],
        },
        {
          name: 'http_version',
          type: 'string',
          key: false,
          stages: ['ingress', 'request'],
          operators: ['eq', 'in'],
        },
        {
          name: 'client_ip',
          type: 'ip',
          key: false,
          stages: ['request'],
          operators: ['eq', 'cidr'],
        },
      ],
    };
    const available = fieldsAtStage(catalog, 'ingress');
    expect(available.map((field) => field.name)).toEqual(['peer_ip', 'http_version']);
    const html = renderToStaticMarkup(
      <ConditionEditor
        condition={{ type: 'match', field: 'http_version', op: 'eq', values: ['HTTP/2.0'] }}
        fields={available}
        onChange={() => {}}
      />,
    );
    expect(html).toContain('value="peer_ip"');
    expect(html).toContain('value="http_version" selected=""');
    expect(html).not.toContain('value="client_ip"');
  });
});
