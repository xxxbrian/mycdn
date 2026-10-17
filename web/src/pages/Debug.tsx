import { ArrowRight, Play, SearchCode } from 'lucide-react';
import { useState } from 'react';
import { api, errorMessage } from '../api';
import { useConfig } from '../config';
import type { ExplainInput, ExplainResult, HTTPVersion } from '../types';
import {
  Alert,
  Badge,
  Button,
  Empty,
  Field,
  Input,
  Loading,
  PageHeading,
  Panel,
  Select,
} from '../components/ui';
import { KeyValueLines } from '../components/SettingsForms';
import { withResponseStatus } from '../lib/explain';

export default function Debug() {
  const { draft, dirty } = useConfig();
  const [input, setInput] = useState<ExplainInput>({
    method: 'GET',
    url: '',
    peer: '127.0.0.1:12345',
    headers: {},
  });
  const [result, setResult] = useState<ExplainResult>();
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  async function run(event: React.FormEvent) {
    event.preventDefault();
    setLoading(true);
    setError('');
    setResult(undefined);
    try {
      setResult(await api.explain(input));
    } catch (error) {
      setError(errorMessage(error));
    } finally {
      setLoading(false);
    }
  }
  return (
    <>
      <PageHeading
        title="Request debugger"
        eyebrow="EXPLAIN / REAL EXECUTOR"
        description="Trace a request through the actual rule engine without contacting an origin."
      />
      {dirty && (
        <Alert tone="warning">
          Explain uses the saved server configuration. Save your draft first to debug its changes.
        </Alert>
      )}
      <div className="debug-layout">
        <Panel
          title="Simulated request"
          subtitle="The peer address determines whether forwarded identity can be trusted."
        >
          <form onSubmit={run}>
            <div className="form-grid">
              <Field label="Method">
                <Select
                  value={input.method}
                  onChange={(event) => setInput({ ...input, method: event.target.value })}
                >
                  {['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS', 'CONNECT'].map(
                    (method) => (
                      <option key={method}>{method}</option>
                    ),
                  )}
                </Select>
              </Field>
              <Field label="Client HTTP version">
                <Select
                  value={input.http_version || 'HTTP/1.1'}
                  onChange={(event) =>
                    setInput({ ...input, http_version: event.target.value as HTTPVersion })
                  }
                >
                  {['HTTP/1.0', 'HTTP/1.1', 'HTTP/2.0', 'HTTP/3.0'].map((version) => (
                    <option key={version}>{version}</option>
                  ))}
                </Select>
              </Field>
              <Field
                label="Absolute URL"
                hint="Use a configured site host, not an upstream origin address. Explain uses the saved routing configuration."
              >
                <Input
                  required
                  type="url"
                  value={input.url}
                  placeholder={`https://${draft?.sites[0]?.domains[0] || 'cdn.example.com'}/assets/app.js`}
                  onChange={(event) => setInput({ ...input, url: event.target.value })}
                />
              </Field>
              <Field label="Host override" hint="Optional; defaults to the URL host.">
                <Input
                  value={input.host || ''}
                  placeholder="cdn.example.com"
                  onChange={(event) =>
                    setInput({ ...input, host: event.target.value || undefined })
                  }
                />
              </Field>
              <Field label="Network peer (IP:port)">
                <Input
                  required
                  value={input.peer}
                  onChange={(event) => setInput({ ...input, peer: event.target.value })}
                />
              </Field>
              <KeyValueLines
                label="Request headers"
                hint="One Name: value per line. Repeated names are preserved."
                value={input.headers}
                onChange={(headers) => setInput({ ...input, headers })}
              />
              <Field
                label="Simulated upstream status"
                hint="Optional. Runs origin_response rules without an origin call."
              >
                <Input
                  type="number"
                  min={200}
                  max={599}
                  value={input.response?.status ?? ''}
                  placeholder="No simulated response"
                  onChange={(event) =>
                    setInput(
                      withResponseStatus(
                        input,
                        event.target.value ? Number(event.target.value) : undefined,
                      ),
                    )
                  }
                />
              </Field>
              {input.response && (
                <>
                  <Field label="Simulated cache result">
                    <Select
                      value={input.response.cache_result}
                      onChange={(event) =>
                        setInput({
                          ...input,
                          response: { ...input.response!, cache_result: event.target.value },
                        })
                      }
                    >
                      {['MISS', 'HIT', 'STALE', 'REVALIDATED', 'COALESCED', 'BYPASS'].map(
                        (value) => (
                          <option key={value}>{value}</option>
                        ),
                      )}
                    </Select>
                  </Field>
                  <KeyValueLines
                    label="Simulated upstream headers"
                    value={input.response.headers || {}}
                    onChange={(headers) =>
                      setInput({ ...input, response: { ...input.response!, headers } })
                    }
                  />
                </>
              )}
            </div>
            <Button type="submit" variant="primary" disabled={loading}>
              <Play size={13} />
              Explain request
            </Button>
          </form>
        </Panel>
        <div>
          {error && <Alert>{error}</Alert>}
          {loading ? (
            <Panel>
              <Loading label="Running the real executor…" />
            </Panel>
          ) : !result ? (
            <Panel>
              <Empty
                title="Make the execution visible"
                description="Enter a request and inspect matched branches, action changes, selected origin, and cache identity."
                icon={<SearchCode size={32} />}
              />
            </Panel>
          ) : (
            <>
              <Panel title="Execution plan" action={<Badge>NO ORIGIN CALL</Badge>}>
                <dl className="key-values">
                  <div>
                    <dt>Site</dt>
                    <dd>
                      {draft?.sites.find((site) => site.id === result.site_id)?.name ||
                        result.site_id ||
                        '—'}
                    </dd>
                  </div>
                  <div>
                    <dt>Origin URL</dt>
                    <dd>{result.origin_url || '—'}</dd>
                  </div>
                  <div>
                    <dt>HTTP authority</dt>
                    <dd>{result.authority || '—'}</dd>
                  </div>
                  <div>
                    <dt>Upstream protocol</dt>
                    <dd>{result.protocol || '—'}</dd>
                  </div>
                  <div>
                    <dt>Cache key</dt>
                    <dd>{result.cache_key || '—'}</dd>
                  </div>
                  <div>
                    <dt>Cache policy</dt>
                    <dd>
                      {result.cache_policy
                        ? `${result.cache_policy.mode} · TTL ${result.cache_policy.ttl_seconds}s · SWR ${result.cache_policy.stale_while_revalidate_seconds}s · SIE ${result.cache_policy.stale_if_error_seconds}s`
                        : '—'}
                    </dd>
                  </div>
                  <div>
                    <dt>Client IP</dt>
                    <dd>{result.client_ip || '—'}</dd>
                  </div>
                  <div>
                    <dt>Socket peer IP</dt>
                    <dd>{result.peer_ip || '—'}</dd>
                  </div>
                  <div>
                    <dt>Client HTTP version</dt>
                    <dd>{result.http_version || '—'}</dd>
                  </div>
                  <div>
                    <dt>Scheme</dt>
                    <dd>{result.scheme || '—'}</dd>
                  </div>
                </dl>
                {!!result.terminal_status && (
                  <Alert tone="info">Terminal response: {result.terminal_status}</Alert>
                )}
              </Panel>
              <Panel
                title="Rule execution trace"
                subtitle="Matches and value changes from the same executor used for proxy requests."
              >
                {!result.trace?.length ? (
                  <Empty
                    title="No rule trace events"
                    description="No configured rules were evaluated for this request."
                  />
                ) : (
                  <ol className="trace">
                    {result.trace.map((event, index) => (
                      <li key={index}>
                        <div className="trace-marker mono">
                          {String(index + 1).padStart(2, '0')}
                        </div>
                        <div className="trace-content">
                          <div className="trace-heading">
                            <Badge>{event.stage}</Badge>
                            <strong>
                              {draft?.rules.find((rule) => rule.id === event.rule_id)?.name ||
                                event.rule_id ||
                                event.action ||
                                'Execution step'}
                            </strong>
                            {event.matched !== undefined && (
                              <Badge tone={event.matched ? 'accent' : ''}>
                                {event.matched ? 'MATCH' : 'NO MATCH'}
                              </Badge>
                            )}
                          </div>
                          {event.action && <p className="mono text-small">{event.action}</p>}
                          {(event.before !== undefined || event.after !== undefined) && (
                            <div className="trace-change">
                              <strong className="mono">{event.action || 'Plan change'}</strong>
                              <span className="mono muted">{event.before || 'unset'}</span>
                              <ArrowRight size={12} />
                              <span className="mono">{event.after || 'unset'}</span>
                            </div>
                          )}
                        </div>
                      </li>
                    ))}
                  </ol>
                )}
              </Panel>
              <Panel title="Resulting delivery headers">
                <dl className="key-values">
                  {Object.entries(result.response_headers || {}).map(([name, values]) => (
                    <div key={name}>
                      <dt className="mono">{name}</dt>
                      <dd>{values.join(', ')}</dd>
                    </div>
                  ))}
                </dl>
                {!Object.keys(result.response_headers || {}).length && (
                  <p className="muted text-small">No response headers were produced.</p>
                )}
              </Panel>
            </>
          )}
        </div>
      </div>
    </>
  );
}
