package analytics

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"time"
)

// Each pinned connection has exactly one private, bounded-lifetime snapshot.
const snapshotTable = "temp.main.analytics_snapshot"

type readSnapshot struct {
	conn     *sql.Conn
	ctx      context.Context
	plan     queryPlan
	coverage Coverage
	close    func()
}

// queryConnection reserves a query lifetime, not a catalog lock. Close stops new
// registrations under admission before waiting for every lifetime to end.
func (s *Store) queryConnection(ctx context.Context) (*sql.Conn, context.Context, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, s.options.QueryTimeout)
	s.admission.RLock()
	if s.closed.Load() {
		s.admission.RUnlock()
		cancel()
		return nil, ctx, nil, ErrClosed
	}
	select {
	case s.queries <- struct{}{}:
	default:
		s.admission.RUnlock()
		cancel()
		return nil, ctx, nil, ErrBusy
	}
	s.readers.Add(1)
	s.admission.RUnlock()
	finish := func() { <-s.queries; cancel(); s.readers.Done() }
	conn, err := s.db.Conn(ctx)
	if err != nil {
		finish()
		return nil, ctx, nil, err
	}
	cleanup := func() {
		// A canceled request must not leave a TEMP table in the connection pool.
		// If bounded cleanup fails, discard the native connection instead.
		cleanupCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := conn.ExecContext(cleanupCtx, "DROP TABLE IF EXISTS "+snapshotTable)
		stop()
		if err != nil {
			s.noteError(err)
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
		finish()
	}
	return conn, ctx, cleanup, nil
}

func (s *Store) lockCatalog(ctx context.Context) error {
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for !s.catalog.TryRLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return nil
}

func (s *Store) snapshot(ctx context.Context, p queryPlan, needsDetails, details bool) (*readSnapshot, error) {
	conn, ctx, cleanup, err := s.queryConnection(ctx)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			cleanup()
		}
	}()
	if err = s.lockCatalog(ctx); err != nil {
		return nil, err
	}
	locked := true
	defer func() {
		if locked {
			s.catalog.RUnlock()
		}
	}()
	now := s.options.Now()
	detailFrom := s.detailFrom(now)
	source, rolled := s.sourceSQL(p.from, p.to, now)
	p.uniqueIPs = !rolled && !p.from.Before(detailFrom)
	if needsDetails && !p.uniqueIPs {
		return nil, ErrDetailRetention
	}
	if details {
		var sources []string
		for _, part := range s.sortedPartitions() {
			if !part.date.Before(p.to) || !part.date.Add(day).After(p.from) {
				continue
			}
			sources = append(sources, "SELECT *,false AS physical FROM "+part.alias+".events WHERE kind=0")
		}
		if len(sources) == 0 {
			sources = []string{"SELECT *,false AS physical FROM empty_events"}
		}
		source = strings.Join(sources, " UNION ALL ")
	}
	_, err = conn.ExecContext(ctx, "CREATE TEMP TABLE analytics_snapshot AS WITH source AS ("+source+") SELECT * FROM source WHERE "+p.where, p.args...)
	if err != nil {
		return nil, fmt.Errorf("materialize analytics snapshot: %w", err)
	}
	// Partition state, drop counts, pending ingestion and horizons refer to this
	// barrier. Later aggregation reads only the private table, never live files.
	status := s.Status()
	status.DetailFrom = stamp(detailFrom)
	status.RollupFrom = stamp(horizon(now, s.options.RollupRetention))
	coverage := snapshotCoverage(status, p, rolled, now, s.options.RollupRetention, s.meta.EvictedBefore)
	s.catalog.RUnlock()
	locked = false
	var low, high sql.NullInt64
	if err = conn.QueryRowContext(ctx, "SELECT min(ts),max(ts) FROM "+snapshotTable).Scan(&low, &high); err != nil {
		return nil, err
	}
	if low.Valid {
		coverage.DataFrom = timePointer(low.Int64)
	}
	if high.Valid {
		coverage.DataTo = timePointer(high.Int64)
	}
	keep = true
	return &readSnapshot{conn, ctx, p, coverage, cleanup}, nil
}
