package cache

import (
	"bytes"
	"container/list"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble"
)

var (
	ErrClosed         = errors.New("cache: closed")
	ErrBusy           = errors.New("cache: concurrency limit reached")
	ErrCapacity       = errors.New("cache: body-byte budget exhausted")
	ErrObjectTooLarge = errors.New("cache: object exceeds configured limit")
	ErrNotStored      = errors.New("cache: refresh did not publish or revalidate an entry")
)

const shardCount = 64

type keyShard struct {
	mu    sync.Mutex
	keys  map[string]*keyState
	idle  list.List
	bytes int64
}

type keyState struct {
	mu      sync.Mutex
	key     string
	rec     *record
	fills   []*fill
	leases  []*rangeLease
	changed chan struct{}
	shard   *keyShard
	refs    int
	cost    int64
	idle    *list.Element
}

type accessHint struct {
	key string
	at  time.Time
}

type counters struct {
	entries, bytes, resident, fills, readers atomic.Int64
	hits, misses, coalesced, bypasses        atomic.Int64
	evictions, storageErrors                 atomic.Int64
}

type Store struct {
	opt       Options
	db        *pebble.DB
	ctx       context.Context
	cancel    context.CancelFunc
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
	wg        sync.WaitGroup
	// Admission protects WaitGroup additions against Close; never held for I/O.
	admission   sync.RWMutex
	shards      [shardCount]keyShard
	fills       chan struct{}
	background  chan struct{}
	readers     chan struct{}
	requests    chan struct{}
	objectsMu   sync.Mutex
	objects     map[string]*object
	recentMu    sync.Mutex
	recent      map[string]accessHint
	recentBytes int64
	wake        chan struct{}
	count       counters
}

func Open(opt Options) (*Store, error) {
	if opt.Dir == "" {
		return nil, errors.New("cache: Dir is required")
	}
	if opt.MaxBytes < 0 || opt.MemoryBytes < 0 || opt.MaxFills < 0 || opt.MaxBackground < 0 ||
		opt.MaxObjectBytes < 0 || opt.InlineBytes < 0 || opt.BlockBytes < 0 || opt.MaxReaders < 0 || opt.FillTimeout < 0 {
		return nil, errors.New("cache: limits must not be negative")
	}
	if opt.MaxBytes == 0 {
		opt.MaxBytes = 1 << 30
	}
	if opt.MemoryBytes == 0 {
		opt.MemoryBytes = 16 << 20
	}
	if opt.MaxFills == 0 {
		opt.MaxFills = 32
	}
	if opt.MaxBackground == 0 {
		opt.MaxBackground = 4
	}
	if opt.MaxReaders == 0 {
		opt.MaxReaders = 4096
	}
	if opt.MaxObjectBytes == 0 {
		opt.MaxObjectBytes = min(opt.MaxBytes, 256<<20)
	}
	if opt.MaxObjectBytes > opt.MaxBytes {
		opt.MaxObjectBytes = opt.MaxBytes
	}
	if opt.InlineBytes == 0 {
		opt.InlineBytes = 32 << 10
	}
	opt.InlineBytes = min(opt.InlineBytes, opt.MaxObjectBytes, 1<<20)
	if opt.FillTimeout == 0 {
		opt.FillTimeout = 30 * time.Minute
	}
	if opt.BlockBytes == 0 {
		opt.BlockBytes = 1 << 20
	}
	for _, dir := range []string{opt.Dir, filepath.Join(opt.Dir, "bodies"), filepath.Join(opt.Dir, "tmp")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
	}
	blockCache := pebble.NewCache(max(opt.MemoryBytes/2, 1<<20))
	db, err := pebble.Open(filepath.Join(opt.Dir, "metadata"), &pebble.Options{
		Cache: blockCache, MemTableSize: uint64(max(opt.MemoryBytes/4, 64<<10)), MemTableStopWritesThreshold: 2,
	})
	blockCache.Unref()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Store{
		opt: opt, db: db, ctx: ctx, cancel: cancel,
		fills: make(chan struct{}, opt.MaxFills), background: make(chan struct{}, opt.MaxBackground),
		readers: make(chan struct{}, opt.MaxReaders), requests: make(chan struct{}, opt.MaxReaders), objects: make(map[string]*object),
		recent: make(map[string]accessHint), wake: make(chan struct{}, 1),
	}
	for i := range s.shards {
		s.shards[i].keys = make(map[string]*keyState)
	}
	if err := s.recover(); err != nil {
		cancel()
		db.Close()
		return nil, err
	}
	for s.count.resident.Load() > opt.MaxBytes {
		before := s.count.resident.Load()
		s.evict(opt.MaxBytes)
		if s.count.resident.Load() >= before {
			cancel()
			db.Close()
			return nil, ErrCapacity
		}
	}
	s.wg.Add(1)
	go s.maintain()
	return s, nil
}

func recordKey(key string) []byte { return []byte("m/" + key) }
func bodyKey(id string) []byte    { return []byte("b/" + id) }
func fileKey(id string) []byte    { return []byte("f/" + id) }

func encodeRecord(r *record) ([]byte, error) {
	var b bytes.Buffer
	err := gob.NewEncoder(&b).Encode(r)
	return b.Bytes(), err
}

func decodeRecord(b []byte) (*record, error) {
	var r record
	err := gob.NewDecoder(bytes.NewReader(b)).Decode(&r)
	return &r, err
}

func (s *Store) key(key string) *keyState {
	var hash uint64 = 1469598103934665603
	for i := range key {
		hash = (hash ^ uint64(key[i])) * 1099511628211
	}
	sh := &s.shards[hash%shardCount]
	sh.mu.Lock()
	k := sh.keys[key]
	if k == nil {
		k = &keyState{key: key, shard: sh, changed: make(chan struct{})}
		sh.keys[key] = k
	}
	if k.idle != nil {
		sh.idle.Remove(k.idle)
		k.idle = nil
	}
	k.refs++
	sh.mu.Unlock()
	return k
}

func (s *Store) keep(k *keyState) {
	k.shard.mu.Lock()
	k.refs++
	k.shard.mu.Unlock()
}

func (s *Store) releaseKey(k *keyState) {
	sh := k.shard
	sh.mu.Lock()
	k.refs--
	if k.refs == 0 {
		k.idle = sh.idle.PushFront(k)
	}
	budget := max(s.opt.MemoryBytes/2/shardCount, 1024)
	for sh.bytes > budget && sh.idle.Len() > 0 {
		e := sh.idle.Back()
		old := e.Value.(*keyState)
		sh.idle.Remove(e)
		old.idle = nil
		delete(sh.keys, old.key)
		sh.bytes -= old.cost
	}
	// Missing keys must not accumulate without using the metadata budget.
	if k.refs == 0 && k.cost == 0 && k.idle != nil {
		sh.idle.Remove(k.idle)
		k.idle = nil
		delete(sh.keys, k.key)
	}
	sh.mu.Unlock()
}

func (s *Store) setRecord(k *keyState, r *record, cost int64) {
	k.rec = r
	k.shard.mu.Lock()
	k.shard.bytes += cost - k.cost
	k.cost = cost
	k.shard.mu.Unlock()
}

func (s *Store) load(k *keyState) error {
	if k.rec != nil {
		return nil
	}
	b, closer, err := s.db.Get(recordKey(k.key))
	if errors.Is(err, pebble.ErrNotFound) {
		k.rec = &record{Key: k.key}
		return nil
	}
	if err != nil {
		return err
	}
	r, err := decodeRecord(b)
	cost := int64(len(b)) + 256
	closer.Close()
	if err == nil {
		s.setRecord(k, r, cost)
	}
	return err
}

// persist is called under the resource lock. Publication and invalidation have
// one durable linearization point, after body durability and before visibility.
func (s *Store) persist(k *keyState, r *record, b *pebble.Batch) error {
	data, err := encodeRecord(r)
	if err != nil {
		return err
	}
	if err = b.Set(recordKey(r.Key), data, nil); err == nil {
		err = b.Commit(pebble.Sync)
	}
	if err != nil {
		s.count.storageErrors.Add(1)
		return err
	}
	s.setRecord(k, r, int64(len(data))+256)
	return nil
}

func (s *Store) scan(ctx context.Context, fn func(*record) error) error {
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte("m/"), UpperBound: []byte("m0")})
	if err != nil {
		return err
	}
	defer it.Close()
	for it.First(); it.Valid(); it.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := decodeRecord(it.Value())
		if err != nil {
			return fmt.Errorf("cache metadata: %w", err)
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return it.Error()
}

func (s *Store) reserve(n int64) bool {
	for {
		old := s.count.resident.Load()
		if n > s.opt.MaxBytes-old {
			select {
			case s.wake <- struct{}{}:
			default:
			}
			return false
		}
		if s.count.resident.CompareAndSwap(old, old+n) {
			return true
		}
	}
}

func (s *Store) Stats() Stats {
	return Stats{
		Entries: s.count.entries.Load(), Bytes: s.count.bytes.Load(),
		ResidentBytes: s.count.resident.Load(), ActiveFills: s.count.fills.Load(), Readers: s.count.readers.Load(),
		Hits: s.count.hits.Load(), Misses: s.count.misses.Load(), Coalesced: s.count.coalesced.Load(),
		Bypasses: s.count.bypasses.Load(), Evictions: s.count.evictions.Load(), StorageErrors: s.count.storageErrors.Load(),
	}
}

func (s *Store) begin() error {
	s.admission.RLock()
	defer s.admission.RUnlock()
	if s.closed.Load() {
		return ErrClosed
	}
	s.wg.Add(1)
	return nil
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.admission.Lock()
		s.closed.Store(true)
		s.cancel()
		s.admission.Unlock()
		s.wg.Wait()
		s.objectsMu.Lock()
		for _, o := range s.objects {
			if o.file != nil {
				o.file.Close()
			}
			if o.temporary {
				os.Remove(o.path)
			}
		}
		s.objectsMu.Unlock()
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}
