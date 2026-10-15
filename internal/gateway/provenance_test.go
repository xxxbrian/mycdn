package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"mycdn/internal/cache"
)

// The completed origin supplies a full 8 KiB wire response. Its first half is
// uncached, the second half is a previously published sparse range. A 32 KiB
// copy-buffer read can read both before a client write accepts only 3893 bytes.
func sparseProvenanceGateway(t *testing.T) (*Gateway, *recorder) {
	t.Helper()
	body := strings.Repeat("n", 4096) + strings.Repeat("c", 4096)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end := 0, len(body)-1
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			t.Error("sparse fixture requires an explicit range")
			w.WriteHeader(400)
			return
		}
		if r.Header.Get("If-Range") != "" && r.Header.Get("If-Range") != `"same"` {
			t.Errorf("incorrect sparse validator %q", r.Header.Get("If-Range"))
		}
		w.Header().Set("Cache-Control", "public, max-age=600")
		w.Header().Set("ETag", `"same"`)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, body[start:end+1])
	}))
	t.Cleanup(origin.Close)
	objects, err := cache.Open(cache.Options{Dir: t.TempDir(), MaxBytes: 16 << 20, MaxObjectBytes: 8 << 20, InlineBytes: 1, BlockBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { objects.Close() })
	observer := &recorder{}
	g, _ := newGateway(t, origin.URL, nil, observer, objects)
	seed := serve(g, "GET", "/mixed", http.Header{"Range": {"bytes=4096-8191"}})
	if seed.Code != 206 || seed.Body.String() != body[4096:] {
		t.Fatalf("sparse seed = %d %d bytes", seed.Code, seed.Body.Len())
	}
	waitForPublication(t, objects)
	observer.mu.Lock()
	observer.requests = nil
	observer.origins = nil
	observer.transfers = nil
	observer.mu.Unlock()
	return g, observer
}

// Consume the complete cache response before ReverseProxy reads it, keeping its
// immutable prefix provenance. This deterministic read-ahead also lets the fill
// publish before testing delivery without relying on scheduling or disk speed.
func preReadCacheResponse(g *Gateway) {
	transport := g.proxy.Transport
	g.proxy.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(request)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		response.Body = io.NopCloser(strings.NewReader(string(body)))
		return response, nil
	})
}

func TestGate1CachedPrefixShortWriteExcludesUnreadCachedTail(t *testing.T) {
	g, observer := sparseProvenanceGateway(t)
	preReadCacheResponse(g)
	writer := &prefixWriter{header: make(http.Header), accepted: 3893, short: true}
	g.ServeHTTP(writer, httptest.NewRequest("GET", "http://cdn.test/mixed", nil))
	if writer.offered != 8192 || writer.written != 3893 {
		t.Fatalf("fixture did not read ahead across cached span: offered=%d written=%d", writer.offered, writer.written)
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.requests) != 1 {
		t.Fatal(observer.requests)
	}
	event := observer.requests[0]
	if event.ResponseBytes != 3893 || event.CachedBytes != 0 || event.Error == "" {
		t.Fatalf("read-ahead misattributed short write: %+v", event)
	}
	for _, delta := range observer.transfers {
		if delta.Kind == "request" && (delta.CachedBytes != 0 || delta.ResponseBytes != 3893) {
			t.Fatalf("short-write transfer = %+v", delta)
		}
	}
}

func TestGate1CachedPrefixSamplerDoesNotFollowReadAhead(t *testing.T) {
	g, observer := sparseProvenanceGateway(t)
	preReadCacheResponse(g)
	// Control sampling directly instead of relying on the one-second ticker.
	// The real writer/copy/cache path runs; only sample instants are explicit.
	g.sampler.close()
	first, second, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{})
	var firstOnce, secondOnce sync.Once
	unblockFirst := func() { firstOnce.Do(func() { close(releaseFirst) }) }
	unblockSecond := func() { secondOnce.Do(func() { close(releaseSecond) }) }
	defer unblockFirst()
	defer unblockSecond()
	writer := &prefixWriter{header: make(http.Header), first: first, second: second, releaseFirst: releaseFirst, releaseSecond: releaseSecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest("GET", "http://cdn.test/mixed", nil).WithContext(ctx)
	// One 8192-byte source body is split by a fixed 4096-byte copy buffer. The
	// source has already read all cache/origin data before any writer acceptance.
	g.proxy.BufferPool = &provenanceBuffer{}
	go func() { defer close(done); g.ServeHTTP(writer, request) }()
	waitSignal(t, first, "first write")
	now := time.Now().UnixMilli()
	g.sampler.flush(now)
	assertRequestTransfers(t, observer, 0, 0)
	unblockFirst()
	waitSignal(t, second, "second write")
	g.sampler.flush(now + 1)
	assertRequestTransfers(t, observer, 4096, 0)
	g.sampler.flush(now + 2)
	assertRequestTransfers(t, observer, 4096, 0)
	unblockSecond()
	waitSignal(t, done, "delivery completion")
	assertRequestTransfers(t, observer, 8192, 4096)
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.requests) != 1 || observer.requests[0].CachedBytes != 4096 || observer.requests[0].Error != "" {
		t.Fatalf("mixed response accounting = %+v", observer.requests)
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func assertRequestTransfers(t *testing.T, observer *recorder, wantBytes, wantCached int64) {
	t.Helper()
	observer.mu.Lock()
	defer observer.mu.Unlock()
	var bytes, cached int64
	for _, delta := range observer.transfers {
		if delta.Kind != "request" {
			continue
		}
		if delta.CachedBytes < 0 || delta.CachedBytes > delta.ResponseBytes {
			t.Fatalf("invalid provenance delta: %+v", delta)
		}
		bytes += delta.ResponseBytes
		cached += delta.CachedBytes
	}
	if bytes != wantBytes || cached != wantCached {
		t.Fatalf("transfer totals = %d/%d, want %d/%d; events=%+v", bytes, cached, wantBytes, wantCached, observer.transfers)
	}
}

type prefixWriter struct {
	header                      http.Header
	accepted                    int
	short                       bool
	offered, written            int
	first, second               chan struct{}
	releaseFirst, releaseSecond <-chan struct{}
}

func (w *prefixWriter) Header() http.Header { return w.header }
func (w *prefixWriter) WriteHeader(int)     {}
func (w *prefixWriter) Flush()              {}
func (w *prefixWriter) Write(p []byte) (int, error) {
	w.offered += len(p)
	if w.short {
		n := min(w.accepted, len(p))
		w.written += n
		return n, io.ErrClosedPipe
	}
	if w.written == 0 {
		close(w.first)
		<-w.releaseFirst
	} else {
		close(w.second)
		<-w.releaseSecond
	}
	w.written += len(p)
	return len(p), nil
}

type provenanceBuffer struct{}

func (*provenanceBuffer) Get() []byte { return make([]byte, 4096) }
func (*provenanceBuffer) Put([]byte)  {}
