# Analytics API and core contract

`internal/analytics` is independent of gateway/config/admin. It records copied
values, uses local DuckDB files, and never fetches GeoIP data over the network.
Management HTTP wiring is deliberately outside this package.
The analytics directory is private (`0700`) and must be exclusively owned by one
process. Its partition/state filenames are reserved for analytics.

## Go seam

```go
store, err := analytics.Open(analytics.Options{
    Dir: "/data/analytics",
    MemoryLimitBytes: 256 << 20,
    DiskBudgetBytes: 2 << 30,
    DetailRetention: 14 * 24 * time.Hour,
    RollupRetention: 90 * 24 * time.Hour,
    QueueCapacity: 8192,
})
accepted := store.RecordRequest(analytics.RequestEvent{ /* copied values */ })
accepted = store.RecordOrigin(analytics.OriginEvent{ /* one physical attempt */ })
accepted = store.RecordTransfer(analytics.TransferEvent{ /* observed delta */ })
err = store.Flush(ctx) // all previously accepted events are visible, or an error
result, err := store.Query(ctx, analytics.Query{From: from, To: to})
details, err := store.Details(ctx, analytics.DetailsQuery{From: from, To: to})
status := store.Status()
err = store.Close() // stops admission, drains ingestion, closes local resources
```

All three `Record*` methods are concurrency-safe, bounded and nonblocking. Their
boolean is admission, not durability. Queue overflow, invalid/expired events,
storage pressure and write errors are separately counted. `Flush` is an ordered
barrier, not an implicit operation on each query. Callers must not replay accepted
events: records are append-only, not deduplicated by ID.
An error losing an admitted event remains sticky for subsequent `Flush`/`Close`
calls; a later successful batch cannot silently repair missing observations.

The exported `RequestEvent`, `OriginEvent`, `TransferEvent` fields follow the
gateway observer's value types, without importing it. Timestamps in events and
detail rows are Unix milliseconds; durations are milliseconds, sizes bytes.
`RequestEvent` has request/site/host/client IP/path/method/content type/status/cache
result, start/completion/duration/first-byte time, request/response/cached bytes and error.
`OriginEvent` describes one actual attempt (including retries/background work),
with attempt/site/origin/host/path/method/protocol/status, times, sizes and error;
it deliberately has **no client identity**. `TransferEvent.Kind` is `request` or
`origin`, with the appropriate identifiers/dimensions and observation interval.
Both completion types have `FirstByteMS int64` (`first_byte_ms`): the elapsed time
from that request/attempt's start until the first response byte/headers, **not**
until the download ends. Positive values are measured samples; `0` means
unavailable (including failures before any response). Collectors should round a
measured sub-millisecond response up to 1ms (and keep `DurationMS` at least as
large as `FirstByteMS`). Negative first-byte values or first-byte values exceeding
the total duration are rejected at admission. `TransferEvent.Protocol string`
(`protocol`) carries the negotiated upstream protocol on physical deltas; empty
means unknown. At the integration snapshot inspected, gateway has neither of
these additive fields yet; the collector/adapter must supply them to populate
TTFB and protocol-byte rankings. Origin task/reason classification is deferred
until gateway exposes it; attempts/retries/background work are still counted once.
Collectors must redact credentials/query strings from free-form error messages.

## Query JSON

Intended endpoint: `POST /api/analytics/query`. JSON directly decodes to `Query`.

```json
{
  "from": "2026-10-15T00:00:00Z",
  "to": "2026-10-16T00:00:00Z",
  "timezone": "UTC",
  "interval_seconds": 60,
  "filters": {
    "site_ids": ["site-a"],
    "hosts": ["cdn.example.com"],
    "client_ips": ["192.0.2.1", "2001:db8::/32"],
    "methods": ["GET"],
    "statuses": [200, 206],
    "cache_results": ["HIT", "COALESCED"],
    "content_types": ["video"],
    "countries": ["US"],
    "asns": [13335],
    "path_prefix": "/assets/"
  },
  "group_by": ["host", "client_ip", "path", "country", "asn"],
  "limit": 20
}
```

`from`/`to` are required RFC3339 strings on/after the Unix epoch, at millisecond precision or coarser,
`[from,to)` in UTC (maximum 367 days per query). `timezone` is an IANA
display timezone (default UTC); storage, bucketing and returned points stay UTC.
There is no local-calendar/DST aggregation. A filter's entries are ORed; different
filters are ANDed. IP entries accept exact IPv4/IPv6 or CIDR, including IPv6.
Hosts/country codes/methods/cache results are normalized to lower/upper case as
appropriate; paths are case-sensitive literal prefixes, not SQL LIKE patterns.
Content types are the collector's category string, not guessed MIME groupings.

`interval_seconds` is a multiple of 10, minimum 10. If omitted it is selected to
produce roughly 600 points. A maximum of 4096 potential points is enforced.
Intervals are aligned to the Unix epoch. Empty buckets are omitted; empty data
produces a zero summary, empty arrays and empty rankings, never demonstration data.

Allowed `group_by`: `site_id`, `host`, `client_ip`, `method`, `status`,
`cache_result`, `content_type`, `country`, `asn`, `path`, `origin_id`, `protocol`.
Default: host/cache_result/content_type/country/asn. Each produces an independent
top ranking (not a cross-product). `limit` defaults to 20, maximum 100. Rankings
sort by delivered response bytes, then completed requests, then value. Geography
contains the country ranking independently of `limit` (up to 300 country codes).
`origin_id` and `protocol` rankings contain physical origin records only and
sort by origin response bytes, then attempts. Protocol byte totals use
`TransferEvent.Protocol`; omitted values remain in the empty/unknown group rather
than being inferred from a later completion.

## Response schema

The machine-readable contract is generated from the Go management routes:
`/api/openapi.json`, or `make api-types` for the checked-in browser contract.
There is no separately maintained analytics schema. Window ordering, millisecond
precision, IANA timezone validity and aggregate filter/series bounds are checked
by the Go query boundary beyond the generated structural schema.

The following TypeScript describes the JSON shape. Integers are JSON numbers;
consumers needing exact counters beyond JavaScript's safe integer range should
use a lossless JSON parser. Every metric is returned, including zeros and explicit
`null` for unavailable distinct-client history.

```ts
type Percentiles = { p50_ms: number; p95_ms: number; p99_ms: number };
type Metrics = {
  unique_ips: number | null;
  requests: number; request_errors: number; status_4xx: number; status_5xx: number;
  cache_hits: number; cache_eligible_requests: number; coalesced_requests: number;
  request_bytes: number; response_bytes: number; cached_bytes: number;
  origin_attempts: number; origin_errors: number;
  origin_request_bytes: number; origin_response_bytes: number;
  duration_sum_ms: number; origin_duration_sum_ms: number;
  requests_per_second: number; error_rate: number;
  cache_hit_rate: number; byte_hit_rate: number;
  response_bytes_per_second: number; origin_response_bytes_per_second: number;
  peak_response_bytes_per_second: number; peak_origin_response_bytes_per_second: number;
  latency: Percentiles; origin_latency: Percentiles;
  ttfb: Percentiles; origin_ttfb: Percentiles;
  ttfb_samples: number; origin_ttfb_samples: number;
};
type DropCounts = { queue: number; invalid: number; expired: number; storage: number };
type Coverage = {
  requested_from: string; requested_to: string;
  detail_from: string; rollup_from: string;
  data_from: string | null; data_to: string | null;
  disk_evicted_before: string | null;
  uses_rollups: boolean; latency_approximate: boolean;
  unique_ips_available: boolean;
  partial: boolean; upstream_attributable: boolean;
  base_bucket_seconds: number; interval_seconds: number;
  dropped: DropCounts; queued_events: number; ingest_lag_ms: number;
  geo_country_available: boolean; geo_asn_available: boolean;
};
type Result = {
  summary: Metrics;
  series: Array<{ time: string } & Metrics>;
  breakdowns: Record<string, Array<{ value: string } & Metrics>>;
  geo: Array<{ country: string } & Metrics>;
  coverage: Coverage;
};
```

Response point `time` is RFC3339 UTC, not epoch milliseconds. Rate fields are
fractions (`0.5`, not `50`); byte rates are **bytes/s**, not bits/s. Summary and
ranking rates use the entire requested time window; each series point uses its
overlap with that window. Cache-query hits are HIT/STALE/REVALIDATED; empty,
BYPASS and DISABLED outcomes are excluded from the denominator. COALESCED is
eligible but **not** a hit. Error counts include a nonempty error or status >=500.

Counts/latencies come from completion records. Bytes come **only from transfer
deltas**, not completion totals, so long-running requests contribute before
completion and byte totals cannot double-count completion. A delta is attributed
to its `interval_end` observation timestamp, never spread retrospectively across
a request's duration. The upstream sampler should drain at 10-second boundaries
and sufficiently often; analytics cannot reconstruct unobserved per-byte timing.
Peak rates first sum **all matching dimensions in each aligned 10-second bucket**,
then take the maximum and divide by 10. They are not sums of per-client peaks and
are not peaks of the displayed coarser series.

`latency` / `origin_latency` remain the total request/attempt **duration**
percentiles; their names and meaning are unchanged. `ttfb` / `origin_ttfb` are
separate first-response percentiles over positive `first_byte_ms` measurements.
The corresponding `ttfb_samples` / `origin_ttfb_samples` counts distinguish missing
measurements from fast responses: when a sample count is zero, its percentiles
are all zero and must be displayed as unavailable, not measured zero latency.
This also handles historical records written before first-byte collection.
Opening an existing analytics directory adds first-byte storage columns to raw
and rolled tables without dropping history. Old rows have no TTFB samples; they
are never backfilled with total durations. New rows can be appended normally.

All four distributions use weighted nearest-rank p50/p95/p99. Raw history is exact.
Older rollups retain separate mergeable first-byte and duration distributions with
logarithmic latency bins (at most about 4.43% upward rounding, with a 1ms floor),
and `latency_approximate` applies to both. Ratios are recomputed from sums
and counts; percentiles and rates are never averaged between rows/buckets.

`unique_ips` is an **exact count of distinct normalized, nonempty client IPs on
completed request records** matching the window/filters. Repeated requests and
IPv4-mapped aliases count once. Transfers alone do not create completed-request
clients. It is recomputed independently for summary, each series bucket and each
ranking/geo row; those distinct counts must not be summed across groups/buckets.
It is `null` throughout a query whose window starts before detail retention or
intersects any rolled partition (`unique_ips_available=false`), even if recent
raw data contributes some clients. No IP history is fabricated as zero. Physical
`origin_id`/`protocol` rankings always have `unique_ips=null` because attempts have
no client identity. Otherwise an empty result has `unique_ips=0`; unknown/empty
client addresses are excluded. Capacity loss remains separately marked `partial`.

All output sections use the same filters. Physical origin work is counted once,
not once per coalesced reader. It is **not attributed to individual clients**.
Client-IP/cache-result/content-type/country/ASN filters therefore omit physical
origin records and set `upstream_attributable=false`; zero origin metrics in that
case mean unavailable attribution, not proof that the origin did no work. A
`client_ip`/cache/content/country/ASN ranking likewise cannot allocate origin cost
to a consumer. Host/site/path/method/status filters can select physical events.

## Request details

`Details(ctx, DetailsQuery)` accepts `from`, `to`, `filters`, `limit` (default 100,
maximum 500), `offset` (0..10000). It returns:

```ts
type RequestDetail = {
  request_id: string; site_id: string; host: string; client_ip: string;
  path: string; method: string; content_type: string; status: number;
  cache_result: string; started_at: number; completed_at: number; duration_ms: number;
  first_byte_ms: number;
  request_bytes: number; response_bytes: number; cached_bytes: number; error?: string;
  country: string; asn: number;
};
type DetailsResult = {
  rows: RequestDetail[]; has_more: boolean; next_offset: number | null; coverage: Coverage;
};
```

Newest completions first, then request ID. Details are request outcomes, not
origin attempts or individual transfer samples. Detail byte fields describe that
completed request and need not equal transfer-window totals. Query strings are
removed from paths on ingestion; headers and bodies are never retained.
At the offset cap, `has_more` can be true with `next_offset=null`; narrow the time
window/filters instead of treating bounded exploration as an unbounded export.

## Retention, bounds and operational status

UTC daily DuckDB partitions retain detail/traffic for 14 days plus the current
UTC day by default. On expiry they are SQL-aggregated into low-cardinality
10-second rollups, retained for 90 days plus the current day. Rollups omit client
IP, path and request/attempt IDs; they retain site/host/method/status/cache/type/
country/ASN/origin/protocol. Queries using client-IP/path filters or rankings that
start before the detail horizon return `ErrDetailRetention`, rather than silently
pretending high-cardinality history exists. Other queries can mix raw and rolled
days without overlap. Old rollup events are represented at the base bucket start;
unaligned time-window edges have that 10-second resolution.
Increasing detail retention cannot recover already rolled data; detail-dependent
queries intersecting such a partition still return `ErrDetailRetention`.
`Status.detail_from` and response `coverage.detail_from` report the **actual
conservative contiguous detail horizon**, not merely `now - DetailRetention`.
It is the latest of the configured UTC-day floor, the durable discarded-detail
fence, and the capacity-eviction fence. The discarded-detail fence advances to
the end of each rolled or removed day and survives reopen and later rollup
expiry; older directories reconstruct it from existing rollup filenames before
those files can expire. Status reads a cached fence without acquiring the
catalog lock. For example, rolling October 15 under one-day detail retention,
then reopening on October 18 with 14 days, advertises October 16 rather than
October 4. High-cardinality queries/details starting at that advertised horizon
do not intersect the known rolled history; low-cardinality queries can still use
older rollups. This is a conservative capability boundary, not a claim of
continuous traffic or complete ingestion; drops/capacity loss remain in coverage.
Late events targeting an already rolled day are rejected asynchronously as
`expired` / `ErrDetailRetention`, not misreported as failed storage appends.

Each rollup is one SQL relation containing a counter row per dimension tuple /
10-second bucket and **independently** grouped duration/TTFB histogram rows.
Counter rows own request/attempt outcomes, duration sums and traffic exactly once,
with all distribution weights zero. Each histogram row has one positive dedicated
sample weight (`latency_n`, `origin_latency_n`, `ttfb_n` or `origin_ttfb_n`) and
zero outcome/traffic counters. Percentiles use those weights, never request counts
as a substitute. Independently varying duration and TTFB therefore add histogram
rows rather than forming a joint-bin cross-product. Time/site/host/etc filters
and 10-second peak summation are unchanged; no peaks are precomputed.

Existing joint-bin rollup files are recognized by the absence of the two dedicated
duration-weight columns and converted once transactionally on Open. Stored
first-byte weights and already rounded bin values are preserved without another
rounding step; missing historical TTFB remains unavailable. Reopen recognizes the
new columns and does not reconvert. This conversion preserves logical history
but DuckDB may reuse the old file's blocks rather than shrinking that existing
file; fresh compaction still writes a separate new file and removes raw detail.

The rollup test uses a varied-bin fixture and compares all additive metrics/sample
totals, rates, peaks, series and rankings before/after and after reopen, with the
percentile families within the bin bound. Compression is workload-dependent across
low-cardinality dimensions; disk admission/eviction, not an assumed compression
ratio, enforces the retention capacity budget.

Capacity eviction removes the **oldest whole daily file**, including its WAL,
rather than assuming SQL DELETE shrinks a file. It can shorten retention and is
reported in coverage. The newest partition is not repeatedly erased to make room:
when it alone reaches the admission watermark, storage-pressure drops replace
unbounded growth. That protection ends at the next UTC day, allowing collection
to resume by evicting yesterday's oversized partition. The budget is an application admission/eviction bound, not a
filesystem quota: one bounded batch, DuckDB checkpoints and native storage
overhead/compaction can transiently exceed it. A filesystem/container quota remains necessary
for a hard physical limit. Temporary query storage is separately capped.

Options (zero values select defaults, except `Dir` is required):

| Go field | Default | Meaning |
| --- | --- | --- |
| `Dir` | required | analytics-owned directory |
| `MemoryLimitBytes` | 256 MiB | DuckDB shared buffer-manager limit |
| `TempLimitBytes` | 128 MiB, capped at 1/8 disk budget | query spill limit |
| `DiskBudgetBytes` | 2 GiB | total analytics directory admission/eviction budget |
| `DetailRetention` | 14 days | full-dimensional detail/traffic retention |
| `RollupRetention` | 90 days | low-cardinality retention |
| `QueueCapacity` | 8192 | bounded event admission queue |
| `BatchSize` | 1024 | maximum records in one append batch |
| `FlushInterval` | 1 second | maximum idle batch delay |
| `QueryConcurrency` | 2 | simultaneous queries; excess returns `ErrBusy` |
| `QueryTimeout` | 15 seconds | deadline for the entire query/details operation |
| `Threads` | 2 | DuckDB worker threads (global, not per client) |
| `CountryMMDB` / `ASNMMDB` | none | optional local MaxMind-compatible MMDB files |
| `Now` | `time.Now` | clock seam for deterministic retention verification |

The composition root maps day counts to durations and millisecond timeouts/flush
intervals to `time.Duration`. Detail and full-dimensional traffic share
`DetailRetention`. `MemoryLimitBytes` is shared across DuckDB work, not a fresh
allowance per query. Endpoint result limits above are fixed.
Country and ASN MMDB paths are separate; a country file does not provide ASN data.

Retention must be whole UTC days, rollups >= detail, maximum 366 days. Minimum
disk budget is 32 MiB and minimum DuckDB memory limit 32 MiB. Event field lengths,
query filters, result counts and potential series points are bounded. These
limits also apply to trusted in-process callers. DuckDB's memory limit is **not a
total-process bound** (CGO/native structures, Go queues and mapped GeoIP files are
additional); size the process/container accordingly.

`Status()` returns typed queue/admission/drop counts, lag, last ingest/error,
disk use/budget/pressure, retention horizons and local GeoIP availability/errors.
Queries materialize matching source rows once into a connection-private TEMP
table while holding the catalog read lock, capture partition/horizon/drop state
at that barrier, then release the lock before aggregation. Later ingestion and
partition eviction cannot change any output section or its covered source times.
Details use the same barrier with completed request rows only. The queue can
still fill/drop during a slow bounded materialization or sustained overload;
`queued_events` includes both queued and staged batch events at the barrier.
Rankings select exact top keys by bytes/count/value first, then compute distinct
clients, peaks and distributions only for those winners; summary and series
remain unrestricted. Geography is computed once and reused for country rankings.
The same query deadline/memory/thread/spill budgets apply to materialization and
aggregation. Cancellation cleanup has a separate maximum 2-second context; if
dropping a TEMP snapshot fails, its native connection is discarded, never pooled
with leftover rows. Close stops admission, drains ingestion, waits for active
query lifetimes and their cleanup, then closes DuckDB.

### Query cost

Rankings and geography run against a private snapshot, so the catalog lock covers
only materialization and state capture, not the whole query; cancellation,
partition rolling and shutdown remain correct while the snapshot is alive.
Benchmark numbers are local costs recorded in the private measurement log, not
capacity targets.

Drop counters are process-lifetime counters, explicitly not a durable audit log.
`partial` is conservative for those process-wide drops or outstanding ingestion;
it is not an exact per-filter loss estimate. It also marks windows predating
retention or the durable capacity-eviction fence. `data_from`/`data_to` describe
matching source-row times, not continuous availability or a claim that an idle
period was missing.
Disk-eviction coverage survives reopen. Optional missing/unreadable/wrong-type
GeoIP files are reported and enrichment is disabled; the proxy is unaffected.
Unknown countries use `""`, unknown ASNs use `0`. Bring appropriately licensed
local MMDB files yourself; analytics bundles no production geolocation database
and requires no map assets. Tiny, MIT-licensed upstream test MMDBs live only in
`internal/analytics/testdata` for offline verification. No coordinates or
IP-derived location are fabricated.

Errors are ordinary wrapped Go errors. `ErrClosed`, `ErrBusy`,
`ErrDetailRetention` and `ErrStoragePressure` support `errors.Is`; invalid query
arguments return `*ValidationError` with `field` and `message`. Query execution
honors caller cancellation and the configured deadline. HTTP error mapping and
authentication belong to the management layer.
