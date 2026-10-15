package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func ranged(req Request, value string) Request {
	req.Header = cloneHeader(req.Header)
	req.Header.Set("Range", value)
	fetch := req.Fetch
	req.Fetch = func(ctx context.Context, overrides http.Header) (*http.Response, error) {
		overrides = cloneHeader(overrides)
		if _, ok := overrides["Range"]; !ok {
			overrides.Set("Range", value)
		}
		return fetch(ctx, overrides)
	}
	return req
}

func generation(t testing.TB, s *Store, req Request) (metadata, *object) {
	t.Helper()
	k := s.key(req.Key)
	defer s.releaseKey(k)
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := s.load(k); err != nil {
		t.Fatal(err)
	}
	m := k.rec.match(cloneHeader(req.Header))
	if m == nil {
		t.Fatal("no published generation")
	}
	o, err := s.acquire(m)
	if err != nil {
		t.Fatal(err)
	}
	return *m, o
}

func partial(part span, total int64, body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: 206, Header: http.Header{
		"Cache-Control": {"max-age=60"}, "Etag": {`"stable"`},
		"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", part.Start, part.End-1, total)},
	}, Body: body, ContentLength: part.End - part.Start}
}

func TestSequentialRangesExtendOneFileWithinFinalSizeBudget(t *testing.T) {
	const block = 4096
	body := strings.Repeat("0123456789abcdef", 8192)
	s := testStore(t, func(o *Options) {
		o.MaxBytes = int64(len(body) * 5 / 4)
		o.MaxObjectBytes = int64(len(body))
		o.BlockBytes = block
		o.InlineBytes = 1
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		part, err := parseRange(r.Header.Get("Range"), int64(len(body)))
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("ETag", `"stable"`)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", part.Start, part.End-1, len(body)))
		w.WriteHeader(206)
		io.WriteString(w, body[part.Start:part.End])
	}))
	defer server.Close()
	transport := &http.Transport{DisableCompression: true}
	defer transport.CloseIdleConnections()
	req := Request{Key: "linear", Method: "GET", Header: make(http.Header), Fetch: func(ctx context.Context, h http.Header) (*http.Response, error) {
		r, err := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
		if err != nil {
			return nil, err
		}
		r.Header = h
		return transport.RoundTrip(r)
	}}
	consume(t, s, ranged(req, fmt.Sprintf("bytes=0-%d", block-1)))
	idle(t, s)
	first, pin := generation(t, s, req)
	defer s.release(pin)
	infoBefore, err := os.Stat(pin.path)
	if err != nil {
		t.Fatal(err)
	}
	var priorID = first.ID
	for start := block; start < len(body); start += block {
		r, got := consume(t, s, ranged(req, fmt.Sprintf("bytes=%d-%d", start, start+block-1)))
		if got != body[start:start+block] || r.CachedBytes() != 0 {
			t.Fatalf("range %d: %q provenance=%d", start, got, r.CachedBytes())
		}
		m, o := generation(t, s, req)
		if m.ID != priorID || m.Inline || m.Bytes != int64(start+block) {
			t.Fatalf("extension replaced/copied generation: %+v", m)
		}
		s.release(o)
	}
	idle(t, s)
	if written := pin.written.Load(); written != int64(len(body)-block) {
		t.Fatalf("body writes=%d, want only newly fetched bytes=%d", written, len(body)-block)
	}
	infoAfter, err := os.Stat(pin.path)
	if err != nil || !os.SameFile(infoBefore, infoAfter) {
		t.Fatalf("sparse extension changed inode: %v", err)
	}
	if stats := s.Stats(); stats.Entries != 1 || stats.Bytes != int64(len(body)) || stats.ResidentBytes != int64(len(body)) {
		t.Fatalf("linear budget: %+v", stats)
	}
	resp, got := consume(t, s, req)
	if resp.Outcome != "HIT" || got != body {
		t.Fatalf("completed sparse generation: %s bytes=%d", resp.Outcome, len(got))
	}
}

func TestDisjointRangesPublishBothOrdersAndReopen(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint("reverse=", reverse), func(t *testing.T) {
			s := testStore(t, func(o *Options) { o.BlockBytes = 1 })
			body := "0123456789abcdefghijklmnopqrstuv"
			entered := make(chan span, 2)
			gates := map[int64]chan struct{}{4: make(chan struct{}), 20: make(chan struct{})}
			req := Request{Key: "parallel", Method: "GET", Header: make(http.Header), Fetch: func(ctx context.Context, h http.Header) (*http.Response, error) {
				part, err := parseRange(h.Get("Range"), int64(len(body)))
				if err != nil {
					return nil, err
				}
				if gate := gates[part.Start]; gate != nil {
					reader, writer := io.Pipe()
					entered <- part
					go func() {
						defer writer.Close()
						select {
						case <-gate:
							writer.Write([]byte(body[part.Start:part.End]))
						case <-ctx.Done():
						}
					}()
					response := partial(part, int64(len(body)), reader)
					response.Header.Set("X-Range", rangeHeader(part))
					return response, nil
				}
				return partial(part, int64(len(body)), io.NopCloser(strings.NewReader(body[part.Start:part.End]))), nil
			}}
			consume(t, s, ranged(req, "bytes=0-3"))
			idle(t, s)
			initial, pin := generation(t, s, req)
			defer func() { s.release(pin) }()
			a, err := s.Do(context.Background(), ranged(req, "bytes=0-11"))
			if err != nil {
				t.Fatal(err)
			}
			b, err := s.Do(context.Background(), ranged(req, "bytes=20-23"))
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("disjoint acquisition did not start before release")
				}
			}
			responses := []*Response{a, b}
			starts := []int64{4, 20}
			wants := []string{body[:12], body[20:24]}
			newerHead := b.HTTP.Header.Get("X-Range")
			if newerHead == "" || a.HTTP.Header.Get("X-Range") == newerHead {
				t.Fatal("fixture did not distinguish response heads")
			}
			if reverse {
				responses[0], responses[1] = responses[1], responses[0]
				starts[0], starts[1] = starts[1], starts[0]
				wants[0], wants[1] = wants[1], wants[0]
			}
			accessAt := time.Now().Add(time.Second)
			for i, r := range responses {
				close(gates[starts[i]])
				got, err := io.ReadAll(r.HTTP.Body)
				r.HTTP.Body.Close()
				if err != nil || string(got) != wants[i] {
					t.Fatalf("parallel range: %q %v", got, err)
				}
				if i == 0 {
					// A later access batch is also a newer metadata snapshot; the
					// other publisher must merge coverage without resetting it.
					s.touch(req.Key, initial.ID, accessAt)
					s.flushAccess()
				}
			}
			if a.CachedBytes() != 4 || b.CachedBytes() != 0 {
				t.Fatalf("provenance changed with publication: %d %d", a.CachedBytes(), b.CachedBytes())
			}
			idle(t, s)
			m, o := generation(t, s, req)
			s.release(o)
			if m.ID != initial.ID || m.Header.Get("X-Range") != newerHead || !m.AccessedAt.Equal(accessAt) || !covers(m.Coverage, span{0, 12}) || !covers(m.Coverage, span{20, 24}) || m.Bytes != 16 {
				t.Fatalf("parallel coverage/head lost: %+v", m)
			}
			s.release(pin)
			pin = nil
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(s.opt)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			for _, v := range []string{"bytes=0-11", "bytes=20-23"} {
				r, _ := consume(t, reopened, ranged(req, v))
				if r.Outcome != "HIT" {
					t.Fatalf("reopen coverage missing: %s %s", v, r.Outcome)
				}
			}
		})
	}
}

func TestReopenIgnoresAbandonedSparseBytes(t *testing.T) {
	s := testStore(t, func(o *Options) { o.BlockBytes = 1 })
	var fail atomic.Bool
	req := Request{Key: "failed-reopen", Method: "GET", Header: make(http.Header), Fetch: func(ctx context.Context, h http.Header) (*http.Response, error) {
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
	initial, pin := generation(t, s, req)
	s.release(pin)
	fail.Store(true)
	r, err := s.Do(context.Background(), ranged(req, "bytes=4-11"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(r.HTTP.Body)
	r.HTTP.Body.Close()
	idle(t, s)
	if !errors.Is(err, io.ErrUnexpectedEOF) || s.Stats().ResidentBytes != 4 {
		t.Fatalf("failed extension accounted as coverage: %v %+v", err, s.Stats())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.opt)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	m, o := generation(t, reopened, req)
	reopened.release(o)
	if m.ID != initial.ID || m.Bytes != 4 || covers(m.Coverage, span{4, 8}) || reopened.Stats().ResidentBytes != 4 {
		t.Fatalf("abandoned bytes recovered: %+v %+v", m, reopened.Stats())
	}
	fail.Store(false)
	r, got := consume(t, reopened, ranged(req, "bytes=4-11"))
	if r.Outcome != "MISS" || got != "456789ab" {
		t.Fatalf("unpublished bytes treated as hit: %s %q", r.Outcome, got)
	}
}

func TestRangeAcquisitionLimitAndIdenticalCoalescing(t *testing.T) {
	s := testStore(t, func(o *Options) { o.BlockBytes = 1 })
	gate := make(chan struct{})
	defer close(gate)
	var calls atomic.Int32
	req := Request{Key: "four", Method: "GET", Header: make(http.Header), Fetch: func(ctx context.Context, h http.Header) (*http.Response, error) {
		part, err := parseRange(h.Get("Range"), 32)
		if err != nil {
			return nil, err
		}
		calls.Add(1)
		if part.Start == 0 {
			return partial(part, 32, io.NopCloser(strings.NewReader("01"))), nil
		}
		reader, writer := io.Pipe()
		go func() {
			defer writer.Close()
			select {
			case <-gate:
				writer.Write([]byte(strings.Repeat("x", int(part.End-part.Start))))
			case <-ctx.Done():
			}
		}()
		return partial(part, 32, reader), nil
	}}
	consume(t, s, ranged(req, "bytes=0-1"))
	idle(t, s)
	var responses []*Response
	for _, start := range []int{4, 8, 12, 16} {
		r, err := s.Do(context.Background(), ranged(req, fmt.Sprintf("bytes=%d-%d", start, start+1)))
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, r)
	}
	joined, err := s.Do(context.Background(), ranged(req, "bytes=4-5"))
	if err != nil {
		t.Fatal(err)
	}
	if joined.Outcome != "COALESCED" {
		t.Fatalf("identical plan not coalesced: %s", joined.Outcome)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if r, err := s.Do(ctx, ranged(req, "bytes=20-21")); !errors.Is(err, context.DeadlineExceeded) {
		if r != nil {
			r.HTTP.Body.Close()
		}
		t.Fatalf("fifth acquisition admitted: %v", err)
	}
	if calls.Load() != 5 || s.Stats().ActiveFills != 4 {
		t.Fatalf("resource acquisition bound: calls=%d %+v", calls.Load(), s.Stats())
	}
	joined.HTTP.Body.Close()
	for _, r := range responses {
		r.HTTP.Body.Close()
	}
	idle(t, s)
}

func TestFragmentedGenerationFallsBackWithoutCopying(t *testing.T) {
	s := testStore(t, func(o *Options) { o.BlockBytes = 1; o.InlineBytes = 1 })
	req := Request{Key: "fragmented", Method: "GET", Header: make(http.Header), Fetch: func(ctx context.Context, h http.Header) (*http.Response, error) {
		part, err := parseRange(h.Get("Range"), 512)
		if err != nil {
			return nil, err
		}
		return partial(part, 512, io.NopCloser(strings.NewReader(strings.Repeat("x", int(part.End-part.Start))))), nil
	}}
	consume(t, s, ranged(req, "bytes=0-0"))
	idle(t, s)
	initial, pin := generation(t, s, req)
	defer s.release(pin)
	// Establish the real fragmentation boundary directly, avoiding 128 fsyncs
	// as test scaffolding. Bytes and coverage use the same production ownership.
	k := s.key(req.Key)
	k.mu.Lock()
	m := initial
	for i := int64(1); i < maxExtents; i++ {
		if !s.reserve(1) {
			k.mu.Unlock()
			s.releaseKey(k)
			t.Fatal("fixture capacity")
		}
		if _, err := pin.writeAt([]byte("x"), i*2); err != nil {
			k.mu.Unlock()
			s.releaseKey(k)
			t.Fatal(err)
		}
		m.Coverage = mergeSpans(m.Coverage, span{i * 2, i*2 + 1})
	}
	m.Bytes = spanBytes(m.Coverage)
	if err := pin.file.Sync(); err != nil {
		k.mu.Unlock()
		s.releaseKey(k)
		t.Fatal(err)
	}
	s.objectsMu.Lock()
	pin.bytes = m.Bytes
	s.objectsMu.Unlock()
	s.count.bytes.Add(m.Bytes - initial.Bytes)
	r := k.rec.copy()
	r.Entries[0] = m
	b := s.db.NewBatch()
	if err := s.persist(k, r, b); err != nil {
		b.Close()
		k.mu.Unlock()
		s.releaseKey(k)
		t.Fatal(err)
	}
	b.Close()
	k.mu.Unlock()
	s.releaseKey(k)
	_, got := consume(t, s, ranged(req, "bytes=300-300"))
	idle(t, s)
	next, o := generation(t, s, req)
	s.release(o)
	if got != "x" || next.ID == initial.ID || next.Bytes != 1 || len(next.Coverage) != 1 {
		t.Fatalf("fragment fallback: %q %+v", got, next)
	}
	if stats := s.Stats(); stats.ResidentBytes != 129 || stats.Bytes != 1 {
		t.Fatalf("fragment fallback copied body: %+v", stats)
	}
}

func BenchmarkSparseAdjacentExtensions(b *testing.B) {
	for _, size := range []int{64 << 10, 256 << 10, 1 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			s, err := Open(Options{Dir: b.TempDir(), MaxBytes: int64(size * 2), MaxObjectBytes: int64(size), InlineBytes: 1, BlockBytes: 4096})
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			body := strings.Repeat("x", size)
			var writes int64
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				req := Request{Key: fmt.Sprint(i), Method: "GET", Header: make(http.Header), Fetch: func(ctx context.Context, h http.Header) (*http.Response, error) {
					part, err := parseRange(h.Get("Range"), int64(size))
					if err != nil {
						return nil, err
					}
					return partial(part, int64(size), io.NopCloser(strings.NewReader(body[part.Start:part.End]))), nil
				}}
				var pin *object
				for start := 0; start < size; start += 4096 {
					r, err := s.Do(context.Background(), ranged(req, fmt.Sprintf("bytes=%d-%d", start, start+4095)))
					if err != nil {
						b.Fatal(err)
					}
					_, err = io.Copy(io.Discard, r.HTTP.Body)
					r.HTTP.Body.Close()
					if err != nil {
						b.Fatal(err)
					}
					if pin == nil {
						_, pin = generation(b, s, req)
					}
				}
				writes += pin.written.Load() + 4096 // discovery was closed before the pin
				s.release(pin)
				if _, err := s.Purge(context.Background(), Filter{}); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(writes)/float64(b.N), "body-write-B/op")
		})
	}
}

func TestFailedLeaseWaitsForActiveReaderButDisjointProceeds(t *testing.T) {
	s := testStore(t, func(o *Options) { o.BlockBytes = 1 })
	body := "0123456789abcdefghijklmnopqrstuv"
	var attempts atomic.Int32
	req := Request{Key: "leased", Method: "GET", Header: make(http.Header), Fetch: func(ctx context.Context, h http.Header) (*http.Response, error) {
		part, err := parseRange(h.Get("Range"), int64(len(body)))
		if err != nil {
			return nil, err
		}
		if part.Start == 4 && attempts.Add(1) == 1 {
			return partial(part, int64(len(body)), io.NopCloser(strings.NewReader("old!"))), nil
		}
		return partial(part, int64(len(body)), io.NopCloser(strings.NewReader(body[part.Start:part.End]))), nil
	}}
	consume(t, s, ranged(req, "bytes=0-3"))
	idle(t, s)
	failed, err := s.Do(context.Background(), ranged(req, "bytes=4-11"))
	if err != nil {
		t.Fatal(err)
	}
	idle(t, s) // the upstream permit is free, but this failed lease still has a reader
	r := failed.HTTP.Body.(*bodyReader)
	entered, release := make(chan struct{}), make(chan struct{})
	var blocked atomic.Bool
	r.object.readFile = func(p []byte, off int64) (int, error) {
		if off == 4 && !blocked.Swap(true) {
			close(entered)
			<-release
		}
		return r.object.file.ReadAt(p, off)
	}
	result := make(chan string, 1)
	go func() { p := make([]byte, 4); n, _ := r.Read(p); result <- string(p[:n]) }()
	<-entered
	r.Close() // Close must not release a range underneath the active ReadAt.
	disjoint, got := consume(t, s, ranged(req, "bytes=12-15"))
	idle(t, s)
	if got != body[12:16] || disjoint.Outcome != "MISS" {
		t.Fatalf("disjoint blocked behind failed reader: %s %q", disjoint.Outcome, got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if resp, err := s.Do(ctx, ranged(req, "bytes=4-11")); !errors.Is(err, context.DeadlineExceeded) {
		if resp != nil {
			resp.HTTP.Body.Close()
		}
		t.Fatalf("overlap entered live lease: %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatal("retry overwrote pinned unpublished bytes")
	}
	close(release)
	if got := <-result; got != "old!" {
		t.Fatalf("active ReadAt was overwritten: %q", got)
	}
	// The drained failed lease is now reusable; no old resident charge remains.
	retry, got := consume(t, s, ranged(req, "bytes=4-11"))
	idle(t, s)
	if got != body[4:12] || attempts.Load() != 2 || s.Stats().ResidentBytes != 16 {
		t.Fatalf("retry after drain: %q attempts=%d %+v", got, attempts.Load(), s.Stats())
	}
	if retry.CachedBytes() != 0 {
		t.Fatalf("retry inherited unpublished byte provenance: %d", retry.CachedBytes())
	}
}

func TestLateRangePublishersCannotReplaceNewGenerationOrPurge(t *testing.T) {
	for _, mode := range []string{"late206", "late200", "purge", "evict"} {
		t.Run(mode, func(t *testing.T) {
			s := testStore(t, func(o *Options) { o.BlockBytes = 1 })
			old := "0123456789abcdef"
			var discovery atomic.Bool
			late := make(chan struct{})
			defer close(late)
			firstHead, headRelease := make(chan struct{}), make(chan struct{})
			secondBody := make(chan struct{})
			defer close(secondBody)
			req := Request{Key: "switch", Method: "GET", Header: make(http.Header), Fetch: func(ctx context.Context, h http.Header) (*http.Response, error) {
				part, err := parseRange(h.Get("Range"), int64(len(old)))
				if err != nil {
					return nil, err
				}
				if !discovery.Swap(true) {
					return partial(part, int64(len(old)), io.NopCloser(strings.NewReader(old[part.Start:part.End]))), nil
				}
				if part.Start == 4 {
					close(firstHead)
					select {
					case <-headRelease:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					reader, writer := io.Pipe()
					go func() {
						defer writer.Close()
						select {
						case <-late:
							if mode == "late200" {
								io.WriteString(writer, strings.Repeat("L", 16))
							} else {
								io.WriteString(writer, old[part.Start:part.End])
							}
						case <-ctx.Done():
						}
					}()
					if mode == "late200" {
						return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}, "Etag": {`"late"`}}, Body: reader, ContentLength: 16}, nil
					}
					return partial(part, 16, reader), nil
				}
				if mode == "purge" || mode == "evict" {
					reader, writer := io.Pipe()
					go func() {
						defer writer.Close()
						select {
						case <-secondBody:
							io.WriteString(writer, old[part.Start:part.End])
						case <-ctx.Done():
						}
					}()
					return partial(part, 16, reader), nil
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}, "Etag": {`"new"`}}, Body: io.NopCloser(strings.NewReader(strings.Repeat("N", 16))), ContentLength: 16}, nil
			}}
			consume(t, s, ranged(req, "bytes=0-3"))
			idle(t, s)
			type result struct {
				resp *Response
				err  error
			}
			first := make(chan result, 1)
			go func() { r, err := s.Do(context.Background(), ranged(req, "bytes=4-7")); first <- result{r, err} }()
			select {
			case <-firstHead:
			case <-time.After(3 * time.Second):
				t.Fatal("first publisher did not start")
			}
			b, err := s.Do(context.Background(), ranged(req, "bytes=12-15"))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "purge" {
				if _, err := s.Purge(context.Background(), Filter{}); err != nil {
					t.Fatal(err)
				}
				secondBody <- struct{}{}
			}
			if mode == "evict" {
				s.evict(0)
				secondBody <- struct{}{}
			}
			got, err := io.ReadAll(b.HTTP.Body)
			b.HTTP.Body.Close()
			wantSecond := "NNNN"
			if mode == "purge" || mode == "evict" {
				wantSecond = old[12:16]
			}
			if err != nil || string(got) != wantSecond {
				t.Fatalf("second publisher slice: %q %v", got, err)
			}
			close(headRelease)
			var a *Response
			select {
			case result := <-first:
				if result.err != nil {
					t.Fatal(result.err)
				}
				a = result.resp
			case <-time.After(3 * time.Second):
				t.Fatal("first head did not complete")
			}
			late <- struct{}{}
			got, err = io.ReadAll(a.HTTP.Body)
			a.HTTP.Body.Close()
			wantFirst := old[4:8]
			if mode == "late200" {
				wantFirst = "LLLL"
			}
			if err != nil || string(got) != wantFirst {
				t.Fatalf("old generation consumer: %q %v", got, err)
			}
			if err := a.wait(context.Background()); !errors.Is(err, ErrNotStored) {
				t.Fatalf("late publisher accepted: %v", err)
			}
			idle(t, s)
			if mode == "purge" || mode == "evict" {
				if s.Stats().Entries != 0 {
					t.Fatal("old fill resurrected purge")
				}
			} else {
				r, got := consume(t, s, Request{Key: req.Key, Method: "GET", Header: make(http.Header), Fetch: req.Fetch})
				if r.Outcome != "HIT" || got != strings.Repeat("N", 16) {
					t.Fatalf("late rollback: %s %q", r.Outcome, got)
				}
			}
		})
	}
}
