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

func testStore(t *testing.T, configure func(*Options)) *Store {
	t.Helper()
	opt := Options{Dir: t.TempDir(), MaxBytes: 8 << 20, InlineBytes: 32, BlockBytes: 4}
	if configure != nil {
		configure(&opt)
	}
	s, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func originRequest(t *testing.T, handler http.HandlerFunc) (Request, *atomic.Int64) {
	t.Helper()
	calls := new(atomic.Int64)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	transport := &http.Transport{DisableCompression: true}
	t.Cleanup(transport.CloseIdleConnections)
	r := Request{Key: "site/example/data", SiteID: "site", Host: "example", Path: "/data", Method: "GET", Header: make(http.Header)}
	r.Fetch = func(ctx context.Context, overrides http.Header) (*http.Response, error) {
		calls.Add(1)
		request, err := http.NewRequestWithContext(ctx, r.Method, server.URL+"/data", nil)
		if err != nil {
			return nil, err
		}
		request.Header = r.Header.Clone()
		for k, v := range overrides {
			request.Header.Del(k)
			for _, value := range v {
				request.Header.Add(k, value)
			}
		}
		return transport.RoundTrip(request)
	}
	return r, calls
}

func consume(t *testing.T, s *Store, req Request) (*Response, string) {
	t.Helper()
	r, err := s.Do(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r.HTTP.Body)
	r.HTTP.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return r, string(b)
}

func idle(t *testing.T, s *Store) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.Stats().ActiveFills != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("fills did not complete: %+v", s.Stats())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSharedStreamingIndependentCancellation(t *testing.T) {
	s := testStore(t, nil)
	release := make(chan struct{})
	defer close(release)
	req, calls := originRequest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Content-Length", "10")
		w.Header().Set("ETag", `"one"`)
		io.WriteString(w, "first")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "last!")
	})
	ctx, cancel := context.WithCancel(context.Background())
	a, err := s.Do(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 5)
	if _, err := io.ReadFull(a.HTTP.Body, first); err != nil || string(first) != "first" {
		t.Fatalf("stream: %q %v", first, err)
	}
	b, err := s.Do(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if a.Outcome != "MISS" || b.Outcome != "COALESCED" {
		t.Fatalf("outcomes: %s %s", a.Outcome, b.Outcome)
	}
	cancel()
	a.HTTP.Body.Close()
	// A canceled consumer must not cancel the common acquisition.
	if _, err := io.ReadFull(b.HTTP.Body, first); err != nil || string(first) != "first" {
		t.Fatalf("second consumer: %q %v", first, err)
	}
	release <- struct{}{}
	last, err := io.ReadAll(b.HTTP.Body)
	b.HTTP.Body.Close()
	if err != nil || string(last) != "last!" {
		t.Fatalf("tail: %q %v", last, err)
	}
	if b.CachedBytes() != 0 {
		t.Fatalf("coalesced bytes counted as hits: %d", b.CachedBytes())
	}
	idle(t, s)
	c, body := consume(t, s, req)
	if c.Outcome != "HIT" || body != "firstlast!" || c.CachedBytes() != 10 || calls.Load() != 1 {
		t.Fatalf("published result: %s %q bytes=%d calls=%d", c.Outcome, body, c.CachedBytes(), calls.Load())
	}
}

func TestLastConsumerCancelsFill(t *testing.T) {
	s := testStore(t, nil)
	canceled := make(chan struct{})
	req, _ := originRequest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	})
	r, err := s.Do(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	r.HTTP.Body.Close()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("orphan acquisition was not canceled")
	}
	idle(t, s)
	if s.Stats().Entries != 0 || s.Stats().ResidentBytes != 0 {
		t.Fatalf("orphan retained: %+v", s.Stats())
	}
}

func TestConditionalRefreshAndStaleIfError(t *testing.T) {
	s := testStore(t, nil)
	var state atomic.Int32
	var conditional atomic.Bool
	req, calls := originRequest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"stable"`)
		if state.Load() == 2 {
			w.WriteHeader(503)
			return
		}
		if r.Header.Get("If-None-Match") == `"stable"` {
			conditional.Store(true)
			w.Header().Set("Cache-Control", "max-age=60, stale-if-error=60")
			w.WriteHeader(304)
			return
		}
		w.Header().Set("Cache-Control", "max-age=0, stale-if-error=60")
		io.WriteString(w, "unchanged")
	})
	consume(t, s, req)
	idle(t, s)
	r, body := consume(t, s, req)
	if r.Outcome != "REVALIDATED" || body != "unchanged" || !conditional.Load() || r.CachedBytes() != 9 {
		t.Fatalf("304: %s %q", r.Outcome, body)
	}
	idle(t, s)
	r, _ = consume(t, s, req)
	if r.Outcome != "HIT" || calls.Load() != 2 {
		t.Fatalf("304 metadata not reused: %s calls=%d", r.Outcome, calls.Load())
	}
	state.Store(2)
	req.Header = http.Header{"Cache-Control": {"no-cache"}}
	r, body = consume(t, s, req)
	if r.Outcome != "STALE" || body != "unchanged" || r.CachedBytes() != 9 {
		t.Fatalf("SIE: %s %q", r.Outcome, body)
	}
}

func TestBackgroundSWRAndPurgeFence(t *testing.T) {
	s := testStore(t, nil)
	release := make(chan struct{})
	entered := make(chan struct{})
	defer close(release)
	var attempt atomic.Int32
	req, _ := originRequest(t, func(w http.ResponseWriter, r *http.Request) {
		if attempt.Add(1) == 2 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Cache-Control", "max-age=0, stale-while-revalidate=60")
		w.Header().Set("ETag", `"v1"`)
		io.WriteString(w, "generation")
	})
	consume(t, s, req)
	idle(t, s)
	r, body := consume(t, s, req)
	if r.Outcome != "STALE" || body != "generation" {
		t.Fatalf("SWR: %s %q", r.Outcome, body)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("no background refresh")
	}
	n, err := s.Purge(context.Background(), Filter{SiteID: "site"})
	if err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
	release <- struct{}{}
	idle(t, s)
	if s.Stats().Entries != 0 {
		t.Fatal("old background fill resurrected purged generation")
	}
}

func TestPurgePinsOldReaderAndFencesFirstFill(t *testing.T) {
	s := testStore(t, nil)
	body := strings.Repeat("12345678", 32)
	req, calls := originRequest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		io.WriteString(w, body)
	})
	consume(t, s, req)
	idle(t, s)
	old, err := s.Do(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.Purge(context.Background(), Filter{}); n != 1 || err != nil {
		t.Fatalf("purge: %d %v", n, err)
	}
	if s.Stats().ResidentBytes != int64(len(body)) {
		t.Fatal("purge did not retain pinned storage")
	}
	got, err := io.ReadAll(old.HTTP.Body)
	old.HTTP.Body.Close()
	if err != nil || string(got) != body {
		t.Fatalf("purged reader: %v", err)
	}
	if s.Stats().ResidentBytes != 0 {
		t.Fatalf("retired bytes remain: %+v", s.Stats())
	}
	consume(t, s, req)
	if calls.Load() != 2 {
		t.Fatal("purge did not prevent new reader")
	}

	release := make(chan struct{})
	defer close(release)
	first := Request{Key: "new", SiteID: "site", Method: "GET", Header: make(http.Header), Fetch: func(ctx context.Context, h http.Header) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			select {
			case <-release:
				writer.Write([]byte("abc"))
			case <-ctx.Done():
			}
			writer.Close()
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: reader, ContentLength: 3}, nil
	}}
	r, err := s.Do(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Purge(context.Background(), Filter{SiteID: "site"}); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	io.Copy(io.Discard, r.HTTP.Body)
	r.HTTP.Body.Close()
	idle(t, s)
	if s.Stats().Entries != 0 {
		t.Fatal("first fill published over purge fence")
	}
}

func TestHEADClientConditionsAndRanges(t *testing.T) {
	s := testStore(t, nil)
	req, calls := originRequest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("ETag", `"body"`)
		w.Header().Set("Last-Modified", "Mon, 01 Jan 2024 00:00:00 GMT")
		io.WriteString(w, "0123456789")
	})
	consume(t, s, req)
	idle(t, s)
	for _, tc := range []struct {
		name, method string
		h            http.Header
		status       int
		body         string
		length       int64
	}{
		{"head", "HEAD", nil, 200, "", 10},
		{"etag", "GET", http.Header{"If-None-Match": {`W/"body"`}}, 304, "", 0},
		{"modified", "GET", http.Header{"If-Modified-Since": {"Tue, 02 Jan 2024 00:00:00 GMT"}}, 304, "", 0},
		{"mismatch", "GET", http.Header{"If-Match": {`"other"`}}, 412, "", 0},
		{"range", "GET", http.Header{"Range": {"bytes=2-5"}}, 206, "2345", 4},
		{"suffix", "GET", http.Header{"Range": {"bytes=-3"}}, 206, "789", 3},
		{"if-range", "GET", http.Header{"Range": {"bytes=2-5"}, "If-Range": {`"old"`}}, 200, "0123456789", 10},
		{"unsatisfiable", "GET", http.Header{"Range": {"bytes=20-"}}, 416, "", 0},
		{"malformed", "GET", http.Header{"Range": {"bytes=bad"}}, 200, "0123456789", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req
			r.Method, r.Header = tc.method, tc.h
			resp, body := consume(t, s, r)
			if resp.HTTP.StatusCode != tc.status || body != tc.body || resp.HTTP.ContentLength != tc.length {
				t.Fatalf("got %d %q length=%d", resp.HTTP.StatusCode, body, resp.HTTP.ContentLength)
			}
		})
	}
	if calls.Load() != 1 {
		t.Fatalf("cache delivery called origin %d times", calls.Load())
	}
}

func TestSparseCompletionValidatorGenerationAndReopen(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(fmt.Sprint("change=", change), func(t *testing.T) {
			s := testStore(t, nil)
			var version atomic.Int32
			version.Store(1)
			var badRange atomic.Bool
			var partials atomic.Int32
			req, calls := originRequest(t, func(w http.ResponseWriter, r *http.Request) {
				body, tag := "0123456789", `"v1"`
				if version.Load() == 2 {
					body, tag = "abcdefghij", `"v2"`
				}
				w.Header().Set("Cache-Control", "max-age=60")
				w.Header().Set("ETag", tag)
				if value := r.Header.Get("Range"); value != "" && (r.Header.Get("If-Range") == "" || r.Header.Get("If-Range") == tag) {
					part, err := parseRange(value, int64(len(body)))
					if err != nil {
						w.WriteHeader(416)
						return
					}
					if partials.Add(1) == 2 && (value != "bytes=4-9" || r.Header.Get("If-Range") != `"v1"`) {
						badRange.Store(true)
					}
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", part.Start, part.End-1, len(body)))
					w.WriteHeader(206)
					io.WriteString(w, body[part.Start:part.End])
					return
				}
				io.WriteString(w, body)
			})
			rangeReq := req
			rangeReq.Header = http.Header{"Range": {"bytes=0-3"}}
			// The fetch closure intentionally follows the mutable fixture header,
			// just as gateway follows its immutable outbound plan per call.
			originalFetch := req.Fetch
			rangeReq.Fetch = func(ctx context.Context, h http.Header) (*http.Response, error) {
				if h == nil {
					h = make(http.Header)
				}
				if _, ok := h["Range"]; !ok {
					h.Set("Range", "bytes=0-3")
				}
				return originalFetch(ctx, h)
			}
			r, body := consume(t, s, rangeReq)
			if r.HTTP.StatusCode != 206 || body != "0123" {
				t.Fatalf("partial: %d %q", r.HTTP.StatusCode, body)
			}
			idle(t, s)
			entries, err := s.Entries(context.Background(), Filter{}, 10)
			if err != nil || len(entries) != 1 || entries[0].Complete {
				t.Fatalf("coverage: %+v %v", entries, err)
			}
			if change {
				version.Store(2)
			}
			r, body = consume(t, s, req)
			want, cached := "0123456789", int64(4)
			if change {
				want, cached = "abcdefghij", 0
			}
			if body != want || r.HTTP.StatusCode != 200 || r.CachedBytes() != cached || badRange.Load() {
				t.Fatalf("completion: %d %q cached=%d badRange=%v", r.HTTP.StatusCode, body, r.CachedBytes(), badRange.Load())
			}
			idle(t, s)
			opt := s.opt
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(opt)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			r, body = consume(t, reopened, req)
			if r.Outcome != "HIT" || body != want || calls.Load() != 2 {
				t.Fatalf("reopen: %s %q calls=%d", r.Outcome, body, calls.Load())
			}
		})
	}
}

func TestEligibilityVaryAndTransform(t *testing.T) {
	for _, tc := range []struct {
		name              string
		request, response http.Header
		policy            Policy
		cached            bool
	}{
		{"private", nil, http.Header{"Cache-Control": {"private, max-age=60"}}, Policy{}, false},
		{"no-store", nil, http.Header{"Cache-Control": {"no-store"}}, Policy{Mode: "force", TTL: time.Minute}, false},
		{"cookie", http.Header{"Cookie": {"session=a"}}, nil, Policy{TTL: time.Minute}, false},
		{"authorization", http.Header{"Authorization": {"Bearer x"}}, nil, Policy{Mode: "force", TTL: time.Minute}, false},
		{"set-cookie", nil, http.Header{"Set-Cookie": {"session=a"}}, Policy{TTL: time.Minute}, false},
		{"custom-vary", nil, http.Header{"Vary": {"X-Secret"}}, Policy{TTL: time.Minute}, true},
		{"sensitive-vary", nil, http.Header{"Vary": {"Cookie"}}, Policy{TTL: time.Minute}, false},
		{"star-vary", nil, http.Header{"Vary": {"*"}}, Policy{TTL: time.Minute}, false},
		{"no-freshness", nil, nil, Policy{}, false},
		{"public", nil, http.Header{"Cache-Control": {"public, max-age=60"}}, Policy{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t, nil)
			req, calls := originRequest(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.response {
					w.Header()[k] = v
				}
				io.WriteString(w, "body")
			})
			req.Header, req.Policy = tc.request, tc.policy
			consume(t, s, req)
			idle(t, s)
			r, _ := consume(t, s, req)
			if (r.Outcome == "HIT") != tc.cached || (calls.Load() == 1) != tc.cached {
				t.Fatalf("eligibility: %s calls=%d", r.Outcome, calls.Load())
			}
		})
	}
	t.Run("transform", func(t *testing.T) {
		s := testStore(t, nil)
		req, _ := originRequest(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "body") })
		req.Transform = func(resp *http.Response, policy *Policy) error {
			resp.Header.Set("Cache-Control", "max-age=60")
			resp.Header.Set("X-Source", "transformed")
			return nil
		}
		consume(t, s, req)
		idle(t, s)
		r, _ := consume(t, s, req)
		if r.Outcome != "HIT" || r.HTTP.Header.Get("X-Source") != "transformed" {
			t.Fatal("source transform did not precede eligibility")
		}
	})
}

func TestReopenSmallAndFileBodies(t *testing.T) {
	s := testStore(t, nil)
	for _, size := range []int{10, 100} {
		req := Request{Key: fmt.Sprint(size), Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", size))), ContentLength: int64(size)}, nil
		}}
		consume(t, s, req)
	}
	idle(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Incomplete acquisitions and crash orphans are never recovered as bodies.
	if err := os.WriteFile(s.opt.Dir+"/tmp/orphan", []byte("discard"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(s.opt)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, size := range []int{10, 100} {
		req := Request{Key: fmt.Sprint(size), Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
			return nil, errors.New("unexpected origin")
		}}
		resp, body := consume(t, r, req)
		if resp.Outcome != "HIT" || len(body) != size {
			t.Fatalf("reopen %d: %s %q", size, resp.Outcome, body)
		}
	}
	if _, err := os.Stat(s.opt.Dir + "/tmp/orphan"); !os.IsNotExist(err) {
		t.Fatal("orphan was not collected")
	}
}

func TestVaryAndEncodingIsolation(t *testing.T) {
	s := testStore(t, nil)
	var calls atomic.Int32
	request := func(lang, encoding string) Request {
		h := http.Header{"Accept-Language": {lang}, "Accept-Encoding": {encoding}}
		body := lang + "/" + encoding
		return Request{Key: "variant", Method: "GET", Header: h, Fetch: func(context.Context, http.Header) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}, "Vary": {"Accept-Language"}, "ETag": {fmt.Sprintf("%q", body)}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}, nil
		}}
	}
	for _, pair := range [][2]string{{"en", "identity"}, {"fr", "identity"}, {"en", "gzip"}, {"en", "identity"}, {"fr", "identity"}} {
		r, body := consume(t, s, request(pair[0], pair[1]))
		if body != pair[0]+"/"+pair[1] {
			t.Fatalf("leaked variant: %s %q", r.Outcome, body)
		}
		idle(t, s)
	}
	if calls.Load() != 3 {
		t.Fatalf("variant calls=%d", calls.Load())
	}
}

func TestCustomVaryInputsAreNotCoalesced(t *testing.T) {
	s := testStore(t, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	request := func(secret string) Request {
		return Request{Key: "unknown-vary", Method: "GET", Header: http.Header{"X-Secret": {secret}}, Fetch: func(ctx context.Context, h http.Header) (*http.Response, error) {
			n := calls.Add(1)
			if n == 1 {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}, "Vary": {"X-Secret"}}, Body: io.NopCloser(strings.NewReader(secret)), ContentLength: int64(len(secret))}, nil
		}}
	}
	first := make(chan string, 1)
	go func() {
		r, err := s.Do(context.Background(), request("alice"))
		if err != nil {
			first <- err.Error()
			return
		}
		b, _ := io.ReadAll(r.HTTP.Body)
		r.HTTP.Body.Close()
		first <- string(b)
	}()
	<-entered
	second := make(chan string, 1)
	go func() {
		r, err := s.Do(context.Background(), request("bob"))
		if err != nil {
			second <- err.Error()
			return
		}
		b, _ := io.ReadAll(r.HTTP.Body)
		r.HTTP.Body.Close()
		second <- string(b)
	}()
	close(release)
	if a, b := <-first, <-second; a != "alice" || b != "bob" {
		t.Fatalf("unknown Vary leaked: %q %q", a, b)
	}
	idle(t, s)
	if calls.Load() != 2 || s.Stats().Entries != 2 {
		t.Fatalf("custom Vary: calls=%d stats=%+v", calls.Load(), s.Stats())
	}
	for _, secret := range []string{"alice", "bob"} {
		resp, body := consume(t, s, request(secret))
		if resp.Outcome != "HIT" || body != secret {
			t.Fatalf("custom Vary reuse: %s %q", resp.Outcome, body)
		}
	}
}

func TestSparseHolesStayHolesAndNeverOverwritePublishedBytes(t *testing.T) {
	s := testStore(t, func(o *Options) { o.BlockBytes = 1 })
	var calls atomic.Int32
	request := func(rangeValue string) Request {
		h := http.Header{}
		if rangeValue != "" {
			h.Set("Range", rangeValue)
		}
		return Request{Key: "holes", Method: "GET", Header: h, Fetch: func(ctx context.Context, overrides http.Header) (*http.Response, error) {
			n := calls.Add(1)
			header := h.Clone()
			for k, v := range overrides {
				header[k] = v
			}
			part, err := parseRange(header.Get("Range"), 10)
			if err != nil {
				return nil, err
			}
			body := "0123456789"
			// This intentionally violates validator semantics for an overlap.
			// The cache must still never rewrite its already published bytes.
			if n == 3 {
				body = "xxxxXXxxxx"
			}
			return &http.Response{StatusCode: 206, Header: http.Header{"Cache-Control": {"max-age=60"}, "ETag": {`"one"`}, "Content-Range": {fmt.Sprintf("bytes %d-%d/10", part.Start, part.End-1)}}, Body: io.NopCloser(strings.NewReader(body[part.Start:part.End])), ContentLength: part.End - part.Start}, nil
		}}
	}
	consume(t, s, request("bytes=0-1"))
	idle(t, s)
	consume(t, s, request("bytes=4-5"))
	idle(t, s)
	entries, _ := s.Entries(context.Background(), Filter{}, 10)
	if len(entries) != 1 || entries[0].Complete || s.Stats().Bytes != 4 {
		t.Fatalf("holes lost: %+v %+v", entries, s.Stats())
	}
	r, body := consume(t, s, request(""))
	idle(t, s)
	if body != "01xx45xxxx" || r.CachedBytes() != 4 {
		t.Fatalf("overlap changed old coverage: %q cached=%d", body, r.CachedBytes())
	}
	r, body = consume(t, s, request(""))
	if r.Outcome != "HIT" || body != "01xx45xxxx" {
		t.Fatalf("completion not published: %s %q", r.Outcome, body)
	}
}

func TestLimitsTruncationAndEviction(t *testing.T) {
	t.Run("fills", func(t *testing.T) {
		s := testStore(t, func(o *Options) { o.MaxFills = 1 })
		reader, writer := io.Pipe()
		defer writer.Close()
		r := Request{Key: "busy", Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: reader, ContentLength: 10}, nil
		}}
		response, err := s.Do(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		r.Key = "other"
		if _, err := s.Do(context.Background(), r); !errors.Is(err, ErrBusy) {
			t.Fatalf("fill limit: %v", err)
		}
		response.HTTP.Body.Close()
		idle(t, s)
	})
	t.Run("truncation", func(t *testing.T) {
		s := testStore(t, nil)
		r := Request{Key: "short", Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: io.NopCloser(strings.NewReader("abc")), ContentLength: 10}, nil
		}}
		response, err := s.Do(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.HTTP.Body)
		response.HTTP.Body.Close()
		idle(t, s)
		if string(body) != "abc" || !errors.Is(err, io.ErrUnexpectedEOF) || s.Stats().Entries != 0 {
			t.Fatalf("truncated body: %q %v %+v", body, err, s.Stats())
		}
	})
	t.Run("eviction", func(t *testing.T) {
		s := testStore(t, func(o *Options) { o.MaxBytes = 1000; o.MaxObjectBytes = 1000 })
		request := func(key string) Request {
			return Request{Key: key, Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: io.NopCloser(strings.NewReader(strings.Repeat(key, 100))), ContentLength: 100}, nil
			}}
		}
		for _, key := range []string{"a", "b", "c"} {
			consume(t, s, request(key))
			idle(t, s)
		}
		consume(t, s, request("a")) // a is the latest hint, not the oldest persisted access
		s.evict(200)
		entries, _ := s.Entries(context.Background(), Filter{}, 10)
		foundA, foundB := false, false
		for _, e := range entries {
			foundA = foundA || e.Key == "a"
			foundB = foundB || e.Key == "b"
		}
		if !foundA || foundB || s.Stats().Evictions != 1 {
			t.Fatalf("recency eviction: %+v %+v", entries, s.Stats())
		}
		s.flushAccess()
		entries, _ = s.Entries(context.Background(), Filter{}, 10)
		if len(entries) != 2 {
			t.Fatal("batched access resurrected eviction")
		}
	})
	t.Run("known-large-bypass", func(t *testing.T) {
		s := testStore(t, func(o *Options) { o.MaxObjectBytes = 4 })
		r := Request{Key: "large", Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: io.NopCloser(strings.NewReader("0123456789")), ContentLength: 10}, nil
		}}
		response, body := consume(t, s, r)
		idle(t, s)
		if response.Outcome != "BYPASS" || body != "0123456789" || s.Stats().ResidentBytes != 0 {
			t.Fatalf("large bypass: %s %q %+v", response.Outcome, body, s.Stats())
		}
	})
}

func TestUnknownLengthAndExpiredAge(t *testing.T) {
	t.Run("unknown-length", func(t *testing.T) {
		s := testStore(t, nil)
		r := Request{Key: "chunked", Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: io.NopCloser(strings.NewReader("stream")), ContentLength: -1}, nil
		}}
		first, body := consume(t, s, r)
		idle(t, s)
		if body != "stream" || first.HTTP.ContentLength != -1 {
			t.Fatalf("unknown length: %q %d", body, first.HTTP.ContentLength)
		}
		second, body := consume(t, s, r)
		if second.Outcome != "HIT" || second.HTTP.ContentLength != 6 || body != "stream" {
			t.Fatalf("completed length: %s %d %q", second.Outcome, second.HTTP.ContentLength, body)
		}
	})
	t.Run("already-expired-age", func(t *testing.T) {
		s := testStore(t, nil)
		var calls atomic.Int32
		r := Request{Key: "old", Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
			if calls.Add(1) > 1 {
				return nil, errors.New("origin down")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=10, stale-if-error=10"}, "Age": {"100"}}, Body: io.NopCloser(strings.NewReader("too old")), ContentLength: 7}, nil
		}}
		consume(t, s, r)
		idle(t, s)
		if response, err := s.Do(context.Background(), r); err == nil {
			response.HTTP.Body.Close()
			t.Fatal("Age reset granted an expired stale window")
		}
	})
}

func TestSparseReopenBeforeCompletion(t *testing.T) {
	s := testStore(t, nil)
	request := func(value string) Request {
		h := make(http.Header)
		if value != "" {
			h.Set("Range", value)
		}
		return Request{Key: "sparse-reopen", Method: "GET", Header: h, Fetch: func(ctx context.Context, overrides http.Header) (*http.Response, error) {
			header := h.Clone()
			for k, v := range overrides {
				header[k] = v
			}
			part, err := parseRange(header.Get("Range"), 10)
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: 206, Header: http.Header{"Cache-Control": {"max-age=60"}, "ETag": {`"same"`}, "Content-Range": {fmt.Sprintf("bytes %d-%d/10", part.Start, part.End-1)}}, Body: io.NopCloser(strings.NewReader("0123456789"[part.Start:part.End])), ContentLength: part.End - part.Start}, nil
		}}
	}
	consume(t, s, request("bytes=0-3"))
	idle(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := Open(s.opt)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	partial, body := consume(t, r, request("bytes=0-3"))
	if partial.Outcome != "HIT" || body != "0123" {
		t.Fatalf("sparse reopen: %s %q", partial.Outcome, body)
	}
	full, body := consume(t, r, request(""))
	idle(t, r)
	if body != "0123456789" || full.CachedBytes() != 4 {
		t.Fatalf("sparse reopened completion: %q %d", body, full.CachedBytes())
	}
}

func TestETagGrammarAndFillBudgets(t *testing.T) {
	if !etagMatches(`"other", W/"has,comma"`, `"has,comma"`, true) ||
		etagMatches(`W/"tag"`, `"tag"`, false) || strongETag(http.Header{"Etag": {`"bad"quote"`}}) != "" {
		t.Fatal("validator grammar/weak comparison")
	}
	for _, tc := range []struct {
		name                string
		maxBytes, maxObject int64
		want                error
	}{
		{"object", 100, 4, ErrObjectTooLarge},
		{"capacity", 4, 4, ErrCapacity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t, func(o *Options) { o.MaxBytes = tc.maxBytes; o.MaxObjectBytes = tc.maxObject })
			if tc.name == "capacity" {
				r := Request{Key: "pin", Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: io.NopCloser(strings.NewReader("1234")), ContentLength: 4}, nil
				}}
				consume(t, s, r)
				idle(t, s)
				pin, err := s.Do(context.Background(), r)
				if err != nil {
					t.Fatal(err)
				}
				defer pin.HTTP.Body.Close()
			}
			r := Request{Key: "unknown-limit", Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: io.NopCloser(strings.NewReader("12345")), ContentLength: -1}, nil
			}}
			if tc.name == "capacity" {
				r.Fetch = func(context.Context, http.Header) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: io.NopCloser(strings.NewReader("1")), ContentLength: -1}, nil
				}
			}
			response, err := s.Do(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			_, err = io.ReadAll(response.HTTP.Body)
			response.HTTP.Body.Close()
			idle(t, s)
			if !errors.Is(err, tc.want) || s.Stats().ResidentBytes > s.opt.MaxBytes {
				t.Fatalf("budget: %v %+v", err, s.Stats())
			}
		})
	}
}

func TestSparseCompletionRejectsUnprovenInternalRange(t *testing.T) {
	s := testStore(t, nil)
	var phase atomic.Int32
	request := func(value string) Request {
		h := make(http.Header)
		if value != "" {
			h.Set("Range", value)
		}
		return Request{Key: "unproven", Method: "GET", Header: h, Fetch: func(ctx context.Context, overrides http.Header) (*http.Response, error) {
			header := h.Clone()
			for k, v := range overrides {
				header[k] = v
			}
			part, err := parseRange(header.Get("Range"), 10)
			if err != nil {
				return nil, err
			}
			head := http.Header{"Cache-Control": {"max-age=60"}, "ETag": {`"strong"`}, "Content-Range": {fmt.Sprintf("bytes %d-%d/10", part.Start, part.End-1)}}
			if phase.Load() == 1 {
				delete(head, "ETag")
			}
			return &http.Response{StatusCode: 206, Header: head, Body: io.NopCloser(strings.NewReader("0123456789"[part.Start:part.End])), ContentLength: part.End - part.Start}, nil
		}}
	}
	consume(t, s, request("bytes=0-3"))
	idle(t, s)
	phase.Store(1)
	if response, err := s.Do(context.Background(), request("")); err == nil {
		response.HTTP.Body.Close()
		t.Fatal("internal missing range escaped as the full client response")
	}
	idle(t, s)
	if s.Stats().Bytes != 4 {
		t.Fatalf("unproven range changed coverage: %+v", s.Stats())
	}
}

func TestBackgroundRefreshSwitchesGenerationWithoutBreakingReader(t *testing.T) {
	s := testStore(t, nil)
	var version atomic.Int32
	version.Store(1)
	request := Request{Key: "refresh-switch", Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
		v := version.Load()
		body := strings.Repeat(fmt.Sprint(v), 100)
		return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=0, stale-while-revalidate=60"}, "ETag": {fmt.Sprintf(`"v%d"`, v)}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}, nil
	}}
	consume(t, s, request)
	idle(t, s)
	version.Store(2)
	old, err := s.Do(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if old.Outcome != "STALE" {
		t.Fatalf("refresh response: %s", old.Outcome)
	}
	idle(t, s)
	body, err := io.ReadAll(old.HTTP.Body)
	old.HTTP.Body.Close()
	if err != nil || string(body) != strings.Repeat("1", 100) {
		t.Fatalf("old generation reader: %q %v", body, err)
	}
	next, body2 := consume(t, s, request)
	if body2 != strings.Repeat("2", 100) || next.CachedBytes() != 100 {
		t.Fatalf("generation switch: %q %d", body2, next.CachedBytes())
	}
}

func TestCloseCancelsDirectStreamsAndRejectsAdmission(t *testing.T) {
	s := testStore(t, nil)
	reader, writer := io.Pipe()
	defer writer.Close()
	req := Request{Key: "direct-close", Method: "GET", Header: make(http.Header), Policy: Policy{Mode: "bypass"}, Fetch: func(ctx context.Context, h http.Header) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: reader, ContentLength: -1}, nil
	}}
	response, err := s.Do(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close left direct stream active")
	}
	response.HTTP.Body.Close()
	if s.Stats().ActiveFills != 0 || s.Stats().Readers != 0 {
		t.Fatalf("direct budget leaked: %+v", s.Stats())
	}
	if _, err := s.Do(context.Background(), req); !errors.Is(err, ErrClosed) {
		t.Fatalf("post-close admission: %v", err)
	}
}

func TestFillTimeoutAndOnlyIfCached(t *testing.T) {
	s := testStore(t, func(o *Options) { o.FillTimeout = 50 * time.Millisecond })
	var calls atomic.Int32
	req := Request{Key: "timeout", Method: "GET", Header: make(http.Header), Fetch: func(ctx context.Context, h http.Header) (*http.Response, error) {
		calls.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	if _, err := s.Do(context.Background(), req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fill timeout: %v", err)
	}
	idle(t, s)
	req.Header.Set("Cache-Control", "only-if-cached")
	response, body := consume(t, s, req)
	if response.HTTP.StatusCode != 504 || body != "" || calls.Load() != 1 {
		t.Fatalf("only-if-cached reached origin: %d calls=%d", response.HTTP.StatusCode, calls.Load())
	}
}

func TestConditionalMissStoresOriginBodyBeforeReturning304(t *testing.T) {
	s := testStore(t, nil)
	var calls atomic.Int32
	req := Request{Key: "conditional-miss", Method: "GET", Header: http.Header{"If-None-Match": {`"same"`}}, Fetch: func(context.Context, http.Header) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}, "ETag": {`"same"`}}, Body: io.NopCloser(strings.NewReader("body")), ContentLength: 4}, nil
	}}
	response, body := consume(t, s, req)
	if response.HTTP.StatusCode != 304 || body != "" {
		t.Fatalf("conditional miss: %d %q", response.HTTP.StatusCode, body)
	}
	idle(t, s)
	req.Header = make(http.Header)
	response, body = consume(t, s, req)
	if response.Outcome != "HIT" || body != "body" || calls.Load() != 1 {
		t.Fatalf("conditional miss lost body: %s %q calls=%d", response.Outcome, body, calls.Load())
	}
}
