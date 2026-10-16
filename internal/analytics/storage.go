package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const rawSchema = `(
 ts BIGINT, kind TINYINT, id VARCHAR, site_id VARCHAR, origin_id VARCHAR,
 host VARCHAR, client_ip VARCHAR, path VARCHAR, method VARCHAR, content_type VARCHAR,
 protocol VARCHAR, status INTEGER, cache_result VARCHAR, country VARCHAR, asn UINTEGER,
 ip_family TINYINT, ip_key BLOB, started_at BIGINT, duration_ms BIGINT,
 request_bytes BIGINT, response_bytes BIGINT, cached_bytes BIGINT, error VARCHAR,
 first_byte_ms BIGINT DEFAULT 0
)`

var dimensions = []string{"site_id", "host", "client_ip", "method", "status", "cache_result", "content_type", "country", "asn", "path", "origin_id", "protocol"}
var rollDimensions = []string{"site_id", "host", "method", "status", "cache_result", "content_type", "country", "asn", "origin_id", "protocol"}
var counterColumns = []string{"n", "errors", "n4", "n5", "hits", "eligible", "coalesced", "rin", "rout", "cached", "onum", "oerrors", "oin", "oout", "duration", "oduration"}
var sumColumns = append(append([]string(nil), counterColumns...), "ttfb_n", "origin_ttfb_n")

// Counter rows own outcomes/traffic once. Sample rows own exactly one independent
// distribution; their weights never stand in for request or physical-attempt counts.
var distributions = []struct{ weight, value string }{
	{"latency_n", "latency_value"},
	{"origin_latency_n", "origin_latency_value"},
	{"ttfb_n", "ttfb_value"},
	{"origin_ttfb_n", "origin_ttfb_value"},
}

// A single relational shape serves raw history and mergeable rolled history.
func rawSource(table string) string {
	return `SELECT ts, ` + strings.Join(dimensions, ",") + `, ip_family, ip_key,
 CAST(kind=0 AS BIGINT) AS n,
 CAST(kind=0 AND (error<>'' OR status>=500) AS BIGINT) AS errors,
 CAST(kind=0 AND status BETWEEN 400 AND 499 AS BIGINT) AS n4,
 CAST(kind=0 AND status>=500 AS BIGINT) AS n5,
 CAST(kind=0 AND cache_result IN ('HIT','STALE','REVALIDATED') AS BIGINT) AS hits,
 CAST(kind=0 AND cache_result NOT IN ('','BYPASS','DISABLED') AS BIGINT) AS eligible,
 CAST(kind=0 AND cache_result='COALESCED' AS BIGINT) AS coalesced,
 CASE WHEN kind=2 THEN request_bytes ELSE 0 END AS rin,
 CASE WHEN kind=2 THEN response_bytes ELSE 0 END AS rout,
 CASE WHEN kind=2 THEN cached_bytes ELSE 0 END AS cached,
 CAST(kind=1 AS BIGINT) AS onum,
 CAST(kind=1 AND (error<>'' OR status>=500) AS BIGINT) AS oerrors,
 CASE WHEN kind=3 THEN request_bytes ELSE 0 END AS oin,
 CASE WHEN kind=3 THEN response_bytes ELSE 0 END AS oout,
 CAST(CASE WHEN kind=0 THEN duration_ms ELSE 0 END AS DOUBLE) AS duration,
 CAST(CASE WHEN kind=1 THEN duration_ms ELSE 0 END AS DOUBLE) AS oduration,
 CAST(kind=0 AND first_byte_ms>0 AS BIGINT) AS ttfb_n,
 CAST(kind=1 AND first_byte_ms>0 AS BIGINT) AS origin_ttfb_n,
 CAST(kind=0 AS BIGINT) AS latency_n,
 CAST(kind=1 AS BIGINT) AS origin_latency_n,
 CAST(CASE WHEN kind=0 THEN duration_ms ELSE -1 END AS DOUBLE) AS latency_value,
 CAST(CASE WHEN kind=1 THEN duration_ms ELSE -1 END AS DOUBLE) AS origin_latency_value,
 CAST(CASE WHEN kind=0 AND first_byte_ms>0 THEN first_byte_ms ELSE -1 END AS DOUBLE) AS ttfb_value,
 CAST(CASE WHEN kind=1 AND first_byte_ms>0 THEN first_byte_ms ELSE -1 END AS DOUBLE) AS origin_ttfb_value,
 kind IN (1,3) AS physical
 FROM ` + table
}

func rollSource(table string) string {
	return `SELECT ts,site_id,host,'' AS client_ip,method,status,cache_result,content_type,
 country,asn,'' AS path,origin_id,protocol,0::TINYINT AS ip_family,NULL::BLOB AS ip_key,
 ` + strings.Join(sumColumns, ",") + `,latency_n,origin_latency_n,latency_value,origin_latency_value,ttfb_value,origin_ttfb_value,physical FROM ` + table
}

func emptySource() string {
	return rawSource("empty_events")
}

type partition struct {
	key   string
	alias string
	path  string
	date  time.Time
	roll  bool
}

type diskState struct {
	Version       int   `json:"version"`
	DetailBefore  int64 `json:"detail_before"`
	EvictedBefore int64 `json:"evicted_before"`
}

func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func (s *Store) initialize(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS empty_events "+rawSchema); err != nil {
		return err
	}
	if err := s.upgradeSchema(ctx, "catalog", "empty_events", false); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(s.options.Dir, "state.json"))
	if err == nil {
		if err = json.Unmarshal(data, &s.meta); err != nil {
			return fmt.Errorf("read analytics state: %w", err)
		}
		if s.meta.Version != 1 {
			return errors.New("unsupported analytics state version")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		s.meta.Version = 1
	}
	s.evictedBefore = s.meta.EvictedBefore
	s.detailBefore = max(s.meta.DetailBefore, s.meta.EvictedBefore)
	for _, before := range []int64{s.meta.DetailBefore, s.meta.EvictedBefore} {
		cutoff := time.UnixMilli(before)
		if before < 0 || !cutoff.Equal(cutoff.UTC().Truncate(day)) {
			return errors.New("invalid analytics detail/eviction horizon")
		}
	}
	entries, err := os.ReadDir(s.options.Dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "compact-") && strings.HasSuffix(name, ".building") {
			if err = s.discardFile(filepath.Join(s.options.Dir, name)); err != nil {
				return err
			}
			continue
		}
		if !strings.HasSuffix(name, ".duckdb") {
			continue
		}
		roll := strings.HasPrefix(name, "roll-")
		if !roll && !strings.HasPrefix(name, "day-") {
			continue
		}
		prefix := "day-"
		if roll {
			prefix = "roll-"
		}
		key := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".duckdb")
		date, err := time.Parse("20060102", key)
		if err != nil {
			return fmt.Errorf("invalid analytics partition %q", name)
		}
		p := &partition{key: key, alias: "d" + key, path: filepath.Join(s.options.Dir, name), date: date, roll: roll}
		// Reconstruct the fence for directories created before detail_before
		// existed, before an old rollup can be removed by retention below.
		if p.roll {
			if err = s.advanceDetailBefore(p.date.Add(day).UnixMilli()); err != nil {
				return err
			}
		}
		if p.date.Before(horizon(s.options.Now(), s.options.RollupRetention)) || p.date.Add(day).UnixMilli() <= s.meta.EvictedBefore {
			if err = s.advanceDetailBefore(p.date.Add(day).UnixMilli()); err != nil {
				return err
			}
			if err = s.discardFile(p.path); err != nil {
				return err
			}
			continue
		}
		if old := s.parts[key]; old != nil {
			// A committed compact file wins if a crash occurred before deleting its raw source.
			if !roll {
				if err = s.discardFile(p.path); err != nil {
					return err
				}
				continue
			}
			if _, err = s.db.ExecContext(ctx, "DETACH "+old.alias); err != nil {
				return err
			}
			if err = s.discardFile(old.path); err != nil {
				return err
			}
		}
		if err = s.attach(ctx, p); err != nil {
			return err
		}
		table := "events"
		if p.roll {
			table = "rollup"
		}
		if err = s.upgradeSchema(ctx, p.alias, table, p.roll); err != nil {
			return err
		}
		s.parts[key] = p
	}
	return nil
}

func (s *Store) attach(ctx context.Context, p *partition) error {
	_, err := s.db.ExecContext(ctx, "ATTACH "+quote(p.path)+" AS "+p.alias)
	return err
}

// Old raw data has no first-byte measurement. Legacy joint-bin rollups can be
// normalized losslessly using their stored sample counts, without original rows.
func (s *Store) upgradeSchema(ctx context.Context, catalog, table string, roll bool) error {
	additions := []struct{ name, definition string }{{"first_byte_ms", "BIGINT DEFAULT 0"}}
	if roll {
		additions = []struct{ name, definition string }{
			{"ttfb_n", "BIGINT DEFAULT 0"}, {"origin_ttfb_n", "BIGINT DEFAULT 0"},
			{"ttfb_value", "DOUBLE DEFAULT -1"}, {"origin_ttfb_value", "DOUBLE DEFAULT -1"},
		}
	}
	rows, err := s.db.QueryContext(ctx, "SELECT column_name FROM duckdb_columns() WHERE database_name=? AND schema_name='main' AND table_name=?", catalog, table)
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		present[name] = true
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, column := range additions {
		if !present[column.name] {
			if _, err = s.db.ExecContext(ctx, "ALTER TABLE "+catalog+"."+table+" ADD COLUMN "+column.name+" "+column.definition); err != nil {
				return err
			}
		}
	}
	if roll && (!present["latency_n"] || !present["origin_latency_n"]) {
		if present["latency_n"] || present["origin_latency_n"] {
			return fmt.Errorf("unsupported partial rollup sample-weight schema in %s", catalog)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		source := "SELECT *,n AS latency_n,onum AS origin_latency_n FROM " + catalog + ".rollup"
		if _, err = tx.ExecContext(ctx, "CREATE TABLE "+catalog+".rollup_next AS "+independentRollupSQL(source, true)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DROP TABLE "+catalog+".rollup"); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "ALTER TABLE "+catalog+".rollup_next RENAME TO rollup"); err != nil {
			return err
		}
		return tx.Commit()
	}
	return nil
}

func (s *Store) ensurePartition(ctx context.Context, key string) (*partition, error) {
	if p := s.parts[key]; p != nil {
		return p, nil
	}
	date, err := time.Parse("20060102", key)
	if err != nil {
		return nil, err
	}
	p := &partition{key: key, alias: "d" + key, path: filepath.Join(s.options.Dir, "day-"+key+".duckdb"), date: date}
	if err = s.attach(ctx, p); err != nil {
		return nil, err
	}
	if _, err = s.db.ExecContext(ctx, "CREATE TABLE "+p.alias+".events "+rawSchema); err != nil {
		_, _ = s.db.ExecContext(ctx, "DETACH "+p.alias)
		return nil, err
	}
	s.parts[key] = p
	return p, nil
}

func (s *Store) sortedPartitions() []*partition {
	parts := make([]*partition, 0, len(s.parts))
	for _, p := range s.parts {
		parts = append(parts, p)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].key < parts[j].key })
	return parts
}

func roundedLatency(column string) string {
	// 16 bins per doubling. 2^(1/16)-1 = 4.4274% maximum upward error.
	return "CASE WHEN " + column + "<=0 THEN " + column + " ELSE pow(2,ceil(log2(greatest(1," + column + "))*16)/16) END"
}

// Five UNION branches share one source scan. Counters group only by dimensions
// and time; each histogram groups by its own value, never a joint-bin tuple.
func independentRollupSQL(source string, alreadyBinned bool) string {
	keys := "ts," + strings.Join(rollDimensions, ",") + ",physical"
	branches := make([]string, 0, 1+len(distributions))
	for which := -1; which < len(distributions); which++ {
		fields := []string{keys}
		for _, c := range counterColumns {
			expression := "0"
			if which < 0 {
				expression = "sum(" + c + ")"
			}
			fields = append(fields, "CAST("+expression+" AS "+metricSQLType(c)+") AS "+c)
		}
		for i, d := range distributions {
			expression := "0"
			if i == which {
				expression = "sum(" + d.weight + ")"
			}
			fields = append(fields, "CAST("+expression+" AS BIGINT) AS "+d.weight)
		}
		for i, d := range distributions {
			expression := "-1::DOUBLE"
			if i == which {
				expression = d.value
				if !alreadyBinned {
					expression = roundedLatency(d.value)
				}
			}
			fields = append(fields, expression+" AS "+d.value)
		}
		where := ""
		if which >= 0 {
			where = " WHERE " + distributions[which].weight + ">0"
		}
		branches = append(branches, "SELECT "+strings.Join(fields, ",")+" FROM source"+where+" GROUP BY ALL")
	}
	return "WITH source AS MATERIALIZED (SELECT * REPLACE ((ts//10000)*10000 AS ts) FROM (" + source + ")) " + strings.Join(branches, " UNION ALL ")
}

func (s *Store) compact(ctx context.Context, p *partition) error {
	// Write a separate compact file: DROP/DELETE do not guarantee physical shrinkage.
	// Renaming the finished file is the commit marker used on reopen.
	building := filepath.Join(s.options.Dir, "compact-"+p.key+".building")
	final := filepath.Join(s.options.Dir, "roll-"+p.key+".duckdb")
	if err := s.discardFile(building); err != nil {
		return err
	}
	const target = "compact_target"
	if _, err := s.db.ExecContext(ctx, "ATTACH "+quote(building)+" AS "+target); err != nil {
		return err
	}
	attached := true
	defer func() {
		if attached {
			_, _ = s.db.ExecContext(context.Background(), "DETACH "+target)
		}
	}()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := "CREATE TABLE " + target + ".rollup AS " + independentRollupSQL(rawSource(p.alias+".events"), false)
	if _, err = tx.ExecContext(ctx, q); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "CHECKPOINT "+target); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "DETACH "+target); err != nil {
		return err
	}
	attached = false
	// Persist lost-detail knowledge before publishing the completed rollup. A
	// later retention increase must not advertise the old day as detail again.
	if err = s.advanceDetailBefore(p.date.Add(day).UnixMilli()); err != nil {
		return err
	}
	if err = os.Rename(building, final); err != nil {
		return err
	}
	if err = syncDirectory(s.options.Dir); err != nil {
		return err
	}
	oldPath := p.path
	if _, err = conn.ExecContext(ctx, "DETACH "+p.alias); err != nil {
		return err
	}
	p.path, p.roll = final, true
	if _, err = conn.ExecContext(ctx, "ATTACH "+quote(p.path)+" AS "+p.alias); err != nil {
		return err
	}
	return s.discardFile(oldPath)
}

func metricSQLType(c string) string {
	if c == "duration" || c == "oduration" {
		return "DOUBLE"
	}
	return "BIGINT"
}

func (s *Store) maintain(ctx context.Context) error {
	now := s.options.Now()
	rawFloor, rollFloor := horizon(now, s.options.DetailRetention), horizon(now, s.options.RollupRetention)
	for _, p := range s.sortedPartitions() {
		if p.date.Before(rollFloor) {
			if err := s.removePartition(ctx, p, false); err != nil {
				return err
			}
		} else if !p.roll && p.date.Before(rawFloor) {
			// Compaction needs a second file; prefer explicit capacity eviction to
			// knowingly copying a large old day with no disk headroom.
			if _, err := s.db.ExecContext(ctx, "CHECKPOINT "+p.alias); err != nil {
				return err
			}
			used, err := directoryBytes(s.options.Dir)
			if err != nil {
				return err
			}
			info, err := os.Stat(p.path)
			if err != nil {
				return err
			}
			if used+3*info.Size()+s.options.TempLimitBytes >= s.options.DiskBudgetBytes {
				if err = s.removePartition(ctx, p, true); err != nil {
					return err
				}
				continue
			}
			if err := s.compact(ctx, p); err != nil {
				return err
			}
		}
	}
	return s.enforceBudget(ctx)
}

func (s *Store) advanceDetailBefore(before int64) error {
	if before <= s.meta.DetailBefore {
		return nil
	}
	next := s.meta
	next.DetailBefore = before
	return s.writeState(next)
}

// Called by the catalog owner; Status reads only the published cached fences.
func (s *Store) writeState(next diskState) error {
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	path := filepath.Join(s.options.Dir, "state.json")
	f, err := os.OpenFile(path+".new", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(path+".new", path); err != nil {
		return err
	}
	if err = syncDirectory(s.options.Dir); err != nil {
		return err
	}
	s.meta = next
	s.statsMu.Lock()
	s.detailBefore = max(next.DetailBefore, next.EvictedBefore)
	s.evictedBefore = next.EvictedBefore
	s.statsMu.Unlock()
	return nil
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func (s *Store) removePartition(ctx context.Context, p *partition, capacity bool) error {
	next := s.meta
	next.DetailBefore = max(next.DetailBefore, p.date.Add(day).UnixMilli())
	if capacity {
		next.EvictedBefore = max(next.EvictedBefore, p.date.Add(day).UnixMilli())
	}
	// Both ordinary expiry and capacity removal irreversibly discard detail.
	if next != s.meta {
		if err := s.writeState(next); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, "DETACH "+p.alias); err != nil {
		return err
	}
	delete(s.parts, p.key)
	return s.discardFile(p.path)
}

func (s *Store) discardFile(name string) error {
	for _, path := range []string{name, name + ".wal"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func directoryBytes(dir string) (int64, error) {
	var size int64
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		// Query snapshots can spill/clean up while ingestion checks admission.
		// A disappearing scratch file is not a storage-write failure.
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		size += info.Size()
		return nil
	})
	return size, err
}

func (s *Store) enforceBudget(ctx context.Context) error {
	size, err := directoryBytes(s.options.Dir)
	if err != nil {
		return err
	}
	// Reserve spill capacity and 10% for the next bounded append/checkpoint.
	watermark := (s.options.DiskBudgetBytes - s.options.TempLimitBytes) * 9 / 10
	parts := s.sortedPartitions()
	today := s.options.Now().UTC().Truncate(day)
	for size >= watermark && len(parts) > 0 && (len(parts) > 1 || parts[0].date.Before(today)) {
		if err = s.removePartition(ctx, parts[0], true); err != nil {
			return err
		}
		parts = parts[1:]
		if size, err = directoryBytes(s.options.Dir); err != nil {
			return err
		}
	}
	s.statsMu.Lock()
	s.diskBytes = size
	s.pressure = size >= watermark
	s.statsMu.Unlock()
	return nil
}

// sourceSQL is called under the catalog lock; identifiers come only from dated filenames.
func (s *Store) sourceSQL(from, to, now time.Time) (string, bool) {
	var queries []string
	rolled := false
	for _, p := range s.sortedPartitions() {
		if !p.date.Before(to) || !p.date.Add(day).After(from) || p.date.Before(horizon(now, s.options.RollupRetention)) {
			continue
		}
		if p.roll {
			queries = append(queries, rollSource(p.alias+".rollup"))
			rolled = true
		} else {
			queries = append(queries, rawSource(p.alias+".events"))
		}
	}
	if len(queries) == 0 {
		return emptySource(), false
	}
	return strings.Join(queries, " UNION ALL "), rolled
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
