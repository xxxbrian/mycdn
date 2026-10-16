# Management API

The typed Go routes are authoritative. The server publishes OpenAPI at
`/api/openapi.json`; `make api-types` exports the same schema and TypeScript
contract to `web/src/generated/` without a running instance.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/config` | Full config, with ETag |
| PUT | `/api/config` | Validate, persist, activate; If-Match required |
| GET | `/api/capabilities` | Rule stages, fields, operators, typed actions |
| POST | `/api/explain` | Saved runtime's actual planner and rule trace |
| GET | `/api/cache/entries` | Bounded listing by site/host/path/prefix |
| POST | `/api/cache/purge` | Scoped invalidation, or explicit `all:true` |
| POST | `/api/cache/refresh` | Refresh explicit client-facing URLs |
| POST | `/api/analytics/query` | Summary, series, rankings and geography |
| POST | `/api/analytics/details` | Retained request records |
| GET | `/api/status` | Live runtime, stores, restart-required settings |
| GET | `/metrics` | Small low-cardinality Prometheus endpoint |
| GET | `/healthz` | Listener liveness |

## Configuration

Fetch the whole document before modifying it. Save exactly that document with the
ETag returned by GET in `If-Match`. A stale tag returns 412, leaving the local
draft untouched. Validation reports field paths. Ordinary site/rule/default changes
take effect immediately; process-level changes appear as restart-required.

## Cache operations

Listing query parameters: `site_id`, `host`, `path`, `path_prefix`, `limit`.
Response: `{entries,limit,truncated}`. No implicit cursor or unbounded export.

Purge body:

```json
{"site_id":"assets","host":"cdn.example.com","path_prefix":"/images/"}
```

Selectors are combined. No selector requires explicit `{"all":true}`; an empty
body cannot accidentally clear the cache. Response: `{"purged":42}`.

Refresh body:

```json
{"urls":["https://cdn.example.com/images/a.webp"],"site_id":"assets"}
```

The URLs are interpreted by the current gateway planner, not sent through an
external public endpoint. Site ID is an optional routing check. Refresh uses
current fetch rules and revalidation while retaining old cached content.
The response is `{results:[{url,status,reason?,error?}]}`, where status is
`refreshed`, `skipped`, or `error`; the UI displays individual outcomes.

Detailed rules and analytics shapes are in [`core-api.md`](core-api.md) and
[`analytics-api.md`](analytics-api.md). Generated OpenAPI is the final wire contract.
