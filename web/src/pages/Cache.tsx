import { Database, RefreshCw, Search, Trash2 } from 'lucide-react';
import { useMemo, useRef, useState } from 'react';
import { api, errorMessage } from '../api';
import { useAsync } from '../lib/useAsync';
import { useConfig } from '../config';
import { bytes, dateTime, number } from '../lib/format';
import { cacheSelection, entryRefreshUrl, refreshInput } from '../lib/cache';
import type { CacheEntry, CacheSelection, RefreshBatch } from '../types';
import RefreshResults from '../components/RefreshResults';
import {
  Alert,
  Badge,
  Button,
  ConfirmButton,
  Empty,
  Field,
  Input,
  Loading,
  LoadError,
  PageHeading,
  Panel,
  Select,
  Textarea,
} from '../components/ui';

export default function Cache() {
  const { draft } = useConfig();
  const [filters, setFilters] = useState({ site_id: '', host: '', path: '', path_prefix: '' });
  const [selection, setSelection] = useState(filters);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [error, setError] = useState('');
  const [refreshUrls, setRefreshUrls] = useState('');
  const [refreshSite, setRefreshSite] = useState('');
  const [refreshResult, setRefreshResult] = useState<RefreshBatch>();
  const refreshField = useRef<HTMLTextAreaElement>(null);
  const search = useMemo(
    () => new URLSearchParams({ ...cacheSelection(selection), limit: '50' }),
    [selection],
  );
  const result = useAsync((signal) => api.cacheEntries(search, signal), search.toString());

  async function purge(target: CacheSelection) {
    if (busy) return;
    setBusy(true);
    setError('');
    setNotice('');
    setRefreshResult(undefined);
    try {
      const response = await api.purge(target);
      setNotice(
        `Purge completed · ${number(response.purged)} entries invalidated. Existing readers may finish before physical deletion.`,
      );
      result.reload();
    } catch (error) {
      setError(errorMessage(error));
    } finally {
      setBusy(false);
    }
  }
  async function refresh(event: React.FormEvent) {
    event.preventDefault();
    if (busy) return;
    setError('');
    setNotice('');
    setRefreshResult(undefined);
    try {
      const input = refreshInput(refreshUrls, refreshSite);
      setBusy(true);
      setRefreshResult(await api.refresh(input));
      result.reload();
    } catch (error) {
      setError(errorMessage(error));
    } finally {
      setBusy(false);
    }
  }
  function prepareRefresh(entry: CacheEntry) {
    const url = entryRefreshUrl(entry);
    if (!url) return;
    setRefreshUrls(url);
    setRefreshSite(entry.site_id || '');
    refreshField.current?.focus();
  }
  const target = cacheSelection(selection);
  const scoped = Object.keys(target).length > 0;
  return (
    <>
      <PageHeading
        title="Cache storage"
        eyebrow="OBJECTS / INVALIDATION"
        description="Inspect published entries. Invalidate safely. Refresh explicit client URLs."
        action={
          <Button onClick={result.reload}>
            <RefreshCw size={14} />
            Reload
          </Button>
        }
      />
      <Panel
        title="Entry selection"
        subtitle="Listing and purge use the applied selection below, not unsubmitted form changes."
      >
        <form
          onSubmit={(event) => {
            event.preventDefault();
            setSelection({ ...filters });
          }}
        >
          <div className="filter-primary">
            <Field label="Site">
              <Select
                value={filters.site_id}
                onChange={(event) => setFilters({ ...filters, site_id: event.target.value })}
              >
                <option value="">All sites</option>
                {draft?.sites.map((site) => (
                  <option key={site.id} value={site.id}>
                    {site.name}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Host">
              <Input
                value={filters.host}
                placeholder="cdn.example.com"
                onChange={(event) => setFilters({ ...filters, host: event.target.value })}
              />
            </Field>
            <Field label="Exact path">
              <Input
                value={filters.path}
                placeholder="/assets/app.js"
                onChange={(event) => setFilters({ ...filters, path: event.target.value })}
              />
            </Field>
            <Field label="Path prefix">
              <Input
                value={filters.path_prefix}
                placeholder="/assets/"
                onChange={(event) => setFilters({ ...filters, path_prefix: event.target.value })}
              />
            </Field>
            <Button type="submit">
              <Search size={14} />
              Apply
            </Button>
          </div>
        </form>
        <div className="cache-operations">
          <span className="mono muted text-small">
            {scoped
              ? Object.entries(target)
                  .map(([key, value]) => `${key}=${value}`)
                  .join(' · ')
              : 'Selection: all cache entries'}
          </span>
          <ConfirmButton
            disabled={busy}
            confirm={scoped ? 'Purge selection' : 'Purge ALL entries'}
            onConfirm={() => void purge(scoped ? target : { all: true })}
          >
            <Trash2 size={13} />
            {scoped ? 'Purge selection' : 'Purge all'}
          </ConfirmButton>
        </div>
        <p className="field-hint">
          Purge covers every matching entry, not only the bounded listing. Empty selection requires
          explicit confirmation to purge all.
        </p>
      </Panel>
      <Panel
        title="Refresh URLs"
        subtitle="Rebuild through the real gateway using explicit client URLs, not a listing filter."
      >
        <form onSubmit={(event) => void refresh(event)}>
          <div className="form-grid two">
            <Field
              label="Client URLs"
              className="full-width"
              hint="One absolute HTTP(S) URL per line, using a configured site host—not the upstream address. Include any required query string."
            >
              <Textarea
                ref={refreshField}
                required
                value={refreshUrls}
                placeholder={
                  'http://cdn.example.com/assets/app.js\nhttps://cdn.example.com/image.webp?width=640'
                }
                onChange={(event) => setRefreshUrls(event.target.value)}
              />
            </Field>
            <Field
              label="Expected site (optional)"
              hint="Restrict the refresh to a known site, or let the configured host route it."
            >
              <Select value={refreshSite} onChange={(event) => setRefreshSite(event.target.value)}>
                <option value="">Route by URL host</option>
                {refreshSite && !draft?.sites.some((site) => site.id === refreshSite) && (
                  <option value={refreshSite}>{refreshSite}</option>
                )}
                {draft?.sites.map((site) => (
                  <option key={site.id} value={site.id}>
                    {site.name}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
          <Button type="submit" disabled={busy || !refreshUrls.trim()}>
            <RefreshCw size={13} className={busy ? 'spin' : ''} />
            Refresh URLs
          </Button>
          <p className="footnote">
            An entry shortcut prepares its HTTP host/path URL here for review. Stored metadata does
            not reconstruct the original scheme, query, headers, or cookies.
          </p>
        </form>
      </Panel>
      {error && <Alert>{error}</Alert>}
      {notice && <Alert tone="success">{notice}</Alert>}
      {refreshResult && <RefreshResults result={refreshResult} />}
      <Panel title="Published entries" action={<Badge>LIMIT {result.data?.limit ?? 50}</Badge>}>
        {result.loading ? (
          <Loading label="Reading cache index…" />
        ) : result.error ? (
          <LoadError error={result.error} retry={result.reload} />
        ) : !result.data?.entries?.length ? (
          <Empty
            title="No cached entries"
            description="Only published reusable responses appear here. Try requesting a cacheable resource or widening the selection."
            icon={<Database size={28} />}
          />
        ) : (
          <>
            <div className="table-wrap">
              <table className="cache-table">
                <thead>
                  <tr>
                    {['Resource', 'State', 'Size', 'Created', 'Expires', 'Actions'].map((label) => (
                      <th key={label}>{label}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {result.data.entries.map((entry) => (
                    <tr key={entry.key}>
                      <td>
                        <strong className="text-small">{entry.host || entry.key}</strong>
                        <div
                          className="mono muted truncate path-cell"
                          title={entry.path || entry.key}
                        >
                          {entry.path || entry.key}
                        </div>
                      </td>
                      <td>
                        <Badge>{entry.state || entry.status || 'PUBLISHED'}</Badge>
                      </td>
                      <td className="mono nowrap">{bytes(entry.bytes)}</td>
                      <td className="mono text-small nowrap muted">{dateTime(entry.created_at)}</td>
                      <td className="mono text-small nowrap muted">{dateTime(entry.expires_at)}</td>
                      <td>
                        <div className="inline-actions">
                          <Button
                            aria-label={`Prepare refresh URL for ${entry.host || ''}${entry.path || entry.key}`}
                            title="Review this client URL in the refresh form"
                            disabled={busy || !entryRefreshUrl(entry)}
                            onClick={() => prepareRefresh(entry)}
                          >
                            <RefreshCw size={13} />
                          </Button>
                          <ConfirmButton
                            confirm="Purge entry"
                            disabled={busy || !entry.site_id || !entry.host || !entry.path}
                            onConfirm={() =>
                              void purge({
                                site_id: entry.site_id,
                                host: entry.host,
                                path: entry.path,
                              })
                            }
                          >
                            <Trash2 size={13} />
                          </ConfirmButton>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="pagination">
              <span className="muted text-small">
                {result.data.entries.length} entries shown · limit {result.data.limit}
              </span>
              {result.data.truncated && (
                <span className="muted text-small">More matches exist. Narrow the filters.</span>
              )}
            </div>
          </>
        )}
      </Panel>
    </>
  );
}
