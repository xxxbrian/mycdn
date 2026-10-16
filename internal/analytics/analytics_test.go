package analytics

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var fixtureTime = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func openTestStore(t *testing.T, o Options) *Store {
	t.Helper()
	if o.Dir == "" {
		o.Dir = t.TempDir()
	}
	if o.Now == nil {
		o.Now = func() time.Time { return fixtureTime.Add(day) }
	}
	s, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s.closed.Load() {
			return
		}
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func flushTest(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, s *Store) {
	t.Helper()
	ms := fixtureTime.UnixMilli()
	requests := []RequestEvent{
		{RequestID: "r1", SiteID: "a", Host: "A.EXAMPLE", ClientIP: "192.0.2.1", Path: "/assets/x?secret=discard", Method: "get", ContentType: "video", Status: 200, CacheResult: "MISS", DurationMS: 100, FirstByteMS: 20, ResponseBytes: 9999},
		{RequestID: "r2", SiteID: "a", Host: "a.example", ClientIP: "192.0.2.2", Path: "/assets/x", Method: "GET", ContentType: "video", Status: 200, CacheResult: "COALESCED", DurationMS: 200, FirstByteMS: 30, ResponseBytes: 9999},
		{RequestID: "r3", SiteID: "b", Host: "b.example", ClientIP: "2001:db8::1", Path: "/img/x", Method: "GET", ContentType: "image", Status: 206, CacheResult: "HIT", DurationMS: 900, FirstByteMS: 5, ResponseBytes: 2000, CachedBytes: 2000},
		{RequestID: "r4", SiteID: "a", Host: "a.example", ClientIP: "192.0.3.1", Path: "/literal%_/x", Method: "POST", ContentType: "text", Status: 503, CacheResult: "BYPASS", DurationMS: 1000, Error: "transport failed"},
	}
	for i, e := range requests {
		e.CompletedAt = ms + int64(35+i)*1000
		e.StartedAt = e.CompletedAt - e.DurationMS
		if !s.RecordRequest(e) {
			t.Fatal("request rejected")
		}
	}
	if !s.RecordOrigin(OriginEvent{AttemptID: "o1", SiteID: "a", OriginID: "upstream", Host: "a.example", Path: "/assets/x", Method: "GET", Protocol: "h2", Status: 200, StartedAt: ms + 29700, CompletedAt: ms + 30000, DurationMS: 300, FirstByteMS: 40, ResponseBytes: 9999}) {
		t.Fatal("origin rejected")
	}
	transfers := []TransferEvent{
		{Kind: "request", RequestID: "r1", SiteID: "a", Host: "a.example", ClientIP: "192.0.2.1", Path: "/assets/x", Method: "GET", ContentType: "video", Status: 200, CacheResult: "MISS", IntervalEnd: ms + 2000, ResponseBytes: 600},
		{Kind: "request", RequestID: "r2", SiteID: "a", Host: "a.example", ClientIP: "192.0.2.2", Path: "/assets/x", Method: "GET", ContentType: "video", Status: 200, CacheResult: "COALESCED", IntervalEnd: ms + 5000, ResponseBytes: 400},
		{Kind: "request", RequestID: "r1", SiteID: "a", Host: "a.example", ClientIP: "192.0.2.1", Path: "/assets/x", Method: "GET", ContentType: "video", Status: 200, CacheResult: "MISS", IntervalEnd: ms + 12000, ResponseBytes: 100},
		{Kind: "request", RequestID: "r2", SiteID: "a", Host: "a.example", ClientIP: "192.0.2.2", Path: "/assets/x", Method: "GET", ContentType: "video", Status: 200, CacheResult: "COALESCED", IntervalEnd: ms + 15000, ResponseBytes: 900},
		{Kind: "request", RequestID: "r3", SiteID: "b", Host: "b.example", ClientIP: "2001:db8::1", Path: "/img/x", Method: "GET", ContentType: "image", Status: 206, CacheResult: "HIT", IntervalEnd: ms + 3000, ResponseBytes: 2000, CachedBytes: 2000},
		{Kind: "request", RequestID: "live", SiteID: "a", Host: "a.example", ClientIP: "192.0.2.1", Path: "/stream", Method: "GET", ContentType: "video", Status: 0, CacheResult: "MISS", IntervalEnd: ms + 22000, ResponseBytes: 500},
		{Kind: "origin", AttemptID: "o1", SiteID: "a", OriginID: "upstream", Host: "a.example", Path: "/assets/x", Method: "GET", Protocol: "h2", Status: 200, IntervalEnd: ms + 3000, ResponseBytes: 600},
		{Kind: "origin", AttemptID: "o1", SiteID: "a", OriginID: "upstream", Host: "a.example", Path: "/assets/x", Method: "GET", Protocol: "h2", Status: 200, IntervalEnd: ms + 13000, ResponseBytes: 300},
	}
	for _, e := range transfers {
		e.IntervalStart = e.IntervalEnd - 1000
		if !s.RecordTransfer(e) {
			t.Fatal("transfer rejected")
		}
	}
	flushTest(t, s)
}

func queryTest(t *testing.T, s *Store, f Filters) *Result {
	t.Helper()
	r, err := s.Query(context.Background(), Query{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Minute)), IntervalSeconds: 60, Filters: f, GroupBy: []string{"host", "client_ip", "path", "status", "country", "asn", "cache_result"}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func near(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-8 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFixtureAccountingAndFilters(t *testing.T) {
	s := openTestStore(t, Options{})
	fixture(t, s)
	r := queryTest(t, s, Filters{})
	m := r.Summary
	if m.Requests != 4 || m.OriginAttempts != 1 || m.RequestErrors != 1 || m.OriginErrors != 0 || m.CoalescedRequests != 1 {
		t.Fatalf("counts: %+v", m)
	}
	if m.ResponseBytes != 4500 || m.OriginResponseBytes != 900 || m.CachedBytes != 2000 {
		t.Fatalf("completion bytes double counted or live bytes absent: %+v", m)
	}
	if m.CacheHits != 1 || m.CacheEligibleRequests != 3 {
		t.Fatalf("cache counts: %+v", m)
	}
	near(t, m.CacheHitRate, 1.0/3)
	near(t, m.ByteHitRate, 2000.0/4500)
	near(t, m.ErrorRate, 0.25)
	near(t, m.Latency.P50MS, 200)
	near(t, m.Latency.P95MS, 1000)
	near(t, m.Latency.P99MS, 1000)
	near(t, m.TTFB.P50MS, 20)
	near(t, m.TTFB.P95MS, 30)
	near(t, m.TTFB.P99MS, 30)
	near(t, m.OriginTTFB.P50MS, 40)
	if m.TTFBSamples != 3 || m.OriginTTFBSamples != 1 {
		t.Fatalf("TTFB samples: %+v", m)
	}
	if m.UniqueIPs == nil || *m.UniqueIPs != 4 || !r.Coverage.UniqueIPsAvailable {
		t.Fatalf("raw distinct clients: %+v", r)
	}
	near(t, m.PeakResponseBytesPerSecond, 300)
	near(t, m.PeakOriginResponseBytesPerSecond, 60)
	near(t, m.ResponseBytesPerSecond, 75)
	if len(r.Series) != 1 || r.Series[0].ResponseBytes != 4500 {
		t.Fatalf("series: %+v", r.Series)
	}
	near(t, r.Series[0].TTFB.P50MS, 20)
	if len(r.Geo) != 1 || r.Geo[0].Country != "" || r.Geo[0].Requests != 4 {
		t.Fatalf("unknown geography: %+v", r.Geo)
	}
	if r.Coverage.DataFrom == nil || r.Coverage.DataTo == nil || r.Coverage.Partial {
		t.Fatalf("coverage: %+v", r.Coverage)
	}
	for _, tc := range []struct {
		name                     string
		filters                  Filters
		requests, bytes, origins int64
		peak                     float64
	}{
		{"host", Filters{Hosts: []string{"A.EXAMPLE"}}, 3, 2500, 1, 100},
		{"ip", Filters{ClientIPs: []string{"192.0.2.1"}}, 1, 1200, 0, 60},
		{"cidr", Filters{ClientIPs: []string{"192.0.2.0/24"}}, 2, 2500, 0, 100},
		{"ipv6", Filters{ClientIPs: []string{"2001:db8::/32"}}, 1, 2000, 0, 200},
		{"mapped", Filters{ClientIPs: []string{"::ffff:192.0.2.1"}}, 1, 1200, 0, 60},
		{"method-status", Filters{Methods: []string{"get"}, Statuses: []int{200}}, 2, 2000, 1, 100},
		{"site-content", Filters{SiteIDs: []string{"a"}, ContentTypes: []string{"video"}}, 2, 2500, 0, 100},
		{"cache", Filters{CacheResults: []string{"coalesced"}}, 1, 1300, 0, 90},
		{"country-asn", Filters{Countries: []string{""}, ASNs: []uint32{0}}, 4, 4500, 0, 300},
		{"path", Filters{PathPrefix: "/assets/"}, 2, 2000, 1, 100},
		{"literal-prefix", Filters{PathPrefix: "/literal%_"}, 1, 0, 0, 0},
		{"empty", Filters{Hosts: []string{"' OR true --"}}, 0, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := queryTest(t, s, tc.filters)
			if r.Summary.Requests != tc.requests || r.Summary.ResponseBytes != tc.bytes || r.Summary.OriginAttempts != tc.origins {
				t.Fatalf("filtered: %+v", r.Summary)
			}
			near(t, r.Summary.PeakResponseBytesPerSecond, tc.peak)
			var count, bytes int64
			for _, b := range r.Breakdowns["host"] {
				count += b.Requests
				bytes += b.ResponseBytes
			}
			if count != tc.requests || bytes != tc.bytes {
				t.Fatal("ranking filters differ")
			}
		})
	}
	r, err := s.Query(context.Background(), Query{From: stamp(fixtureTime.Add(10 * time.Second)), To: stamp(fixtureTime.Add(20 * time.Second)), IntervalSeconds: 10, GroupBy: []string{"host"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary.Requests != 0 || r.Summary.ResponseBytes != 1000 || r.Summary.OriginResponseBytes != 300 || len(r.Series) != 1 {
		t.Fatalf("live transfer window: %+v", r)
	}
	near(t, r.Summary.ResponseBytesPerSecond, 100)
	near(t, r.Summary.PeakResponseBytesPerSecond, 100)
	r, err = s.Query(context.Background(), Query{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Minute)), GroupBy: []string{"origin_id", "protocol"}})
	if err != nil {
		t.Fatal(err)
	}
	if rows := r.Breakdowns["origin_id"]; len(rows) != 1 || rows[0].Value != "upstream" || rows[0].OriginAttempts != 1 || rows[0].OriginResponseBytes != 900 || rows[0].Requests != 0 {
		t.Fatalf("physical ranking: %+v", rows)
	}
	if rows := r.Breakdowns["protocol"]; len(rows) != 1 || rows[0].Value != "h2" || rows[0].OriginResponseBytes != 900 || rows[0].OriginAttempts != 1 || rows[0].UniqueIPs != nil {
		t.Fatalf("protocol byte accounting: %+v", rows)
	}
}

func TestDetailsEmptyJSONAndReopen(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, Options{Dir: dir, CountryMMDB: filepath.Join(dir, "absent.mmdb")})
	r := queryTest(t, s, Filters{})
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary.UniqueIPs == nil || *r.Summary.UniqueIPs != 0 {
		t.Fatalf("empty distinct count: %s", data)
	}
	empty := r.Summary
	empty.UniqueIPs = nil
	if empty != (Metrics{}) || len(r.Series) != 0 || r.Coverage.DataFrom != nil || strings.Contains(string(data), `"series":null`) {
		t.Fatalf("empty result: %s", data)
	}
	if s.Status().GeoCountryAvailable || s.Status().GeoError == "" {
		t.Fatal("missing GeoIP not surfaced")
	}
	fixture(t, s)
	d, err := s.Details(context.Background(), DetailsQuery{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Minute)), Filters: Filters{ClientIPs: []string{"192.0.2.0/24"}}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Rows) != 1 || d.Rows[0].RequestID != "r2" || d.Rows[0].FirstByteMS != 30 || !d.HasMore || d.NextOffset == nil || *d.NextOffset != 1 {
		t.Fatalf("details page 1: %+v", d)
	}
	d, err = s.Details(context.Background(), DetailsQuery{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Minute)), Filters: Filters{ClientIPs: []string{"192.0.2.0/24"}}, Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Rows) != 1 || d.Rows[0].RequestID != "r1" || d.Rows[0].Path != "/assets/x" || d.HasMore {
		t.Fatalf("details page 2: %+v", d)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if s.RecordRequest(RequestEvent{StartedAt: 1, CompletedAt: 2}) {
		t.Fatal("closed admitted")
	}
	if err = s.Flush(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	s2 := openTestStore(t, Options{Dir: dir})
	r = queryTest(t, s2, Filters{})
	if r.Summary.Requests != 4 || r.Summary.ResponseBytes != 4500 {
		t.Fatalf("reopen lost history: %+v", r.Summary)
	}
	near(t, r.Summary.TTFB.P50MS, 20)
}

func TestRollupRetentionAndWeightedLatency(t *testing.T) {
	dir := t.TempDir()
	var clock atomic.Int64
	clock.Store(fixtureTime.Add(day).UnixMilli())
	o := Options{Dir: dir, DetailRetention: day, RollupRetention: 4 * day, Now: func() time.Time { return time.UnixMilli(clock.Load()) }}
	s := openTestStore(t, o)
	fixture(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	clock.Store(fixtureTime.Add(3 * day).UnixMilli())
	s = openTestStore(t, o)
	if _, err := os.Stat(filepath.Join(dir, "day-20261015.duckdb")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("raw detail file survives compaction: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "roll-20261015.duckdb")); err != nil {
		t.Fatalf("rollup file absent: %v", err)
	}
	q := Query{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Minute)), GroupBy: []string{"host", "status", "cache_result", "country", "asn"}, IntervalSeconds: 60}
	r, err := s.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Coverage.UsesRollups || !r.Coverage.LatencyApproximate || r.Summary.Requests != 4 || r.Summary.ResponseBytes != 4500 || r.Summary.OriginAttempts != 1 {
		t.Fatalf("rollup: %+v", r)
	}
	if r.Summary.UniqueIPs != nil || r.Coverage.UniqueIPsAvailable {
		t.Fatalf("rolled clients fabricated: %+v", r)
	}
	if r.Summary.TTFB.P50MS < 20 || r.Summary.TTFB.P50MS > 20*math.Pow(2, 1.0/16) || r.Summary.OriginTTFB.P95MS < 40 || r.Summary.OriginTTFB.P95MS > 40*math.Pow(2, 1.0/16) || r.Summary.TTFBSamples != 3 || r.Summary.OriginTTFBSamples != 1 {
		t.Fatalf("rolled TTFB: %+v", r.Summary)
	}
	if r.Summary.Latency.P50MS < 200 || r.Summary.Latency.P50MS > 209 || r.Summary.Latency.P95MS < 1000 || r.Summary.Latency.P95MS > 1045 {
		t.Fatalf("weighted rollup percentile: %+v", r.Summary.Latency)
	}
	near(t, r.Summary.PeakResponseBytesPerSecond, 300)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	// Increasing retention cannot recreate discarded IP/path detail.
	o.DetailRetention = 4 * day
	s = openTestStore(t, o)
	q.Filters.ClientIPs = []string{"192.0.2.1"}
	if _, err = s.Query(context.Background(), q); !errors.Is(err, ErrDetailRetention) {
		t.Fatalf("silent partial IP history: %v", err)
	}
	if _, err = s.Details(context.Background(), DetailsQuery{From: q.From, To: q.To}); !errors.Is(err, ErrDetailRetention) {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	clock.Store(fixtureTime.Add(6 * day).UnixMilli())
	s = openTestStore(t, o)
	q.Filters = Filters{}
	r, err = s.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary.Requests != 0 || !r.Coverage.Partial {
		t.Fatalf("retention: %+v", r)
	}
	if r.Summary.UniqueIPs != nil || r.Coverage.UniqueIPsAvailable {
		t.Fatal("expired IP history presented as zero clients")
	}
	if _, err = os.Stat(filepath.Join(dir, "day-20261015.duckdb")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired file persists: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "roll-20261015.duckdb")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired rollup file persists: %v", err)
	}
}

func TestBoundedAdmissionConcurrencyAndCancellation(t *testing.T) {
	s := openTestStore(t, Options{QueueCapacity: 4, BatchSize: 1, QueryConcurrency: 1, QueryTimeout: time.Second})
	// Holding the catalog simulates a long query, not a slow proxy producer.
	s.catalog.Lock()
	for i := 0; i < 100; i++ {
		s.RecordRequest(RequestEvent{StartedAt: fixtureTime.UnixMilli(), CompletedAt: fixtureTime.UnixMilli() + 1})
	}
	if s.Status().Dropped.Queue == 0 {
		t.Fatal("queue was unbounded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Flush(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.catalog.Unlock()
	flushTest(t, s)
	_, _, cleanup, err := s.queryConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Query(context.Background(), Query{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Minute))})
	cleanup()
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrency: %v", err)
	}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Go(func() {
			for i := 0; i < 100; i++ {
				s.RecordTransfer(TransferEvent{Kind: "request", IntervalStart: fixtureTime.UnixMilli(), IntervalEnd: fixtureTime.UnixMilli() + 2, ResponseBytes: 1})
				_ = s.Status()
			}
		})
	}
	wg.Wait()
	flushTest(t, s)
	status := s.Status()
	if status.Ingested != status.Accepted {
		t.Fatalf("accepted events not drained: %+v", status)
	}
}

func TestCloseDrainsAcceptedEventsDuringConcurrentAdmission(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, Options{Dir: dir, QueueCapacity: 256, BatchSize: 32})
	if !s.RecordRequest(RequestEvent{StartedAt: fixtureTime.UnixMilli(), CompletedAt: fixtureTime.UnixMilli() + 1}) {
		t.Fatal("initial request")
	}
	var producers sync.WaitGroup
	for n := 0; n < 4; n++ {
		producers.Go(func() {
			for i := 0; i < 200; i++ {
				s.RecordRequest(RequestEvent{StartedAt: fixtureTime.UnixMilli(), CompletedAt: fixtureTime.UnixMilli() + 1})
			}
		})
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	producers.Wait()
	status := s.Status()
	if status.Accepted != status.Ingested || status.PendingEvents != 0 {
		t.Fatalf("close did not drain: %+v", status)
	}
	s2 := openTestStore(t, Options{Dir: dir})
	r := queryTest(t, s2, Filters{})
	if r.Summary.Requests != int64(status.Accepted) {
		t.Fatalf("close did not persist: %d != %d", r.Summary.Requests, status.Accepted)
	}
}

func TestValidation(t *testing.T) {
	s := openTestStore(t, Options{})
	for _, q := range []Query{
		{From: "bad", To: stamp(fixtureTime)},
		{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Minute)), GroupBy: []string{"host;DROP TABLE x"}},
		{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Minute)), Filters: Filters{ClientIPs: []string{"not an IP"}}},
		{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Minute)), IntervalSeconds: 11},
	} {
		var invalid *ValidationError
		if _, err := s.Query(context.Background(), q); !errors.As(err, &invalid) {
			t.Fatalf("expected validation: %v", err)
		}
	}
	if s.RecordTransfer(TransferEvent{Kind: "other", IntervalEnd: 1}) {
		t.Fatal("invalid transfer admitted")
	}
	if s.Status().Dropped.Invalid != 1 {
		t.Fatal("invalid drop not counted")
	}
}

func TestDiskEvictionPressureAndCoverageFence(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, Options{Dir: dir, DiskBudgetBytes: 32 << 20, Now: func() time.Time { return fixtureTime.Add(time.Hour) }})
	for _, at := range []time.Time{fixtureTime.Add(-day), fixtureTime} {
		if !s.RecordRequest(RequestEvent{StartedAt: at.UnixMilli(), CompletedAt: at.UnixMilli() + 100, Host: "a.example"}) {
			t.Fatal("not admitted")
		}
	}
	flushTest(t, s)
	// An unrelated local allocation represents pressure from the newest partition.
	// Sparse length is enough because admission deliberately uses logical file size.
	fill := filepath.Join(dir, "pressure.fixture")
	f, err := os.Create(fill)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(26 << 20); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	flushTest(t, s)
	status := s.Status()
	if status.DiskEvictedBefore == nil || !status.StoragePressure {
		t.Fatalf("disk pressure: %+v", status)
	}
	if status.DetailFrom != *status.DiskEvictedBefore {
		t.Fatalf("capacity fence absent from actual detail horizon: %+v", status)
	}
	if _, err = os.Stat(filepath.Join(dir, "day-20261014.duckdb")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old partition not removed: %v", err)
	}
	if !s.RecordRequest(RequestEvent{StartedAt: fixtureTime.UnixMilli(), CompletedAt: fixtureTime.UnixMilli() + 200}) {
		t.Fatal("admission should be async")
	}
	if err = s.Flush(context.Background()); !errors.Is(err, ErrStoragePressure) {
		t.Fatalf("missing pressure error: %v", err)
	}
	if s.Status().Dropped.Storage != 1 {
		t.Fatal("missing storage drop")
	}
	if err = s.Flush(context.Background()); !errors.Is(err, ErrStoragePressure) {
		t.Fatalf("lost error was not sticky: %v", err)
	}
	if err = os.Remove(fill); err != nil {
		t.Fatal(err)
	}
	// An admitted loss must remain visible to shutdown even after pressure clears.
	if err = s.Close(); !errors.Is(err, ErrStoragePressure) {
		t.Fatal(err)
	}
	s2 := openTestStore(t, Options{Dir: dir, DiskBudgetBytes: 32 << 20})
	q := Query{From: stamp(fixtureTime.Add(-day)), To: stamp(fixtureTime.Add(time.Minute)), GroupBy: []string{"host"}}
	r, err := s2.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary.Requests != 1 || !r.Coverage.Partial || r.Coverage.DiskEvictedBefore == nil {
		t.Fatalf("coverage after reopen: %+v", r)
	}
}

func TestMixedRawRollupWeightedPopulation(t *testing.T) {
	var clock atomic.Int64
	clock.Store(fixtureTime.Add(day).UnixMilli())
	s := openTestStore(t, Options{DetailRetention: day, RollupRetention: 4 * day, Now: func() time.Time { return time.UnixMilli(clock.Load()) }})
	// A large fast population and a tiny slow population must not become an
	// average of their percentiles, and peaks on separate days must not be added.
	for i := 0; i < 99; i++ {
		if !s.RecordRequest(RequestEvent{RequestID: "old", Host: "a.example", ClientIP: "192.0.2.1", StartedAt: fixtureTime.UnixMilli(), CompletedAt: fixtureTime.UnixMilli() + 1000, DurationMS: 1000, FirstByteMS: 1, CacheResult: "HIT"}) {
			t.Fatal("old admission")
		}
	}
	if !s.RecordOrigin(OriginEvent{AttemptID: "old", Host: "a.example", StartedAt: fixtureTime.UnixMilli(), CompletedAt: fixtureTime.UnixMilli() + 2000, DurationMS: 2000, FirstByteMS: 3}) {
		t.Fatal("old origin")
	}
	if !s.RecordOrigin(OriginEvent{AttemptID: "old-2", Host: "a.example", StartedAt: fixtureTime.UnixMilli(), CompletedAt: fixtureTime.UnixMilli() + 2000, DurationMS: 2000, FirstByteMS: 3}) {
		t.Fatal("old origin")
	}
	if !s.RecordTransfer(TransferEvent{Kind: "request", Host: "a.example", IntervalStart: fixtureTime.UnixMilli(), IntervalEnd: fixtureTime.UnixMilli() + 2, ResponseBytes: 1000, CachedBytes: 1000}) {
		t.Fatal("old traffic")
	}
	flushTest(t, s)
	clock.Store(fixtureTime.Add(3 * day).UnixMilli())
	flushTest(t, s)
	newTime := fixtureTime.Add(2 * day)
	if !s.RecordRequest(RequestEvent{RequestID: "new", Host: "a.example", ClientIP: "192.0.2.2", StartedAt: newTime.UnixMilli(), CompletedAt: newTime.UnixMilli() + 1000, DurationMS: 1000, FirstByteMS: 1000, CacheResult: "MISS"}) {
		t.Fatal("new admission")
	}
	if !s.RecordOrigin(OriginEvent{AttemptID: "new", Host: "a.example", StartedAt: newTime.UnixMilli(), CompletedAt: newTime.UnixMilli() + 2000, DurationMS: 2000, FirstByteMS: 1900}) {
		t.Fatal("new origin")
	}
	if !s.RecordTransfer(TransferEvent{Kind: "request", Host: "a.example", IntervalStart: newTime.UnixMilli(), IntervalEnd: newTime.UnixMilli() + 2, ResponseBytes: 1000}) {
		t.Fatal("new traffic")
	}
	flushTest(t, s)
	r, err := s.Query(context.Background(), Query{From: stamp(fixtureTime), To: stamp(newTime.Add(time.Minute)), GroupBy: []string{"host"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary.Requests != 100 || r.Summary.ResponseBytes != 2000 || !r.Coverage.UsesRollups {
		t.Fatalf("mixed sources: %+v", r)
	}
	if r.Summary.Latency.P50MS < 1000 || r.Summary.Latency.P50MS > 1045 {
		t.Fatalf("duration changed to TTFB: %+v", r.Summary.Latency)
	}
	near(t, r.Summary.TTFB.P50MS, 1)
	near(t, r.Summary.TTFB.P95MS, 1)
	near(t, r.Summary.TTFB.P99MS, 1)
	if r.Summary.TTFBSamples != 100 || r.Summary.OriginTTFBSamples != 3 || r.Summary.OriginTTFB.P50MS < 3 || r.Summary.OriginTTFB.P50MS > 3*math.Pow(2, 1.0/16) {
		t.Fatalf("mixed weighted first byte: %+v", r.Summary)
	}
	near(t, r.Summary.OriginTTFB.P95MS, 1900)
	if r.Summary.UniqueIPs != nil || r.Coverage.UniqueIPsAvailable {
		t.Fatal("partial raw distinct clients presented as full history")
	}
	near(t, r.Summary.CacheHitRate, 0.99)
	near(t, r.Summary.ByteHitRate, 0.5)
	near(t, r.Summary.PeakResponseBytesPerSecond, 100)
	for _, row := range r.Series {
		if row.UniqueIPs != nil {
			t.Fatal("mixed query series claims full IP history")
		}
	}
	for _, row := range r.Breakdowns["host"] {
		if row.UniqueIPs != nil {
			t.Fatal("mixed ranking claims full IP history")
		}
	}
}

func TestLocalMMDBEnrichment(t *testing.T) {
	s := openTestStore(t, Options{CountryMMDB: "testdata/GeoIP2-Country-Test.mmdb", ASNMMDB: "testdata/GeoLite2-ASN-Test.mmdb"})
	if !s.Status().GeoCountryAvailable || !s.Status().GeoASNAvailable || s.Status().GeoError != "" {
		t.Fatalf("GeoIP open: %+v", s.Status())
	}
	for _, ip := range []string{"2001:218::1", "1.128.0.1"} {
		at := fixtureTime.UnixMilli()
		if !s.RecordRequest(RequestEvent{RequestID: ip, Host: "geo.example", ClientIP: ip, StartedAt: at, CompletedAt: at + 100, DurationMS: 100}) {
			t.Fatal("request")
		}
		if !s.RecordTransfer(TransferEvent{Kind: "request", Host: "geo.example", ClientIP: ip, IntervalStart: at, IntervalEnd: at + 200, ResponseBytes: 500}) {
			t.Fatal("traffic")
		}
	}
	flushTest(t, s)
	r := queryTest(t, s, Filters{Countries: []string{"jp"}})
	if r.Summary.Requests != 1 || r.Summary.ResponseBytes != 500 || len(r.Geo) != 1 || r.Geo[0].Country != "JP" {
		t.Fatalf("country: %+v", r)
	}
	r = queryTest(t, s, Filters{ASNs: []uint32{1221}})
	if r.Summary.Requests != 1 || r.Summary.ResponseBytes != 500 {
		t.Fatalf("ASN: %+v", r)
	}
	d, err := s.Details(context.Background(), DetailsQuery{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Minute)), Filters: Filters{Countries: []string{"JP"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Rows) != 1 || d.Rows[0].Country != "JP" || d.Rows[0].ClientIP != "2001:218::1" {
		t.Fatalf("detail enrichment: %+v", d)
	}
}

func TestQueryDeadlineIncludesWaitingAndInterruptsSQL(t *testing.T) {
	s := openTestStore(t, Options{QueryTimeout: 30 * time.Millisecond})
	s.catalog.Lock()
	_, err := s.Query(context.Background(), Query{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Minute))})
	s.catalog.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait escaped deadline: %v", err)
	}
	conn, ctx, cleanup, err := s.queryConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var total float64
	err = conn.QueryRowContext(ctx, "SELECT sum(sin(i::DOUBLE)) FROM range(1000000000000) AS r(i)").Scan(&total)
	cleanup()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("native SQL was not interrupted: %v", err)
	}
}

func TestCapacityProtectionExpiresAtNextDay(t *testing.T) {
	var clock atomic.Int64
	clock.Store(fixtureTime.UnixMilli())
	s := openTestStore(t, Options{DiskBudgetBytes: 32 << 20, Now: func() time.Time { return time.UnixMilli(clock.Load()) }})
	if !s.RecordRequest(RequestEvent{StartedAt: fixtureTime.UnixMilli(), CompletedAt: fixtureTime.UnixMilli() + 1}) {
		t.Fatal("request")
	}
	flushTest(t, s)
	// An incompressible diagnostic column makes a valid oversized current file.
	payload := make([]byte, 28_000_000)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	s.catalog.Lock()
	p := s.parts["20261015"]
	_, err := s.db.Exec("ALTER TABLE " + p.alias + ".events ADD COLUMN pressure BLOB")
	if err == nil {
		_, err = s.db.Exec("UPDATE "+p.alias+".events SET pressure=?", payload)
	}
	s.catalog.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	flushTest(t, s)
	if !s.Status().StoragePressure {
		t.Fatal("current partition was not protected")
	}
	clock.Store(fixtureTime.Add(day).UnixMilli())
	flushTest(t, s)
	if s.Status().StoragePressure || s.Status().DiskEvictedBefore == nil {
		t.Fatalf("old oversized partition stalled forever: %+v", s.Status())
	}
	if !s.RecordRequest(RequestEvent{StartedAt: clock.Load(), CompletedAt: clock.Load() + 1}) {
		t.Fatal("new admission")
	}
	flushTest(t, s)
	if s.Status().Ingested != 2 {
		t.Fatal("did not resume")
	}
}

func TestTransportFailureCountsWithoutHTTPStatus(t *testing.T) {
	s := openTestStore(t, Options{})
	at := fixtureTime.UnixMilli()
	if !s.RecordRequest(RequestEvent{Host: "failure.example", StartedAt: at, CompletedAt: at + 100, DurationMS: 100, Error: "connection reset", CacheResult: "BYPASS"}) {
		t.Fatal("request")
	}
	if !s.RecordOrigin(OriginEvent{Host: "failure.example", StartedAt: at, CompletedAt: at + 90, DurationMS: 90, Error: "dial timeout"}) {
		t.Fatal("origin")
	}
	flushTest(t, s)
	r := queryTest(t, s, Filters{Statuses: []int{0}})
	if r.Summary.RequestErrors != 1 || r.Summary.OriginErrors != 1 || r.Summary.OriginAttempts != 1 || r.Summary.Status5xx != 0 {
		t.Fatalf("transport failure: %+v", r.Summary)
	}
	near(t, r.Summary.CacheHitRate, 0)
	near(t, r.Summary.ErrorRate, 1)
	if r.Summary.TTFBSamples != 0 || r.Summary.OriginTTFBSamples != 0 || r.Summary.TTFB != (Percentiles{}) || r.Summary.OriginTTFB != (Percentiles{}) {
		t.Fatal("missing response measured as TTFB")
	}
}

func TestUniqueIPsExactAcrossWindowsDimensionsAndAliases(t *testing.T) {
	s := openTestStore(t, Options{})
	at := fixtureTime.UnixMilli()
	for _, e := range []RequestEvent{
		{RequestID: "a", Host: "a.example", ClientIP: "192.0.2.1", CompletedAt: at + 1000},
		{RequestID: "b", Host: "a.example", ClientIP: "::ffff:192.0.2.1", CompletedAt: at + 2000},
		{RequestID: "c", Host: "b.example", ClientIP: "192.0.2.1", CompletedAt: at + 11000},
		{RequestID: "d", Host: "b.example", ClientIP: "2001:db8::1", CompletedAt: at + 12000},
		{RequestID: "unknown", Host: "a.example", CompletedAt: at + 13000},
	} {
		e.StartedAt = at
		if !s.RecordRequest(e) {
			t.Fatal("request")
		}
	}
	if !s.RecordTransfer(TransferEvent{Kind: "request", Host: "a.example", ClientIP: "192.0.2.99", IntervalStart: at, IntervalEnd: at + 1000, ResponseBytes: 1}) {
		t.Fatal("live traffic")
	}
	flushTest(t, s)
	r, err := s.Query(context.Background(), Query{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Minute)), IntervalSeconds: 10, GroupBy: []string{"host", "client_ip", "country"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary.UniqueIPs == nil || *r.Summary.UniqueIPs != 2 || !r.Coverage.UniqueIPsAvailable {
		t.Fatalf("summary clients: %+v", r.Summary)
	}
	if len(r.Series) != 2 || r.Series[0].UniqueIPs == nil || *r.Series[0].UniqueIPs != 1 || r.Series[1].UniqueIPs == nil || *r.Series[1].UniqueIPs != 2 {
		t.Fatalf("bucket clients: %+v", r.Series)
	}
	for _, row := range r.Breakdowns["host"] {
		want := int64(1)
		if row.Value == "b.example" {
			want = 2
		}
		if row.UniqueIPs == nil || *row.UniqueIPs != want {
			t.Fatalf("host clients: %+v", row)
		}
	}
	if len(r.Geo) != 1 || r.Geo[0].UniqueIPs == nil || *r.Geo[0].UniqueIPs != 2 {
		t.Fatalf("geo clients: %+v", r.Geo)
	}
	filtered := queryTest(t, s, Filters{ClientIPs: []string{"192.0.2.1"}})
	if filtered.Summary.Requests != 3 || filtered.Summary.UniqueIPs == nil || *filtered.Summary.UniqueIPs != 1 {
		t.Fatalf("filtered clients: %+v", filtered.Summary)
	}
}

func TestPreTTFBHistoryUpgradesWithoutInventingMeasurements(t *testing.T) {
	for _, rolled := range []bool{false, true} {
		t.Run(fmt.Sprintf("rolled=%t", rolled), func(t *testing.T) {
			dir := t.TempDir()
			o := Options{Dir: dir, Now: func() time.Time { return fixtureTime.Add(day) }}
			s := openTestStore(t, o)
			fixture(t, s)
			if rolled {
				s.catalog.Lock()
				err := s.compact(context.Background(), s.parts["20261015"])
				s.catalog.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			s.catalog.Lock()
			p := s.parts["20261015"]
			if rolled {
				// Recreate the earlier joint-bin shape, rather than merely dropping
				// columns from independent histogram rows whose counters are zero.
				tx, err := s.db.Begin()
				if err != nil {
					s.catalog.Unlock()
					t.Fatal(err)
				}
				_, err = tx.Exec("CREATE TABLE " + p.alias + ".legacy AS SELECT * EXCLUDE (latency_n,origin_latency_n) FROM " + p.alias + ".rollup WHERE n>0 OR onum>0 OR rout>0 OR oout>0")
				if err == nil {
					_, err = tx.Exec("UPDATE " + p.alias + ".legacy SET latency_value=CASE WHEN n>0 THEN duration/n ELSE -1 END,origin_latency_value=CASE WHEN onum>0 THEN oduration/onum ELSE -1 END")
				}
				if err == nil {
					_, err = tx.Exec("DROP TABLE " + p.alias + ".rollup")
				}
				if err == nil {
					_, err = tx.Exec("ALTER TABLE " + p.alias + ".legacy RENAME TO rollup")
				}
				if err == nil {
					err = tx.Commit()
				} else {
					_ = tx.Rollback()
				}
				if err != nil {
					s.catalog.Unlock()
					t.Fatal(err)
				}
			}
			table, columns := "events", []string{"first_byte_ms"}
			if rolled {
				table, columns = "rollup", []string{"ttfb_n", "origin_ttfb_n", "ttfb_value", "origin_ttfb_value"}
			}
			for _, column := range columns {
				if _, err := s.db.Exec("ALTER TABLE " + p.alias + "." + table + " DROP COLUMN " + column); err != nil {
					s.catalog.Unlock()
					t.Fatal(err)
				}
			}
			if _, err := s.db.Exec("ALTER TABLE catalog.empty_events DROP COLUMN first_byte_ms"); err != nil {
				s.catalog.Unlock()
				t.Fatal(err)
			}
			s.catalog.Unlock()
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s = openTestStore(t, o)
			q := Query{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Minute)), GroupBy: []string{"host"}}
			r, err := s.Query(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			if r.Summary.Requests != 4 || r.Summary.ResponseBytes != 4500 || r.Summary.OriginAttempts != 1 || r.Summary.TTFBSamples != 0 || r.Summary.OriginTTFBSamples != 0 || r.Summary.TTFB != (Percentiles{}) {
				t.Fatalf("historical measurement invented: %+v", r.Summary)
			}
			if rolled {
				if r.Summary.Latency.P50MS != 200 || r.Summary.Latency.P95MS != 1000 || r.Summary.OriginLatency.P50MS != 300 {
					t.Fatalf("legacy duration weights lost: %+v", r.Summary)
				}
				var counts int64
				if err = s.db.QueryRow("SELECT sum(latency_n)+sum(origin_latency_n) FROM d20261015.rollup").Scan(&counts); err != nil || counts != 5 {
					t.Fatalf("legacy sample conversion: %d %v", counts, err)
				}
				return
			}
			if !s.RecordRequest(RequestEvent{RequestID: "new", Host: "a.example", StartedAt: fixtureTime.UnixMilli(), CompletedAt: fixtureTime.UnixMilli() + 500, DurationMS: 500, FirstByteMS: 7}) {
				t.Fatal("append after migration")
			}
			flushTest(t, s)
			r, err = s.Query(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			if r.Summary.Requests != 5 || r.Summary.TTFBSamples != 1 {
				t.Fatalf("migration append: %+v", r.Summary)
			}
			near(t, r.Summary.TTFB.P50MS, 7)
			d, err := s.Details(context.Background(), DetailsQuery{From: q.From, To: q.To})
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range d.Rows {
				if row.RequestID != "new" && row.FirstByteMS != 0 {
					t.Fatal("historical detail first byte fabricated")
				}
			}
		})
	}
}
