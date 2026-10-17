# Performance measurement

Performance work is measured, not asserted. This file is normative: it describes how
to measure and what the existing benchmark does and does not claim. It records no
machine-specific numbers — a baseline is point-in-time and belongs to the private
measurement log, not to a specification.

## How to run

```sh
make bench
```

which runs:

```sh
go test ./internal/gateway -run '^$' -bench BenchmarkCacheHit -benchmem
```

`BenchmarkCacheHit` includes fresh `httptest.NewRequest` construction,
routing/planning, a real warm cache lookup, and `ServeHTTP` delivery for a 1024-byte
inline-eligible payload with force caching and a one-hour TTL. The observer is nil;
a reusable typed discard writer implements Flusher without buffering. Setup,
publication and origin work are excluded. Untimed checks verify every measured
operation is a HIT, delivered bytes match, and origin calls remain exactly one.

## What it does not claim

- It is a local direct-handler measurement, not network throughput or service capacity.
- It does not inspect Pebble writes and does not prove an absence of database writes on HIT.
- It is not a hard memory budget; buffer pooling changes allocation reuse only.

## Design relevant to measurement

`httputil.ReverseProxy.BufferPool` is backed by one fixed-32-KiB `sync.Pool`
implementation per gateway; buffers with other capacities are discarded.
ReverseProxy retains ordinary streaming/backpressure behavior and owns a buffer
exclusively until its copy returns. This is allocation reuse, not a new request
admission limit.

## Recording a baseline

When a change needs a before/after comparison, capture the numbers, the exact
`go test ... -benchtime=... -count=...` command, and the machine/toolchain in the
private measurement log. Cite the conclusion in review or the commit message; do
not promote it to a standing claim in this file.
