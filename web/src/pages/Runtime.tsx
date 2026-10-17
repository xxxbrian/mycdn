import { Cpu, Database, HardDrive, RefreshCw, Radio, Server } from 'lucide-react';
import { api } from '../api';
import { useAsync } from '../lib/useAsync';
import { bytes, dateTime, duration, milliseconds, number } from '../lib/format';
import {
  Alert,
  Badge,
  Button,
  Loading,
  LoadError,
  Metric,
  PageHeading,
  Panel,
} from '../components/ui';
import RuntimeTrends from '../components/RuntimeTrends';

export default function Runtime({ embedded = false }: { embedded?: boolean }) {
  const result = useAsync((signal) => api.status(signal), 'status', 15000);
  const data = result.data;
  return (
    <>
      {!embedded && (
        <PageHeading title="Runtime" description="The live process and its resource budgets." />
      )}
      <div className="runtime-heading">
        <span className="eyebrow">LIVE PROCESS / 15 SECOND REFRESH</span>
        <Button onClick={result.reload} disabled={result.loading}>
          <RefreshCw size={13} />
          Refresh status
        </Button>
      </div>
      {result.loading ? (
        <Loading label="Reading runtime status…" />
      ) : result.error ? (
        <LoadError error={result.error} retry={result.reload} />
      ) : (
        data && (
          <>
            {data.restart_required && (
              <Alert tone="warning">
                Restart required to apply saved settings
                {data.restart_fields?.length ? `: ${data.restart_fields.join(', ')}` : '.'} Existing
                runtime resources still use startup values.
              </Alert>
            )}
            <div className="metric-grid">
              <Metric
                label="Uptime"
                value={duration(data.uptime_seconds)}
                note={`Started ${dateTime(data.started_at)}`}
                icon={<Radio size={14} />}
                accent
              />
              <Metric
                label="Go memory"
                value={bytes(data.memory_bytes)}
                note={`Heap ${bytes(data.heap_bytes)}`}
                icon={<Cpu size={14} />}
              />
              <Metric
                label="Active requests"
                value={number(data.active_requests)}
                icon={<Server size={14} />}
              />
              <Metric
                label="Cached objects"
                value={number(data.cache?.entries)}
                icon={<Database size={14} />}
              />
              <Metric
                label="Cache footprint"
                value={bytes(data.cache?.bytes)}
                icon={<HardDrive size={14} />}
              />
            </div>
            <RuntimeTrends samples={data.history} />
            <div className="two-column">
              <Panel title="Process" action={<Badge>{data.version || 'VERSION UNREPORTED'}</Badge>}>
                <dl className="key-values">
                  <div>
                    <dt>Go runtime</dt>
                    <dd>{data.go_version || '—'}</dd>
                  </div>
                  <div>
                    <dt>Goroutines</dt>
                    <dd>{number(data.goroutines)}</dd>
                  </div>
                  <div>
                    <dt>CPU usage</dt>
                    <dd>{number(data.cpu_percent, 1)}%</dd>
                  </div>
                  <div>
                    <dt>Available CPUs</dt>
                    <dd>{number(data.cpus)}</dd>
                  </div>
                  <div>
                    <dt>Proxy listener</dt>
                    <dd>{data.proxy_listen || '—'}</dd>
                  </div>
                  <div>
                    <dt>Admin listener</dt>
                    <dd>{data.admin_listen || '—'}</dd>
                  </div>
                  <div>
                    <dt>Data directory</dt>
                    <dd>{data.data_dir || '—'}</dd>
                  </div>
                  <div>
                    <dt>Active fills</dt>
                    <dd>{number(data.active_fills ?? data.cache?.active_fills)}</dd>
                  </div>
                </dl>
              </Panel>
              <Panel
                title="Analytics collector"
                subtitle="Reported by the live collector, not inferred from configured defaults."
              >
                <dl className="key-values">
                  <div>
                    <dt>Collection</dt>
                    <dd>
                      {data.analytics?.enabled === undefined
                        ? '—'
                        : data.analytics.enabled
                          ? 'Enabled'
                          : 'Disabled'}
                    </dd>
                  </div>
                  <div>
                    <dt>Queue depth</dt>
                    <dd>{number(data.analytics?.queue_depth)}</dd>
                  </div>
                  <div>
                    <dt>Ingestion lag</dt>
                    <dd>{milliseconds(data.analytics?.lag_ms)}</dd>
                  </div>
                  <div>
                    <dt>Dropped events</dt>
                    <dd className={data.analytics?.dropped_events ? 'accent-text' : ''}>
                      {number(data.analytics?.dropped_events)}
                    </dd>
                  </div>
                  <div>
                    <dt>Geographic enrichment</dt>
                    <dd>
                      {data.analytics?.geoip_available === undefined
                        ? 'Unreported'
                        : data.analytics.geoip_available
                          ? 'Available'
                          : 'Unavailable'}
                    </dd>
                  </div>
                </dl>
              </Panel>
            </div>
            <p className="footnote">— not reported by the running process.</p>
          </>
        )
      )}
    </>
  );
}
