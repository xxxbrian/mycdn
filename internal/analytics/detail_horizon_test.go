package analytics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestDetailHorizonSurvivesRetentionIncreaseAndRollupExpiry(t *testing.T) {
	var clock atomic.Int64
	clock.Store(fixtureTime.Add(day).UnixMilli()) // October 16.
	dir := t.TempDir()
	o := Options{Dir: dir, DetailRetention: 14 * day, Now: func() time.Time { return time.UnixMilli(clock.Load()) }}
	s := openTestStore(t, o)
	if got, want := s.Status().DetailFrom, "2026-10-02T00:00:00Z"; got != want {
		t.Fatalf("ordinary 14-day horizon: %s != %s", got, want)
	}
	fixture(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	clock.Store(fixtureTime.Add(3 * day).UnixMilli()) // October 18.
	o.DetailRetention = day
	s = openTestStore(t, o)
	if !s.parts["20261015"].roll {
		t.Fatal("fixture did not actually roll October 15")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-fence directory: Open must reconstruct and persist from
	// actual rollup files, not only trust the newly introduced state field.
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"version":1,"evicted_before":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	o.DetailRetention = 14 * day
	s = openTestStore(t, o)
	const actual = "2026-10-16T00:00:00Z"
	// Status must remain callable while the catalog owner is doing maintenance.
	s.catalog.Lock()
	status := s.Status()
	s.catalog.Unlock()
	if status.DetailFrom != actual {
		t.Fatalf("advertised configured floor instead of actual history: %+v", status)
	}
	if !s.RecordRequest(RequestEvent{RequestID: "recent", Host: "a.example", ClientIP: "192.0.2.1", Path: "/recent", StartedAt: clock.Load(), CompletedAt: clock.Load() + 1}) {
		t.Fatal("recent request")
	}
	flushTest(t, s)
	to := stamp(time.UnixMilli(clock.Load()).Add(time.Hour))
	q := Query{From: stamp(fixtureTime), To: to, GroupBy: []string{"host"}}
	low, err := s.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if low.Summary.Requests != 5 || !low.Coverage.UsesRollups || low.Coverage.DetailFrom != actual {
		t.Fatalf("historical low-cardinality query: %+v", low)
	}
	q.From = status.DetailFrom
	q.GroupBy = []string{"client_ip", "path"}
	q.Filters.ClientIPs = []string{"192.0.2.1"}
	r, err := s.Query(context.Background(), q)
	if err != nil {
		t.Fatalf("UI-advertised detail window rejected: %v", err)
	}
	if r.Summary.Requests != 1 || r.Coverage.UsesRollups || r.Coverage.DetailFrom != actual || !r.Coverage.UniqueIPsAvailable {
		t.Fatalf("actual detail coverage: %+v", r)
	}
	d, err := s.Details(context.Background(), DetailsQuery{From: status.DetailFrom, To: to})
	if err != nil || len(d.Rows) != 1 || d.Rows[0].RequestID != "recent" || d.Coverage.DetailFrom != actual {
		t.Fatalf("advertised details window: %+v %v", d, err)
	}
	q.From = stamp(fixtureTime)
	if _, err = s.Query(context.Background(), q); !errors.Is(err, ErrDetailRetention) {
		t.Fatalf("rolled IP query unexpectedly available: %v", err)
	}
	if _, err = s.Details(context.Background(), DetailsQuery{From: q.From, To: to}); !errors.Is(err, ErrDetailRetention) {
		t.Fatalf("rolled details unexpectedly available: %v", err)
	}

	// Removing the last rolled file must not erase knowledge of lost detail.
	s.catalog.Lock()
	err = s.removePartition(context.Background(), s.parts["20261015"], false)
	s.catalog.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, o)
	if s.Status().DetailFrom != actual {
		t.Fatalf("deleted rollup erased durable horizon: %+v", s.Status())
	}
	if _, err = s.Details(context.Background(), DetailsQuery{From: stamp(fixtureTime), To: to}); !errors.Is(err, ErrDetailRetention) {
		t.Fatal("deleted detail advertised as available")
	}
}
