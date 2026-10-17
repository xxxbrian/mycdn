import { useEffect, useState } from 'react';
import { Filter, RotateCcw, Search } from 'lucide-react';
import { useNavigation } from '../lib/navigation';
import { filterKeys, filterLabels, resetTimeZoom } from '../lib/analytics';
import type { FilterKey } from '../lib/analytics';
import { Button, Field, Input, Select } from './ui';
import { useConfig } from '../config';

const ranges = [
  { id: 'today', label: 'Today' },
  { id: 'yesterday', label: 'Yesterday' },
  { id: '7d', label: '7 days' },
  { id: '14d', label: '14 days' },
  { id: 'custom', label: 'Custom' },
];
export default function AnalyticsFilters() {
  const { params, update } = useNavigation();
  const { draft } = useConfig();
  const key = JSON.stringify(
    [...filterKeys, 'from', 'to'].map((key) => [key, params.get(key) || '']),
  );
  const [advanced, setAdvanced] = useState(false);
  const [values, setValues] = useState<Record<string, string>>({});
  useEffect(() => {
    setValues(
      Object.fromEntries([...filterKeys, 'from', 'to'].map((key) => [key, params.get(key) || ''])),
    );
  }, [key]); // URL is the source of truth, including browser back/forward.
  const range = params.get('range') || 'today';
  const active = filterKeys.filter((key) => params.get(key));
  function set(key: string, value: string) {
    setValues((current) => ({ ...current, [key]: value }));
  }
  function apply(event: React.FormEvent) {
    event.preventDefault();
    const next: Record<string, string | null> = {};
    for (const key of filterKeys) next[key] = values[key] || null;
    if (range === 'custom') {
      if (
        !values.from ||
        !values.to ||
        Number.isNaN(Date.parse(values.from)) ||
        Number.isNaN(Date.parse(values.to))
      )
        return;
      next.from = new Date(values.from).toISOString();
      next.to = new Date(values.to).toISOString();
      if (next.from !== params.get('from') || next.to !== params.get('to')) {
        Object.assign(next, resetTimeZoom);
      }
    }
    update(next);
  }
  function field(key: FilterKey, placeholder: string) {
    return (
      <Field label={filterLabels[key]}>
        <Input
          value={values[key] || ''}
          placeholder={placeholder}
          onChange={(event) => set(key, event.target.value)}
        />
      </Field>
    );
  }
  function localDate(value: string) {
    if (!value || Number.isNaN(Date.parse(value))) return value;
    const date = new Date(value);
    return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
  }
  return (
    <div className="filter-block">
      <form onSubmit={apply}>
        <div className="filter-toolbar">
          <div className="range-buttons" aria-label="Time range">
            {ranges.map((item) => (
              <Button
                key={item.id}
                className={range === item.id ? 'selected' : ''}
                aria-pressed={range === item.id}
                onClick={() => {
                  if (item.id === 'custom' && (!params.get('from') || !params.get('to'))) {
                    const to = new Date();
                    const from = new Date();
                    from.setHours(0, 0, 0, 0);
                    update({
                      range: item.id,
                      from: from.toISOString(),
                      to: to.toISOString(),
                      ...resetTimeZoom,
                    });
                  } else update({ range: item.id, ...resetTimeZoom });
                }}
              >
                {item.label}
              </Button>
            ))}
          </div>
          <div className="filter-meta">
            <span className="square-dot" />
            LOCAL TIME
            <span className="mono">{Intl.DateTimeFormat().resolvedOptions().timeZone}</span>
          </div>
        </div>
        {range === 'custom' && (
          <div className="custom-range">
            <Field label="From">
              <Input
                type="datetime-local"
                required
                value={localDate(values.from || '')}
                onChange={(event) => set('from', event.target.value)}
              />
            </Field>
            <Field label="To">
              <Input
                type="datetime-local"
                required
                value={localDate(values.to || '')}
                onChange={(event) => set('to', event.target.value)}
              />
            </Field>
            <Button type="submit">Apply range</Button>
          </div>
        )}
        <div className="filter-primary">
          <Field label="Site">
            <Select value={values.site || ''} onChange={(event) => set('site', event.target.value)}>
              <option value="">All sites</option>
              {values.site && !draft?.sites.some((site) => site.id === values.site) && (
                <option value={values.site}>{values.site}</option>
              )}
              {draft?.sites.map((site) => (
                <option key={site.id} value={site.id}>
                  {site.name}
                </option>
              ))}
            </Select>
          </Field>
          {field('host', 'All hosts')}
          {field('ip', 'IP address or CIDR')}
          <Button
            className={advanced ? 'selected' : ''}
            onClick={() => setAdvanced(!advanced)}
            aria-expanded={advanced}
          >
            <Filter size={14} />
            Filters{active.length > 0 && <span className="mono">{active.length}</span>}
          </Button>
          <Button type="submit">
            <Search size={14} />
            Apply
          </Button>
          <Button
            aria-label="Clear all analytics filters"
            onClick={() => update(Object.fromEntries(filterKeys.map((key) => [key, null])))}
          >
            <RotateCcw size={14} />
          </Button>
        </div>
        {advanced && (
          <div className="filter-advanced">
            {field('path', '/assets/')}
            {field('status', '200, 304, 404')}
            {field('cache', 'HIT, MISS, COALESCED')}
            {field('method', 'GET, POST')}
            {field('type', 'image, video, html')}
            {field('country', 'US, DE, JP')}
            {field('asn', '13335, 15169')}
            <span className="field-hint">
              Lists use commas. IP also accepts CIDR. Filters apply consistently to every
              visualization.
            </span>
          </div>
        )}
      </form>
    </div>
  );
}
