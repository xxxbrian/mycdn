package cache

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
)

// Reports the request's first context wait, so the regression tests establish
// admission/wait ordering without guessing from a sleep or a scheduler delay.
type waitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *waitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestGate1LastConsumerCancellationJoin(t *testing.T) {
	s := testStore(t, nil)
	gate, entered := make(chan struct{}), make(chan struct{})
	reader, writer := io.Pipe()
	defer writer.Close()
	var attempts atomic.Int32
	req := Request{Key: "cancel-join", Method: "GET", Header: make(http.Header), Fetch: func(ctx context.Context, _ http.Header) (*http.Response, error) {
		var body io.ReadCloser = io.NopCloser(strings.NewReader("body"))
		if attempts.Add(1) == 1 {
			body = reader
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: body, ContentLength: 4}, nil
	}}
	first, err := s.Do(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	f := first.HTTP.Body.(*bodyReader).fill
	originalCancel := f.cancel
	var cancellations atomic.Int32
	f.cancel = func() {
		if cancellations.Add(1) == 1 {
			close(entered)
			<-gate
		}
		originalCancel()
	}
	var release sync.Once
	defer release.Do(func() { close(gate) })
	closed := make(chan struct{})
	go func() { first.HTTP.Body.Close(); close(closed) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("last consumer did not cancel")
	}
	// Cancellation has been decided, but the fill context is still live. A join
	// at exactly the old race window must be rejected without retaining a slot.
	before := s.Stats().Readers
	f.key.mu.Lock()
	joined, err := s.subscribe(context.Background(), f)
	f.key.mu.Unlock()
	if err == nil {
		joined.close()
		t.Fatal("orphan transition admitted a new consumer")
	}
	if !errors.Is(err, errFillOrphaned) || s.Stats().Readers != before {
		t.Fatalf("rejected subscription: %v %+v", err, s.Stats())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	observed := &waitContext{Context: ctx, waiting: make(chan struct{})}
	type result struct {
		outcome, body string
		err           error
	}
	results := make(chan result, 1)
	go func() {
		r, err := s.Do(observed, req)
		if err != nil {
			results <- result{err: err}
			return
		}
		body, err := io.ReadAll(r.HTTP.Body)
		r.HTTP.Body.Close()
		results <- result{r.Outcome, string(body), err}
	}()
	select {
	case <-observed.waiting:
	case <-ctx.Done():
		t.Fatal("new consumer did not reach key wait")
	}
	select {
	case got := <-results:
		t.Fatalf("orphan returned before cancellation: %+v", got)
	default:
	}
	release.Do(func() { close(gate) })
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case got := <-results:
		if got.err != nil || got.outcome != "MISS" || got.body != "body" || attempts.Load() != 2 {
			t.Fatalf("consumer inherited old cancellation: %+v attempts=%d", got, attempts.Load())
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	idle(t, s)
}

func TestGate1InitialSubscriberFailureClosesAdmission(t *testing.T) {
	s := testStore(t, func(o *Options) { o.MaxReaders = 2 })
	heldReq := Request{Key: "held", Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: io.NopCloser(strings.NewReader("held")), ContentLength: 4}, nil
	}}
	consume(t, s, heldReq)
	idle(t, s)
	heldA, err := s.Do(context.Background(), heldReq)
	if err != nil {
		t.Fatal(err)
	}
	defer heldA.HTTP.Body.Close()
	heldB, err := s.Do(context.Background(), heldReq)
	if err != nil {
		t.Fatal(err)
	}
	defer heldB.HTTP.Body.Close()
	if heldA.Outcome != "HIT" || heldB.Outcome != "HIT" {
		t.Fatal("fixture must retain two published HIT bodies")
	}

	var attempts atomic.Int32
	started := make(chan struct{})
	req := Request{Key: "initial-slot-failure", Method: "GET", Header: make(http.Header), Fetch: func(ctx context.Context, _ http.Header) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: io.NopCloser(strings.NewReader("body")), ContentLength: 4}, nil
	}}
	k := s.key(req.Key)
	defer s.releaseKey(k)
	k.mu.Lock()
	if err := s.load(k); err != nil {
		k.mu.Unlock()
		t.Fatal(err)
	}
	// Exercise Do's exact locked initial-admission step, then force its legal
	// deschedule after key unlock and before cancellation, without a test hook
	// in production. Both reader slots are occupied by independent HIT bodies.
	f, sub, firstErr := s.startSubscribedFill(context.Background(), k, req, nil)
	k.mu.Unlock()
	if firstErr == nil {
		sub.close()
	}
	if f == nil || sub != nil || !errors.Is(firstErr, ErrBusy) {
		t.Fatalf("initial admission: fill=%v sub=%v err=%v", f != nil, sub != nil, firstErr)
	}
	defer f.cancel()
	// Ensure the original acquisition remains alive until the modeled caller
	// cancels it; only a second physical fetch can complete the new consumer.
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("initial acquisition did not start")
	}
	heldA.HTTP.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	observed := &waitContext{Context: ctx, waiting: make(chan struct{})}
	type result struct {
		outcome, body string
		err           error
	}
	results := make(chan result, 1)
	go func() {
		r, err := s.Do(observed, req)
		if err != nil {
			results <- result{err: err}
			return
		}
		body, err := io.ReadAll(r.HTTP.Body)
		r.HTTP.Body.Close()
		results <- result{r.Outcome, string(body), err}
	}()
	select {
	case <-observed.waiting:
	case <-ctx.Done():
		t.Fatal("retry did not reach admission wait")
	}
	// A rejected join must not own the newly available slot, and Do must not
	// return a successful subscription to the soon-to-be-canceled original fill.
	select {
	case got := <-results:
		t.Fatalf("joined initial orphan: %+v", got)
	default:
	}
	if readers := s.Stats().Readers; readers != 1 {
		t.Fatalf("retry retained orphan reader slot: %d", readers)
	}
	f.cancel()
	select {
	case got := <-results:
		if got.err != nil || got.outcome != "MISS" || got.body != "body" || attempts.Load() != 2 {
			t.Fatalf("retry inherited creator cancellation: %+v attempts=%d", got, attempts.Load())
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	idle(t, s)
}

func TestGate1VarySchemaChange304RetiresOldSchema(t *testing.T) {
	for _, schema := range []string{"X-Tenant", "*", "Cookie"} {
		t.Run(schema, func(t *testing.T) {
			s := testStore(t, nil)
			var changed atomic.Bool
			request := func(lang, tenant string) Request {
				return Request{Key: "schema-304", Method: "GET", Header: http.Header{"Accept-Language": {lang}, "X-Tenant": {tenant}}, Fetch: func(ctx context.Context, h http.Header) (*http.Response, error) {
					body, vary := lang, "Accept-Language"
					if changed.Load() {
						body, vary = "en", schema
						if tenant == "two" {
							body = "tenant-two"
						}
					}
					head := http.Header{"Cache-Control": {"max-age=60"}, "Etag": {`"` + body + `"`}, "Vary": {vary}}
					if h.Get("If-None-Match") == head.Get("ETag") {
						return &http.Response{StatusCode: 304, Header: head, Body: http.NoBody}, nil
					}
					return &http.Response{StatusCode: 200, Header: head, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}, nil
				}}
			}
			consume(t, s, request("en", "one"))
			consume(t, s, request("fr", "one"))
			idle(t, s)
			pin, err := s.Do(context.Background(), request("fr", "one"))
			if err != nil {
				t.Fatal(err)
			}
			defer pin.HTTP.Body.Close()
			changed.Store(true)
			force := request("en", "one")
			force.Revalidate = true
			r, body := consume(t, s, force)
			if r.Outcome != "REVALIDATED" || body != "en" {
				t.Fatalf("schema-changing 304: %s %q", r.Outcome, body)
			}
			idle(t, s)
			if stats := s.Stats(); stats.Entries != 0 {
				t.Fatalf("old schema siblings remain: %+v", stats)
			}
			r, body = consume(t, s, request("fr", "two"))
			if body != "tenant-two" || r.Outcome == "HIT" {
				t.Fatalf("cross-tenant old-schema reuse: %s %q", r.Outcome, body)
			}
			old, err := io.ReadAll(pin.HTTP.Body)
			pin.HTTP.Body.Close()
			if err != nil || string(old) != "fr" {
				t.Fatalf("schema retirement invalidated pinned reader: %q %v", old, err)
			}
		})
	}
}

func TestGate1PurgeWakesFailedLeaseWaiter(t *testing.T) {
	s := testStore(t, func(o *Options) { o.BlockBytes = 1 })
	var fail atomic.Bool
	req := Request{Key: "lease-wakeup", Method: "GET", Header: make(http.Header), Fetch: func(ctx context.Context, h http.Header) (*http.Response, error) {
		part, err := parseRange(h.Get("Range"), 16)
		if err != nil {
			return nil, err
		}
		body := "0123456789abcdef"[part.Start:part.End]
		if fail.Load() {
			body = body[:4]
		}
		return partial(part, 16, io.NopCloser(strings.NewReader(body))), nil
	}}
	consume(t, s, ranged(req, "bytes=0-3"))
	idle(t, s)
	fail.Store(true)
	old, err := s.Do(context.Background(), ranged(req, "bytes=4-11"))
	if err != nil {
		t.Fatal(err)
	}
	defer old.HTTP.Body.Close()
	idle(t, s)
	fail.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	observed := &waitContext{Context: ctx, waiting: make(chan struct{})}
	type result struct {
		outcome, body string
		err           error
	}
	results := make(chan result, 1)
	go func() {
		r, err := s.Do(observed, ranged(req, "bytes=4-11"))
		if err != nil {
			results <- result{err: err}
			return
		}
		body, err := io.ReadAll(r.HTTP.Body)
		r.HTTP.Body.Close()
		results <- result{r.Outcome, string(body), err}
	}()
	select {
	case <-observed.waiting:
	case <-ctx.Done():
		t.Fatal("overlapping consumer did not reach wait")
	}
	select {
	case got := <-results:
		t.Fatalf("consumer escaped retained lease: %+v", got)
	default:
	}
	if n, err := s.Purge(ctx, Filter{}); err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
	select {
	case got := <-results:
		if got.err != nil || got.outcome != "MISS" || got.body != "456789ab" {
			t.Fatalf("purge did not wake private-generation retry: %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("purge left consumer waiting for retained old reader")
	}
	// The old reader is still open: new-generation progress must not mutate it.
	got, err := io.ReadAll(old.HTTP.Body)
	old.HTTP.Body.Close()
	if string(got) != "4567" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("retired failed reader: %q %v", got, err)
	}
}

func TestGate1RevalidationStorageFailureKeepsDelivery(t *testing.T) {
	s := testStore(t, nil)
	var invalid atomic.Int32
	req := Request{Key: "304-disk-error", Method: "GET", Header: make(http.Header), Fetch: func(ctx context.Context, h http.Header) (*http.Response, error) {
		if h.Get("If-None-Match") != "" {
			head := http.Header{"Cache-Control": {"max-age=120"}, "Etag": {`"stable"`}, "X-Revalidated": {"yes"}}
			switch invalid.Load() {
			case 1:
				head.Set("ETag", `"changed"`)
			case 2:
				head.Set("Content-Encoding", "gzip")
			case 3:
				head.Set("Content-Length", "5")
			}
			return &http.Response{StatusCode: 304, Header: head, Body: http.NoBody}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}, "Etag": {`"stable"`}}, Body: io.NopCloser(strings.NewReader("body")), ContentLength: 4}, nil
	}}
	consume(t, s, req)
	idle(t, s)
	before, pin := generation(t, s, req)
	s.release(pin)
	// Keep persisted reads intact and make every metadata commit fail, rather
	// than breaking the body or simulating data loss.
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := pebble.Open(filepath.Join(s.opt.Dir, "metadata"), &pebble.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	s.db = db
	force := req
	force.Header = http.Header{"Cache-Control": {"no-cache"}}
	r, body := consume(t, s, force)
	if r.Outcome != "REVALIDATED" || body != "body" || r.CachedBytes() != 4 || r.HTTP.Header.Get("X-Revalidated") != "yes" {
		t.Fatalf("validated body delivery: %s %q", r.Outcome, body)
	}
	if err := r.wait(context.Background()); !errors.Is(err, pebble.ErrReadOnly) {
		t.Fatalf("storage completion: %v", err)
	}
	if err := s.Refresh(context.Background(), req); !errors.Is(err, pebble.ErrReadOnly) {
		t.Fatalf("manual refresh masked failed metadata commit: %v", err)
	}
	after, pin := generation(t, s, req)
	s.release(pin)
	if after.ID != before.ID || !after.StoredAt.Equal(before.StoredAt) || !after.FreshUntil.Equal(before.FreshUntil) || after.Header.Get("X-Revalidated") != "" {
		t.Fatalf("failed commit mutated published metadata: before=%+v after=%+v", before, after)
	}
	value, closer, err := s.db.Get(recordKey(req.Key))
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := decodeRecord(value)
	closer.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.Entries[0].FreshUntil.Equal(before.FreshUntil) {
		t.Fatal("failed commit changed persisted metadata")
	}
	for _, field := range []struct {
		name  string
		value int32
	}{{"validator", 1}, {"encoding", 2}, {"length", 3}} {
		t.Run(field.name, func(t *testing.T) {
			invalid.Store(field.value)
			r, err := s.Do(context.Background(), force)
			if err == nil {
				r.HTTP.Body.Close()
				t.Fatal("invalid 304 became a delivery success")
			}
			idle(t, s)
		})
	}
}

func TestGate1CachedPrefixUsesFrozenDeliveryProvenance(t *testing.T) {
	s := testStore(t, func(o *Options) { o.BlockBytes = 1 })
	body := "0123456789abcdef"
	req := Request{Key: "prefix", Method: "GET", Header: make(http.Header), Fetch: func(ctx context.Context, h http.Header) (*http.Response, error) {
		part, err := parseRange(h.Get("Range"), int64(len(body)))
		if err != nil {
			return nil, err
		}
		return partial(part, int64(len(body)), io.NopCloser(strings.NewReader(body[part.Start:part.End]))), nil
	}}
	first, _ := consume(t, s, ranged(req, "bytes=4-7"))
	idle(t, s)
	if first.CachedPrefix(4) != 0 {
		t.Fatal("discovery bytes had cached provenance")
	}
	// The completion's body starts at offset 2: two origin bytes, four cached
	// bytes, then two origin bytes. Read everything ahead before counting writes.
	r, got := consume(t, s, ranged(req, "bytes=2-9"))
	idle(t, s)
	if got != body[2:10] || r.CachedBytes() != 4 {
		t.Fatalf("mixed body fixture: %q read-cached=%d", got, r.CachedBytes())
	}
	for _, tc := range []struct{ prefix, cached int64 }{{-1, 0}, {0, 0}, {1, 0}, {2, 0}, {3, 1}, {5, 3}, {6, 4}, {8, 4}, {100, 4}} {
		if got := r.CachedPrefix(tc.prefix); got != tc.cached {
			t.Fatalf("mixed prefix %d: cached=%d want=%d", tc.prefix, got, tc.cached)
		}
	}
	var previous int64
	for delivered := int64(1); delivered <= 8; delivered++ {
		cached := r.CachedPrefix(delivered)
		if cached < previous || cached-previous > 1 || cached > delivered {
			t.Fatalf("impossible delivered delta: n=%d cached=%d previous=%d", delivered, cached, previous)
		}
		previous = cached
	}
	hit, _ := consume(t, s, ranged(req, "bytes=4-7"))
	if hit.Outcome != "HIT" || hit.CachedPrefix(3) != 3 || hit.CachedPrefix(100) != 4 {
		t.Fatal("full cached range prefix is not length bounded")
	}
	if r.CachedPrefix(2) != 0 {
		t.Fatal("later publication/reuse changed frozen provenance")
	}
	direct := req
	direct.Policy.Mode = "bypass"
	direct = ranged(direct, "bytes=4-7")
	bypass, _ := consume(t, s, direct)
	if bypass.CachedPrefix(100) != 0 {
		t.Fatal("bypass prefix counted cached bytes")
	}
	head := req
	head.Method = "HEAD"
	head.Header = http.Header{"If-None-Match": {`"stable"`}}
	empty, _ := consume(t, s, head)
	if empty.CachedPrefix(100) != 0 {
		t.Fatal("empty body prefix counted cached bytes")
	}
}
