import { Plus, Trash2 } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import type {
  CacheKey,
  CachePolicy,
  ClientIPHeader,
  CorsSettings,
  HeaderOperation,
  IdentitySettings,
  RequestSettings,
  UpstreamSettings,
} from '../types';
import { splitLines, uid } from '../lib/format';
import { MAX_IP_HEADERS, withClientIPHeaders } from '../lib/identity';
import { Button, CheckField, Field, IconButton, Input, Select, SortableList, Textarea } from './ui';

export const defaultCacheKey: CacheKey = { include_host: true, query_mode: 'all' };
export function OptionalNumber({
  label,
  value,
  onChange,
  unit,
  min = 0,
  max,
  hint,
  inherit = true,
}: {
  label: string;
  value?: number;
  onChange: (value: number | undefined) => void;
  unit?: string;
  min?: number;
  max?: number;
  hint?: string;
  inherit?: boolean;
}) {
  return (
    <Field label={label} hint={hint}>
      <div className="input-unit">
        <Input
          type="number"
          min={min}
          max={max}
          required={!inherit}
          value={value ?? ''}
          placeholder={inherit ? 'Inherit' : 'Required'}
          onChange={(event) =>
            onChange(event.target.value === '' ? undefined : Number(event.target.value))
          }
        />
        {unit && <span>{unit}</span>}
      </div>
    </Field>
  );
}
export function OptionalBoolean({
  label,
  value,
  onChange,
  hint,
  inherit = true,
}: {
  label: string;
  value?: boolean;
  onChange: (value: boolean | undefined) => void;
  hint?: string;
  inherit?: boolean;
}) {
  return (
    <Field label={label} hint={hint}>
      <Select
        required={!inherit}
        value={value === undefined ? '' : String(value)}
        onChange={(event) =>
          onChange(event.target.value === '' ? undefined : event.target.value === 'true')
        }
      >
        {inherit && <option value="">Inherit</option>}
        <option value="true">Enabled</option>
        <option value="false">Disabled</option>
      </Select>
    </Field>
  );
}
export function StringList({
  label,
  value,
  onChange,
  hint,
  placeholder,
  multiline = false,
  required = false,
  className = '',
}: {
  label: string;
  value?: string[];
  onChange: (value: string[] | undefined) => void;
  hint?: string;
  placeholder?: string;
  multiline?: boolean;
  required?: boolean;
  className?: string;
}) {
  const signature = JSON.stringify(value || []);
  const emitted = useRef(signature);
  const [text, setText] = useState((value || []).join(multiline ? '\n' : ', '));
  useEffect(() => {
    if (signature !== emitted.current) setText((value || []).join(multiline ? '\n' : ', '));
    emitted.current = signature;
  }, [signature, multiline]);
  const change = (text: string) => {
    setText(text);
    const next = text ? splitLines(text) : undefined;
    emitted.current = JSON.stringify(next || []);
    onChange(next);
  };
  return (
    <Field label={label} hint={hint} className={className}>
      {multiline ? (
        <Textarea
          value={text}
          required={required}
          placeholder={placeholder || 'None'}
          onChange={(event) => change(event.target.value)}
        />
      ) : (
        <Input
          value={text}
          required={required}
          placeholder={placeholder || 'None'}
          onChange={(event) => change(event.target.value)}
        />
      )}
    </Field>
  );
}
export function CachePolicyForm({
  value,
  onChange,
  patch = true,
}: {
  value: CachePolicy;
  onChange: (value: CachePolicy) => void;
  patch?: boolean;
}) {
  const set = <K extends keyof CachePolicy>(key: K, next: CachePolicy[K]) =>
    onChange({ ...value, [key]: next });
  return (
    <>
      <div className="form-grid three">
        <Field label="Caching mode">
          <Select
            required={!patch}
            value={value.mode || ''}
            onChange={(event) =>
              set('mode', (event.target.value || undefined) as CachePolicy['mode'])
            }
          >
            {patch && <option value="">Inherit</option>}
            <option value="respect">Respect origin headers</option>
            <option value="force">Force freshness TTL</option>
            <option value="bypass">Bypass object cache</option>
          </Select>
        </Field>
        <OptionalNumber
          label="Fresh TTL"
          value={value.ttl_seconds}
          onChange={(v) => set('ttl_seconds', v)}
          unit="s"
          inherit={patch}
        />
        <OptionalBoolean
          label="Prefetch / background refresh"
          value={value.prefetch}
          onChange={(v) => set('prefetch', v)}
          inherit={patch}
        />
        <OptionalNumber
          label="Stale while revalidate"
          value={value.stale_while_revalidate_seconds}
          onChange={(v) => set('stale_while_revalidate_seconds', v)}
          unit="s"
          inherit={patch}
        />
        <OptionalNumber
          label="Stale if error"
          value={value.stale_if_error_seconds}
          onChange={(v) => set('stale_if_error_seconds', v)}
          unit="s"
          inherit={patch}
        />
      </div>
      <div className="status-ttl">
        <div className="item-heading">
          <span className="field-label">Per-status freshness overrides</span>
          {patch && (
            <Button
              variant="ghost"
              onClick={() => set('status_ttl', value.status_ttl === undefined ? [] : undefined)}
            >
              {value.status_ttl === undefined ? 'Override list' : 'Inherit list'}
            </Button>
          )}
        </div>
        {value.status_ttl?.map((entry, index) => (
          <div key={index} className="status-ttl-row">
            <Field label="HTTP status">
              <Input
                type="number"
                required
                min={200}
                max={599}
                value={entry.status}
                onChange={(event) =>
                  set(
                    'status_ttl',
                    value.status_ttl!.map((item, i) =>
                      i === index ? { ...item, status: Number(event.target.value) } : item,
                    ),
                  )
                }
              />
            </Field>
            <OptionalNumber
              label="Fresh TTL"
              value={entry.ttl_seconds}
              inherit={false}
              onChange={(ttl_seconds) =>
                set(
                  'status_ttl',
                  value.status_ttl!.map((item, i) =>
                    i === index ? { ...item, ttl_seconds: ttl_seconds ?? 0 } : item,
                  ),
                )
              }
              unit="s"
            />
            <IconButton
              label={`Remove status TTL ${index + 1}`}
              onClick={() =>
                set(
                  'status_ttl',
                  value.status_ttl!.filter((_, i) => i !== index),
                )
              }
            >
              <Trash2 size={13} />
            </IconButton>
          </div>
        ))}
        {(!patch || value.status_ttl !== undefined) && (
          <Button
            disabled={(value.status_ttl?.length || 0) >= 32}
            onClick={() =>
              set('status_ttl', [...(value.status_ttl || []), { status: 200, ttl_seconds: 60 }])
            }
          >
            <Plus size={12} />
            Status TTL
          </Button>
        )}
        {patch && (
          <p className="field-hint">
            {value.status_ttl === undefined
              ? 'No list override; existing status policy is inherited.'
              : value.status_ttl.length
                ? 'This list replaces the inherited status policy.'
                : 'Explicit empty list clears inherited status overrides.'}
          </p>
        )}
      </div>
    </>
  );
}
export function CacheKeyForm({
  value,
  onChange,
}: {
  value: CacheKey;
  onChange: (value: CacheKey) => void;
}) {
  const set = <K extends keyof CacheKey>(key: K, next: CacheKey[K]) =>
    onChange({ ...value, [key]: next });
  return (
    <>
      <p className="form-note">
        Select the request inputs that distinguish cached content. Site isolation is always
        retained.
      </p>
      <div className="form-grid three">
        <Field label="Namespace">
          <Input
            value={value.namespace || ''}
            placeholder="Optional namespace"
            onChange={(event) => set('namespace', event.target.value || undefined)}
          />
        </Field>
        <Field label="Query identity">
          <Select
            value={value.query_mode}
            onChange={(event) => set('query_mode', event.target.value as CacheKey['query_mode'])}
          >
            <option value="all">Include all query parameters</option>
            <option value="ignore">Ignore query string</option>
            <option value="include">Include named parameters only</option>
            <option value="exclude">Exclude named parameters</option>
          </Select>
        </Field>
        {['include', 'exclude'].includes(value.query_mode) && (
          <StringList
            label="Query parameter names"
            value={value.query_names}
            onChange={(v) => set('query_names', v)}
            placeholder="width, format"
          />
        )}
        <StringList
          label="Vary by request headers"
          value={value.headers}
          onChange={(v) => set('headers', v)}
          placeholder="Accept-Language"
        />
        <StringList
          label="Vary by cookies"
          value={value.cookies}
          onChange={(v) => set('cookies', v)}
        />
        <CheckField
          label="Include request host"
          checked={value.include_host}
          onChange={(v) => set('include_host', v)}
          hint="Disable only for intentionally shared aliases within a site."
        />
      </div>
    </>
  );
}
export function UpstreamForm({
  value,
  onChange,
  patch = true,
}: {
  value: UpstreamSettings;
  onChange: (value: UpstreamSettings) => void;
  patch?: boolean;
}) {
  const times = value.timeouts || {};
  const setTime = (key: keyof typeof times, next: number | undefined) =>
    onChange({ ...value, timeouts: { ...times, [key]: next } });
  return (
    <div className="form-grid three">
      <Field
        label="Upstream protocol"
        hint="Automatic: HTTPS H2/H1, HTTP H1. h2c requires an HTTP origin."
      >
        <Select
          required={!patch}
          value={value.protocol || ''}
          onChange={(event) =>
            onChange({
              ...value,
              protocol: (event.target.value || undefined) as UpstreamSettings['protocol'],
            })
          }
        >
          {patch && <option value="">Inherit</option>}
          <option value="auto">Automatic</option>
          <option value="h1">HTTP/1.1</option>
          <option value="h2">HTTP/2 · HTTPS</option>
          <option value="h2c">HTTP/2 cleartext · HTTP only</option>
          <option value="h3">HTTP/3 · HTTPS</option>
        </Select>
      </Field>
      <OptionalNumber
        label="Connect timeout"
        value={times.connect_ms}
        onChange={(v) => setTime('connect_ms', v)}
        unit="ms"
        inherit={patch}
        max={86400000}
      />
      <OptionalNumber
        label="TLS handshake timeout"
        value={times.tls_handshake_ms}
        onChange={(v) => setTime('tls_handshake_ms', v)}
        unit="ms"
        inherit={patch}
        max={86400000}
      />
      <OptionalNumber
        label="Response header timeout"
        value={times.response_header_ms}
        onChange={(v) => setTime('response_header_ms', v)}
        unit="ms"
        inherit={patch}
        max={86400000}
      />
      <OptionalNumber
        label="Idle connection timeout"
        value={times.idle_ms}
        onChange={(v) => setTime('idle_ms', v)}
        unit="ms"
        inherit={patch}
        max={86400000}
      />
      <OptionalNumber
        label="Whole upstream request budget"
        value={times.request_ms}
        onChange={(v) => setTime('request_ms', v)}
        unit="ms"
        inherit={patch}
        max={86400000}
        hint="0 disables the request deadline. Cache fills have a separate budget."
      />
    </div>
  );
}
export function IdentityForm({
  value,
  onChange,
  patch = true,
}: {
  value: IdentitySettings;
  onChange: (value: IdentitySettings) => void;
  patch?: boolean;
}) {
  const headers = value.client_ip_headers;
  const overridden = headers != null;
  const identities = useRef(new WeakMap<ClientIPHeader, string>());
  const previousOverride = useRef<ClientIPHeader[] | undefined>(undefined);
  function keyFor(header: ClientIPHeader) {
    let key = identities.current.get(header);
    if (!key) {
      key = uid();
      identities.current.set(header, key);
    }
    return key;
  }
  function changeHeaders(next: ClientIPHeader[] | undefined) {
    if (next === undefined && headers != null) previousOverride.current = headers;
    if (next !== undefined) previousOverride.current = next;
    onChange(withClientIPHeaders(value, next));
  }
  return (
    <>
      <div className="form-grid two">
        <Field
          label="Client IP preset"
          hint={
            overridden
              ? 'The ordered list below overrides this preset.'
              : 'Header sources require a trusted proxy peer.'
          }
        >
          <Select
            value={value.client_ip_source || ''}
            required={!patch && !overridden}
            disabled={overridden}
            onChange={(event) =>
              onChange({
                ...value,
                client_ip_source: (event.target.value ||
                  undefined) as IdentitySettings['client_ip_source'],
              })
            }
          >
            {(patch || !value.client_ip_source) && (
              <option value="">{patch ? 'Inherit' : 'No preset'}</option>
            )}
            <option value="peer">Network peer</option>
            <option value="trusted_x_forwarded_for">Trusted X-Forwarded-For</option>
            <option value="trusted_forwarded">Trusted Forwarded</option>
          </Select>
        </Field>
        <Field label="Client scheme source">
          <Select
            value={value.scheme_source || ''}
            required={!patch}
            onChange={(event) =>
              onChange({
                ...value,
                scheme_source: (event.target.value ||
                  undefined) as IdentitySettings['scheme_source'],
              })
            }
          >
            {patch && <option value="">Inherit</option>}
            <option value="connection">Connection</option>
            <option value="trusted_x_forwarded_proto">Trusted X-Forwarded-Proto</option>
            <option value="trusted_forwarded">Trusted Forwarded</option>
          </Select>
        </Field>
      </div>
      <div className="status-ttl">
        <div className="item-heading">
          <span className="field-label">Ordered client IP headers</span>
          <Button
            onClick={() => changeHeaders(overridden ? undefined : previousOverride.current || [])}
            aria-pressed={overridden}
          >
            {overridden ? (patch ? 'Inherit header list' : 'Use preset') : 'Override header list'}
          </Button>
        </div>
        {overridden ? (
          <>
            <p className="form-note">
              {headers.length
                ? 'First valid header wins; otherwise use the peer IP.'
                : 'Peer-only · no header sources. Add a source to use a forwarded IP.'}
            </p>
            <SortableList
              label="IP header"
              items={headers.map((header) => ({ id: keyFor(header), header }))}
              onChange={(items) => changeHeaders(items.map((item) => item.header))}
              render={(item, index) => (
                <div className="ip-header-row">
                  <Field label={`Header ${index + 1}`}>
                    <Input
                      required
                      pattern={"[!#$%&'*+.^_`\\|~0-9A-Za-z\\-]+"}
                      value={item.header.name}
                      placeholder="X-Real-IP"
                      onChange={(event) => {
                        const next = { ...item.header, name: event.target.value };
                        identities.current.set(next, item.id);
                        changeHeaders(headers.map((current, i) => (i === index ? next : current)));
                      }}
                    />
                  </Field>
                  <Field label="Format">
                    <Select
                      value={item.header.format}
                      onChange={(event) => {
                        const next = {
                          ...item.header,
                          format: event.target.value as ClientIPHeader['format'],
                        };
                        identities.current.set(next, item.id);
                        changeHeaders(headers.map((current, i) => (i === index ? next : current)));
                      }}
                    >
                      <option value="single">Single IP</option>
                      <option value="xff">X-Forwarded-For chain</option>
                      <option value="forwarded">Forwarded elements</option>
                    </Select>
                  </Field>
                  <IconButton
                    label={`Remove IP header ${index + 1}`}
                    onClick={() => changeHeaders(headers.filter((_, i) => i !== index))}
                  >
                    <Trash2 size={13} />
                  </IconButton>
                </div>
              )}
            />
            <Button
              disabled={headers.length >= MAX_IP_HEADERS}
              onClick={() => changeHeaders([...headers, { name: '', format: 'single' }])}
            >
              <Plus size={12} />
              Add IP header
            </Button>
            <p className="field-hint">
              For Single IP, the trusted proxy must overwrite the header. Chain formats remove
              trusted hops from right to left.
            </p>
          </>
        ) : (
          <p className="field-hint">
            {patch
              ? 'Header-list override is unset; the current plan is inherited.'
              : 'Header list is unset; the selected preset is used.'}
          </p>
        )}
      </div>
    </>
  );
}
export function LimitsForm({
  value,
  onChange,
  patch = true,
}: {
  value: RequestSettings;
  onChange: (value: RequestSettings) => void;
  patch?: boolean;
}) {
  return (
    <div className="form-grid two">
      <OptionalNumber
        label="Maximum upload"
        value={value.max_upload_bytes}
        onChange={(max_upload_bytes) => onChange({ ...value, max_upload_bytes })}
        unit="bytes"
        inherit={patch}
      />
      <OptionalBoolean
        label="Allow protocol upgrades"
        value={value.allow_upgrade}
        onChange={(allow_upgrade) => onChange({ ...value, allow_upgrade })}
        inherit={patch}
      />
    </div>
  );
}
export function CorsForm({
  value,
  onChange,
}: {
  value: CorsSettings;
  onChange: (value: CorsSettings) => void;
}) {
  return (
    <div className="form-grid three">
      <StringList
        label="Allowed origins"
        value={value.origins}
        onChange={(origins) => onChange({ ...value, origins: origins || [] })}
        placeholder="https://app.example.com"
      />
      <StringList
        label="Allowed methods"
        value={value.methods}
        onChange={(methods) => onChange({ ...value, methods })}
        placeholder="GET, HEAD, OPTIONS"
      />
      <StringList
        label="Allowed request headers"
        value={value.headers}
        onChange={(headers) => onChange({ ...value, headers })}
      />
      <StringList
        label="Exposed response headers"
        value={value.expose}
        onChange={(expose) => onChange({ ...value, expose })}
      />
      <OptionalNumber
        label="Preflight max age"
        value={value.max_age_seconds}
        onChange={(max_age_seconds) => onChange({ ...value, max_age_seconds })}
        unit="s"
      />
      <CheckField
        label="Allow credentials"
        checked={value.credentials}
        onChange={(credentials) => onChange({ ...value, credentials })}
        hint="A wildcard origin cannot be used with credentials."
      />
    </div>
  );
}
export function MutationForm({
  value,
  onChange,
  label = 'Header',
  cookie = false,
}: {
  value: HeaderOperation;
  onChange: (value: HeaderOperation) => void;
  label?: string;
  cookie?: boolean;
}) {
  return (
    <div className="form-grid three">
      <Field label="Operation">
        <Select
          value={value.operation}
          onChange={(event) =>
            onChange({ ...value, operation: event.target.value as HeaderOperation['operation'] })
          }
        >
          <option value="set">Set</option>
          {!cookie && <option value="add">Add</option>}
          <option value="delete">Delete</option>
        </Select>
      </Field>
      <Field label={`${label} name`}>
        <Input
          required
          value={value.name}
          placeholder={label === 'Header' ? 'X-Example' : 'Name'}
          onChange={(event) => onChange({ ...value, name: event.target.value })}
        />
      </Field>
      <Field label="Value">
        <Input
          disabled={value.operation === 'delete'}
          value={value.value || ''}
          onChange={(event) => onChange({ ...value, value: event.target.value })}
        />
      </Field>
    </div>
  );
}
export function HeadersForm({
  value,
  onChange,
  label = 'header',
}: {
  value: HeaderOperation[];
  onChange: (value: HeaderOperation[]) => void;
  label?: string;
}) {
  function set(index: number, update: Partial<HeaderOperation>) {
    onChange(value.map((entry, i) => (i === index ? { ...entry, ...update } : entry)));
  }
  return (
    <>
      <div className="header-rows">
        {value.map((entry, index) => (
          <div className="header-row" key={index}>
            <Field label="Operation">
              <Select
                value={entry.operation}
                onChange={(event) =>
                  set(index, { operation: event.target.value as HeaderOperation['operation'] })
                }
              >
                <option value="set">Set</option>
                <option value="add">Add</option>
                <option value="delete">Delete</option>
              </Select>
            </Field>
            <Field label="Header name">
              <Input
                required
                value={entry.name}
                placeholder="X-Example"
                onChange={(event) => set(index, { name: event.target.value })}
              />
            </Field>
            <Field label="Value">
              <Input
                disabled={entry.operation === 'delete'}
                value={entry.value || ''}
                onChange={(event) => set(index, { value: event.target.value })}
              />
            </Field>
            <IconButton
              label={`Remove ${label} ${index + 1}`}
              onClick={() => onChange(value.filter((_, i) => i !== index))}
            >
              <Trash2 size={14} />
            </IconButton>
          </div>
        ))}
      </div>
      <Button
        disabled={value.length >= 64}
        onClick={() => onChange([...value, { operation: 'set', name: '', value: '' }])}
      >
        <Plus size={13} />
        Add {label}
      </Button>
      <p className="field-hint">
        Framing headers, cache validators, and protocol-owned headers cannot be edited generically.
      </p>
    </>
  );
}
export function KeyValueLines({
  label,
  value,
  onChange,
  hint,
}: {
  label: string;
  value: Record<string, string[]>;
  onChange: (value: Record<string, string[]>) => void;
  hint?: string;
}) {
  const entries = Object.entries(value)
    .flatMap(([key, values]) => values.map((value) => `${key}: ${value}`))
    .join('\n');
  const emitted = useRef(entries);
  const [text, setText] = useState(entries);
  useEffect(() => {
    if (entries !== emitted.current) setText(entries);
    emitted.current = entries;
  }, [entries]);
  return (
    <Field label={label} hint={hint}>
      <Textarea
        value={text}
        placeholder="Accept: text/html"
        onChange={(event) => {
          const raw = event.target.value;
          setText(raw);
          const next: Record<string, string[]> = Object.create(null);
          let invalid = false;
          for (const line of raw.split('\n').filter((line) => line.trim())) {
            const index = line.indexOf(':');
            if (index < 1 || !/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(line.slice(0, index).trim())) {
              invalid = true;
              break;
            }
            const key = line.slice(0, index).trim();
            (next[key] ||= []).push(line.slice(index + 1).trim());
          }
          event.target.setCustomValidity(
            invalid ? 'Use one valid Header-Name: value per line.' : '',
          );
          if (!invalid) {
            emitted.current = Object.entries(next)
              .flatMap(([key, values]) => values.map((value) => `${key}: ${value}`))
              .join('\n');
            onChange(next);
          }
        }}
      />
    </Field>
  );
}
