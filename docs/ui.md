# Edge console

The frontend is a real React / TypeScript / Vite SPA in `web/`. It never runs a
mock server, fabricates a metric, or substitutes configured values for observed
runtime measurements. The management listener serves the built assets; Node is
only a build/development dependency.

## Development and verification

```sh
pnpm --dir web install --frozen-lockfile
pnpm --dir web dev
pnpm --dir web test
pnpm --dir web build
```

Vite proxies `/api` and `/healthz` to `http://127.0.0.1:8081`. Override the target
with `MYCDN_ADMIN_URL`. The production output is `web/dist/`; the repository build
owns copying it into `internal/admin/dist`. The frontend owner does not edit that
backend-owned directory.

Strict TypeScript, including unused-variable checks, is part of `build`.
`pnpm --dir web format` formats the source. Dependencies are pinned by
`web/pnpm-lock.yaml`; `pnpm-workspace.yaml` explicitly permits esbuild's install
script. No runtime CDN fonts, tile server, or external JavaScript are needed.

## Visual and interaction model

- Black/dark surfaces, fine gray borders, orange `#ff5a36`, muted labels, tabular
  system monospace measurements. Every corner is square. No shadows or gradients.
- Responsive top navigation, compact forms and tables, keyboard-visible focus,
  labeled controls, skip link, error/status regions, keyboard-operable tabs, and
  keyboard alternatives to drag ordering.
- ECharts is imported modularly. Canvas renderer and chart code are split from
  the application; pages and the country atlas are lazy chunks. Tooltips and
  data-zoom handles explicitly avoid rounded/shadowed defaults.
- Loading, empty, unavailable, partial coverage, validation, transport failures,
  and ETag conflicts are distinct visible states. Routine data caveats are compact
  notes/tooltips, not repeated caution banners.

## Pages

### Analytics

Overview / traffic / cache / visitors / runtime. One shared filter selection
persists in URL search parameters (`range`, `from`, `to`, `site`, `host`, `ip`,
`path`, `status`, `cache`, `method`, `type`, `country`, `asn`). Browser back/forward
restores it and its reading position. Ranking and country-map selection update
those same filters without scrolling to the top.

Today/yesterday use the browser's local calendar; 7/14 days use rolling windows.
The API receives RFC3339 UTC boundaries and the browser's IANA timezone. Displayed
rates, counts and percentiles come from the documented analytics metrics.
Queries select a multiple-of-10-second interval for at most about 720 buckets;
ECharts adds LTTB display sampling. Gaps are not interpolated across missing
buckets. Series share linked zoom and axis tooltips; completing a zoom updates
the global query and requeries KPIs, maps, rankings and details too. `zoom_from` /
`zoom_to` select the subrange; `zoom_base_from` / `zoom_base_to` freeze its slider
bounds. The base does not shrink after each request. Restore range clears zoom
without clearing dimension filters, and selecting a time preset clears prior zoom.

Only first load replaces content with a loading view. Refresh/filter transitions
retain chart instances, the previous query/result pair, and fixed-height chart /
ranking / detail viewports. The compact sticky selection summary shows current
filters and updating/error state. Empty results and query failures do not collapse
the document; newer requests supersede older ones without stale result replacement.

Before the dashboard query, the UI reads the running collector's
`/api/status.analytics_status.detail_from`. It does not infer retention from the
saved config, which may await a restart. Selections starting at or after that
UTC floor request IP/path rankings and load the first 100 details. Older
selections issue one low-cardinality query for summary, series, geography, and
host/method/status/cache/content/country/ASN rankings; IP/path panels and the
request explorer show retained-detail unavailability without sending a detail
request. Genuine client-IP/CIDR or path filters are never dropped: an older
selection with those filters keeps the backend's explicit retention error.
There is no failed-query fallback, hidden range narrowing, or retry with broader
filters. If the running collector does not report a detail horizon, only the
low-cardinality views are requested and detail availability remains unknown.

KPIs include requests, delivered bytes, peak bit/s from the backend's 10-second
base peak, eligible-request cache hit ratio, byte hit ratio, and p95 first response
(TTFB), with p95 full duration separately labeled in the same timing tile.
The traffic timing chart pairs the independent TTFB and full-duration
distributions; origin first-response/full-duration p95 are separately reported
when attributable. TTFB sample count zero displays a dash, never full duration or
a fabricated 0ms. Request details retain their existing first-100 scope and show
first response separately from full duration; historical `first_byte_ms:0` is
unavailable. Ratios with no denominator also display a dash, not a fictitious 0%.
There are cache/status distributions, host/IP/path/content/method/country/ASN
rankings, an offline country heat map, and the first 100 retained detail records.
Rankings follow backend order (delivered bytes, then requests); bar lengths show
requests. The visitor tile uses the backend's exact `unique_ips` on completed
requests, never a top-IP ranking count or a sum of buckets. `unique_ips:null`
remains unavailable with a coverage notice for windows without full IP history;
an actual empty raw result can report zero unique IPs.

`upstream_attributable=false` removes origin totals/trends rather than presenting
unattributable zeros as no origin work. Coverage notes disclose dropped events,
ingest lag, capacity eviction, approximate historical latency, and rollups.

### Sites, rules, and settings

These edit one shared in-memory draft of the entire config. Navigate freely
between editors; no per-node CRUD or publication/release system exists. Save sends
the entire document with the last exact ETag in `If-Match`. Missing ETag disables
save; 409/412 preserves the draft. Field-path validation errors remain visible.
Edits made while a save is in flight remain as a new local draft. Page unload
warns about unsaved changes. Export/import is optional, not the primary editor.

Sites support enable/delete/create/edit, domain lists, multiple named origins and
a default origin. Advanced connection forms separate URL, dial address, HTTP
Host, TLS verification name, CA file, protocol and all timeout fields. Protocol
choices include HTTP-only prior-knowledge `h2c`, separate from HTTPS `h2`/`h3`.
An upstream request timeout of zero remains disabled; cache fills use their own
`fill_timeout_ms` budget read from the server rather than a frontend default.

Rules use the owned tagged AST from `docs/contracts.md`, not a third-party
expression tree or JSON text editor. Stage/field/operator/action choices come
from `/api/capabilities`. Forms cover every current core action: identity,
limits, cache patch/status TTL, cache key replacement, origin, path, query,
cookie, request/source/delivery header mutation, authority, protocol/timeouts,
browser TTL, CORS, static reply, and redirect. Unset, false, zero and explicit
empty status-TTL lists remain distinct.

Recursive ALL/ANY/NOT groups, keyed header/query/cookie matches, independent IF
steps, first-match IF/ELSE IF/ELSE chains, and nested action/branch steps are
graphical. Rules, steps, condition children and non-ELSE branches have sibling-only
native drag and up/down controls. ELSE is pinned last. New nested sibling lists are
bounded to 32 and new nesting to 8; existing deeper data is preserved and called
out rather than truncated. Duplicates receive fresh rule/step/branch IDs.

Settings group cache/identity/upstream/request/delivery defaults, listeners/trusted
proxies, storage budgets, analytics collection/retention/query budgets, and live
runtime. Restart-required settings are shown when reported by the API.

Client identity has a preset and an optional ordered `client_ip_headers` list.
Unset/null keeps the preset (or inherited plan for a rule patch); explicit `[]`
is peer-only. The override toggle never rewrites `[]` into unset. Each source has
a header name and `single`, `xff`, or `forwarded` parser; the first valid source
wins. Up to 16 rows support sibling drag and keyboard up/down, with local stable
keys that are not serialized. Custom sources still require trusted proxy peers.

Storage includes `max_readers` and independent `fill_timeout_ms`; the current
server fill default is 1800000ms and existing saved values are not overwritten.
The explicit config requires positive `memory_bytes`, `max_background`, and
`inline_bytes`: cache-library zero values select defaults, not disabled budgets.
These forms use minimum 1 with a short hint, and import/save rejects zero before
submission. This is separate from valid zero values such as upload-unlimited
`max_upload_bytes:0` and disabled upstream `request_ms:0`.
Analytics uses only runtime-backed `memory_bytes`, `temp_bytes`, `threads`,
`country_mmdb`, `asn_mmdb`, the shared detail/traffic retention, and the existing
collector/query budgets. Removed `traffic_days`, `query_max_rows`,
`query_memory_bytes`, and `geoip_file` fields have no forms/types and are rejected
at import/save before network submission, not silently translated or sent.

### Debug and cache

Explain calls the actual backend executor with request headers, peer identity,
optional Host override, client HTTP version, and simulated upstream status/headers/cache
result. It displays selected site/origin/authority/protocol, cache policy/key,
trusted identity, socket peer IP, client HTTP version, terminal result, rule trace before/after changes and resulting
delivery headers. It explicitly uses saved config, not the unsaved local draft.
Entering a simulated response status sets its model's `cache_result` to `MISS`
immediately, matching the visible selection and submitted request body. Changing
status preserves an explicitly selected cache result/headers; clearing status
removes the simulated response.

Cache has applied site/host/exact-path/path-prefix selection, entry metadata,
scope-confirmed purge, and explicit URL refresh. Listing is `{entries,limit,
truncated}`, not cursor pagination. A truncation message asks the operator to
narrow filters. Purge sends nonempty selectors, or `all:true` only after explicit
all-entry confirmation; it never sends a cache key.

Refresh sends `{urls:string[],site_id?:string}` using absolute HTTP(S) client URLs
at configured hosts. The separate textarea is not a filter-wide refresh. An
entry shortcut prepares its host/path URL there for review before submission;
it does not invent a missing query, original scheme, header, or cookie. HTTP is
used for the initial suggestion and can be edited. Operations only report an
outcome from the server's `{results:[{url,status,reason?,error?}]}` response.
Every URL's actual status/reason/error is shown; HTTP 200 is not treated as
all-success. The current route has no top-level refreshed/skipped/failed counters,
so displayed counts are derived from those returned outcomes (unknown statuses
remain visible as other outcomes). A batch is limited to 32 URLs.

Runtime includes two linked CPU and Go-memory trend panels above process details.
They use only `/api/status.history`'s five-second samples, retained for up to an
hour. CPU values are process percentages and may exceed 100% across cores; Go
memory uses `go_bytes`, with a separate allocated-heap line. The summary's
`memory_bytes` is labeled **Go memory**, not process RSS. Empty history stays empty.

## API boundaries and integration

`web/src/generated/api.d.ts` and `openapi.json` are generated from the Go API by
`scripts/generate-api.mjs`. They are the wire-type source for analytics
metrics/results/details/coverage, runtime info/status, cache envelopes and
operations, explain, and the capability catalog. `web/src/wire.ts` performs
nullable-list normalization and display-unit translation at the API boundary.
`web/src/api.ts` centralizes HTTP/errors/concurrency, with generated-schema
`satisfies` checks for outbound query/operation bodies.

`web/src/types.ts` keeps discriminated editable rule/config types and optional
draft fields; storage/analytics/capabilities/runtime/operations/explain reuse
generated aliases or narrow extensions. `web/src/configWire.ts` converts the
generated document into those editor types, validating plain generated strings
before narrowing enum/tagged values. Save requires every generated complete
document field and uses `satisfies WireConfiguration`; it never invents absent
defaults or casts through `unknown` to hide incompatibility. Explicit false/zero
and peer-only `[]` survive the conversion. All nullable wire collections are
normalized once rather than propagating null handling through forms. Generated
files remain owned by the schema-generation script (`scripts/generate-api.mjs`); frontend
formatting does not rewrite them.

Endpoints used:

| Endpoint | Client contract |
| --- | --- |
| `GET /api/config` | Plain config document + ETag |
| `PUT /api/config` | Plain whole document, `If-Match`, updated config + ETag |
| `GET /api/capabilities` | `stages`, typed `fields` and `actions` catalog |
| `POST /api/analytics/query` | Query and result exactly as analytics-api.md |
| `POST /api/analytics/details` | `{from,to,filters,limit}` -> `{rows,has_more,...}` |
| `POST /api/explain` | Gateway ExplainInput/ExplainResult JSON types |
| `GET /api/status` | Top-level `RuntimeStatus`; no nested envelope |
| `GET /api/cache/entries` | `site_id/host/path/path_prefix/limit` -> `{entries,limit,truncated}` |
| `POST /api/cache/purge` | `{site_id?,host?,path?,path_prefix?,all?}` -> `{purged:number}` |
| `POST /api/cache/refresh` | `{urls:string[],site_id?:string}` -> `{results:[{url,status,reason?,error?}]}` |

Runtime/cache boundaries are aligned with the final admin API decision.
The API error parser was not expanded or redesigned during this focused pass.
The current core/config schema is aligned, including `h2c`, ordered identity
sources, storage fill/reader budgets and split country/ASN MMDB options. New
`peer_ip`/`http_version` conditions come from the live capability catalog; ingress
`client_ip` is not hardcoded into the editor. The accepted visual structure is
preserved.

Other explicit limitations:

- Unique IPs measure normalized nonempty client addresses on completed requests,
  not people/accounts. The count is unavailable for windows without full IP
  history; ranking cardinality is not used as a substitute.
- Request explorer shows 100 results with a truncation message, not full detail
  pagination. Narrow filters for a precise sample.
- Historical low-cardinality dashboard views work within rollup retention.
  Explicit IP/path filters outside detail retention still return the backend's
  retained-detail error; IP/path ranking/detail availability is labeled separately.
- Runtime history is process-local (up to one hour), not durable analytics history.
- Only dark appearance is implemented. No user/account system exists.
- Generated Go schema strings are narrowed only at the boundary. Editable AST
  types intentionally remain discriminated rather than exposing the raw tagged
  Go struct to every form.

## Geographic assets

The bundled world-atlas 1:110m country polygons are Natural Earth public-domain
data; world-atlas and topojson-client are ISC. Numeric country IDs are joined to
ISO alpha-2 codes using i18n-iso-countries. Antarctica is omitted. Map geometry is
loaded offline and registered with ECharts only when needed. Unknown country
events are not assigned a fake location. An empty map shows the geographic
reference outline with an explicit no-data / missing-enrichment notice.
The map uses a D3 equirectangular projection stream, including antimeridian
clipping. Russia/Fiji polygons crossing ±180° are split at the map edge rather
than drawn as cross-world lines. The viewport contains the projected bounds
without stretching or initial zoom cropping.

License notices ship at `/licenses.txt` (`web/public/licenses.txt`). Operators
provide and license their own GeoIP MMDB; no database is bundled.

## Browser verification

`pnpm --dir web build` runs strict TypeScript and the production build;
`pnpm --dir web test` covers filters, rule editing, action forms, ETag/error
handling, analytics mapping, offline map assets, identity sources and config
roundtrips.

No browser interaction test is claimed here; verify against the real backend.
Use the real backend and a controllable origin to verify:

1. Create a site, two origins, choose a default, save, reload and verify values.
2. Build nested IF/ELSE IF/ELSE + NOT/OR, every representative action family,
   drag and keyboard reorders, explicit false/0, save and inspect real explain.
3. Generate requests, see linked KPI/series/rankings, apply ranking/map filters,
   use back/forward/custom range and verify consistent network query filters.
4. Open two consoles; save one and confirm the other's stale ETag preserves its
   local draft with an explicit conflict.
5. Verify exact cache selectors cannot broaden a purge, bounded-list truncation,
   and refresh URL review/submission using configured site hosts.
6. Check 390px, 768px and desktop layouts, focus navigation, error/loading/empty
    states, map offline operation, and restart-required status.
7. With a slow-body origin, verify first-response p95 differs from full-duration
   p95, historic no-TTFB rows stay unavailable, and unique IPs come from the exact
    summary metric rather than the top ranking.
8. Check the runtime CPU and Go/heap traces against `/api/status.history`; verify
   mixed refreshed/skipped/error batches display the exact returned per-URL
    result and never present partial success as a completed full refresh.
9. Select 30 days with a 14-day detail/90-day rollup setup (also 7 days with a
   running 3-day detail horizon): summary/series/map/low-cardinality rankings
   should load, IP/path/detail panels should show unavailable, and no details
   request should be sent. Add an old IP/path filter and retain the explicit error.
10. In Explain, enter only response status 200 and verify the network body contains
    `response.cache_result:"MISS"`. Try zero for the three explicit cache budgets
    and confirm form/import/save rejection without changing the live config.
