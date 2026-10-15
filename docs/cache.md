# Object cache

`internal/cache` is independent of configuration, rules, gateway, and analytics.
Its entry points are:

```go
Open(Options) (*Store, error)
(*Store).Do(context.Context, Request) (*Response, error)
(*Store).Refresh(context.Context, Request) error
(*Store).Purge(context.Context, Filter) (int, error)
(*Store).Entries(context.Context, Filter, int) ([]EntryInfo, error)
(*Store).Stats() Stats
(*Store).Close() error
```

`Refresh` takes the caller's current immutable GET plan, not a persisted URL or a
filter: entries do not contain origin credentials or executable fetch plans. It
sets `Request.Revalidate`, drains the response, and waits for body/metadata
publication or conditional revalidation. It does not purge the old entry first.
`Revalidate` skips fresh and SWR reuse for this consumer without injecting
Cache-Control, changing the key/signature, or preventing a join of compatible
in-flight work. Normal request headers and Fetch's immutable plan are unchanged.
Refresh returns the acquisition/publication error, or `ErrNotStored` for bypass,
stale fallback, or publication rejected by purge; none is reported as success.
A management caller should provide an ordinary full GET plan, without client
range/precondition headers. Refresh does not turn a HEAD plan into a GET.

## Caller ownership

`Request.Key` is the resource identity, including site/origin/path/query and any
rule-selected identity dimensions. GET and HEAD use the same key. Headers describe
the actual outbound representation inputs; they must retain client conditional
and range headers for cache delivery. `Fetch` must be safe to retain beyond the
original HTTP request. Each override field **replaces** that outgoing field;
a nil value removes it. Fetch honors its supplied context and returns an owned
response body. The cache calls Fetch exactly once per acquisition; attempt and
transfer observation belongs inside Fetch, not per cache consumer.

The store copies headers and status-TTL maps. `Transform` executes once on each
origin response before eligibility, including 304 and non-cacheable responses.
It may adjust policy and safe response headers, not read/replace the body or
rewrite framing, validators, status, or encoding. Delivery-only rules belong to
the gateway and must not mutate the stored head.

Every returned `HTTP.Body` must be closed, including after a short read. Body reads
and Close may run concurrently; multiple concurrent Reads of one body are not
supported. Consumers use independent offsets and cancellation. `CachedBytes()` is
an atomic cumulative count of successfully read bytes originating in a previously
published generation, not necessarily bytes written to a client. For output
accounting, `CachedPrefix(n)` instead maps the first `n` body bytes to their
previously published byte count, bounded by this response's body length. It is a
stateless immutable mapping over the initial delivery offset and frozen provenance,
independent of read-ahead or later coverage publication. Call it with cumulative
successfully written bytes and subtract adjacent results for cached output deltas;
direct/empty bodies return zero. Both callbacks are safe to call concurrently.
Reading a shared
fill's temporary file does not make those newly fetched bytes cache-hit bytes.

Outcomes are `MISS`, `COALESCED`, `HIT`, `STALE`, `REVALIDATED`, and `BYPASS`.
Joining an acquisition is COALESCED, including a shared revalidation, never HIT.
An initiating 304 revalidation is REVALIDATED. Sparse completion can contain both
cached and upstream bytes; its byte provenance is separate from its outcome.

## Lifetimes and coordination

One resource admits at most four active cache acquisitions, within the global fill
limit. Only disjoint unpublished write sets in an already-proven generation run
concurrently. Unknown representation discovery, independent generations, and
revalidation serialize. Identical end-to-end plans coalesce; overlapping ranges
wait for a resource change and repeat lookup. One consumer reads one fill plus
its initial published coverage snapshot, never an arbitrary set of fills.
Before Vary is known, only identical complete input headers can share work.
Non-cacheable/personalized heads
are streamed directly to their initiating consumer; other subscribers perform
their own bounded bypass request.

A fill owns its context, timeout, writer, and notification channel. Discovery and
replacement fills own a private temporary file; compatible range fills lease only
unpublished positions in the existing generation's file.
Closing one subscription does not cancel other subscribers. The last subscription
cancels an unfinished acquisition unless it has background purpose or has already
acquired its declared bytes and is completing publication. Subscriber admission
and the orphan transition share the fill mutex: a departing last consumer closes
admission before context cancellation, and a rejected join waits/retries lookup
rather than inheriting that cancellation. Readers wait on progress
without sharing a body or seek position. A shared maintenance goroutine handles
access persistence and eviction; there is no goroutine per published entry.
Upstream admission and byte leases have separate lifetimes. A completed/failed
acquisition releases its upstream permit immediately; its lease remains until
every subscriber and active ReadAt has drained. A failed fill's progressed bytes
cannot be overwritten by a retry while an old reader can still observe them.
Disjoint leases can proceed during that drain. Cancel/Close signal blocked readers
immediately, but release references only after their active reads return.
Cacheable refresh retains the old published entry until the replacement commits
atomically. A failed/truncated replacement leaves that entry available for later
eligible stale fallback. Genuinely non-cacheable replacement responses can still
invalidate it. Normal delivery does not fail merely because persistence fails;
`Refresh` separately checks the fill's storage completion result.

Resource locks serialize reference acquisition, publication, invalidation, and
generation switches. Shard-table locks only protect bounded hot metadata and key
references; no network, body, database, or filesystem I/O holds them. The body
registry mutex protects references, not file opens/reads/deletes. Small metadata
commits run under resource locks. Network and body copying do not.

Purge logically removes matching variants and durably advances the resource's
epoch before returning, checking every active publisher. An old fill may finish delivery to existing consumers but
cannot publish or accept post-purge subscribers. New consumers wait for fenced
acquisitions before fetching again, and cannot join a fill whose target generation
has been replaced or evicted. Purged/replaced files remain readable
until their last reference is released. Empty fences are reclaimed only after no
publisher remains. Purge is linearized per resource, not a globally atomic
namespace snapshot; requests beginning during/after a purge may repopulate it.
Its count is the number of removed published variants, not unfinished fills.
Successful logical removal/fencing signals resource waiters after metadata and
retirement state change. An overlapping waiter can therefore start a private
generation after purge without waiting for an old failed lease's reader to close.

## HTTP policy

- Authorization, Cookie (even with forced TTL), request no-store, non-GET/HEAD,
  upgrades, and multipart ranges bypass object storage.
- Response private, no-store, Set-Cookie, `Vary: *`, invalid/unsafe Vary, SSE, gRPC,
  multipart byte-range responses, and unsupported statuses bypass. Force mode
  changes freshness, not these privacy/protocol restrictions.
- Vary accepts arbitrary valid end-to-end field names, including custom fields.
  Protocol-owned/framing/conditional fields, Host, Authorization, and Cookie are
  excluded. Limits are 16 names including mandatory Accept-Encoding, 256 bytes
  per name, 2 KiB of additional names, and 16 KiB of captured values. Request
  header presence and all values are part of variant identity; before the origin
  head arrives, complete input headers distinguish shared fills.
  Accept-Encoding is always in identity, even if the origin forgets Vary. A Vary
  schema change invalidates the previous schema, including all sibling variants
  when a 304 changes to a different or unsafe/unsupported schema. The epoch/target
  check, retirement, and metadata commit share the resource lock; already pinned
  readers finish on their old bodies. Each resource has at most 16
  variants. The cache does not decode/re-encode bodies; Fetch should disable
  transport auto-decompression so validators and wire ranges remain meaningful.
  Responses marked `Uncompressed` by a transport bypass storage.
- Default reusable statuses are 200, 203, 204, 206, 301, 308, 404, and 410;
  `StatusTTL` explicitly enables other final statuses except conditional/range
  results 304, 412, and 416. An explicit
  TTL/max-age/s-maxage/Expires or a validator is required. A validator alone is
  stored immediately stale; there is no heuristic freshness.
- s-maxage precedes max-age and Expires. `Policy.TTL` is fallback freshness;
  force mode and StatusTTL override freshness. Date and Age reduce freshness and
  stale windows. no-cache requires validation; must-revalidate/proxy-revalidate
  suppress stale delivery. Positive policy stale durations override origin stale
  durations. Request no-cache, max-age, min-fresh, and only-if-cached are honored;
  arbitrary client max-stale is not implemented.
- Fresh hits support HEAD, If-Match, If-Unmodified-Since, If-None-Match,
  If-Modified-Since, one byte range (including suffix/open-ended), If-Range, and
  416. Malformed ranges are ignored; multipart ranges bypass to the origin.
  HEAD misses fetch HEAD and never publish a fabricated body.
- Stale-while-revalidate serves published bytes while one bounded background fill
  runs. Prefetch starts after 80% of freshness on a GET. Stale-if-error applies to
  transport errors and 5xx responses before delivering a new response head, not
  failures after new body bytes have already been delivered. Conditional 304
  updates freshness/headers and keeps the body; changed validators, encodings,
  or representation lengths are rejected. Once validation succeeds, metadata
  persistence failure does not prevent delivery of the validated body/head.
  Published metadata stays unchanged on commit failure; the separate storage
  completion result and `Refresh` report the error.

## Ranges and persistence

Pebble holds binary metadata, a rebuildable file index, and complete small bodies.
Large/partial bodies use stable generation files and immutable coverage snapshots.
**Published bytes, not the inode, are immutable.** A generation's file is opened
read/write so disjoint unpublished holes can be filled in place. No published byte
is overwritten; published readers remain bounded by their own coverage/provenance.
A sparse hole is never interpreted as zero-filled content. A complete GET streams
continuously; range completion issues one continuous request spanning needed holes,
aligned outward to `BlockBytes`, not one upstream request per block.

206 storage requires a strong ETag, valid Content-Range, matching requested range,
and consistent body length. Stitching additionally requires equal strong ETag,
total length, content encoding, and Vary values/schema. Completion sends If-Range;
an origin's 200 replacement uses a private new generation. Inconsistent partial
responses fail explicitly rather than join different representations. Compatible
206 extensions write only their reserved holes directly into the pinned body;
there is no copying of existing coverage. Continuous upstream overlap is consumed
but not rewritten, and admission excludes outstanding write leases.

After syncing new bytes, extension publication checks both the captured epoch and
the current target generation ID under the resource lock. It unions additions
into the latest coverage, retaining the newest response head/access metadata and the same
generation ID/file. Coverage expansion never retires storage or moves a completed
sparse file back into Pebble. Replacement publishers use the same epoch/target
check, so late 206/200 responses cannot roll back a newer generation or resurrect
an evicted/purged one. Only whole generations are retired. Sparse coverage plus
reservations are bounded to 128 extents; exceeding the bound falls back to an
independent private generation without copying old bytes. No weak/date-validator
stitching is performed.

Large bodies are synced and renamed, then their directory is synced, before a
Sync metadata batch references them. Inline body and metadata are one Sync batch.
Only fully received response extents publish; in-process streaming progress is
not crash-recoverable progress. Failed extension bytes remain unreferenced in the
file, are released from the covered/resident byte budget after reader drain, and
may be overwritten only by a new lease. They are never served or recovered as
coverage (filesystem allocation can therefore exceed this logical body budget).
On reopen, metadata-referenced coverage is checked,
temporary/orphan files are removed with bounded directory batches, and statistics
are rebuilt. Missing/truncated bodies remove only their affected variants and
indexes in a durable repair batch; leftover files are then collected as orphans.
Healthy variants remain hits and repaired variants become misses. Invalid metadata
and other filesystem/Pebble errors still fail Open rather than masking corruption
or permissions/I/O problems. A lower reopened byte limit triggers eviction before
admission.

## Bounds and operation

Zero-valued options select defaults:

| Option | Default | Meaning |
|---|---:|---|
| MaxBytes | 1 GiB | Resident body bytes: entries, unpublished fills, and retired pinned generations |
| MemoryBytes | 16 MiB | Approximate hot-metadata, access-hint, and Pebble cache/memtable sizing |
| MaxFills | 32 | Actual active acquisitions, including direct bypass streams |
| MaxBackground | 4 | Background subset of MaxFills |
| MaxReaders | 4096 | Reader subscriptions and concurrent Do admission bound |
| MaxObjectBytes | min(MaxBytes, 256 MiB) | Cached object/representation size bound |
| InlineBytes | 32 KiB | Inline threshold, capped at 1 MiB and MaxObjectBytes |
| BlockBytes | 1 MiB | Sparse acquisition alignment |
| FillTimeout | 30 minutes | Configurable acquisition lifetime budget, not an idle timeout |

Known oversized or ineligible bodies stream directly without caching/spooling.
Unknown-length eligible bodies stream through a bounded fill; crossing object or
resident capacity aborts that fill explicitly (`ErrObjectTooLarge`/`ErrCapacity`).
Global admission is non-queued (`ErrBusy`); same-resource overlap/limits wait for
a resource change within the admitted request bound. Each fill uses a 32 KiB
stream buffer; range extension has no old-body copy buffer. File handles and inline-reader allocations are
bounded by fills/readers. MemoryBytes is not a total-process memory limit: active
reader inline bodies, Go runtime, network buffers, Pebble background overhead,
filesystem allocation granularity, WAL/LSM amplification, and OS page cache are
additional. MaxBytes is a covered body-byte budget, not a total-volume quota.

HIT writes no Pebble data. A bounded generation-ID access table coalesces hints;
every two seconds metadata updates commit in batches of at most 32 resource locks.
Access batches are NoSync (crash loss is acceptable). Publication, purge and body
references are Sync (identity/invalidation loss is not). Generation-ID checks and
holding the resource locks through access commit prevent resurrection. Eviction
scans metadata with a bounded 256-candidate heap, checks current hints again, and
retires oldest generations under pressure. Recent access is approximate, never
content identity. Statistics expose published bytes, resident bytes, active fills,
readers, outcomes, evictions, and storage failures. Close cancels acquisitions,
waits for admitted work, flushes hints, closes files and Pebble; old response
bodies are no longer usable after Close.

## Test coverage

`internal/cache/cache_test.go`, `internal/cache/lifecycle_test.go`, and
`internal/cache/sparse_test.go` use controllable HTTP origins and owned pipe bodies
plus a byte-work benchmark to establish shared
streaming/independent cancellation, orphan cancellation,
304 freshness, SIE/SWR, purge fences and pinned readers, client conditions and
ranges, encoding/Vary isolation, sparse holes/overlap/generation changes, partial
and complete reopen (inline and files), selective lost-body repair, origin
truncation with old-entry retention, real current-plan refresh and storage errors,
limits, and eviction without resurrection. Sparse evidence includes a final-size
1.25x budget with adjacent extensions and stable ID/inode, both parallel publication
orders surviving reopen, failed leases retained through active ReadAt, frozen
provenance, four-acquisition admission, late publishers versus replacement/purge/
eviction, fragmented fallback, and reopen ignoring failed bytes.

The focused scenarios cover schema-wide 304 retirement with pinned siblings, a
deterministic orphan-close/join schedule, purge waking a waiter behind a retained
failed lease, and read-only metadata commit failure preserving 304 delivery while
refresh reports storage failure.
Invalid 304 validators/encodings/lengths remain delivery errors. A fifth focused
test checks mixed sparse `CachedPrefix` boundaries after full-body read-ahead,
including an origin-only written prefix, bounded deltas, and unchanged provenance
after publication. Run:

```sh
go test ./internal/cache
go test -race ./internal/cache
go vet ./internal/cache
go test ./internal/cache -run '^$' -bench BenchmarkSparseAdjacentExtensions -benchtime=1x
```

The adjacent-extension benchmark reports `body-write-B/op` separately from time.
The 64 KiB, 256 KiB, and 1 MiB cases write exactly 65,536, 262,144, and 1,048,576
body bytes respectively, including discovery: work grows with fetched bytes, not
with the sum of repeatedly copied prefixes. Timings include durable storage commits
and are not a proxy-throughput claim.

On macOS, use `CC=/usr/bin/clang CXX=/usr/bin/clang++` for executable/race
checks; a package-manager clang may not locate libresolv. The tests exercise reopen
and durability ordering, not simulated power loss or disk-controller guarantees.
