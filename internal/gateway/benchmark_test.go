package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mycdn/internal/cache"
	"mycdn/internal/config"
)

// BenchmarkCacheHit includes constructing a fresh request, routing/planning,
// cache lookup and delivery through ServeHTTP. It excludes network/client cost,
// origin acquisition, analytics, and response buffering.
func BenchmarkCacheHit(b *testing.B) {
	b.StopTimer()
	payload := strings.Repeat("x", 1024)
	var calls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, payload)
	}))
	defer origin.Close()
	objects, err := cache.Open(cache.Options{Dir: b.TempDir(), MaxBytes: 16 << 20, MaxObjectBytes: 8 << 20, InlineBytes: 32 << 10})
	if err != nil {
		b.Fatal(err)
	}
	defer objects.Close()
	store, err := config.NewStore(filepath.Join(b.TempDir(), "config.json"))
	if err != nil {
		b.Fatal(err)
	}
	document := config.Default()
	document.Sites = []config.Site{{ID: "bench", Enabled: true, Domains: []string{"cdn.test"}, Origins: []config.Origin{{ID: "origin", URL: origin.URL}}, DefaultOrigin: "origin"}}
	document.Defaults.Cache.Mode = "force"
	document.Defaults.Cache.TTLSeconds = 3600
	if err := store.Save(document); err != nil {
		b.Fatal(err)
	}
	gateway := New(store, objects, nil)
	defer gateway.Close()
	writer := &benchmarkWriter{header: make(http.Header)}
	request := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://cdn.test/asset.bin?v=1", nil)
		r.RemoteAddr = "192.0.2.1:1234"
		return r
	}
	gateway.ServeHTTP(writer, request())
	if writer.status != 200 || writer.bytes != int64(len(payload)) {
		b.Fatalf("warm response: status=%d bytes=%d", writer.status, writer.bytes)
	}
	deadline := time.Now().Add(5 * time.Second)
	for objects.Stats().ActiveFills != 0 {
		if time.Now().After(deadline) {
			b.Fatal("warm fill did not publish")
		}
		time.Sleep(time.Millisecond)
	}
	entries, err := objects.Entries(context.Background(), cache.Filter{SiteID: "bench"}, 1)
	if err != nil || len(entries) != 1 || !entries[0].Complete || entries[0].Size != int64(len(payload)) {
		b.Fatalf("warm cache entry: %+v, %v", entries, err)
	}
	// Prime the published-reader path too; timed operations must all be HITs.
	clear(writer.header)
	writer.status = 0
	gateway.ServeHTTP(writer, request())
	before := objects.Stats()
	if calls.Load() != 1 || before.Hits != 1 {
		b.Fatalf("warm lookup was not a HIT: calls=%d stats=%+v", calls.Load(), before)
	}
	writer.bytes = 0
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	b.StartTimer()
	for range b.N {
		clear(writer.header)
		writer.status = 0
		gateway.ServeHTTP(writer, request())
	}
	b.StopTimer()
	if calls.Load() != 1 || objects.Stats().Hits-before.Hits != int64(b.N) {
		b.Fatalf("timed requests performed non-HIT work: calls=%d stats=%+v", calls.Load(), objects.Stats())
	}
	if writer.status != 200 || writer.bytes != int64(b.N)*int64(len(payload)) {
		b.Fatalf("timed delivery: status=%d bytes=%d", writer.status, writer.bytes)
	}
}

type benchmarkWriter struct {
	header http.Header
	status int
	bytes  int64
}

func (w *benchmarkWriter) Header() http.Header         { return w.header }
func (w *benchmarkWriter) WriteHeader(status int)      { w.status = status }
func (w *benchmarkWriter) Write(p []byte) (int, error) { w.bytes += int64(len(p)); return len(p), nil }
func (w *benchmarkWriter) Flush()                      {}
