# mycdn — agreed design

## Product

A small, single-instance, self-hosted caching reverse proxy with visual management,
graphical rules, and integrated traffic analytics. One Go program, one container,
local volumes. External Nginx/Caddy handles public TLS and HTTP/3 termination.
No users, tenants, distributed control plane, configuration release workflow, or
external Redis/database/queue service. One process owns each data directory.

## Deliberate boundaries

- Public certificates and HTTP/3 termination belong to the outer reverse proxy.
- One process owns a data directory; there is no multi-node consistency or user system.
- GeoIP databases are supplied locally by the operator, not bundled.
- HTTP single ranges are cached; multipart ranges stream through. Arbitrary body
  transformation and arbitrary code execution are not part of the rule language.
- The console uses the accepted dark appearance and a bounded request-detail list.
  Listener and store-budget changes require a restart.
- Application cache/analytics budgets are not filesystem quotas; supported HTTP
  policy is documented in `cache.md` and `analytics-api.md`.

## Engineering character

Elegance, readability, performance, and correctness are equally important.
Use data structures and ownership to remove invalid states. Prefer ordinary Go
functions, concrete types, explicit control flow, and a few real module boundaries.
Do not build speculative abstractions, universal managers, plugin frameworks,
layered defensive programming, or testing scaffolding that dominates the code.
When fixes accumulate, reconsider the model before adding another exception.

## User model

- Sites: stable ID, name, enabled, domains, named origins, default origin.
- Global defaults: caching, freshness/revalidation/stale policy, cache identity,
  origin communication, client identity, request and delivery settings.
- Global ordered rules: enabled/name/stage, nested IF/ELSE IF/ELSE, AND/OR/NOT,
  typed conditions and actions. Rules may span sites through matching.
- Cache management: inspect, purge, refresh.
- Analytics: linked charts, geography, rankings, request exploration, runtime state.

Domains match exact before wildcard. Unknown host is rejected. Global defaults
are overlaid by matching rules; only explicitly set fields change. No separate
site-local behavior hierarchy.

## Runtime

`net/http` serves HTTP/1.1 and h2c. `http.Transport` performs H1/H2 upstream
communication; explicit H3 uses `quic-go`. Reuse immutable transports, distinguish
dial target, HTTP authority, and TLS verification name. HTTPS defaults to verified
H2/H1 ALPN; HTTP defaults to reused H1. Frequency is not a protocol selector.

`httputil.ReverseProxy` supplies the protocol/delivery shell; our RoundTripper
owns planning, cache selection and shared fetching. Never capture cache content
through a ResponseWriter interceptor. Stream SSE/gRPC/upgrades without object caching.

Each request loads one immutable config snapshot. Save compiles and validates,
atomically persists the config document, then swaps the pointer. Existing requests
keep their snapshot; transports and cache are long-lived. No publishing platform.

## Rules

Four matching stages: ingress, request, origin_response, delivery. Outbound
request assembly is an internal step. Request actions determine content identity
before lookup and may schedule delivery operations. Source response rules affect
storage; delivery rules affect an individual response, including HIT and STALE.

Conditions use stage-entry snapshots; actions write output, not later condition
inputs in the same stage. Original request fields remain available. Independent
IF blocks all run; branch chains choose the first match. Array order is the only
priority. Actions and nested branches share one ordered step list. Terminal reply
and redirect end normal processing, but use the delivery path.

Compile typed trees, sets, regex and CIDRs at config save. No CEL, scripting,
JSON interpretation, or per-request expression compilation. Explicit capability
definitions drive validation and UI selection. Explain mode uses the real executor.

Fields: site, host/path/URL/query, extension/name, method, headers/cookies, trusted
client IP/scheme; stage-appropriate upstream status/headers and delivery/cache
results. Operators: typed comparisons, membership, prefix/suffix/contains/glob,
regex, existence, CIDR, AND/OR/NOT. Header names are case-insensitive; values are not.

Actions: client identity sources; cache policy/key; origin selection, path/query/
cookie/header/authority changes; upstream protocol/timeouts; storage decisions;
delivery headers/browser TTL/CORS; redirect/static response; upload/upgrade limits.
Protocol-owned framing headers and cache validators are not generic editable fields.
No general response-body transformation engine or GeoIP dependency in rule matching.

## Cache

Request = a consumer; Fill = shared acquisition work; Entry = reusable published
result. Separate lifetimes, not services. Independent readers use offsets; never
share upstream Body or seek position. A fill has its own cancellation and budget;
one reader disconnecting does not cancel other readers. No subscribers/background
purpose means cancel. A completed fill's body becomes the entry without copying.

Resource -> representation variant -> generation -> covered byte ranges. Generation
is an internal content identity, not config versioning. Refresh writes a new
generation; readers of the old generation finish on it. Conditional 304 updates
metadata and keeps the body. Range completion across independent origin responses
requires a proven compatible representation, e.g. a strong validator and checked
Content-Range. Otherwise do not stitch. Full GET can fetch continuously; logical
blocks are coordination/persistence units, not mandatory separate HTTP requests.

Pebble stores metadata, indexes and small bodies; files store larger bodies.
One generation file can hold sparse ranges. Explicit coverage distinguishes holes
from content. Published bytes and coverage snapshots are immutable; a proven
generation may extend into previously unpublished positions in its existing file.
Concrete range leases prevent overlapping writers and remain pinned until their
readers drain. Up to four disjoint acquisitions per resource share global budgets;
unknown representation discovery and revalidation remain serialized. Body persistence precedes persisted
references. In-process readable progress is distinct from crash-recoverable progress.

Bounded hot metadata/access hints accelerate reads. HIT does not synchronously
update Pebble. Recent access is coalesced, persisted in batches, and checked before
eviction. Approximate recency is acceptable; content identity and invalidation are
not approximate. Access updates cannot resurrect replaced/deleted entries.

Purge, publication, reference acquisition and generation switches share same-key
coordination. A durable invalidation fence rejects old fills. Logical deletion
prevents new readers; physical deletion waits for active readers. Locks are short;
network/body I/O never holds a global/shard-table lock.

## Concurrency

No fixed worker pool in front of HTTP handlers. Go schedules request goroutines.
Separate bounded fills/background work; cap actual upstream requests, buffers,
temporary disk, handles, queued work and analytics. Acquire budgets before spawning.
No per-entry goroutine or per-request timer when a shared sampler can do the work.
Pool buffers for reuse, but account bytes separately. Runtime/CGO/page-cache memory
all share container resources; GOMEMLIMIT alone is not a total-process bound.

## Analytics

Local DuckDB, separate from cache. Accept CGO and a larger binary for embedded
columnar queries; no ClickHouse/Prometheus/Grafana service required. Optional low-
cardinality `/metrics` is separate from multidimensional access analytics.

Collect request outcomes, actual upstream attempts, transfer deltas and runtime
samples separately. Shared fetch cost is counted once; request bytes per reader.
Account transfer throughout long downloads, not all at completion. Request outcomes
default to completion time, realtime ingress/inflight are separate. Track transport
failures as well as status codes. Local GeoIP MMDB enrichment happens off hot path.

Filter by time/site/host/client IP or CIDR/method/status/cache/type/geo/ASN/path.
Use consistent filters for totals, series, map, rankings and details. Store sums /
counts or mergeable distributions, never average percentiles. 10s base traffic
buckets define peak bandwidth. Requests joining active fills are COALESCED, not HIT.
Bypass excluded from cache-query hit denominator. Byte hits follow original data
provenance in the successfully written response prefix, not body read-ahead or
whether a temporary file happened to be read. A frozen per-response prefix mapping
lets final counters and periodic deltas share the same definition.
Older windows remain queryable from low-cardinality history; IP/path rankings and
request details are unavailable outside their retained horizon. A genuinely
detail-dependent user filter is never silently removed to make a query succeed.

Default detail/traffic retention 14 days, low-cardinality rollups 90 days, explicit
capacity budget. Ingestion is bounded and asynchronous, batch appended. Query
concurrency, workers, duration, memory, temporary storage and result count are limited.
Expose lag/coverage/drops; no invisible sampling or fake data. Sensitive headers,
bodies and full query strings are not recorded by default. Geographic data and
map assets have explicit licenses, work offline, and degrade without affecting proxy.

## Frontend

React + TypeScript, embedded static output. Minimal engineering visual language:
square corners everywhere, hairline borders, dark/light,
restrained accent, tabular monospace numbers, compact grids, no decorative gradients
or shadows. ECharts for series/maps, bounded sortable trees, no workflow canvas.
Reusable setting forms serve defaults/actions. Typed OpenAPI -> frontend types;
whole config read/save plus a few operation/query endpoints, no per-node CRUD.

## Verification approach

Keep a small controllable origin fixture and direct scenario tests for shared fills,
independent cancellation, refresh/purge, range/generation integrity, and persistence.
Use race detector for shared state, deterministic data for analytics aggregation,
and a real browser for rules/filter interactions and visual checks. No coverage KPI
or blanket test explosion. Benchmarks establish costs, not hypothetical QPS claims.
