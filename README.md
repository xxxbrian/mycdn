# mycdn

A self-hosted caching reverse proxy: sites, graphical rules, shared origin fetches,
cache refresh, and traffic analytics in a single Go program.

An outer Nginx/Caddy handles public certificates; mycdn handles caching and origin
fetches. There is no account platform, control cluster, external database, or queue.
The full design is in [`docs/design.md`](docs/design.md).

## Run

```sh
docker compose up --build -d
```

- Console: <http://localhost:8081>
- Proxy: `localhost:8080`, selecting a site by request Host.
- Create a site, set its domains and origins, then reach it through the proxy.
- Defaults are written to the data volume on first run; changes saved in the console
  take effect immediately.

The console has no user system. Put it on a local machine, a private network, or
behind an authenticated outer entry point. If the outer proxy also runs in a
container, join it to the same private network and connect to `mycdn:8080`; then
configure trusted proxy ranges and client-IP header sources for the actual network.

## Local development

Requires Go 1.26+, a CGO/C++ toolchain, Node.js, and pnpm. DuckDB is linked into the
binary; no separate database service is needed.

```sh
make build
./bin/mycdn -config data/config.json
```

Frontend development: start the backend, then in another terminal run
`pnpm --dir web dev`.

```sh
make test
make race
make vet
```

Management API types: `make api-types`. The source is the Go management routes, so
no running instance is needed. Real-path check: `python3 scripts/smoke.py`.

## Layout

| Path | Responsibility |
| --- | --- |
| `internal/gateway` | HTTP entry, request planning, cache and origin orchestration |
| `internal/rules` | Condition trees, branches, actions, rule interpretation |
| `internal/cache` | Pebble index, body files, shared fills and reads |
| `internal/origin` | Connection reuse, TLS, upstream protocols |
| `internal/analytics` | DuckDB ingestion, queries, local GeoIP |
| `internal/admin` | Management API and embedded frontend |
| `web` | Square-cornered engineering console |

Interfaces and module boundaries: [`docs/contracts.md`](docs/contracts.md).
Management API: [`docs/api.md`](docs/api.md). Deployment and toolchain:
[`docs/deployment.md`](docs/deployment.md). Verification:
[`docs/verification.md`](docs/verification.md). Performance method:
[`docs/performance.md`](docs/performance.md).

## Data

Configuration and data live in the mounted directory. One data directory belongs to
exactly one running instance. GeoIP uses an MMDB database you supply; without one,
the proxy and other analytics are unaffected. Do not copy a database that is being
written to as a consistent backup; stop the instance and back up the whole data
directory.

## License

MIT; see [LICENSE](LICENSE). Third-party components and data retain their own
licenses; see [THIRD_PARTY.md](THIRD_PARTY.md).
