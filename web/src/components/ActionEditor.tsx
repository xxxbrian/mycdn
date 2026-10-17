import type { Action, ActionCapability, Site } from '../types';
import { pretty } from '../lib/rules';
import { Alert, Field, Input, Select, Textarea } from './ui';
import {
  CacheKeyForm,
  CachePolicyForm,
  CorsForm,
  defaultCacheKey,
  IdentityForm,
  LimitsForm,
  MutationForm,
  OptionalNumber,
  UpstreamForm,
} from './SettingsForms';

export function newAction(type: string): Action {
  switch (type) {
    case 'cache':
      return { type, cache: {} };
    case 'cache_key':
      return { type, cache_key: { ...defaultCacheKey } };
    case 'origin':
      return { type, origin: '' };
    case 'path':
      return { type, path: '/' };
    case 'query':
      return { type, query: { operation: 'set', name: '', value: '' } };
    case 'cookie':
      return { type, cookie: { operation: 'set', name: '', value: '' } };
    case 'request_header':
    case 'source_header':
    case 'response_header':
      return { type, header: { operation: 'set', name: '', value: '' } };
    case 'authority':
      return { type, authority: '' };
    case 'upstream':
      return { type, upstream: {} };
    case 'identity':
      return { type, identity: {} };
    case 'limits':
      return { type, limits: {} };
    case 'browser_ttl':
      return { type, browser_ttl_seconds: 0 };
    case 'cors':
      return { type, cors: { origins: [], credentials: false } };
    case 'reply':
      return { type, reply: { status: 200, content_type: 'text/plain; charset=utf-8', body: '' } };
    case 'redirect':
      return { type, redirect: { status: 302, location: '' } };
    default:
      return { type };
  }
}
export default function ActionEditor({
  action,
  onChange,
  available,
  sites,
}: {
  action: Action;
  onChange: (action: Action) => void;
  available: ActionCapability[];
  sites: Site[];
}) {
  const capability = available.find((cap) => cap.type === action.type);
  const set = <K extends keyof Action>(key: K, value: Action[K]) =>
    onChange({ ...action, [key]: value });
  const origins = sites.flatMap((site) =>
    site.origins.map((origin) => ({ ...origin, siteName: site.name })),
  );
  let form;
  switch (action.type) {
    case 'cache':
      form = (
        <CachePolicyForm value={action.cache || {}} onChange={(cache) => set('cache', cache)} />
      );
      break;
    case 'cache_key':
      form = (
        <CacheKeyForm
          value={action.cache_key || defaultCacheKey}
          onChange={(key) => set('cache_key', key)}
        />
      );
      break;
    case 'origin':
      form = (
        <Field
          label="Named origin"
          hint="Origin IDs are site-local. Use a site condition; the selected ID must exist on each matched site."
        >
          <Select
            required
            value={action.origin || ''}
            onChange={(event) => set('origin', event.target.value)}
          >
            <option value="" disabled>
              Select origin
            </option>
            {!origins.some((origin) => origin.id === action.origin) && action.origin && (
              <option value={action.origin}>{action.origin} (not found)</option>
            )}
            {origins.map((origin) => (
              <option key={`${origin.siteName}/${origin.id}`} value={origin.id}>
                {origin.siteName} / {origin.name}
              </option>
            ))}
          </Select>
        </Field>
      );
      break;
    case 'path':
      form = (
        <Field
          label="Upstream path"
          hint="Absolute path only; query mutation is a separate action."
        >
          <Input
            required
            pattern="/.*"
            value={action.path || ''}
            placeholder="/new/path"
            onChange={(event) => set('path', event.target.value)}
          />
        </Field>
      );
      break;
    case 'query':
      form = (
        <MutationForm
          label="Query parameter"
          value={action.query || { operation: 'set', name: '' }}
          onChange={(value) => set('query', value)}
        />
      );
      break;
    case 'cookie':
      form = (
        <MutationForm
          label="Cookie"
          cookie
          value={action.cookie || { operation: 'set', name: '' }}
          onChange={(value) => set('cookie', value)}
        />
      );
      break;
    case 'request_header':
    case 'source_header':
    case 'response_header':
      form = (
        <>
          <MutationForm
            value={action.header || { operation: 'set', name: '' }}
            onChange={(value) => set('header', value)}
          />
          <p className="field-hint">
            {action.type === 'source_header'
              ? 'Changes the source response before storage.'
              : action.type === 'response_header'
                ? 'Scheduled delivery changes apply to each response, including HIT and STALE.'
                : 'Changes the assembled outbound request.'}{' '}
            Protocol-owned headers are protected.
          </p>
        </>
      );
      break;
    case 'authority':
      form = (
        <Field
          label="HTTP authority / Host"
          hint="Does not change the network dial target or TLS certificate verification name."
        >
          <Input
            required
            value={action.authority || ''}
            placeholder="origin.example.com"
            onChange={(event) => set('authority', event.target.value)}
          />
        </Field>
      );
      break;
    case 'upstream':
      form = (
        <UpstreamForm value={action.upstream || {}} onChange={(value) => set('upstream', value)} />
      );
      break;
    case 'identity':
      form = (
        <IdentityForm value={action.identity || {}} onChange={(value) => set('identity', value)} />
      );
      break;
    case 'limits':
      form = <LimitsForm value={action.limits || {}} onChange={(value) => set('limits', value)} />;
      break;
    case 'browser_ttl':
      form = (
        <OptionalNumber
          label="Browser TTL"
          value={action.browser_ttl_seconds}
          inherit={false}
          unit="s"
          onChange={(value) => set('browser_ttl_seconds', value)}
        />
      );
      break;
    case 'cors':
      form = (
        <CorsForm
          value={action.cors || { origins: [], credentials: false }}
          onChange={(value) => set('cors', value)}
        />
      );
      break;
    case 'reply': {
      const reply = action.reply || { status: 200, content_type: 'text/plain', body: '' };
      form = (
        <div className="form-grid two">
          <Field label="Response status">
            <Input
              type="number"
              min={200}
              max={599}
              required
              value={reply.status}
              onChange={(event) => set('reply', { ...reply, status: Number(event.target.value) })}
            />
          </Field>
          <Field label="Content type">
            <Input
              value={reply.content_type}
              placeholder="text/plain; charset=utf-8"
              onChange={(event) => set('reply', { ...reply, content_type: event.target.value })}
            />
          </Field>
          <Field
            label="Static response body"
            className="full-width"
            hint="Maximum 1 MiB. No response-body transformation engine."
          >
            <Textarea
              value={reply.body}
              onChange={(event) => set('reply', { ...reply, body: event.target.value })}
            />
          </Field>
        </div>
      );
      break;
    }
    case 'redirect': {
      const redirect = action.redirect || { status: 302, location: '' };
      form = (
        <div className="form-grid two">
          <Field label="Redirect status">
            <Select
              value={redirect.status}
              onChange={(event) =>
                set('redirect', { ...redirect, status: Number(event.target.value) })
              }
            >
              {[301, 302, 303, 307, 308].map((status) => (
                <option key={status}>{status}</option>
              ))}
            </Select>
          </Field>
          <Field label="Location">
            <Input
              required
              value={redirect.location}
              placeholder="https://example.com/new-path"
              onChange={(event) => set('redirect', { ...redirect, location: event.target.value })}
            />
          </Field>
        </div>
      );
      break;
    }
    default:
      form = (
        <Alert tone="warning">
          This UI version does not support this action. Existing parameters are preserved, not
          silently replaced.
        </Alert>
      );
  }
  return (
    <div className="action-editor">
      <div className="action-type">
        <Field label="Action">
          <Select value={action.type} onChange={(event) => onChange(newAction(event.target.value))}>
            {!capability && (
              <option value={action.type}>{pretty(action.type)} (unavailable in this stage)</option>
            )}
            {available.map((cap) => (
              <option key={cap.type} value={cap.type}>
                {cap.label || pretty(cap.type)}
              </option>
            ))}
          </Select>
        </Field>
        <p className="field-hint">
          {capability?.description ||
            (['cache', 'upstream', 'identity', 'limits'].includes(action.type)
              ? 'Only explicitly set fields change the inherited plan. False and zero are valid overrides.'
              : 'Typed action parameters are validated when the configuration is saved.')}
        </p>
      </div>
      {!capability && (
        <Alert tone="warning">
          This action is not advertised for the current stage. Select a compatible action or rule
          stage.
        </Alert>
      )}
      {form}
    </div>
  );
}
