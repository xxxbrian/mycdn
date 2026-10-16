# Deployment

## Container

```sh
docker compose up --build -d
```

The example binds proxy `8080` and management `8081` to loopback. The program runs
as UID 10001 and stores config, cache and analytics under `/data`. The named volume
keeps its contents across image updates. If using a bind mount instead, make it
writable by UID 10001. Stop the instance before backing up the data directory.

Only one process may own the directory. Multiple independent instances need
separate volumes. No external database or queue is needed.

## Outer reverse proxy

Public Nginx/Caddy handles certificates and public H1/H2/H3. It forwards to the
mycdn proxy listener over HTTP/1.1 or h2c. Keep caching off there, preserve Host,
and allow streaming. Point a separately protected management route at port 8081.

Example Caddy routing when both services share a private Docker network:

```caddyfile
assets.example.com, downloads.example.com {
    reverse_proxy mycdn:8080
}

cdn-admin.example.com {
    # Protect this route using your existing access/authentication setup.
    reverse_proxy mycdn:8081
}
```

Configure the actual trusted proxy network in mycdn. Do not assume all private
networks are trustworthy. Choose client IP header sources through ingress rules;
the outer proxy must clean and populate those headers consistently.

For Nginx, keep the original Host, set forwarding information and turn response
buffering off on streaming routes. Standard WebSocket upgrades need the normal
outer-proxy upgrade configuration as well.

## GeoIP

Provide licensed MMDB files locally and configure their paths in analytics settings.
Mount them read-only when practical. Country/city and ASN databases are separate.
Missing GeoIP data leaves geography unknown, without affecting requests or caching.

## Toolchain

- Go 1.26+ with CGO enabled and a C++ toolchain (the container build uses 1.27).
- Node.js and pnpm for the frontend (`pnpm --dir web build`).
- DuckDB and its Go bindings are linked in; no separate database service.

`make` uses Apple's SDK-aware clang on macOS. If a package manager shadows clang
and a raw `go build` fails with `library not found for -lresolv`, use:

```sh
CC=/usr/bin/clang CXX=/usr/bin/clang++ go build ./cmd/mycdn
```

The Makefile pins the same compiler on Darwin. This does not affect the Docker build.

## Smoke check

With the program running locally:

```sh
python3 scripts/smoke.py
```

For a Docker instance on Docker Desktop/OrbStack:

```sh
python3 scripts/smoke.py --origin-host host.docker.internal
```

The script creates a temporary origin, saves a temporary site through the real API,
checks cache reuse and concurrent acquisition, then restores configuration. It
does not generate traffic in the background or install persistent demo sites.

The Dockerfile builds only target-platform Go imports. This matters for DuckDB's
native bundles: a blanket `go mod download` would also fetch unrelated platforms.
CGO stays enabled; the runtime contains CA roots and the C++ runtime, not a Go or
Node toolchain.
