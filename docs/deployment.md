# Deployment

## Container

```sh
export MYCDN_ADMIN_KEY="$(openssl rand -hex 32)"
docker compose up --build -d
```

The example binds proxy `8080` and management `8081` to loopback. The program runs
as UID 10001 and stores config, cache and analytics under `/data`. The named volume
keeps its contents across image updates. If using a bind mount instead, make it
writable by UID 10001. Stop the instance before backing up the data directory.

Only one process may own the directory. Multiple independent instances need
separate volumes. No external database or queue is needed.

## Management key

Set `MYCDN_ADMIN_KEY` in the runtime environment to a strong random key (for
example, `openssl rand -hex 32`). The binary refuses to start without a 32-to-4096
byte key, including at least 32 bytes after trimming surrounding whitespace.
There is no default key or unauthenticated mode. Keep the real value in deployment
secrets or a local uncommitted `.env` file; `.env.example` contains no usable key.
Do not pass it as a Docker build argument, commit it, or put it in `config.json`.
Reuse the saved value across deployments rather than generating it blindly each
time. The key and login sessions are not written into `/data` or a database.

Both mycdn listeners accept HTTP only. There is no TLS listener, certificate
configuration, HTTPS redirect, or transport-mode environment variable. Dokploy,
Nginx, Caddy, or another outer reverse proxy terminates public TLS and forwards
HTTP to mycdn. Keep that backend hop private and enforce public transport policy
at the ingress; management authentication does not encrypt the connection.
The local Compose example binds both HTTP ports to loopback.

Sign in with the key, without a username. Sessions expire after 12 hours; logout
revokes one session, and restart/key rotation invalidates all sessions. Health
checks can still use public `/healthz`. Monitoring clients must obtain a session
before reading `/metrics` or the management API. `-version` and `-openapi` require
no key and do not open stores.

## GitHub Actions and GHCR

`.github/workflows/container.yml` runs frontend build/tests, Go tests, focused race
tests and vet for pull requests to `main` and pushes to `main`. A push to `main`,
or a manual run on `main`, also builds Linux AMD64 and ARM64 images on native
runners. Pull requests and manual runs on other branches cannot publish images.

Each platform image is uploaded by digest, then checked for its embedded version,
frontend, real proxy/cache/analytics behavior and restart with the same data volume.
Only after both platforms pass does the workflow publish multi-platform tags:

```text
ghcr.io/<owner>/<repository>:main
ghcr.io/<owner>/<repository>:sha-<full-commit>
```

The branch tag moves to a successful build only while its commit is still the
current branch head. Failed checks leave it unchanged; outdated runs still publish
their commit tag but cannot move the branch tag backward. Automatic publishing is
enabled only for `main`.

The repository path is lowercased. The workflow uses its built-in `GITHUB_TOKEN`
with `packages: write` in publishing jobs; no stored personal token is needed for
publishing. OCI source/revision labels associate the image with its source. The
binary's `-version` output and management status report the same `sha-<full-commit>`
build identifier. Local Docker builds default to `dev`; override with
`docker build --build-arg VERSION=<identifier> .`.

There are no `latest`, `stable`, semantic-version aliases or Git tag/release
triggers. Publishing an image does not trigger a Dokploy deployment. The Actions
run summary includes the published tags and an exact `ghcr.io/...@sha256:...`
reference. Pin the digest when exact image bytes matter: tags can move, and rerunning
a build for the same commit can pick up changed base images or system packages.

Check GHCR package visibility after the first publication rather than inferring
pull access from repository visibility; the workflow does not set visibility. For a
private package, configure Dokploy's registry credentials using a GitHub account
with package access and a classic personal access token with `read:packages`. A
public package can be pulled anonymously. Do not commit registry tokens or place
them in Docker build arguments.

## Dokploy

Use an Application with Docker/GHCR as its source and select a published tag or
digest; no server-side source build is needed. Mount a persistent volume at `/data`.
Use `:main` to pull the current branch build on deployment, or a commit tag/digest
to select a particular build. A changed `:main` tag does not update an already
running container without a pull and redeployment.
The existing `compose.yaml` is a local-build example, not a registry-only Dokploy
configuration.

Route business domains through Dokploy's Traefik to container port `8080`, preserving
the original Host. Add the same domains to sites in mycdn. Route a separate
management entry point to port `8081`; enable public HTTPS in Dokploy, not mycdn.
Set the runtime management key in Dokploy's environment settings. The built-in
key login protects management without a Traefik authentication middleware.
Direct public host-port bindings are unnecessary. Configure trusted proxy networks
and client-IP headers for the actual ingress network.

Run one replica, use stop-first updates and allow at least 30 seconds for graceful
shutdown. Never overlap old and new containers against the same `/data`, including
during rollback. Updates can therefore cause a brief interruption. Pinning an older
image does not restore data; stop the service and back up the complete data directory
before changes requiring a recoverable data snapshot.

## Outer reverse proxy

Public Dokploy/Nginx/Caddy handles certificates and public H1/H2/H3. It forwards
HTTP/1.1 or h2c to the mycdn proxy listener, and HTTP/1.1 to management. Neither
mycdn listener accepts TLS. Keep caching off at the outer proxy, preserve Host,
and allow streaming. Terminate a separate public HTTPS management route there
and forward HTTP to port 8081.

Example Caddy routing when both services share a private Docker network:

```caddyfile
assets.example.com, downloads.example.com {
    reverse_proxy mycdn:8080
}

cdn-admin.example.com {
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

Give the smoke script the running instance's `MYCDN_ADMIN_KEY` in its environment.
It checks anonymous rejection, signs in using a separate admin cookie jar, and
never forwards that cookie to the public proxy. It logs out after cleanup.

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
