package admin

import (
	"fmt"
	"io"
	"net/http"
	"runtime"

	"mycdn/internal/cache"
)

// The built-in endpoint is intentionally small and low-cardinality. Access
// analysis lives in DuckDB, not in a label set per IP or URL.
func metrics(store *cache.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		snapshot := store.Stats()
		writeMetric(w, "mycdn_go_goroutines", "gauge", "Current Go goroutines.", runtime.NumGoroutine())
		writeMetric(w, "mycdn_go_heap_bytes", "gauge", "Allocated Go heap bytes.", memory.HeapAlloc)
		writeMetric(w, "mycdn_cache_entries", "gauge", "Published cache entries.", snapshot.Entries)
		writeMetric(w, "mycdn_cache_bytes", "gauge", "Cache body storage bytes.", snapshot.Bytes)
		writeMetric(w, "mycdn_cache_active_fills", "gauge", "Active shared acquisitions.", snapshot.ActiveFills)
		writeMetric(w, "mycdn_cache_readers", "gauge", "Active cached body readers.", snapshot.Readers)
		writeMetric(w, "mycdn_cache_hits_total", "counter", "Cache hits since startup.", snapshot.Hits)
		writeMetric(w, "mycdn_cache_misses_total", "counter", "Cache misses since startup.", snapshot.Misses)
		writeMetric(w, "mycdn_cache_evictions_total", "counter", "Evictions since startup.", snapshot.Evictions)
	})
}

func writeMetric(w io.Writer, name, kind, help string, value any) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", name, help, name, kind, name, value)
}
