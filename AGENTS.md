# mycdn

Small, self-hosted caching reverse proxy. Read `docs/design.md` and
`docs/contracts.md` before changing module boundaries.

## Code principles

- Prefer clear ownership and small concrete types to defensive branches and frameworks.
- Keep the hot path free of database writes, JSON interpretation, and analytics queries.
- Validate at external boundaries; trust internal invariants instead of checking them repeatedly.
- Use mature protocol/storage libraries, not homemade replacements.
- Keep mutable state local. Never mutate shared configuration, headers, transports, or published bodies.
- Tests should establish meaningful behavior with controllable origins; no coverage targets or mock pyramids.
- UI is compact, square-cornered, low-decoration engineering tooling. No rounded cards, pills, gradients, or heavy shadows.
- Do not silently pretend unsupported or partial features are complete.

## Documentation map

`docs/` is normative and present-tense: it describes how the system is now. Update
the relevant document in the same change as the behavior.

| Document | Tier | Responsibility |
| --- | --- | --- |
| `README.md` | entry | What it is, how to run, layout |
| `AGENTS.md` | contract | Agent bootstrap, principles, boundaries, verification |
| `docs/design.md` | design | Product, invariants, module character |
| `docs/contracts.md` | contract | Package/API seams and interfaces |
| `docs/api.md`, `docs/core-api.md`, `docs/analytics-api.md` | contract | Wire contracts |
| `docs/cache.md`, `docs/management.md`, `docs/ui.md` | behavior | Module behavior |
| `docs/deployment.md` | ops | Container, outer proxy, toolchain |
| `docs/implementation-notes.md` | rationale | Why non-obvious choices were made |
| `docs/verification.md` | policy | Claims and their minimum evidence |
| `docs/performance.md` | method | How performance is measured |

Rules:

- Normative documents are current truth. If they disagree with the code, that is a bug.
- Rationale is append-mostly; change it only when a decision is reversed.
- Point-in-time results (benchmark numbers, one-off test evidence, delivery
  checkpoints, construction logs) do not belong in this repository. Keep them in the
  private working log (`.slim/deepwork/`, gitignored) and cite the conclusion in the
  commit message instead.

## Boundaries

- Never commit secrets, credentials, private hosts, personal domains, session IDs,
  agent-coordination logs, temporary scripts, or machine-specific measurements.
- Public documents describe the system and how to work on it, never how a particular
  run was coordinated.
- Keep generated browser contracts (`web/src/generated/`) in sync via `make api-types`.

## From a fresh clone

```sh
make build          # frontend + Go binary
make test           # go test ./...
make race           # focused go test -race on shared state
make vet
pnpm --dir web build
make api-types      # regenerate web/src/generated after API struct changes
```

## Verification

Use `go test ./...`, focused `go test -race` for shared state, `go vet ./...`,
and `pnpm --dir web build`. Keep scenario checks focused on behavior, not internals.
`docs/verification.md` lists the claims and their minimum evidence.

`make` selects Apple's SDK-aware clang on macOS; direct CGO commands in environments
with a package-manager clang may need `CC=/usr/bin/clang CXX=/usr/bin/clang++`.
Regenerate browser contracts with `make api-types` after changing API structs.
