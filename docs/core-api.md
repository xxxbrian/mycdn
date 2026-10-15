# Headless core API (schema version 1)

The implementation is in `internal/config`, `internal/rules`, `internal/origin`,
and `internal/gateway`. It is usable without management/UI/analytics. A nil cache
means an uncached proxy; a nil observer disables analytics callbacks.

## Go entry points

```go
// internal/config
func Default() Document
func Decode(io.Reader) (Document, error) // strict JSON, one document, <= 4 MiB
func Validate(Document) (*Snapshot, error)
func NewStore(path string) (*Store, error)
func (*Store) Get() *Snapshot
func (*Store) Save(Document) error
func (*Snapshot) Document() Document
func (*Snapshot) Revision() string // SHA-256 hex of normalized document JSON
func (*Snapshot) Program() *rules.Program
func (*Snapshot) MatchSite(normalizedHost string) (Site, bool)
func (*Snapshot) IsTrusted(netip.Addr) bool
func (*Snapshot) Plans() rules.Plans

// internal/rules
func Compile([]Rule) (*Program, error)
func Capabilities() Catalog
func (*Program) Run(Stage, Facts, *Plans, *[]Trace)
func (*Program) FactsKey(Stage, Facts) string
func (*Program) CaptureFacts(Stage, Facts) Facts
func (Plans) Clone() Plans

// internal/origin
func New() *Manager
func (*Manager) Transport(config.Origin, rules.UpstreamSettings) (http.RoundTripper, error)
func (*Manager) Close() error

// internal/gateway
func New(*config.Store, *cache.Store, Observer) *Gateway
func (*Gateway) ServeHTTP(http.ResponseWriter, *http.Request)
func (*Gateway) Explain(context.Context, ExplainInput) (ExplainResult, error)
func (*Gateway) Refresh(ctx context.Context, urls []string, siteID string) ([]RefreshResult, error)
func (*Gateway) Stats() Stats
func (*Gateway) Close() error
```

`NewStore` creates/persists defaults if the file is missing. Save validates and
compiles a private copy, writes a mode-0600 temporary file in the same directory,
fsyncs it, renames it, fsyncs the directory, then swaps an atomic snapshot pointer.
Invalid saves do not publish or overwrite the document. Existing requests keep
their snapshot. `Document`, `MatchSite`, and `Plans` return independent editable
copies; `Program` is immutable and shareable. CA material is loaded at validation
time, not read during acquisition. `rules.ValidationError` is
`{"path":"rules[0].steps[0].action","message":"..."}`.

Save is serialized, last successful save wins. Management code should serialize
its revision/If-Match check with Save if it needs compare-and-swap semantics.
Listener/data-directory/storage/analytics process changes must be applied by the
composition root (normally with a restart); Store does not silently reconfigure
listeners, existing stores, or the initial upstream admission capacity.

Shut down listeners and drain requests/cache fills before closing Gateway. It
stops its sampler and closes origin transports; it does **not** close config,
cache, or the observer.

## Complete configuration example

All names and units below are the actual JSON shape. Documents are whole configs,
not JSON merge patches. Start with `config.Default()` / GET config before editing;
there is no implicit default-merging of an incomplete submitted document.

```json
{
  "schema_version": 1,
  "listen": {"proxy": ":8080", "admin": "127.0.0.1:8081"},
  "data_dir": "data",
  "trusted_proxies": ["127.0.0.1/32", "::1/128"],
  "sites": [
    {
      "id": "example",
      "name": "Example",
      "enabled": true,
      "domains": ["cdn.example.com", "*.cdn.example.com"],
      "origins": [
        {
          "id": "main",
          "name": "Main origin",
          "url": "https://origin.example.com/assets",
          "host_header": "origin.example.com",
          "tls_server_name": "origin.example.com",
          "protocol": "auto",
          "timeouts": {"response_header_ms": 30000}
        }
      ],
      "default_origin": "main"
    }
  ],
  "defaults": {
    "cache": {
      "mode": "respect",
      "ttl_seconds": 0,
      "stale_while_revalidate_seconds": 0,
      "stale_if_error_seconds": 0,
      "prefetch": false
    },
    "cache_key": {"include_host": true, "query_mode": "all"},
    "upstream": {
      "protocol": "auto",
      "timeouts": {
        "connect_ms": 10000,
        "tls_handshake_ms": 10000,
        "response_header_ms": 30000,
        "idle_ms": 90000,
        "request_ms": 0
      }
    },
    "identity": {
      "client_ip_source": "trusted_x_forwarded_for",
      "scheme_source": "trusted_x_forwarded_proto"
    },
    "request": {"max_upload_bytes": 67108864, "allow_upgrade": true},
    "delivery": {}
  },
  "rules": [],
  "storage": {
    "max_bytes": 10737418240,
    "memory_bytes": 67108864,
    "max_fills": 64,
    "max_background": 4,
    "max_object_bytes": 2147483648,
    "inline_bytes": 32768,
    "block_bytes": 1048576,
    "max_readers": 4096,
    "fill_timeout_ms": 1800000
  },
  "analytics": {
    "enabled": true,
    "detail_days": 14,
    "rollup_days": 90,
    "max_bytes": 2147483648,
    "queue_size": 8192,
    "batch_size": 1024,
    "flush_interval_ms": 1000,
    "query_concurrency": 2,
    "query_timeout_ms": 15000,
    "memory_bytes": 268435456,
    "temp_bytes": 134217728,
    "threads": 2
  }
}
```

`config.Default()` has no sites and no trusted proxies. An empty proxy list never
trusts forwarding headers. Host matching is ASCII/case-insensitive, drops a final
dot and request port, prefers exact names, then longest `*.suffix`. The wildcard
can span labels but does not match its apex; other glob patterns are rejected.
International hostnames must be punycode. Disabled sites do not route. Duplicate
domains/IDs, malformed listeners, invalid URLs/budgets/rules are rejected.
Unknown host is HTTP 421; malformed authority/query is HTTP 400.
An empty client URL path is normalized to `/` before original/stage facts, rules
and cache identity are built, without mutating the caller's request. Bare-host
URLs in Explain/manual refresh therefore have the same plan as an explicit `/`.

Origins accept optional `dial_address` (`host:port`) and `ca_file` (PEM file).
`url` has no userinfo/query/fragment. Its base path is joined to the planned path
without decoding escaped separators. HTTP Host is `host_header` or URL authority,
and may be changed by an authority action. Dial address does not change either
Host or TLS verification name. The latter is `tls_server_name` or URL hostname.
Custom CA certificates are added to the system trust pool; verification is never
disabled. Origin `protocol`/individual timeout members override defaults; request
rule overrides are applied **after** the selected origin's settings.

Cache `mode` is `respect`, `force`, or `bypass`. Times are seconds; `status_ttl` is
an optional array such as `[{"status":404,"ttl_seconds":30}]`. The cache owns
freshness, conservative sharing, Vary, conditional/range handling and publication.
`force` sets TTL but does not override private/no-store/Set-Cookie safety barriers.
Cache key also accepts `namespace`, `query_names`, `headers`, `cookies` arrays.
`query_mode` is `all`, `ignore`, `include`, or `exclude`; `query_names` selects the
latter two. Query keys are canonicalized; repeated value order is preserved.
Keys always include site, selected origin/URL/dial/TLS identity, authority and
escaped outbound path. Config revision and delivery actions are not content
identity. Request facts used by source conditions additionally partition keys,
so a source rule depending on client IP/header cannot poison another partition.
`CaptureFacts` retains only these explicit immutable rule dependencies for a fill;
unused consumer headers, client identity and the original request are not retained
by the source-transform callback.

`identity.client_ip_source` is `peer`, `trusted_x_forwarded_for`, or
`trusted_forwarded`. It is the convenience preset when optional
`client_ip_headers` is omitted or null. A non-null list overrides the preset,
and an explicit empty list `[]` means peer-only (preserved through save/reload).
Headers are tried in array order; the first present, valid source wins. Invalid,
ambiguous or missing sources are skipped; no valid source means peer fallback.
Every source requires a trusted immediate socket peer. For example:

```json
{
  "client_ip_source": "trusted_x_forwarded_for",
  "client_ip_headers": [
    {"name":"X-Real-IP","format":"single"},
    {"name":"X-Visitor-IP","format":"single"},
    {"name":"X-Forwarded-For","format":"xff"},
    {"name":"Forwarded","format":"forwarded"}
  ],
  "scheme_source": "trusted_x_forwarded_proto"
}
```

Formats are `single` (exactly one bare IP value, no ports/comma chains), `xff`
(comma-separated bare IPs), and `forwarded` (RFC-style Forwarded elements).
The latter two walk right-to-left, stripping trusted hops and stopping at the
first untrusted address; spoofed leftmost addresses never override that boundary.
Malformed/obfuscated/oversized chains invalidate that source as a whole. At most
16 named sources, 64 hops per chain, and bounded header lengths are accepted.
The operator must ensure the trusted immediate proxy **overwrites** a `single`
header rather than forwarding an arbitrary client-supplied value; appended
multi-hop headers must use `xff` or `forwarded`. Choosing priority is explicit
trust policy, not proof that a client-supplied named header is authentic.

`scheme_source` is `connection`, `trusted_x_forwarded_proto`, or
`trusted_forwarded`. X-Forwarded-Proto accepts one unambiguous `http`/`https`
value. Forwarded proto is taken from the last (immediate proxy) element. An
ingress identity action changes request-stage identity, not other ingress
conditions. Use `peer_ip` and `http_version` at ingress; `client_ip` and
`original.client_ip` are rejected there to avoid circular source selection.
Identity is for matching/analytics; shared origin fetches deliberately strip
Forwarded/X-Forwarded-* and the configured custom identity headers rather than
claiming the first client's identity on subsequent refreshes.
Known per-consumer IP/request-ID/tracing headers (`X-Real-IP`, `X-Client-IP`,
`True-Client-IP`, `CF-Connecting-IP`, `X-Request-ID`, `X-Correlation-ID`,
`traceparent`, `tracestate`, `baggage`) are removed at outbound planning too.
Applications must not encode hidden client identity into other cacheable inputs
without a matching key/Vary boundary; arbitrary application headers cannot be
automatically classified as consumer metadata.

Request `max_upload_bytes:0` is unlimited; `allow_upgrade:false` returns HTTP 403
for an upgrade. Known oversized bodies return 413 without contacting an origin;
unknown-size bodies are bounded while read. Defaults `delivery` accepts
`headers:HeaderMutation[]`, `browser_ttl_seconds`, and `cors` (shapes below).

### Storage and analytics option mapping

The composition root passes storage fields directly to `cache.Options`, with
`Dir` under `data_dir` and `FillTimeout = time.Duration(fill_timeout_ms) *
time.Millisecond`. `max_readers` maps to `MaxReaders`. Defaults are 4096 readers
and a 1800000ms (30-minute) independent fill budget; `fill_timeout_ms` must be 1..86400000.
All saved storage budgets must be positive, including `memory_bytes`,
`max_background`, and `inline_bytes`. Zero is rejected at its exact `storage.*`
field instead of silently selecting cache.Open's Go zero-value defaults. Disable
prefetch/stale refresh through cache policy rather than a zero background budget.
This fill budget is deliberately separate from the upstream request deadline,
which defaults to **zero/disabled** so uncached long streams are not terminated
after five minutes.

Analytics config has only runtime-backed options; it does not expose separate
traffic retention or a global query-row setting. Optional `country_mmdb` and
`asn_mmdb` map to the two independent local enrichment databases.

| Config JSON | analytics.Options field | Validation |
| --- | --- | --- |
| `memory_bytes` | `MemoryLimitBytes` | at least 32 MiB; entire DuckDB memory budget, not per query |
| `temp_bytes` | `TempLimitBytes` | 1 MiB..`max_bytes/8` |
| `max_bytes` | `DiskBudgetBytes` | at least 32 MiB |
| `detail_days` | `DetailRetention` | whole days, at least 1 |
| `rollup_days` | `RollupRetention` | `detail_days`..366 |
| `queue_size` | `QueueCapacity` | 1..1048576 |
| `batch_size` | `BatchSize` | 1..8192 |
| `flush_interval_ms` | `FlushInterval` | 1..60000ms |
| `query_concurrency` | `QueryConcurrency` | 1..8 |
| `query_timeout_ms` | `QueryTimeout` | 1..120000ms |
| `threads` | `Threads` | 1..16 |
| `country_mmdb` | `CountryMMDB` | optional local path |
| `asn_mmdb` | `ASNMMDB` | optional local path |

`enabled` controls whether the composition root opens analytics/attaches its
observer. Durations are converted to Go durations at composition; config has no
analytics import or database work. Explicit complete configs must meet these
bounds even when analytics is disabled. The former `traffic_days`,
`query_max_rows`, `query_memory_bytes`, and `geoip_file` fields are not accepted.

## Rule JSON and execution

```json
{
  "id": "assets", "name": "Cache static assets", "enabled": true,
  "stage": "request",
  "steps": [
    {
      "id": "choose", "type": "if",
      "branches": [
        {
          "id": "static",
          "condition": {"type":"match","field":"extension","op":"in","values":["js","css"]},
          "steps": [
            {"id":"cache","type":"action","action":{"type":"cache","cache":{"mode":"force","ttl_seconds":3600}}}
          ]
        },
        {
          "id": "otherwise", "condition": null,
          "steps": [
            {"id":"bypass","type":"action","action":{"type":"cache","cache":{"mode":"bypass"}}}
          ]
        }
      ]
    }
  ]
}
```

Stages: `ingress`, `request`, `origin_response`, `delivery`. Rules and steps run
in array order; independent IF blocks all execute. Each branch chain executes
only its first match. A null condition is ELSE, unique and last, after an IF.
Actions and nested IFs share the ordered step list. Reply/redirect stop the
current stage and normal origin work, but still pass through delivery. A delivery
terminal can replace a fetched/cached response and closes its unused body.

Conditions read a stage-entry snapshot. Actions cannot affect later conditions
in that stage. Later stages see request-stage output; `original.*` always sees
ingress values. Source rules run once per source acquisition before cache
eligibility/storage; delivery runs separately for MISS, COALESCED, HIT, STALE,
bypassed, terminal and routed error responses.

Condition forms:

* `{"type":"all","children":Condition[]}` (empty means match all).
* `{"type":"any","children":Condition[]}` (empty means match none).
* `{"type":"not","children":[Condition]}` (exactly one).
* `{"type":"match","field":"...","key":"...","op":"...","values":["..."]}`.

Fields available in all stages: `site` (stable site ID), `host`, `path`, `url`,
`query`, `extension` (without dot), `filename`, `method`, `header`, `cookie`,
`peer_ip` (socket peer, never a header), `http_version` (e.g. `HTTP/2.0`), `scheme`,
and each corresponding `original.*` field. `client_ip` and `original.client_ip`
are available only in request/source/delivery, **not ingress**. `query`,
`header`, `cookie` require `key`. `status` and keyed `response_header` exist in
source/delivery. `cache_result` exists only in delivery.

String operators: `eq`, `ne`, `in`, `not_in`, `prefix`, `suffix`, `contains`,
`glob` (Go path.Match, `*` does not cross `/`), `regex` (RE2), `exists`,
`not_exists`. Integers: `eq`, `ne`, `in`, `not_in`, `gt`, `gte`, `lt`, `lte`.
IP fields: `eq`, `ne`, `in`, `not_in`, `cidr`, `exists`, `not_exists`.
All comparison values are strings in JSON; integers/IPs/CIDRs are typed at save.
Only membership/CIDR accept multiple values; existence takes none. A positive
match succeeds on any value; negative membership succeeds only when none match,
including a missing field. Present empty header/query/cookie values exist.
Header names are case-insensitive; string values remain case-sensitive.

### Actions

`Action` has `type` and **exactly one** typed parameter. Omitted patch members
do not change output; false and zero are explicit settings. Values below are
literal, not templates/interpolation or scripts.

| Type | Parameter JSON | Stages |
| --- | --- | --- |
| `identity` | `identity:{client_ip_source?,client_ip_headers?:[{name,format}],scheme_source?}` | ingress |
| `limits` | `limits:{max_upload_bytes?,allow_upgrade?}` | ingress, request |
| `cache` | `cache:{mode?,ttl_seconds?,stale_while_revalidate_seconds?,stale_if_error_seconds?,prefetch?,status_ttl?}` | request, origin_response |
| `cache_key` | `cache_key:{namespace?,include_host,query_mode,query_names?,headers?,cookies?}` (whole key settings) | request |
| `origin` | `origin:"named-origin-id"` | request |
| `path` | `path:"/absolute/path"` (no query/fragment) | request |
| `query` | `query:{operation:"set\|add\|delete",name,value?}` | request |
| `cookie` | `cookie:{operation:"set\|delete",name,value?}` | request |
| `request_header` | `header:{operation:"set\|add\|delete",name,value?}` | request |
| `authority` | `authority:"host[:port]"` | request |
| `upstream` | `upstream:{protocol?,timeouts?:{connect_ms?,tls_handshake_ms?,response_header_ms?,idle_ms?,request_ms?}}` | request |
| `source_header` | `header:{operation:"set\|add\|delete",name,value?}` | origin_response |
| `response_header` | `header:{operation:"set\|add\|delete",name,value?}` | ingress, request, delivery |
| `browser_ttl` | `browser_ttl_seconds:300` | ingress, request, delivery |
| `cors` | `cors:{origins:["https://app.example"],methods?,headers?,expose?,credentials,max_age_seconds?}` | ingress, request, delivery |
| `reply` | `reply:{status:403,content_type:"text/plain",body:"Denied"}` | ingress, request, delivery |
| `redirect` | `redirect:{status:302,location:"https://example.com/"}` | ingress, request, delivery |

`browser_ttl` replaces delivery Cache-Control with `public, max-age=N` and removes
Expires without modifying stored metadata. CORS is delivery-only and emits headers
for allowed Origin values; it does not automatically answer preflight. Use a reply
rule for preflight if desired. Wildcard origins cannot allow credentials. Explicit
origins always add one normalized `Vary: Origin`, including responses to missing
or disallowed Origin headers; existing Vary members are merged case-insensitively.
Wildcard-only CORS does not add variation. Reply bodies are limited to 1 MiB; status is 200..599.
Redirect statuses are 301/302/303/307/308. Source headers can change freshness
policy headers before storage, but not framing/validators/Vary. Header names,
values and authority are validated for injection. Framing, hop-by-hop headers,
Host, Range/conditionals, validators, Age, Vary, and content range are not generic
editable fields. Request Forwarded/X-Forwarded-* edits are prohibited.
Set-Cookie edits belong to delivery, never the source stage: removing a cookie
header must not accidentally make a personalized origin response shareable.

`rules.Capabilities()` returns `{stages,fields,actions,schemas}`. Field entries
have `{name,type,key,stages,operators}`; actions have `{type,parameter,stages}`;
schemas is keyed by parameter name with a JSON-Schema-compatible subset
`{type,properties?,items?,required?,enum?}` generated from actual typed structs.
It describes editor parameters; compiler validation additionally enforces bounds,
protected headers, tagged-union correctness and stage semantics. Disabled rules
are validated too. Trees have bounded depth/size. No JSON interpretation occurs
on the request path.

## Explain

```json
{
  "method": "GET",
  "url": "https://cdn.example.com/a.css?v=1",
  "peer": "127.0.0.1:55000",
  "headers": {"X-Forwarded-For":["192.0.2.5"]},
  "response": {"status":200,"headers":{"Content-Type":["text/css"]},"cache_result":"HIT"}
}
```

Optional `host` overrides URL authority. Headers are arrays of strings. URL must
be absolute HTTP(S); HTTPS models a TLS connection. Method defaults to GET.
Optional `http_version` defaults to `HTTP/1.1` and accepts `HTTP/1.0`, `HTTP/1.1`,
`HTTP/2.0`, or `HTTP/3.0` as stage facts; it does not make a network request.
`response` is optional: without it source rules are not run; delivery can still
be explained against a hypothetical 200/MISS or a terminal result. With it, the
source and delivery stages execute against the supplied response. No transport,
origin, or cache operation is performed.
Request and simulated response header names are validated and canonicalized at
the Explain boundary, so lowercase JSON keys match real HTTP response rules.
An omitted/empty simulated `cache_result` defaults to `MISS`.

Result fields are `site_id`, `origin_id`, `origin_url`, `authority`, `protocol`,
`cache_key`, `cache_policy`, `client_ip`, `peer_ip`, `http_version`, `scheme`, optional `terminal_status`,
`trace`, `response_headers`. Trace items contain `stage`, `rule_id`, `step_id`,
optional `branch_id`/`matched`, or `action`/`before`/`after`. Request/cookie header
trace values are redacted. Explain uses the real compiler/executor/planner, not a
second matching implementation.

## Transport, streaming and accounting

`auto`: HTTP uses reused H1; HTTPS offers verified H2/H1 ALPN. `h1` disables H2;
`h2` is HTTPS H2-only, failing rather than falling back. `h2c` is **HTTP-only
prior-knowledge cleartext HTTP/2**, using Go Transport.Protocols with HTTP1 false
and UnencryptedHTTP2 true; there is no HTTP/1 Upgrade negotiation or downgrade.
HTTPS with `h2c` and HTTP with `h2`/`h3` are rejected. `h3` is explicit verified
HTTPS over QUIC via `github.com/quic-go/quic-go/http3` with no downgrade. There is
no frequency-based protocol choice, Alt-Svc auto-upgrade or insecure TLS option.
All share immutable transports. A manager is bounded to 256 distinct transport
configurations; retired configs remain reusable until close/restart, not an
unbounded accumulation. Timeouts are milliseconds, zero disables the corresponding
Go timeout (QUIC uses its documented zero defaults for handshake/idle). Request
timeout bounds acquisition through body completion, except an accepted upgrade.
Its default is disabled (`request_ms:0`); cache fills still have their independent
`storage.fill_timeout_ms` budget.
HTTP/3 response-header timeout currently includes upload time.

SSE/gRPC/upgrades and uploads bypass object lookup. SSE/gRPC response types are
also rejected by cache storage if not recognizable until origin headers arrive.
The proxy streams using ReverseProxy's delivery shell and immediate flushes;
upgrade duplex bodies remain duplex through accounting wrappers. It does not
capture content via ResponseWriter for caching. Actual upstream work, including
bypass and upgrade traffic, has an admission cap initialized from `storage.max_fills`.
Cache cancellation/refresh semantics belong to `cache.Store`; fetch callbacks own
independent immutable URL/header/transport plans and accept cache override headers
(including nil values to remove headers).

```go
type Observer interface {
    RequestCompleted(RequestEvent)
    OriginCompleted(OriginEvent)
    Transfer(TransferEvent)
}
```

Observer methods must be concurrency-safe and nonblocking. Events own copied
values, never request buffers. Timestamps are Unix ms, durations ms, byte counts
successful application-body bytes (not wire headers/TLS/framing). Request records
have request/site/host/IP/path/method, MIME content type, status/cache result,
start/end/duration, request/response/cached bytes and optional error. Origin events
have attempt/site/origin/host/path/method/protocol/status, timing/body bytes/error;
no client IP or first-consumer RequestID. httptrace counts actual transport
attempts, including transparent retries and connection/TLS failures, once rather
than per coalesced reader. No-body origin responses complete at headers.

Origin and request events additionally expose `first_byte_ms` (positive
milliseconds; zero means unavailable). Downstream measures the first final header
write; origins use the transport's first-response-byte trace, falling back to
header arrival for a transport without that trace. Transfer events include
`protocol`.
Elapsed first-byte and total durations both round **up** to milliseconds, so a
sub-millisecond measurement is positive and never exceeds the total duration.
Request completion follows actual delivery: cancellation after all advertised
bytes were successfully written (or an unknown-length body reached EOF) does not
retroactively make the request fail. Early cancellation, client write failures,
truncation and independent non-EOF source errors remain failures.

Transfer deltas are drained by one shared one-second sampler and on completion;
there is no event/channel allocation per tiny Write. They carry `kind` (request
or origin), IDs, dimensions, `interval_start`/`interval_end`, status/cache/type and
request/response/cached-byte deltas. Request deltas include IP; shared origin cost
is intentionally not attributable to a client IP. Cached-byte provenance comes
from the immutable `cache.Response.CachedPrefix(deliveredBytes)` mapping, not a
body reader's cumulative cached-byte count. The sampler loads accepted output
bytes first, then evaluates that prefix, so read-ahead and partial writes cannot
attribute cached tail bytes to an undelivered prefix or create cached-only deltas
while client output is unchanged. Origins and consumers
are separate records; joining an active fill remains COALESCED, never HIT.

## Live gateway statistics

`Gateway.Stats()` reads only atomics and the upstream admission channel; no
analytics query, timer, or database is involved. Its exported JSON shape is:

```json
{"started_at":1792108800000,"active_requests":2,"requests":120,"active_upstreams":1,"max_upstreams":64}
```

`started_at` is gateway creation time in Unix milliseconds. `requests` counts
handler admissions, including unknown-host/failed requests; `active_requests`
counts handlers that have not returned, including open upgrades. `active_upstreams`
counts occupied acquisition permits through body/upgrade completion (including
cache refresh), not TCP connections. `max_upstreams` is the initial configured
admission capacity. Independent fields are approximate concurrent snapshots, not
a transaction, and work with a nil observer.

## Manual refresh

`Gateway.Refresh` uses the real cache refresh operation, not purge+public request:

```go
func (*cache.Store) Refresh(context.Context, cache.Request) error
func (*Gateway) Refresh(ctx context.Context, urls []string, siteID string) ([]RefreshResult, error)
type RefreshResult struct {
    URL string `json:"url"`
    Status string `json:"status"` // refreshed, skipped, or error
    Reason string `json:"reason,omitempty"`
    Error string `json:"error,omitempty"`
}
```

Input is 1..32 explicit absolute **client** HTTP(S) URLs (not arbitrary origin
URLs), each at most 8192 bytes, without credentials/fragments. One fresh immutable
config snapshot is loaded for the batch. All URLs, enabled-host routing, query
syntax, selected origin resolution and optional `siteID` binding are validated
**before any acquisition**. Invalid input returns a field-path overall error and
nil results. A closed gateway, unavailable cache, or already-canceled caller also
returns an overall error without acquisitions. Validation messages identify the
input index, never echo raw URLs/query values.

The batch executes sequentially in input order, within normal cache/upstream
budgets. Each plan is an independent bodyless GET with no admin/client headers,
peer, HTTP protocol facts or inherited context values. The URL scheme is available
to rules; HTTPS models its client scheme without inventing a trusted proxy. Rules
that require a client IP/header cannot target an individual client's variant from
this API. Request/origin/source plans use the current executor exactly; terminal
or explicit bypass plans return `skipped` with a `reason` and no acquisition.
No delivery-stage rules, external public self-request, ServeHTTP/ResponseRecorder,
RequestCompleted event, or gateway request counter increment is involved.

`cache.Request.Revalidate` is set explicitly. Cache Refresh **forces a conditional
acquisition even when fresh**, drains the response and waits for publication/304
commit through same-key coordination and generation fences; old usable content
is not purged and remains available during replacement. A non-storable response,
stale-error fallback, rejected publication or origin error is an `error`, never a
fake successful refresh. Only a committed replacement/revalidation returns
`refreshed`. Physical OriginCompleted/Transfer events use the usual independent
acquisition plan, with no first-consumer metadata retained.

After validation, overall error is nil and each URL has its actual result:
`refreshed`, `skipped` with `reason`, or `error` with a query-safe diagnostic
category. A URL error does not stop other URLs. Caller cancellation marks the
active/unstarted remainder as errors, with cache owning shared-fill lifetime.
The result `url` intentionally echoes the explicit input; treat it as sensitive
operator input, not an analytics/log record. Error strings never include it.

## Boundaries and verification coverage

Tests use controllable HTTP/TLS/QUIC origins for protocol selection, verification,
connection reuse, MISS/HIT/delivery separation, shared cancellation, background
conditional refresh, malformed/trusted identity, local terminal/explain behavior,
streaming sampler, upgrade echo, SSE bypass, upload limits and failure accounting.
H2 accounting, a transparent H1 retry, HEAD terminal responses, gRPC upload/download
trailers, H2-only refusal of H1, and verified H3 are exercised directly as well.
Additional focused tests cover reused prior-knowledge h2c, ordered custom IP
sources, empty-list persistence, ingress peer/protocol source selection, runtime
budget validation, disabled default deadlines, and active request/upstream stats.
Manual refresh tests prove changed current plans and source rules are used,
fresh old content stays readable while replacement waits, conditional 304 updates
commit, full-batch validation precedes acquisition, errors/skips are not success,
and only physical origin work generates analytics for the admin operation.
Cancellation is also tested to stop the active wait and avoid acquiring later
URLs in the batch; caller HTTP trace/context values never reach the origin.

This package does not create proxy/admin listeners or implement the management
API, TLS termination, downstream H3, GeoIP rule conditions, body transformations,
automatic retry policy, or explicit refresh-by-filter endpoint. Its cache layer
integration uses the public `Do`/Transform/Refresh APIs, including cache-scheduled refresh.
Source rule/header changes do not retroactively rewrite existing stored entries;
purge or change namespace when changing stored-response semantics. Origin IDs are
site-local; an origin action must select a name present on the matched site or
that routed request fails explicitly with 502. No unsupported setting is silently
treated as successful.
