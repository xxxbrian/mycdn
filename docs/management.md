# Management behavior

## Saving

The browser edits a local configuration document. Save validates the entire
document and atomically makes it current. There are no drafts, publication states
or release controls. The editor shows unsaved changes and preserves the draft on
validation errors. A config ETag prevents another tab's changes being overwritten.

Site, rule, and origin IDs are stable identifiers. Display names can change freely.
Array order is rule precedence; the browser does not submit a second priority field.

Listener/data-path/storage-engine settings that require restarting must be named
as such in settings. Do not label those changes immediately active merely because
the file was saved. Ordinary routes, rules and origin choices update live.

## Entry points

The admin listener is separate from the public proxy listener. It serves the
frontend and the JSON API; the proxy never routes unknown domains to management.
Both listeners accept HTTP only; public TLS terminates at the outer reverse proxy.
All management operations require a login session. The public proxy is unaffected.

## Authentication

One shared management key is supplied through `MYCDN_ADMIN_KEY`, never through
the config API, a database, or a command-line argument. It must be 32 to 4096 bytes
with at least 32 bytes after trimming surrounding whitespace. Use a strong random
key, not a human password. Startup fails before opening storage when the key is
missing or invalid. Authentication retains a SHA-256 digest for constant-time key
comparison; it does not retain the submitted login key in session state.

Login creates/rotates an opaque SCS session. Session tokens are hashed in the
process-local memory store, expire 12 hours after login, and do not survive a
restart. Logout revokes the session server-side as well as deleting its cookie.
Changing the environment key requires a restart, invalidating all old sessions.
No usernames, roles, registration, password-reset flow, or account records exist.

The fixed HTTP-compatible `mycdn_session` cookie is host-only, `HttpOnly`,
`SameSite=Strict`, and `Path=/`, without the `Secure` attribute. There is no
scheme-dependent cookie mode or forwarding-header detection in authentication.
Public TLS and transport enforcement belong entirely to the outer reverse proxy;
keep the backend HTTP connection private. Authentication does not encrypt the key
or session in transit.

The login shell/assets, `/api/auth/login`, `/api/auth/session`, and minimal
`/healthz` are public. Every other management path, including config reads,
analytics, OpenAPI, unknown API paths and `/metrics`, rejects missing/expired/
forged sessions with JSON 401. No wildcard CORS is enabled. Go's cross-origin
protection rejects unsafe cross-origin browser requests, including login/logout.
Login requires JSON, has an 8 KiB request limit, and does not echo submitted keys
in validation errors. An instance-wide token bucket admits 10 login attempts in a
burst and refills one every five seconds; excess attempts return 429 with
`Retry-After`. It ignores untrusted forwarding headers and does not throttle
already-authenticated management operations. Failed-login floods can temporarily
delay legitimate logins; the key's high entropy remains essential.

## Cache operations

- Purge removes matching objects from future lookups and prevents old fills from
  republishing. Existing downloads can finish.
- Refresh reacquires matching content while keeping usable old content available.
  It is not implemented as purge followed by a public request.
- Manual refresh accepts explicit client-facing URLs, replans them against current
  saved routes/rules, and waits for genuine acquisition completion. It does not
  reconstruct unknown request headers/cookies from a disk entry's path.
- Filters identify site, host, exact path or prefix; UI previews the scope.
- Cache listing is bounded. A text filter is not permission to scan an unbounded
  database on every keystroke.

## Analytics

One filter object drives totals, time series, breakdowns, map, and request details.
Time controls include today, yesterday, 7d, 14d and custom. Display timezone is
visible; stored timestamps remain UTC. Browser navigation restores filters through
URL state. Slow results cannot replace a newer filter selection.

Same-view updates keep mounted charts and the reading position. Only the first
load uses a full loading placeholder; later requests keep their previous result
with its original query and a small updating indicator. A failed update preserves
that result. Empty chart/ranking/detail regions retain their layout. A compact
sticky selection summary exposes the active filters without moving the page.

Chart sliders select a global subrange inside a stable base window. The URL keeps
both windows, so the slider can widen again and Restore range clears only zoom.
Explicit time presets/custom range changes establish a new base and clear zoom;
dimension filters do not. Each committed selection creates one history entry,
and Back/Forward restores the entry's reading position after its data is ready.

Selecting host/IP/country/path rankings adds a removable filter. Chart zoom changes
the same shared range rather than an isolated hidden zoom state. Missing data and
ordinary zeros are visually distinct where appropriate. No demo fallback numbers.

Analysis operations have fixed resource limits. Returning fewer chart points is
aggregation, not event sampling. Summaries use the full selected range, not just
the chart's returned point count.
Detail availability follows the running store's actual retained history, not the
configured day count: increasing retention cannot recreate previously rolled data.

## Rule editing

Changing the stage updates selectable fields/actions; incompatible existing steps
are surfaced rather than silently deleted. IF/ELSE chains preserve branch scope
when moved. ELSE stays last. Editing, duplication and keyboard move controls use
stable IDs rather than array positions as object identity.

The condition editor uses explicit groups for precedence. Missing and empty field
values are not conflated. Labels are readable, but stored field/action identifiers
are stable and language-independent.
