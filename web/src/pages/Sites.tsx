import { ArrowUpRight, Globe2, Plus, Server, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { useConfig } from '../config';
import { uid } from '../lib/format';
import type { Origin, Site } from '../types';
import {
  AddButton,
  Badge,
  Button,
  CheckField,
  ConfirmButton,
  Disclosure,
  Empty,
  Field,
  Input,
  PageHeading,
  Panel,
  Select,
} from '../components/ui';
import { StringList, UpstreamForm } from '../components/SettingsForms';

function newOrigin(): Origin {
  return { id: uid(), name: 'Primary', url: '' };
}
function SiteEditor({
  site,
  onChange,
  onDelete,
}: {
  site: Site;
  onChange: (site: Site) => void;
  onDelete: () => void;
}) {
  function originChange(index: number, next: Origin) {
    onChange({ ...site, origins: site.origins.map((origin, i) => (i === index ? next : origin)) });
  }
  return (
    <Panel
      title="Site configuration"
      eyebrow={site.id}
      action={
        <ConfirmButton confirm="Delete site" onConfirm={onDelete}>
          <Trash2 size={13} />
          Delete
        </ConfirmButton>
      }
    >
      <div className="form-grid two">
        <Field label="Site name">
          <Input
            required
            value={site.name}
            placeholder="Production assets"
            onChange={(event) => onChange({ ...site, name: event.target.value })}
          />
        </Field>
        <Field label="Default origin">
          <Select
            required
            value={site.default_origin}
            onChange={(event) => onChange({ ...site, default_origin: event.target.value })}
          >
            <option value="" disabled>
              Select an origin
            </option>
            {site.origins.map((origin) => (
              <option key={origin.id} value={origin.id}>
                {origin.name || origin.id}
              </option>
            ))}
          </Select>
        </Field>
        <StringList
          label="Domains"
          hint="One per line. Exact names win over wildcards. Unknown hosts are rejected."
          className="full-width"
          multiline
          required
          value={site.domains}
          placeholder={'cdn.example.com\n*.assets.example.com'}
          onChange={(domains) => onChange({ ...site, domains: domains || [] })}
        />
        <CheckField
          label="Site enabled"
          checked={site.enabled}
          onChange={(enabled) => onChange({ ...site, enabled })}
          hint="Disabled sites do not accept requests."
        />
      </div>
      <div className="section-divider">
        <div>
          <h3>Named origins</h3>
          <p>Separate the origin URL, dial target, HTTP authority, and TLS identity.</p>
        </div>
        <AddButton
          disabled={site.origins.length >= 32}
          onClick={() =>
            onChange({
              ...site,
              origins: [
                ...site.origins,
                { ...newOrigin(), name: `Origin ${site.origins.length + 1}` },
              ],
            })
          }
        >
          Add origin
        </AddButton>
      </div>
      <div className="origin-list">
        {site.origins.map((origin, index) => (
          <div className="origin-editor" key={origin.id}>
            <div className="item-heading">
              <span>
                <Server size={14} />
                <strong>{origin.name || 'Unnamed origin'}</strong>
                {site.default_origin === origin.id && <Badge tone="accent">DEFAULT</Badge>}
              </span>
              <ConfirmButton
                disabled={site.origins.length === 1}
                confirm="Remove origin"
                onConfirm={() => {
                  const origins = site.origins.filter((item) => item.id !== origin.id);
                  onChange({
                    ...site,
                    origins,
                    default_origin:
                      site.default_origin === origin.id
                        ? origins[0]?.id || ''
                        : site.default_origin,
                  });
                }}
              >
                <Trash2 size={13} />
                Remove
              </ConfirmButton>
            </div>
            <div className="form-grid two">
              <Field label="Origin name">
                <Input
                  required
                  value={origin.name}
                  onChange={(event) => originChange(index, { ...origin, name: event.target.value })}
                />
              </Field>
              <Field label="Origin URL" hint="Include scheme. A path prefix is allowed.">
                <Input
                  required
                  type="url"
                  value={origin.url}
                  placeholder="https://origin.example.com"
                  onChange={(event) => originChange(index, { ...origin, url: event.target.value })}
                />
              </Field>
            </div>
            <Disclosure title="Advanced upstream connection">
              <div className="form-grid two">
                <Field label="HTTP authority / Host" hint="Independent of the network dial target.">
                  <Input
                    value={origin.host_header || ''}
                    placeholder="Origin URL host"
                    onChange={(event) =>
                      originChange(index, {
                        ...origin,
                        host_header: event.target.value || undefined,
                      })
                    }
                  />
                </Field>
                <Field label="TLS verification name" hint="Certificate verification stays enabled.">
                  <Input
                    value={origin.tls_server_name || ''}
                    placeholder="Origin URL hostname"
                    onChange={(event) =>
                      originChange(index, {
                        ...origin,
                        tls_server_name: event.target.value || undefined,
                      })
                    }
                  />
                </Field>
                <Field
                  label="Network dial address"
                  hint="host:port, independent of Host and TLS name."
                >
                  <Input
                    value={origin.dial_address || ''}
                    placeholder="Origin URL address"
                    onChange={(event) =>
                      originChange(index, {
                        ...origin,
                        dial_address: event.target.value || undefined,
                      })
                    }
                  />
                </Field>
                <Field
                  label="Custom CA file"
                  hint="Path inside the mycdn process/container. Blank uses system roots."
                >
                  <Input
                    value={origin.ca_file || ''}
                    placeholder="/etc/mycdn/origin-ca.pem"
                    onChange={(event) =>
                      originChange(index, { ...origin, ca_file: event.target.value || undefined })
                    }
                  />
                </Field>
              </div>
              <UpstreamForm
                value={origin}
                onChange={(value) => originChange(index, { ...origin, ...value })}
              />
            </Disclosure>
          </div>
        ))}
      </div>
    </Panel>
  );
}
export default function Sites() {
  const { draft, change } = useConfig();
  const [selected, setSelected] = useState<string>();
  if (!draft) return null;
  const site = draft.sites.find((site) => site.id === selected) || draft.sites[0];
  function updateSite(next: Site) {
    change((config) => ({
      ...config,
      sites: config.sites.map((item) => (item.id === next.id ? next : item)),
    }));
  }
  function create() {
    const origin = newOrigin();
    const site: Site = {
      id: uid(),
      name: 'New site',
      enabled: true,
      domains: [],
      origins: [origin],
      default_origin: origin.id,
    };
    change((config) => ({ ...config, sites: [...config.sites, site] }));
    setSelected(site.id);
  }
  return (
    <>
      <PageHeading
        title="Sites & origins"
        eyebrow="ROUTING / UPSTREAM"
        description="Explicit host routing. Named upstreams. No hidden site-local behavior hierarchy."
        action={
          <Button variant="primary" onClick={create}>
            <Plus size={14} />
            Create site
          </Button>
        }
      />
      <div className="sites-layout">
        <aside className="site-list" aria-label="Sites">
          <div className="list-heading">
            SITES <span className="mono">{draft.sites.length}</span>
          </div>
          {!draft.sites.length ? (
            <Empty
              title="No sites yet"
              description="Create your first site to begin routing traffic."
              icon={<Globe2 size={25} />}
            />
          ) : (
            draft.sites.map((item) => (
              <div
                key={item.id}
                className={`site-list-item ${site?.id === item.id ? 'active' : ''}`}
              >
                <button type="button" onClick={() => setSelected(item.id)}>
                  <div>
                    <Globe2 size={15} />
                    <strong>{item.name}</strong>
                    <ArrowUpRight size={12} />
                  </div>
                  <span className="mono text-small truncate">
                    {item.domains[0] || 'No domain configured'}
                    {item.domains.length > 1 && ` +${item.domains.length - 1}`}
                  </span>
                  <span className="site-detail">
                    <span className={`square-dot ${item.enabled ? '' : 'muted-dot'}`} />
                    {item.enabled ? 'Enabled' : 'Disabled'}
                    <span>
                      {item.origins.length} origin{item.origins.length !== 1 ? 's' : ''}
                    </span>
                  </span>
                </button>
              </div>
            ))
          )}
        </aside>
        <div>
          {site ? (
            <SiteEditor
              key={site.id}
              site={site}
              onChange={updateSite}
              onDelete={() => {
                change((config) => ({
                  ...config,
                  sites: config.sites.filter((item) => item.id !== site.id),
                }));
                setSelected(undefined);
              }}
            />
          ) : (
            <Panel>
              <Empty
                title="Configure your first edge site"
                description="Add domains, then point a named origin at your upstream. Save the configuration when ready."
                icon={<Server size={32} />}
              />
              <Button variant="primary" onClick={create}>
                <Plus size={14} />
                Create site
              </Button>
            </Panel>
          )}
        </div>
      </div>
    </>
  );
}
