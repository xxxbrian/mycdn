package analytics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	_ "time/tzdata" // IANA timezone validation also works in minimal/offline containers.
)

const (
	day       = 24 * time.Hour
	maxPoints = 4096
)

type message struct {
	value    any
	enqueued time.Time
	barrier  chan error
	stop     bool
}

// Store owns one directory, one bounded ingestion goroutine and one DuckDB instance.
// The catalog lock protects daily-file attachment/removal and coherent query snapshots.
type Store struct {
	options Options
	db      *sql.DB
	geo     geoReaders
	queue   chan message
	queries chan struct{}
	done    chan struct{}
	readers sync.WaitGroup // Registration is serialized with Close by admission.

	admission sync.RWMutex
	closed    atomic.Bool
	closeOnce sync.Once
	catalog   sync.RWMutex
	parts     map[string]*partition
	meta      diskState

	accepted      atomic.Uint64
	ingested      atomic.Uint64
	queueDrop     atomic.Uint64
	invalidDrop   atomic.Uint64
	expiredDrop   atomic.Uint64
	storageDrop   atomic.Uint64
	pending       atomic.Int64
	oldestPending atomic.Int64
	lastIngest    atomic.Int64
	lagMillis     atomic.Int64
	statsMu       sync.RWMutex
	since         time.Time
	diskBytes     int64
	pressure      bool
	lastError     string
	detailBefore  int64 // Durable cutoff, published with stats; never needs catalog to read.
	evictedBefore int64
	closeError    error
}

func defaults(o Options) (Options, error) {
	if o.Dir == "" {
		return o, &ValidationError{"dir", "is required"}
	}
	if o.MemoryLimitBytes == 0 {
		o.MemoryLimitBytes = 256 << 20
	}
	if o.DiskBudgetBytes == 0 {
		o.DiskBudgetBytes = 2 << 30
	}
	if o.TempLimitBytes == 0 {
		o.TempLimitBytes = min(128<<20, o.DiskBudgetBytes/8)
	}
	if o.DetailRetention == 0 {
		o.DetailRetention = 14 * day
	}
	if o.RollupRetention == 0 {
		o.RollupRetention = 90 * day
	}
	if o.QueueCapacity == 0 {
		o.QueueCapacity = 8192
	}
	if o.BatchSize == 0 {
		o.BatchSize = 1024
	}
	if o.FlushInterval == 0 {
		o.FlushInterval = time.Second
	}
	if o.QueryConcurrency == 0 {
		o.QueryConcurrency = 2
	}
	if o.QueryTimeout == 0 {
		o.QueryTimeout = 15 * time.Second
	}
	if o.Threads == 0 {
		o.Threads = 2
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	checks := []struct {
		bad            bool
		field, message string
	}{
		{o.MemoryLimitBytes < 32<<20, "memory_limit_bytes", "minimum is 32 MiB"},
		{o.DiskBudgetBytes < 32<<20, "disk_budget_bytes", "minimum is 32 MiB"},
		{o.TempLimitBytes < 1<<20 || o.TempLimitBytes > o.DiskBudgetBytes/8, "temp_limit_bytes", "must be between 1 MiB and one eighth of disk budget"},
		{o.DetailRetention < day || o.DetailRetention%day != 0, "detail_retention", "must be whole days, at least one"},
		{o.RollupRetention < o.DetailRetention || o.RollupRetention%day != 0 || o.RollupRetention > 366*day, "rollup_retention", "must be whole days, >= detail retention, <= 366 days"},
		{o.QueueCapacity < 1 || o.QueueCapacity > 1<<20, "queue_capacity", "must be 1..1048576"},
		{o.BatchSize < 1 || o.BatchSize > 8192, "batch_size", "must be 1..8192"},
		{o.FlushInterval < time.Millisecond || o.FlushInterval > time.Minute, "flush_interval", "must be 1ms..1m"},
		{o.QueryConcurrency < 1 || o.QueryConcurrency > 8, "query_concurrency", "must be 1..8"},
		{o.QueryTimeout < time.Millisecond || o.QueryTimeout > 2*time.Minute, "query_timeout", "must be 1ms..2m"},
		{o.Threads < 1 || o.Threads > 16, "threads", "must be 1..16"},
	}
	for _, c := range checks {
		if c.bad {
			return o, &ValidationError{c.field, c.message}
		}
	}
	abs, err := filepath.Abs(o.Dir)
	if err != nil {
		return o, err
	}
	if strings.ContainsAny(abs, "?#") {
		return o, &ValidationError{"dir", "must not contain ? or #"}
	}
	o.Dir = abs
	return o, nil
}

func Open(options Options) (*Store, error) {
	o, err := defaults(options)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(o.Dir, 0o700); err != nil {
		return nil, err
	}
	if err = os.Chmod(o.Dir, 0o700); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Join(o.Dir, "tmp"), 0o700); err != nil {
		return nil, err
	}
	params := url.Values{
		"memory_limit":                 {fmt.Sprintf("%dB", o.MemoryLimitBytes)},
		"threads":                      {fmt.Sprint(o.Threads)},
		"temp_directory":               {filepath.Join(o.Dir, "tmp")},
		"max_temp_directory_size":      {fmt.Sprintf("%dB", o.TempLimitBytes)},
		"autoinstall_known_extensions": {"false"},
		"autoload_known_extensions":    {"false"},
		"preserve_insertion_order":     {"false"},
	}
	db, err := sql.Open("duckdb", filepath.Join(o.Dir, "catalog.duckdb")+"?"+params.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(o.QueryConcurrency + 1)
	db.SetMaxIdleConns(o.QueryConcurrency + 1)
	s := &Store{
		options: o, db: db, queue: make(chan message, o.QueueCapacity),
		queries: make(chan struct{}, o.QueryConcurrency), done: make(chan struct{}),
		parts: make(map[string]*partition), since: o.Now().UTC(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err = s.initialize(ctx); err == nil {
		err = s.maintain(ctx)
	}
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open analytics: %w", err)
	}
	s.geo = openGeo(o.CountryMMDB, o.ASNMMDB)
	go s.run()
	return s, nil
}

func (s *Store) RecordRequest(e RequestEvent) bool {
	if !validRequest(e) {
		s.invalidDrop.Add(1)
		return false
	}
	return s.record(e)
}

func (s *Store) RecordOrigin(e OriginEvent) bool {
	if !validOrigin(e) {
		s.invalidDrop.Add(1)
		return false
	}
	return s.record(e)
}

func (s *Store) RecordTransfer(e TransferEvent) bool {
	if !validTransfer(e) {
		s.invalidDrop.Add(1)
		return false
	}
	return s.record(e)
}

func (s *Store) record(value any) bool {
	// Never wait for Close/Flush or a database lock on the proxy path.
	if !s.admission.TryRLock() {
		s.queueDrop.Add(1)
		return false
	}
	defer s.admission.RUnlock()
	if s.closed.Load() {
		return false
	}
	s.pending.Add(1)
	select {
	case s.queue <- message{value: value, enqueued: time.Now()}:
		s.accepted.Add(1)
		return true
	default:
		s.pending.Add(-1)
		s.queueDrop.Add(1)
		return false
	}
}

// Flush is an ordered barrier for events admitted before this call.
func (s *Store) Flush(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.admission.RLock()
	if s.closed.Load() {
		s.admission.RUnlock()
		return ErrClosed
	}
	ack := make(chan error, 1)
	select {
	case s.queue <- message{barrier: ack}:
		s.admission.RUnlock()
	case <-ctx.Done():
		s.admission.RUnlock()
		return ctx.Err()
	}
	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.admission.Lock()
		s.closed.Store(true)
		s.admission.Unlock()
		s.queue <- message{stop: true}
	})
	<-s.done
	s.statsMu.RLock()
	defer s.statsMu.RUnlock()
	return s.closeError
}

func (s *Store) run() {
	ticker := time.NewTicker(s.options.FlushInterval)
	defer ticker.Stop()
	batch := make([]message, 0, s.options.BatchSize)
	var pendingError error
	flush := func() {
		s.catalog.Lock()
		defer s.catalog.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), s.options.QueryTimeout)
		defer cancel()
		if len(batch) > 0 {
			if err := s.appendBatch(ctx, batch); err != nil {
				pendingError = err
				s.noteError(err)
			}
			s.pending.Add(-int64(len(batch)))
			s.oldestPending.Store(0)
			batch = batch[:0]
		}
		if err := s.maintain(ctx); err != nil {
			pendingError = err
			s.noteError(err)
		}
	}
	for {
		select {
		case m := <-s.queue:
			if m.value != nil {
				if len(batch) == 0 {
					s.oldestPending.Store(m.enqueued.UnixMilli())
				}
				batch = append(batch, m)
				if len(batch) == s.options.BatchSize {
					flush()
				}
				continue
			}
			flush()
			if m.barrier != nil {
				m.barrier <- pendingError
			}
			if m.stop {
				s.readers.Wait()
				s.catalog.Lock()
				closeErr := errors.Join(pendingError, s.db.Close(), s.geo.close())
				s.catalog.Unlock()
				s.statsMu.Lock()
				s.closeError = closeErr
				s.statsMu.Unlock()
				close(s.done)
				return
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (s *Store) noteError(err error) {
	s.statsMu.Lock()
	s.lastError = err.Error()
	s.statsMu.Unlock()
}

func horizon(now time.Time, retention time.Duration) time.Time {
	return now.UTC().Truncate(day).Add(-retention)
}

func detailHorizon(now time.Time, retention time.Duration, before int64) time.Time {
	return time.UnixMilli(max(horizon(now, retention).UnixMilli(), before)).UTC()
}

func (s *Store) detailFrom(now time.Time) time.Time {
	s.statsMu.RLock()
	defer s.statsMu.RUnlock()
	return detailHorizon(now, s.options.DetailRetention, s.detailBefore)
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func timePointer(ms int64) *string {
	if ms == 0 {
		return nil
	}
	v := stamp(time.UnixMilli(ms))
	return &v
}

func (s *Store) Status() Status {
	s.statsMu.RLock()
	defer s.statsMu.RUnlock()
	now := s.options.Now()
	pending := int(s.pending.Load())
	lag := int64(0)
	if pending > 0 {
		lag = s.lagMillis.Load()
		if oldest := s.oldestPending.Load(); oldest != 0 {
			lag = max(lag, time.Now().UnixMilli()-oldest)
		}
	}
	return Status{
		Since: s.since.UnixMilli(), Accepted: s.accepted.Load(), Ingested: s.ingested.Load(),
		Dropped:    DropCounts{s.queueDrop.Load(), s.invalidDrop.Load(), s.expiredDrop.Load(), s.storageDrop.Load()},
		QueueDepth: len(s.queue), QueueCapacity: cap(s.queue), PendingEvents: pending, IngestLagMS: lag,
		LastIngestAt: s.lastIngest.Load(), LastError: s.lastError,
		DiskBytes: s.diskBytes, DiskBudgetBytes: s.options.DiskBudgetBytes, StoragePressure: s.pressure,
		DetailFrom: stamp(detailHorizon(now, s.options.DetailRetention, s.detailBefore)), RollupFrom: stamp(horizon(now, s.options.RollupRetention)),
		DiskEvictedBefore:   timePointer(s.evictedBefore),
		GeoCountryAvailable: s.geo.country != nil, GeoASNAvailable: s.geo.asn != nil, GeoError: s.geo.err,
		Closed: s.closed.Load(),
	}
}
