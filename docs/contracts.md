# Implementation contracts

The design lives in `design.md`. This file is normative: it fixes package and API
seams so that internals can change without breaking other modules. Concrete
internals remain each module's choice. No stubs masquerade as working features.
Go module is `mycdn`.

## Core: config, rules, origin, gateway

The cache entry point is `internal/cache/types.go`. Keep `go.mod`/`go.sum` changes
scoped and deliberate. Use the standard library where possible.

Export `gateway.New(*config.Store, *cache.Store, Observer) *Gateway`; `Gateway`
implements `http.Handler`. `Observer` is a small interface owned by gateway with
`RequestCompleted(RequestEvent)`, `OriginCompleted(OriginEvent)`, and
`Transfer(TransferEvent)`. A nil Observer is allowed. Export its event types in
`gateway/observe.go`. Events have copied values, never reference request buffers.
Transfer events are deltas from successful operations, aggregated by a shared
sampler/collector, not an event/channel allocation for every tiny write. Include
request ID/site/host/client IP/path/method/content category/status/cache result and
timestamps in request records. Origin events count *actual attempts* once incl.
background refresh and retries. Transfer dimensions must support IP/host filtering.
Retain a hook to count cached-byte provenance correctly from `cache.Response`.

Config is a single JSON document: schema_version, listen {proxy,admin}, data_dir,
trusted_proxies, sites, defaults, rules, storage, analytics. `config.Default`
returns working defaults without a site. `NewStore(path)` creates defaults if
missing; `Get` returns an immutable compiled snapshot; `Save` validates/compiles
then persists. Rules owns its typed AST and settings, config consumes and compiles
those types into an immutable runtime snapshot. This direction avoids a package cycle.

Site: id/name/enabled/domains/origins/default_origin. Origin: id/name/url, optional
host_header/tls_server_name/dial_address/ca_file/protocol and timeout fields.
Glob domains supported, exact wins. Stable IDs carry cache/rule references.

Rules: id/name/enabled/stage/steps. Stages ingress/request/origin_response/delivery.
Step is an ordered tagged union: {id,type:'action',action:Action} or
{id,type:'if',branches:[{id,condition:Condition|null,steps:Step[]}]}. Null condition
is ELSE, unique and last. Condition is {type:'all'|'any'|'not',children:Condition[]}
or {type:'match',field,key?,op,values:string[]}. Explicit match-all uses
{type:'all',children:[]} (UI shows 'all requests').

Action has type plus typed parameter fields. The core packages define the exact
Go/JSON shape and document it via OpenAPI/capability catalog. Use a limited tagged
struct union, not `map[string]any` in runtime. Required semantic families: cache
policy/key, named origin, upstream host/path/query/cookie/request heads, response
heads/browser TTL, reply/redirect, ingress IP/scheme, upstream protocol/timeouts,
upload/upgrade control. No general upstream body rewrite. Distinguish unset vs false
vs 0 in patches. Expose a catalog of valid stages, fields/operators, actions and
parameter schema.

`Gateway.Explain(input)` returns the real executor's matches and value changes plus
site, origin URL, cache key/policy and client identity. No actual origin call by
default; it may accept a simulated response status/headers. Inputs include peer and
request heads.

## Cache

Own `internal/cache` except honor the public types; propose compatible additions.
`cache.Open` and `Store.Do` use types in `types.go`. `Do` owns conditional requests,
vary/authorization/cookie conservative sharing, fill/revalidation, streaming and
ranges. No imports of config/rules/gateway/admin. The fetch callback is safe to
retain: gateway builds it from an immutable copied outbound plan. Supplied override
headers replace corresponding outgoing headers. Implement store/purge/stats/close.

## Management API

GET /api/config -> full JSON configuration (ETag). PUT /api/config uses If-Match,
returns updated config; validation errors have field paths. POST /api/explain,
GET /api/capabilities, POST /api/cache/purge, POST /api/cache/refresh,
GET /api/cache/entries, GET /api/status. GET /healthz.

Analytics API: POST /api/analytics/query with {from,to,timezone?,interval_seconds?,
filters:{site_ids?,hosts?,client_ips?,methods?,statuses?,cache_results?,
content_types?,countries?,asns?,path_prefix?},group_by?,limit?}. Response
{summary,series,breakdowns,geo,coverage}. GET/POST details endpoints as needed.
Use Unix milliseconds for JSON event and time-series timestamps; durations in ms,
byte counts in bytes. Use RFC3339 strings for request `from`/`to` and response point
times. UTC storage, frontend-selected timezone.

Static UI is embedded in `internal/admin/dist` using go:embed. The build copies
`web/dist` there, so runtime needs no Node process. Proxy and admin listeners are
separate.
