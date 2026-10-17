import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import RuntimeTrends, { runtimeTrendOption } from './RuntimeTrends';
import type { RuntimeInfo } from '../types';

const history: RuntimeInfo[] = [
  {
    time: '2026-10-16T00:00:00Z',
    cpu_percent: 12.5,
    cpus: 8,
    go_bytes: 1048576,
    heap_bytes: 524288,
    go_version: 'go1.26',
    goroutines: 40,
    uptime_seconds: 5,
  },
  {
    time: '2026-10-16T00:00:05Z',
    cpu_percent: 145,
    cpus: 8,
    go_bytes: 2097152,
    heap_bytes: 786432,
    go_version: 'go1.26',
    goroutines: 42,
    uptime_seconds: 10,
  },
];
describe('real runtime trend rendering', () => {
  it('charts exact CPU percentages and Go/heap bytes without inventing RSS or clamping multi-core CPU', () => {
    const cpu = runtimeTrendOption(history, 'cpu');
    const memory = runtimeTrendOption(history, 'memory');
    expect(JSON.stringify(cpu.series)).toContain('["2026-10-16T00:00:05Z",145]');
    expect(JSON.stringify(memory.series)).toContain('Go memory');
    expect(JSON.stringify(memory.series)).toContain('["2026-10-16T00:00:05Z",2097152]');
    expect(JSON.stringify(memory.series)).toContain('["2026-10-16T00:00:05Z",786432]');
    expect(JSON.stringify(memory)).not.toContain('RSS');
  });
  it('does not fabricate samples while history is unavailable', () => {
    const html = renderToStaticMarkup(<RuntimeTrends samples={[]} />);
    expect(html).toContain('No CPU samples yet');
    expect(html).toContain('No memory samples yet');
    expect(html).toContain('not process RSS');
    expect(runtimeTrendOption([], 'cpu').series).toMatchObject([{ data: [] }]);
  });
});
