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

## GitHub Actions and GHCR

`.github/workflows/container.yml` runs frontend build/tests, Go tests, focused race
tests and vet for pull requests to `main` and pushes to `main`. A push to `main`,
or a manual run on `main`, also builds Linux AMD64 and ARM64 images on native
runners. Pull requests and manual runs on other branches cannot publish images.

Each platform image is uploaded by digest, then checked for its embedded version,
frontend, real proxy/cache/analytics behavior and restart with the same data volume.
Only after both platforms pass does the workflow publish a multi-platform tag:

```text
ghcr.io/<owner>/<repository>:sha-<full-commit>
```

The repository path is lowercased. The workflow uses its built-in `GITHUB_TOKEN`
with `packages: write` in publishing jobs; no stored personal token is needed for
publishing. OCI source/revision labels associate the image with its source. The
binary's `-version` output and management status report the same `sha-<full-commit>`
build identifier. Local Docker builds default to `dev`; override with
`docker build --build-arg VERSION=<identifier> .`.

There are no `latest`, `stable`, semantic-version aliases or Git tag/release
triggers. Publishing an image does not trigger a Dokploy deployment. The Actions
run summary includes both the tag and an exact `ghcr.io/...@sha256:...` reference.
Pin the digest when exact image bytes matter: tags can move, and rerunning a build
for the same commit can pick up changed base images or system packages.

GHCR package visibility is independent of repository visibility. A newly created
package defaults to private; the workflow does not change visibility. For a private
package, configure Dokploy's registry credentials using a GitHub account with
package access and a classic personal access token with `read:packages`. An
explicitly public package can be pulled anonymously. Do not commit registry tokens
or place them in Docker build arguments.

## Dokploy

Use an Application with Docker/GHCR as its source and select a published tag or
digest; no server-side source build is needed. Mount a persistent volume at `/data`.
The existing `compose.yaml` is a local-build example, not a registry-only Dokploy
configuration.

Route business domains through Dokploy's Traefik to container port `8080`, preserving
the original Host. Add the same domains to sites in mycdn. Route a separately
authenticated management entry point to port `8081`, or keep it private: the console
has no login system. Direct public host-port bindings are unnecessary. Configure
trusted proxy networks and client-IP headers for the actual ingress network.

Run one replica, use stop-first updates and allow at least 30 seconds for graceful
shutdown. Never overlap old and new containers against the same `/data`, including
during rollback. Updates can therefore cause a brief interruption. Pinning an older
image does not restore data; stop the service and back up the complete data directory
before changes requiring a recoverable data snapshot.

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
