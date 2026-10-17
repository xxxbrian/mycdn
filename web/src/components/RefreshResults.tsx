import type { RefreshBatch } from '../types';
import { Badge, Empty, Panel } from './ui';

export function refreshCounts(results: RefreshBatch['results']) {
  return results.reduce(
    (counts, result) => {
      if (result.status === 'error' || result.error) counts.failed++;
      else if (result.status === 'refreshed') counts.refreshed++;
      else if (result.status === 'skipped') counts.skipped++;
      else counts.other++;
      return counts;
    },
    { refreshed: 0, skipped: 0, failed: 0, other: 0 },
  );
}

export default function RefreshResults({ result }: { result: RefreshBatch }) {
  const counts = refreshCounts(result.results);
  return (
    <Panel
      title="Refresh results"
      subtitle={`${counts.refreshed} refreshed · ${counts.skipped} skipped · ${counts.failed} failed${counts.other ? ` · ${counts.other} other` : ''}`}
    >
      {result.results.length ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Client URL</th>
                <th>Result</th>
                <th>Details</th>
              </tr>
            </thead>
            <tbody>
              {result.results.map((entry, index) => (
                <tr key={`${index}-${entry.url}`}>
                  <td className="mono text-small" style={{ overflowWrap: 'anywhere' }}>
                    {entry.url}
                  </td>
                  <td>
                    <Badge
                      tone={
                        entry.error || entry.status === 'error'
                          ? 'error'
                          : entry.status === 'refreshed'
                            ? 'accent'
                            : ''
                      }
                    >
                      {entry.status || '—'}
                    </Badge>
                  </td>
                  <td className={entry.error ? 'accent-text' : 'muted'}>
                    {[entry.reason, entry.error].filter(Boolean).join(' · ') || '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <Empty
          title="No URL results returned"
          description="The server did not report a per-URL outcome for this batch."
        />
      )}
    </Panel>
  );
}
