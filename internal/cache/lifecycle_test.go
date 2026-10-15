package cache

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
)

func TestTruncatedReplacementRetainsStaleBase(t *testing.T) {
	s := testStore(t, nil)
	var phase atomic.Int32
	req, _ := originRequest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=0, stale-if-error=60")
		switch phase.Load() {
		case 0:
			w.Header().Set("ETag", `"old"`)
			io.WriteString(w, "old body")
		case 1:
			w.Header().Set("ETag", `"replacement"`)
			w.Header().Set("Content-Length", "20")
			io.WriteString(w, "short")
		default:
			w.WriteHeader(503)
		}
	})
	consume(t, s, req)
	idle(t, s)
	phase.Store(1)
	replacement := req
	replacement.Revalidate = true
	resp, err := s.Do(context.Background(), replacement)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.HTTP.Body)
	resp.HTTP.Body.Close()
	if string(body) != "short" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("replacement: %q %v", body, err)
	}
	idle(t, s)
	phase.Store(2)
	resp, old := consume(t, s, req)
	if resp.Outcome != "STALE" || old != "old body" || resp.HTTP.Header.Get("ETag") != `"old"` || s.Stats().Entries != 1 {
		t.Fatalf("failed replacement lost base: %s %q %+v", resp.Outcome, old, s.Stats())
	}
}

func TestRefreshRevalidatesFreshAndSWRWithoutHeaderChanges(t *testing.T) {
	for _, policy := range []string{"max-age=60", "max-age=0, stale-while-revalidate=60"} {
		t.Run(policy, func(t *testing.T) {
			s := testStore(t, func(o *Options) { o.MaxReaders = 1 })
			var changedHeaders atomic.Bool
			req, calls := originRequest(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Cache-Control") != "" {
					changedHeaders.Store(true)
				}
				w.Header().Set("ETag", `"same"`)
				if r.Header.Get("If-None-Match") == `"same"` {
					w.Header().Set("Cache-Control", "max-age=120")
					w.Header().Set("X-Refreshed", "yes")
					w.WriteHeader(304)
					return
				}
				w.Header().Set("Cache-Control", policy)
				io.WriteString(w, "body")
			})
			consume(t, s, req)
			idle(t, s)
			force := req
			force.Revalidate = true
			if requestSignature(force) != requestSignature(req) {
				t.Fatal("revalidation changed acquisition identity")
			}
			if s.opt.FillTimeout != 30*time.Minute {
				t.Fatalf("default fill timeout: %v", s.opt.FillTimeout)
			}
			if err := s.Refresh(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			resp, body := consume(t, s, req)
			if resp.Outcome != "HIT" || body != "body" || resp.HTTP.Header.Get("X-Refreshed") != "yes" || calls.Load() != 2 || changedHeaders.Load() || req.Revalidate {
				t.Fatalf("refresh: %s %q calls=%d headersChanged=%v", resp.Outcome, body, calls.Load(), changedHeaders.Load())
			}
		})
	}
}

func TestRefreshWaitsForPublicationAndReportsStorageFailure(t *testing.T) {
	s := testStore(t, nil)
	reader, writer := io.Pipe()
	defer writer.Close()
	req := Request{Key: "refresh-publish", Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: reader, ContentLength: 100}, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.Refresh(ctx, req) }()
	// The first read establishes that the fill's temporary file exists. Block
	// only its final rename, so delivery succeeds while publication must fail.
	if _, err := writer.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(filepath.Join(s.opt.Dir, "tmp"))
	if err != nil || len(files) != 1 {
		t.Fatalf("active temporary body: %v %v", files, err)
	}
	if err := os.Mkdir(s.bodyPath(files[0].Name()), 0o750); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		t.Fatalf("refresh returned before completion: %v", err)
	default:
	}
	if _, err := writer.Write([]byte(strings.Repeat("b", 99))); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	if err := <-result; err == nil || errors.Is(err, ErrNotStored) {
		t.Fatalf("publication failure: %v", err)
	}
	if s.Stats().Entries != 0 {
		t.Fatal("failed publication became visible")
	}
}

func TestRefreshPublishesReplacementBeforeReturning(t *testing.T) {
	s := testStore(t, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var version atomic.Int32
	req, calls := originRequest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		if version.Load() == 0 {
			w.Header().Set("ETag", `"old"`)
			io.WriteString(w, "old body")
			return
		}
		w.Header().Set("ETag", `"new"`)
		w.Header().Set("Content-Length", "8")
		io.WriteString(w, "new ")
		w.(http.Flusher).Flush()
		close(entered)
		select {
		case <-release:
			io.WriteString(w, "body")
		case <-r.Context().Done():
		}
	})
	consume(t, s, req)
	version.Store(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.Refresh(ctx, req) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	resp, body := consume(t, s, req)
	if resp.Outcome != "HIT" || body != "old body" {
		t.Fatalf("refresh removed published base: %s %q", resp.Outcome, body)
	}
	release <- struct{}{}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	resp, body = consume(t, s, req)
	if resp.Outcome != "HIT" || body != "new body" || calls.Load() != 2 {
		t.Fatalf("refresh not published: %s %q calls=%d", resp.Outcome, body, calls.Load())
	}
}

func TestRefreshDoesNotReportStaleOrPrivateAsStored(t *testing.T) {
	s := testStore(t, nil)
	var phase atomic.Int32
	req, _ := originRequest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=0, stale-if-error=60")
		w.Header().Set("ETag", `"old"`)
		if phase.Load() == 1 {
			w.WriteHeader(503)
			return
		}
		if phase.Load() == 2 {
			w.Header().Set("Cache-Control", "private")
		}
		io.WriteString(w, "body")
	})
	consume(t, s, req)
	idle(t, s)
	for _, phaseValue := range []int32{1, 2} {
		phase.Store(phaseValue)
		if err := s.Refresh(context.Background(), req); !errors.Is(err, ErrNotStored) {
			t.Fatalf("refresh phase %d: %v", phaseValue, err)
		}
	}
	idle(t, s)
	if s.Stats().Entries != 0 {
		t.Fatal("private replacement did not invalidate base")
	}
}

func TestRefreshDoesNotReportPurgeRejectedPublication(t *testing.T) {
	s := testStore(t, nil)
	reader, writer := io.Pipe()
	defer writer.Close()
	req := Request{Key: "refresh-purge", Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: reader, ContentLength: 4}, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.Refresh(ctx, req) }()
	if _, err := writer.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Purge(ctx, Filter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("bcd")); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	if err := <-result; !errors.Is(err, ErrNotStored) {
		t.Fatalf("purge-rejected refresh: %v", err)
	}
	idle(t, s)
	if s.Stats().Entries != 0 {
		t.Fatal("refresh published over purge fence")
	}
}

func TestReopenRepairsOnlyLostBodyVariants(t *testing.T) {
	s := testStore(t, nil)
	request := func(lang string) Request {
		return Request{Key: "repair", Method: "GET", Header: http.Header{"Accept-Language": {lang}}, Fetch: func(context.Context, http.Header) (*http.Response, error) {
			body := strings.Repeat(lang, 30)
			return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}, "Vary": {"Accept-Language"}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}, nil
		}}
	}
	for _, lang := range []string{"en", "fr", "de"} {
		consume(t, s, request(lang))
	}
	idle(t, s)
	k := s.key("repair")
	k.mu.Lock()
	missing := k.rec.match(request("en").Header).ID
	truncated := k.rec.match(request("fr").Header).ID
	k.mu.Unlock()
	s.releaseKey(k)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.bodyPath(missing)); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(s.bodyPath(truncated), 1); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.opt)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if stats := reopened.Stats(); stats.Entries != 1 || stats.Bytes != 60 || stats.ResidentBytes != 60 {
		t.Fatalf("repair stats: %+v", stats)
	}
	if _, err := os.Stat(s.bodyPath(truncated)); !os.IsNotExist(err) {
		t.Fatalf("truncated orphan survived: %v", err)
	}
	for _, lang := range []string{"de", "en", "fr"} {
		resp, body := consume(t, reopened, request(lang))
		wantOutcome := "MISS"
		if lang == "de" {
			wantOutcome = "HIT"
		}
		if resp.Outcome != wantOutcome || body != strings.Repeat(lang, 30) {
			t.Fatalf("repair %s: %s %q", lang, resp.Outcome, body)
		}
	}
}

func TestVaryRejectsProtocolFieldsAndBoundsMetadata(t *testing.T) {
	for _, value := range []string{"*", "Authorization", "Cookie", "Content-Length", "If-None-Match", "Connection", "Range", "X Bad", strings.Repeat("x", 257)} {
		if _, ok := varyFields(http.Header{"Vary": {value}}); ok {
			t.Fatalf("unsafe Vary accepted: %q", value)
		}
	}
	fields := make([]string, 16)
	for i := range fields {
		fields[i] = "X-" + strings.Repeat("x", i+1)
	}
	if _, ok := varyFields(http.Header{"Vary": {strings.Join(fields, ",")}}); ok {
		t.Fatal("unbounded Vary field count")
	}
}

func TestReopenDoesNotMaskBodyIOErrors(t *testing.T) {
	s := testStore(t, nil)
	req := Request{Key: "io-error", Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 100))), ContentLength: 100}, nil
	}}
	consume(t, s, req)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(filepath.Join(s.opt.Dir, "bodies"))
	if err != nil || len(files) != 1 {
		t.Fatalf("body file: %v %v", files, err)
	}
	path := s.bodyPath(files[0].Name())
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(files[0].Name(), path); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.opt)
	if reopened != nil {
		reopened.Close()
	}
	if !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("body I/O error was hidden: %v", err)
	}
}

func TestReopenRepairsMissingInlineBodyWithoutHidingBadMetadata(t *testing.T) {
	s := testStore(t, nil)
	request := func(key string) Request {
		return Request{Key: key, Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: io.NopCloser(strings.NewReader(key)), ContentLength: int64(len(key))}, nil
		}}
	}
	consume(t, s, request("lost"))
	consume(t, s, request("healthy"))
	idle(t, s)
	k := s.key("lost")
	k.mu.Lock()
	id := k.rec.Entries[0].ID
	k.mu.Unlock()
	s.releaseKey(k)
	if err := s.db.Delete(bodyKey(id), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.opt)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	resp, _ := consume(t, reopened, request("healthy"))
	if resp.Outcome != "HIT" || reopened.Stats().Entries != 1 {
		t.Fatal("inline repair removed healthy entry")
	}
	resp, _ = consume(t, reopened, request("lost"))
	if resp.Outcome != "MISS" {
		t.Fatalf("missing inline body was not invalidated: %s", resp.Outcome)
	}
	idle(t, reopened)
	if err := reopened.db.Set(recordKey("corrupt"), []byte("not metadata"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	bad, err := Open(s.opt)
	if bad != nil {
		bad.Close()
	}
	if err == nil {
		t.Fatal("corrupt metadata was silently repaired")
	}
}

func TestCanceledReaderPreservesContextErrorAfterDeliveredLength(t *testing.T) {
	s := testStore(t, nil)
	req := Request{Key: "late-cancel", Method: "GET", Header: make(http.Header), Fetch: func(context.Context, http.Header) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=60"}}, Body: io.NopCloser(strings.NewReader("body")), ContentLength: 4}, nil
	}}
	for _, outcome := range []string{"MISS", "HIT"} {
		ctx, cancel := context.WithCancel(context.Background())
		r, err := s.Do(ctx, req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if r.Outcome != outcome {
			cancel()
			r.HTTP.Body.Close()
			t.Fatalf("fixture outcome: %s", r.Outcome)
		}
		if _, err := io.ReadFull(r.HTTP.Body, make([]byte, 4)); err != nil {
			cancel()
			r.HTTP.Body.Close()
			t.Fatal(err)
		}
		cancel()
		// Exercise the independently closed subscription/reference, not only
		// a cancellation observed before its callback runs.
		r.HTTP.Body.Close()
		if _, err := r.HTTP.Body.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
			t.Fatalf("late cancellation became source failure: %v", err)
		}
		idle(t, s)
	}
}
