import { Download, Upload } from 'lucide-react';
import { useRef, useState } from 'react';
import { useConfig } from '../config';
import { normalizeConfiguration } from '../wire';
import type { WireConfiguration } from '../configWire';
import type { AnalyticsSettings, Defaults, StorageSettings } from '../types';
import {
  Alert,
  Button,
  Disclosure,
  Field,
  Input,
  PageHeading,
  Panel,
  Tabs,
} from '../components/ui';
import {
  CacheKeyForm,
  CachePolicyForm,
  CorsForm,
  defaultCacheKey,
  HeadersForm,
  IdentityForm,
  LimitsForm,
  OptionalBoolean,
  OptionalNumber,
  StringList,
  UpstreamForm,
} from '../components/SettingsForms';
import Runtime from './Runtime';

const tabs = [
  { id: 'defaults', label: 'Global defaults' },
  { id: 'system', label: 'System & storage' },
  { id: 'analytics', label: 'Analytics' },
  { id: 'runtime', label: 'Runtime' },
];

export default function Settings() {
  const { draft, change } = useConfig();
  const [tab, setTab] = useState('defaults');
  const [importError, setImportError] = useState('');
  const file = useRef<HTMLInputElement>(null);
  if (!draft) return null;
  const defaults = draft.defaults;
  function setDefaults(patch: Partial<Defaults>) {
    change((config) => ({ ...config, defaults: { ...config.defaults, ...patch } }));
  }
  function setStorage<K extends keyof StorageSettings>(key: K, value: StorageSettings[K]) {
    change((config) => ({ ...config, storage: { ...config.storage, [key]: value } }));
  }
  function setAnalytics<K extends keyof AnalyticsSettings>(key: K, value: AnalyticsSettings[K]) {
    change((config) => ({ ...config, analytics: { ...config.analytics, [key]: value } }));
  }
  function exportConfig() {
    const url = URL.createObjectURL(
      new Blob([JSON.stringify(draft, null, 2)], { type: 'application/json' }),
    );
    const link = document.createElement('a');
    link.href = url;
    link.download = 'mycdn-config.json';
    link.click();
    URL.revokeObjectURL(url);
  }
  async function importConfig(selected: File | undefined) {
    if (!selected) return;
    try {
      const value = JSON.parse(await selected.text()) as WireConfiguration;
      if (
        !value ||
        typeof value !== 'object' ||
        !value.defaults ||
        !value.listen ||
        !value.storage ||
        !value.analytics ||
        (value.sites !== null && !Array.isArray(value.sites)) ||
        (value.rules !== null && !Array.isArray(value.rules)) ||
        (value.trusted_proxies !== null && !Array.isArray(value.trusted_proxies))
      )
        throw new Error('Not a complete mycdn configuration document.');
      if (
        !window.confirm(
          'Replace the current local draft? Nothing changes on the server until you save.',
        )
      )
        return;
      const normalized = normalizeConfiguration(value);
      change(() => normalized);
      setImportError('');
    } catch (error) {
      setImportError(error instanceof Error ? error.message : String(error));
    } finally {
      if (file.current) file.current.value = '';
    }
  }
  return (
    <>
      <PageHeading
        title="System settings"
        eyebrow="CONFIGURATION / DEFAULTS"
        description="Edit proxy defaults, storage budgets, and analytics settings."
        action={
          <div className="inline-actions">
            <Button onClick={exportConfig}>
              <Download size={14} />
              Export
            </Button>
            <Button onClick={() => file.current?.click()}>
              <Upload size={14} />
              Import
            </Button>
            <input
              hidden
              ref={file}
              type="file"
              accept="application/json,.json"
              onChange={(event) => void importConfig(event.target.files?.[0])}
            />
          </div>
        }
      />
      <Tabs items={tabs} value={tab} onChange={setTab} label="Settings sections" />
      {importError && <Alert>{importError}</Alert>}
      {tab === 'defaults' && (
        <>
          <Panel
            title="Cache policy"
            subtitle="Default caching behavior; matching rules can override it."
          >
            <CachePolicyForm
              patch={false}
              value={defaults.cache || {}}
              onChange={(cache) => setDefaults({ cache })}
            />
          </Panel>
          <Panel
            title="Cache identity"
            subtitle="The site always isolates content identity. Rules can replace these settings."
          >
            <CacheKeyForm
              value={defaults.cache_key || defaultCacheKey}
              onChange={(cache_key) => setDefaults({ cache_key })}
            />
          </Panel>
          <Panel title="Origin communication" subtitle="Default protocol and connection timeouts.">
            <UpstreamForm
              patch={false}
              value={defaults.upstream || {}}
              onChange={(upstream) => setDefaults({ upstream })}
            />
          </Panel>
          <Panel
            title="Client identity"
            subtitle="Choose IP and scheme sources for trusted proxy traffic."
          >
            <IdentityForm
              patch={false}
              value={defaults.identity || {}}
              onChange={(identity) => setDefaults({ identity })}
            />
          </Panel>
          <Panel title="Request limits">
            <LimitsForm
              patch={false}
              value={defaults.request || {}}
              onChange={(request) => setDefaults({ request })}
            />
          </Panel>
          <Panel
            title="Delivery headers"
            subtitle="Applied to every response, including cached responses. Outbound request mutations belong in request-stage rules."
          >
            <HeadersForm
              value={defaults.delivery?.headers || []}
              onChange={(headers) => setDefaults({ delivery: { ...defaults.delivery, headers } })}
            />
          </Panel>
          <Panel
            title="Browser freshness & CORS"
            subtitle="Delivery policy is distinct from object-cache freshness."
          >
            <div className="form-grid two">
              <OptionalNumber
                label="Browser TTL override"
                hint="Blank leaves the source Cache-Control unchanged."
                value={defaults.delivery?.browser_ttl_seconds}
                onChange={(browser_ttl_seconds) =>
                  setDefaults({ delivery: { ...defaults.delivery, browser_ttl_seconds } })
                }
                unit="s"
              />
            </div>
            <div className="item-heading">
              <span className="field-label">Cross-origin response policy</span>
              <Button
                onClick={() =>
                  setDefaults({
                    delivery: {
                      ...defaults.delivery,
                      cors: defaults.delivery?.cors
                        ? undefined
                        : { origins: [], credentials: false },
                    },
                  })
                }
              >
                {defaults.delivery?.cors ? 'Remove CORS override' : 'Configure CORS'}
              </Button>
            </div>
            {defaults.delivery?.cors && (
              <CorsForm
                value={defaults.delivery.cors}
                onChange={(cors) => setDefaults({ delivery: { ...defaults.delivery, cors } })}
              />
            )}
          </Panel>
        </>
      )}
      {tab === 'system' && (
        <>
          <p className="footnote">Listener, storage, and analytics changes require a restart.</p>
          <Panel title="Listeners & trust boundaries">
            <div className="form-grid two">
              <Field label="Proxy listen address">
                <Input
                  required
                  value={draft.listen.proxy}
                  placeholder=":8080"
                  onChange={(event) =>
                    change((config) => ({
                      ...config,
                      listen: { ...config.listen, proxy: event.target.value },
                    }))
                  }
                />
              </Field>
              <Field
                label="Admin listen address"
                hint="Keep the admin listener on a private network."
              >
                <Input
                  required
                  value={draft.listen.admin}
                  placeholder="127.0.0.1:8081"
                  onChange={(event) =>
                    change((config) => ({
                      ...config,
                      listen: { ...config.listen, admin: event.target.value },
                    }))
                  }
                />
              </Field>
              <Field label="Data directory">
                <Input
                  required
                  value={draft.data_dir}
                  onChange={(event) =>
                    change((config) => ({ ...config, data_dir: event.target.value }))
                  }
                />
              </Field>
              <StringList
                label="Trusted proxy CIDRs"
                hint="Forwarded client identity is trusted only from these network peers."
                multiline
                value={draft.trusted_proxies}
                placeholder={'127.0.0.1/32\n::1/128'}
                onChange={(trusted_proxies) =>
                  change((config) => ({ ...config, trusted_proxies: trusted_proxies || [] }))
                }
              />
            </div>
          </Panel>
          <Panel
            title="Cache storage & budgets"
            subtitle="Local cache capacity and shared-fill limits."
          >
            <div className="form-grid three">
              <OptionalNumber
                inherit={false}
                label="Total cache budget"
                value={draft.storage.max_bytes}
                onChange={(v) => setStorage('max_bytes', v)}
                unit="bytes"
                min={1}
              />
              <OptionalNumber
                inherit={false}
                label="Object size limit"
                value={draft.storage.max_object_bytes}
                onChange={(v) => setStorage('max_object_bytes', v)}
                unit="bytes"
                min={1}
              />
              <OptionalNumber
                inherit={false}
                label="Hot metadata memory"
                value={draft.storage.memory_bytes}
                onChange={(v) => setStorage('memory_bytes', v)}
                unit="bytes"
                min={1}
                hint="Positive budget required; zero selects cache defaults."
              />
              <OptionalNumber
                inherit={false}
                label="Inline body threshold"
                value={draft.storage.inline_bytes}
                onChange={(v) => setStorage('inline_bytes', v)}
                unit="bytes"
                min={1}
                hint="Positive threshold required; zero selects cache defaults."
              />
              <OptionalNumber
                inherit={false}
                label="Coordination block size"
                value={draft.storage.block_bytes}
                onChange={(v) => setStorage('block_bytes', v)}
                unit="bytes"
                min={1}
              />
              <OptionalNumber
                inherit={false}
                label="Concurrent fills"
                value={draft.storage.max_fills}
                onChange={(v) => setStorage('max_fills', v)}
                min={1}
              />
              <OptionalNumber
                inherit={false}
                label="Background fill budget"
                value={draft.storage.max_background}
                onChange={(v) => setStorage('max_background', v)}
                min={1}
                hint="Positive budget required; zero selects cache defaults."
              />
              <OptionalNumber
                inherit={false}
                label="Concurrent cache readers"
                value={draft.storage.max_readers}
                onChange={(v) => setStorage('max_readers', v)}
                min={1}
              />
              <OptionalNumber
                inherit={false}
                label="Shared fill timeout"
                value={draft.storage.fill_timeout_ms}
                onChange={(v) => setStorage('fill_timeout_ms', v)}
                unit="ms"
                min={1}
                max={86400000}
                hint="Independent of the upstream request deadline."
              />
            </div>
          </Panel>
        </>
      )}
      {tab === 'analytics' && (
        <>
          <Panel
            title="Collection & retention"
            subtitle="Local DuckDB collection, retention, and query budgets."
          >
            <div className="form-grid three">
              <OptionalBoolean
                inherit={false}
                label="Analytics collection"
                value={draft.analytics.enabled}
                onChange={(v) => setAnalytics('enabled', v)}
              />
              <OptionalNumber
                inherit={false}
                label="Detail & traffic retention"
                value={draft.analytics.detail_days}
                onChange={(v) => setAnalytics('detail_days', v)}
                unit="days"
                min={1}
                max={366}
              />
              <OptionalNumber
                inherit={false}
                label="Rollup retention"
                value={draft.analytics.rollup_days}
                onChange={(v) => setAnalytics('rollup_days', v)}
                unit="days"
                min={draft.analytics.detail_days ?? 1}
                max={366}
              />
              <OptionalNumber
                inherit={false}
                label="Analytics disk budget"
                value={draft.analytics.max_bytes}
                onChange={(v) => setAnalytics('max_bytes', v)}
                unit="bytes"
                min={32 * 1024 * 1024}
              />
              <OptionalNumber
                inherit={false}
                label="Query timeout"
                value={draft.analytics.query_timeout_ms}
                onChange={(v) => setAnalytics('query_timeout_ms', v)}
                unit="ms"
                min={1}
                max={120000}
              />
              <OptionalNumber
                inherit={false}
                label="Concurrent queries"
                value={draft.analytics.query_concurrency}
                onChange={(v) => setAnalytics('query_concurrency', v)}
                min={1}
                max={8}
              />
              <OptionalNumber
                inherit={false}
                label="DuckDB memory budget"
                value={draft.analytics.memory_bytes}
                onChange={(v) => setAnalytics('memory_bytes', v)}
                unit="bytes"
                min={32 * 1024 * 1024}
              />
              <OptionalNumber
                inherit={false}
                label="Temporary storage budget"
                value={draft.analytics.temp_bytes}
                onChange={(v) => setAnalytics('temp_bytes', v)}
                unit="bytes"
                min={1024 * 1024}
                max={
                  draft.analytics.max_bytes === undefined
                    ? undefined
                    : Math.floor(draft.analytics.max_bytes / 8)
                }
                hint="Up to one eighth of the analytics disk budget."
              />
              <OptionalNumber
                inherit={false}
                label="DuckDB worker threads"
                value={draft.analytics.threads}
                onChange={(v) => setAnalytics('threads', v)}
                min={1}
                max={16}
              />
              <OptionalNumber
                inherit={false}
                label="Event queue capacity"
                value={draft.analytics.queue_size}
                onChange={(v) => setAnalytics('queue_size', v)}
                min={1}
                max={1048576}
              />
              <OptionalNumber
                inherit={false}
                label="Ingestion batch size"
                value={draft.analytics.batch_size}
                onChange={(v) => setAnalytics('batch_size', v)}
                min={1}
                max={8192}
              />
              <OptionalNumber
                inherit={false}
                label="Flush interval"
                value={draft.analytics.flush_interval_ms}
                onChange={(v) => setAnalytics('flush_interval_ms', v)}
                unit="ms"
                min={1}
                max={60000}
              />
              <Field label="GeoIP country MMDB path" hint="Optional local country database.">
                <Input
                  value={draft.analytics.country_mmdb || ''}
                  placeholder="/data/GeoLite2-Country.mmdb"
                  onChange={(event) =>
                    setAnalytics('country_mmdb', event.target.value || undefined)
                  }
                />
              </Field>
              <Field label="GeoIP ASN MMDB path" hint="Optional local ASN database.">
                <Input
                  value={draft.analytics.asn_mmdb || ''}
                  placeholder="/data/GeoLite2-ASN.mmdb"
                  onChange={(event) => setAnalytics('asn_mmdb', event.target.value || undefined)}
                />
              </Field>
            </div>
          </Panel>
          <Disclosure title="Privacy & geographic assets">
            <p className="form-note">
              Bodies, sensitive headers, and full query strings are not recorded by default. Offline
              country boundaries are Natural Earth public-domain data distributed through
              world-atlas (ISC). GeoIP database licensing and updates remain the operator’s
              responsibility. Missing geographic data never interrupts proxy traffic.
            </p>
          </Disclosure>
        </>
      )}
      {tab === 'runtime' && <Runtime embedded />}
    </>
  );
}
