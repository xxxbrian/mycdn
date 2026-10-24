# Implementation decisions

This records choices that might otherwise be lost among the design discussions.

- **Personal tool, not a platform.** Keep defaults useful and operations direct.
  Do not surface protocol/accounting caveats as alarming copy throughout the UI.
- **Typed rule tree, not CEL.** The graphical grammar is finite; compiling its
  own typed tree is clearer than converting to and recovering an expression language.
- **Four user-visible stages.** Outbound assembly is not a fifth matching stage.
  Content-changing outbound settings are decided before cache identity is computed.
- **Cache does not own rules.** It receives immutable fetch instructions and a
  response-policy callback. It does not know how the UI represents a condition.
- **ReverseProxy is an adapter, not the cache engine.** Reuse protocol behavior;
  keep decisions and shared acquisition under our control.
- **Recency is approximate; content is not.** Batch access hints, strictly coordinate
  body generations, publication, references and invalidation.
- **Small means few moving parts.** DuckDB makes a larger binary but avoids a
  separate analytics service or a homemade multidimensional database.
- **No fake completeness.** Missing GeoIP stays unknown, no traffic stays empty,
  dropped analytics stays visible. Unsupported configuration is rejected at save.
- **Parameters are not architecture.** Inline size, block size, flush frequency,
  queue capacities and chart granularity can be tuned from measured behavior.
- **Evidence, not test volume.** A few direct lifecycle scenarios matter more
  than mocking every function or aiming at a coverage percentage.
- **Storage failure is not delivery failure.** Once a valid response body is
  available, a cache metadata commit can fail without corrupting that delivery.
  Explicit refresh still reports that persistence failed.
- **Published bytes, not frozen files.** A strong-validator sparse generation may
  gain disjoint unpublished extents in place. Coverage snapshots stay immutable,
  published positions never change, and failed writers retain leases until reads drain.
- **Count what was delivered.** Cache-read progress can run ahead of client writes.
  A frozen body-prefix provenance function is shared by completed counters and
  periodic samples; it avoids extra mutable per-chunk accounting state.
- **One key, not an account platform.** An environment-supplied random management
  key is exchanged for an opaque SCS session. Memory-only sessions give real logout
  revocation without database credentials, JWT signing/refresh machinery, or a
  mandatory outer authentication service. Restart intentionally revokes them all.
  The pinned SCS revision includes its upstream memory-store shutdown race fix.
