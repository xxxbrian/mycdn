# Verification

Verify architectural claims directly, not by inflating test count. This file is
normative: it lists what the project claims and the minimum evidence for each claim.
Point-in-time results do not live here; see the private working log.

## Claims and evidence

| Claim | Area | Minimum evidence |
| --- | --- | --- |
| Site/default/rule planning has deterministic precedence | Core | Table of nested branch and stage-snapshot cases; real HTTP origin checks |
| Shared work is independent of the first reader | Cache | Two concurrent readers, cancel first, second completes with one origin acquisition |
| Published content cannot mix generations or resurrect after purge | Cache | Controlled refresh/purge overlap, range validator case, reopen |
| Readers see complete success or an actual stream failure | Cache/gateway | Truncated origin and independent readers; no fake EOF |
| Request and physical origin accounting are separate | Analytics/integration | Fixed events plus live shared-fetch scenario |
| Filtered chart totals and peaks are computed from matching records | Analytics | Deterministic multi-host/IP/time fixture |
| First-response latency is separate from download duration | Analytics/integration | Slow body with immediate headers; percentile inputs preserve both |
| Management edits produce actual runtime behavior | Integration | Create a site, save rule, send request, see cache and analytics |
| UI is usable and square-cornered | UI/integration | Real browser desktop/mobile; edit branch, reorder, filter chart, inspect console |
| Single-container deployment works | Integration | Docker build, run, manage site, proxy request, restart with same volume |
| Management is closed without a session | Admin/integration | Anonymous API/metrics rejection, HTTP key login/session/logout, fixed cookie policy regardless of forwarding headers, expiry/logout/restart revocation, cross-site rejection, key-free config/status; public proxy still serves |

Run race detection for shared Go state, `go vet ./...`, the frontend build and a
small set of benchmarks. Repeat a claim only when its relevant code changes; earlier
evidence does not carry over to changed code.

## Runnable entry points

- `make test`, `make race`, `make vet`: Go checks using the correct macOS compiler.
- `pnpm --dir web test` and `pnpm --dir web build`: focused frontend behavior/types.
- `python3 scripts/smoke.py`: actual app config, origin, cache coalescing,
  manual refresh, scoped purge and analytics accounting. Pass
  `--origin-host host.docker.internal` for Docker Desktop/OrbStack.
- `make api-types`: regenerate browser contract from the actual typed server API.
- `make bench`: local warm-cache handler cost, with allocation reporting.

The smoke uses a unique site ID and removes/restores its temporary configuration;
analytics history remains genuine smoke traffic rather than fabricated fixtures.

## Standing scenarios

These guard invariants that are easy to break; keep them covered as the code changes.

- Vary-schema siblings, including a valid 304.
- Orphan close versus a new subscriber.
- Purge waking a failed-range-lease waiter.
- Valid 304 delivery despite a metadata commit failure.
- Mixed sparse delivery: accepted-prefix provenance, not body read-ahead.
- Bare-host refresh and Explain normalize to the same root plan.
- Explicit CORS variation and zero-budget configuration at their boundaries.
- Analytics detail horizon: a detail-dependent filter is never silently removed.
- A simulated response status produces the displayed MISS outcome.
- Smoke waits for observable cache publication before the final EOF/commit check.
