package analytics

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// BenchmarkQueryCost measures the whole bounded query, not just one aggregate.
// The seed has 60k completed requests + 60k deltas across 5k clients/paths, with
// varied durations/TTFB and one physical attempt per 100 requests. There is one
// untimed warm-up query so the first native execution is not the measured result.
func BenchmarkQueryCost(b *testing.B) {
	s, err := Open(Options{Dir: b.TempDir(), Now: func() time.Time { return fixtureTime.Add(day) }, FlushInterval: time.Minute, QueryTimeout: time.Minute})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	seedQueryCost(b, s)
	q := Query{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Hour)), IntervalSeconds: 60, GroupBy: dimensions, Limit: 20}
	if _, err = s.Query(context.Background(), q); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := s.Query(context.Background(), q)
		if err != nil {
			b.Fatal(err)
		}
		if r.Summary.Requests != 60_000 || r.Summary.ResponseBytes != 60_000_000 {
			b.Fatal("fixture totals changed")
		}
	}
}

func BenchmarkQuerySnapshotCost(b *testing.B) {
	s, err := Open(Options{Dir: b.TempDir(), Now: func() time.Time { return fixtureTime.Add(day) }, FlushInterval: time.Minute, QueryTimeout: time.Minute})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	seedQueryCost(b, s)
	p, err := plan(stamp(fixtureTime), stamp(fixtureTime.Add(time.Hour)), Filters{})
	if err != nil {
		b.Fatal(err)
	}
	snap, err := s.snapshot(context.Background(), p, false, false)
	if err != nil {
		b.Fatal(err)
	}
	snap.close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snap, err = s.snapshot(context.Background(), p, false, false)
		if err != nil {
			b.Fatal(err)
		}
		snap.close()
	}
}

func seedQueryCost(t testing.TB, s *Store) {
	t.Helper()
	s.catalog.Lock()
	defer s.catalog.Unlock()
	ctx := context.Background()
	p, err := s.ensurePartition(ctx, "20261015")
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]rawRow, 0, 4096)
	appendRow := func(r rawRow) {
		rows = append(rows, r)
		if len(rows) == cap(rows) {
			if err := s.appendRows(ctx, p, rows); err != nil {
				t.Fatal(err)
			}
			rows = rows[:0]
		}
	}
	for i := 0; i < 60_000; i++ {
		client := fmt.Sprintf("192.0.%d.%d", i%5000/250, i%250+1)
		completed := fixtureTime.UnixMilli() + int64(i%3600)*1000 + 500
		e := RequestEvent{RequestID: fmt.Sprint(i), SiteID: fmt.Sprint(i % 4), Host: fmt.Sprintf("h%d.example", i%40), ClientIP: client, Path: fmt.Sprintf("/asset/%d", i%5000),
			Method: "GET", ContentType: "video", Status: 200, CacheResult: "MISS", StartedAt: completed - 100_000, CompletedAt: completed,
			DurationMS: int64(i%90_000 + 1000), FirstByteMS: int64(i%900 + 1)}
		r, err := s.normalize(e)
		if err != nil {
			t.Fatal(err)
		}
		appendRow(r)
		r.kind, r.in, r.out, r.cached = 2, 10, 1000, 0
		appendRow(r)
		if i%100 == 0 {
			r.kind, r.id, r.origin, r.client, r.ipFamily, r.ipKey = 1, "attempt-"+r.id, "upstream", "", 0, nil
			r.protocol = "h2"
			appendRow(r)
			r.kind = 3
			appendRow(r)
		}
	}
	if len(rows) > 0 {
		if err = s.appendRows(ctx, p, rows); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVariedLatencyRollupReduction(t *testing.T) {
	dir := t.TempDir()
	o := Options{Dir: dir, FlushInterval: time.Minute}
	s := openTestStore(t, o)
	ctx := context.Background()
	p, err := s.ensurePartition(ctx, "20261015")
	if err != nil {
		t.Fatal(err)
	}
	// The same 160x160 independently varied latency pairs as the joint-bin
	// measurement, over four base buckets so physical file savings exceed a
	// single DuckDB allocation. Add request/origin traffic to test all weights.
	rows := make([]rawRow, 0, 4096)
	const bins, buckets = 160, 4
	for bucket := 0; bucket < buckets; bucket++ {
		for d := 0; d < bins; d++ {
			for f := 0; f < bins; f++ {
				duration := int64(math.Pow(2, 20+(float64(d)+0.5)/16))
				first := int64(math.Pow(2, 10+(float64(f)+0.5)/16))
				rows = append(rows, rawRow{time: fixtureTime.UnixMilli() + int64(bucket)*10_000 + 1, started: fixtureTime.UnixMilli() - duration, duration: duration, firstByte: first,
					id: fmt.Sprintf("%d-%d", d, f), host: "a.example", site: "a", client: "192.0.2.1", method: "GET", cache: "HIT", status: 200})
				if len(rows) == cap(rows) {
					if err = s.appendRows(ctx, p, rows); err != nil {
						t.Fatal(err)
					}
					rows = rows[:0]
				}
			}
		}
	}
	if len(rows) > 0 {
		if err = s.appendRows(ctx, p, rows); err != nil {
			t.Fatal(err)
		}
	}
	fixture(t, s)
	if _, err = s.db.ExecContext(ctx, "CHECKPOINT "+p.alias); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p.path)
	if err != nil {
		t.Fatal(err)
	}
	rawBytes := info.Size()
	q := Query{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Minute)), IntervalSeconds: 10, GroupBy: rollDimensions, Limit: 100}
	before, err := s.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	s.catalog.Lock()
	err = s.compact(ctx, p)
	s.catalog.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var rolledRows int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+p.alias+".rollup").Scan(&rolledRows); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(p.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("varied duration/TTFB plus traffic fixture: raw_rows=%d rollup_rows=%d row_reduction=%.2f%% raw_file_bytes=%d rollup_file_bytes=%d file_reduction=%.2f%%",
		buckets*bins*bins+13, rolledRows, 100*(1-float64(rolledRows)/(buckets*bins*bins+13)), rawBytes, info.Size(), 100*(1-float64(info.Size())/float64(rawBytes)))
	if rolledRows >= buckets*bins*bins/10 {
		t.Fatal("independent distributions did not reduce row count")
	}
	if info.Size() >= rawBytes {
		t.Fatal("multi-bucket rollup did not reduce physical file size")
	}
	var samples, originSamples, ttfbSamples, originTTFBSamples, badCounters, badSamples int64
	if err = s.db.QueryRowContext(ctx, `SELECT sum(latency_n),sum(origin_latency_n),sum(ttfb_n),sum(origin_ttfb_n),
 count(*) FILTER (WHERE (n>0 OR onum>0 OR rout>0 OR oout>0) AND (latency_n>0 OR origin_latency_n>0 OR ttfb_n>0 OR origin_ttfb_n>0)),
 count(*) FILTER (WHERE CAST(latency_n>0 AS INT)+CAST(origin_latency_n>0 AS INT)+CAST(ttfb_n>0 AS INT)+CAST(origin_ttfb_n>0 AS INT)>1)
 FROM `+p.alias+`.rollup`).Scan(&samples, &originSamples, &ttfbSamples, &originTTFBSamples, &badCounters, &badSamples); err != nil {
		t.Fatal(err)
	}
	if samples != buckets*bins*bins+4 || originSamples != 1 || ttfbSamples != buckets*bins*bins+3 || originTTFBSamples != 1 || badCounters != 0 || badSamples != 0 {
		t.Fatalf("weights/counters overlap: %d/%d/%d/%d bad=%d/%d", samples, originSamples, ttfbSamples, originTTFBSamples, badCounters, badSamples)
	}
	var histogramCounters int64
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+p.alias+`.rollup WHERE
 (latency_n>0 OR origin_latency_n>0 OR ttfb_n>0 OR origin_ttfb_n>0) AND (`+strings.Join(counterColumns, "<>0 OR ")+`<>0)`).Scan(&histogramCounters); err != nil {
		t.Fatal(err)
	}
	if histogramCounters != 0 {
		t.Fatal("histogram rows own additive metric counters")
	}
	after, err := s.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	checkRollupResult(t, before, after)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, o)
	reopened, err := s.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, reopened) {
		t.Fatal("independent histogram changed after reopen")
	}
}

func checkRollupMetrics(t *testing.T, want, got Metrics) {
	t.Helper()
	for _, pair := range [][2]Percentiles{{want.Latency, got.Latency}, {want.OriginLatency, got.OriginLatency}, {want.TTFB, got.TTFB}, {want.OriginTTFB, got.OriginTTFB}} {
		for _, v := range [][2]float64{{pair[0].P50MS, pair[1].P50MS}, {pair[0].P95MS, pair[1].P95MS}, {pair[0].P99MS, pair[1].P99MS}} {
			if v[1] < v[0] || v[1] > v[0]*math.Pow(2, 1.0/16)+1e-8 {
				t.Fatalf("rolled percentile outside bin bound: %v -> %v", v[0], v[1])
			}
		}
	}
	want.UniqueIPs = nil
	want.Latency, want.OriginLatency, want.TTFB, want.OriginTTFB = Percentiles{}, Percentiles{}, Percentiles{}, Percentiles{}
	got.Latency, got.OriginLatency, got.TTFB, got.OriginTTFB = Percentiles{}, Percentiles{}, Percentiles{}, Percentiles{}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("rollup counters/rates changed:\nwant %+v\n got %+v", want, got)
	}
}

func checkRollupResult(t *testing.T, want, got *Result) {
	t.Helper()
	checkRollupMetrics(t, want.Summary, got.Summary)
	if len(want.Series) != len(got.Series) || len(want.Geo) != len(got.Geo) {
		t.Fatal("rollup changed output groups")
	}
	for i := range want.Series {
		if want.Series[i].Time != got.Series[i].Time {
			t.Fatal("rollup changed series bucket")
		}
		checkRollupMetrics(t, want.Series[i].Metrics, got.Series[i].Metrics)
	}
	for group, rows := range want.Breakdowns {
		if len(rows) != len(got.Breakdowns[group]) {
			t.Fatalf("rollup changed %s ranking size", group)
		}
		for i, row := range rows {
			other := got.Breakdowns[group][i]
			if row.Value != other.Value {
				t.Fatalf("rollup changed %s ranking order", group)
			}
			checkRollupMetrics(t, row.Metrics, other.Metrics)
		}
	}
	for i, row := range want.Geo {
		checkRollupMetrics(t, row.Metrics, got.Geo[i].Metrics)
	}
}

func TestJointBinRollupConvertsOnceWithAllSamples(t *testing.T) {
	dir := t.TempDir()
	o := Options{Dir: dir, FlushInterval: time.Minute}
	s := openTestStore(t, o)
	fixture(t, s)
	q := Query{From: stamp(fixtureTime), To: stamp(fixtureTime.Add(time.Minute)), IntervalSeconds: 10, GroupBy: rollDimensions, Limit: 100}
	before, err := s.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	s.catalog.Lock()
	p := s.parts["20261015"]
	keys := "ts," + strings.Join(rollDimensions, ",") + ",physical,latency_value,origin_latency_value,ttfb_value,origin_ttfb_value"
	var sums []string
	for _, c := range sumColumns {
		sums = append(sums, "CAST(sum("+c+") AS "+metricSQLType(c)+") AS "+c)
	}
	legacy := `CREATE TABLE ` + p.alias + `.legacy AS WITH source AS (` + rawSource(p.alias+".events") + `),
 binned AS (SELECT * REPLACE ((ts//10000)*10000 AS ts,` + roundedLatency("latency_value") + ` AS latency_value,
 ` + roundedLatency("origin_latency_value") + ` AS origin_latency_value,` + roundedLatency("ttfb_value") + ` AS ttfb_value,
 ` + roundedLatency("origin_ttfb_value") + ` AS origin_ttfb_value) FROM source)
 SELECT ` + keys + `,` + strings.Join(sums, ",") + ` FROM binned GROUP BY ALL`
	_, err = s.db.Exec(legacy)
	if err == nil {
		_, err = s.db.Exec("DROP TABLE " + p.alias + ".events")
	}
	if err == nil {
		_, err = s.db.Exec("ALTER TABLE " + p.alias + ".legacy RENAME TO rollup")
	}
	if err == nil {
		_, err = s.db.Exec("DETACH " + p.alias)
	}
	oldPath := p.path
	newPath := filepath.Join(dir, "roll-20261015.duckdb")
	if err == nil {
		err = os.Rename(oldPath, newPath)
	}
	delete(s.parts, p.key)
	s.catalog.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, o)
	after, err := s.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	checkRollupResult(t, before, after)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, o)
	again, err := s.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, again) {
		t.Fatal("legacy conversion was not idempotent on reopen")
	}
}
