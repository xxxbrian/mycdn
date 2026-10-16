package analytics

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func fixturePlan(t *testing.T) queryPlan {
	t.Helper()
	p, err := plan(stamp(fixtureTime), stamp(fixtureTime.Add(time.Minute)), Filters{})
	if err != nil {
		t.Fatal(err)
	}
	p.interval = 10
	return p
}

func TestPrivateSnapshotReleasesCollectorAndPinsCoverage(t *testing.T) {
	var clock atomic.Int64
	clock.Store(fixtureTime.Add(day).UnixMilli())
	s := openTestStore(t, Options{QueryConcurrency: 1, DetailRetention: day, RollupRetention: 4 * day, Now: func() time.Time { return time.UnixMilli(clock.Load()) }})
	fixture(t, s)
	snap, err := s.snapshot(context.Background(), fixturePlan(t), false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.close()
	captured := snap.coverage
	if !s.RecordRequest(RequestEvent{RequestID: "later", Host: "later.example", StartedAt: fixtureTime.UnixMilli(), CompletedAt: fixtureTime.UnixMilli() + 59_000}) {
		t.Fatal("late request rejected")
	}
	// This barrier cannot complete if the snapshot still holds catalog.RLock.
	flushTest(t, s)
	if s.Status().Ingested != 14 {
		t.Fatalf("collector stalled: %+v", s.Status())
	}
	if s.RecordTransfer(TransferEvent{Kind: "invalid", IntervalEnd: 1}) {
		t.Fatal("invalid transfer admitted")
	}
	// Roll/removal of the original source also proceeds with the query active.
	clock.Store(fixtureTime.Add(3 * day).UnixMilli())
	flushTest(t, s)
	rows, err := aggregate(snap.ctx, snap.conn, "''", allEvents, snap.plan.uniqueIPs, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Requests != 4 || rows[0].ResponseBytes != 4500 {
		t.Fatalf("snapshot read live events: %+v", rows)
	}
	if !reflect.DeepEqual(captured, snap.coverage) || snap.coverage.UsesRollups || snap.coverage.Dropped.Invalid != 0 || s.Status().Dropped.Invalid != 1 || *snap.coverage.DataTo != stamp(fixtureTime.Add(38*time.Second)) {
		t.Fatalf("snapshot coverage changed: %+v", snap.coverage)
	}
}

func TestCanceledSnapshotCleansConnectionBeforeReuse(t *testing.T) {
	s := openTestStore(t, Options{QueryConcurrency: 1})
	fixture(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	snap, err := s.snapshot(ctx, fixturePlan(t), false, false)
	if err != nil {
		t.Fatal(err)
	}
	var native any
	if err = snap.conn.Raw(func(c any) error { native = c; return nil }); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err = aggregate(snap.ctx, snap.conn, "''", allEvents, true, 1, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query: %v", err)
	}
	snap.close()
	conn, ctx, cleanup, err := s.queryConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err = conn.Raw(func(c any) error {
		if c != native {
			t.Error("successful cleanup discarded a reusable connection")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var leftovers int
	if err = conn.QueryRowContext(ctx, "SELECT count(*) FROM duckdb_tables() WHERE temporary AND table_name='analytics_snapshot'").Scan(&leftovers); err != nil {
		t.Fatal(err)
	}
	if leftovers != 0 {
		t.Fatal("canceled query leaked TEMP snapshot")
	}
	if _, err = conn.ExecContext(ctx, "CREATE TEMP TABLE analytics_snapshot AS SELECT 1 AS ts"); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledMaterializationDoesNotLeakTemporaryTable(t *testing.T) {
	s := openTestStore(t, Options{QueryConcurrency: 1})
	conn, ctx, cleanup, err := s.queryConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	_, err = conn.ExecContext(deadline, "CREATE TEMP TABLE analytics_snapshot AS SELECT sum(sin(i::DOUBLE)) AS ts FROM range(1000000000000) AS r(i)")
	cancel()
	cleanup()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("materialization was not interrupted: %v", err)
	}
	conn, ctx, cleanup, err = s.queryConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var leftovers int
	if err = conn.QueryRowContext(ctx, "SELECT count(*) FROM duckdb_tables() WHERE temporary AND table_name='analytics_snapshot'").Scan(&leftovers); err != nil {
		t.Fatal(err)
	}
	if leftovers != 0 {
		t.Fatal("interrupted CREATE TEMP TABLE leaked snapshot")
	}
}

func TestCloseWaitsForPrivateQueryLifetime(t *testing.T) {
	s := openTestStore(t, Options{})
	fixture(t, s)
	snap, err := s.snapshot(context.Background(), fixturePlan(t), false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if snap != nil {
			snap.close()
		}
	}()
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	deadline := time.Now().Add(time.Second)
	for !s.closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("Close did not stop admission")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("Close ended before snapshot: %v", err)
	default:
	}
	rows, err := aggregate(snap.ctx, snap.conn, "''", allEvents, true, 1, false)
	if err != nil || len(rows) != 1 || rows[0].Requests != 4 {
		t.Fatalf("Close invalidated active query: %v %+v", err, rows)
	}
	snap.close()
	snap = nil
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not finish after query cleanup")
	}
}

func TestLateEventsInRolledDayAreExpiredNotStorageFailures(t *testing.T) {
	var clock atomic.Int64
	clock.Store(fixtureTime.Add(day).UnixMilli())
	dir := t.TempDir()
	o := Options{Dir: dir, DetailRetention: day, RollupRetention: 4 * day, Now: func() time.Time { return time.UnixMilli(clock.Load()) }}
	s := openTestStore(t, o)
	fixture(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	clock.Store(fixtureTime.Add(3 * day).UnixMilli())
	s = openTestStore(t, o)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	o.DetailRetention = 4 * day
	s = openTestStore(t, o)
	if !s.RecordRequest(RequestEvent{StartedAt: fixtureTime.UnixMilli(), CompletedAt: fixtureTime.UnixMilli() + 1}) {
		t.Fatal("async admission")
	}
	if err := s.Flush(context.Background()); !errors.Is(err, ErrDetailRetention) {
		t.Fatalf("late rolled event: %v", err)
	}
	status := s.Status()
	if status.Dropped.Expired != 1 || status.Dropped.Storage != 0 || status.Ingested != 0 {
		t.Fatalf("misclassified rolled event: %+v", status)
	}
	if err := s.Close(); !errors.Is(err, ErrDetailRetention) {
		t.Fatal(err)
	}
}

func TestTopWinnerOrderingPreservesFullMetrics(t *testing.T) {
	s := openTestStore(t, Options{})
	at := fixtureTime.UnixMilli()
	// Equal bytes; completed count wins, followed by lexical host. A large
	// nonwinning latency cannot leak into the winner's distribution.
	for _, host := range []string{"z.example", "b.example", "a.example"} {
		count := 2
		if host == "z.example" {
			count = 1
		}
		for n := 0; n < count; n++ {
			duration := int64(10)
			if host == "z.example" {
				duration = 100_000
			}
			if !s.RecordRequest(RequestEvent{Host: host, ClientIP: "192.0.2.1", StartedAt: at - duration, CompletedAt: at + 1000, DurationMS: duration, FirstByteMS: 5, CacheResult: "HIT"}) {
				t.Fatal("request")
			}
		}
		if !s.RecordTransfer(TransferEvent{Kind: "request", Host: host, IntervalStart: at, IntervalEnd: at + 2000, ResponseBytes: 100}) {
			t.Fatal("traffic")
		}
	}
	flushTest(t, s)
	r, err := s.Query(context.Background(), Query{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Minute)), GroupBy: []string{"host", "country"}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	rows := r.Breakdowns["host"]
	if len(rows) != 1 || rows[0].Value != "a.example" || rows[0].Requests != 2 || rows[0].UniqueIPs == nil || *rows[0].UniqueIPs != 1 || rows[0].Latency.P99MS != 10 || rows[0].TTFB.P99MS != 5 || rows[0].PeakResponseBytesPerSecond != 10 {
		t.Fatalf("top winner metrics: %+v", rows)
	}
	if r.Summary.Requests != 5 || r.Summary.Latency.P99MS != 100_000 {
		t.Fatal("ranking limit truncated summary")
	}
	if len(r.Geo) != 1 || !reflect.DeepEqual(r.Geo[0].Metrics, r.Breakdowns["country"][0].Metrics) {
		t.Fatal("country and geo reused metrics differ")
	}
}
