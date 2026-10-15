package cache

import (
	"container/heap"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/cockroachdb/pebble"
)

func (s *Store) Entries(ctx context.Context, filter Filter, limit int) ([]EntryInfo, error) {
	if err := s.begin(); err != nil {
		return nil, err
	}
	defer s.wg.Done()
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 10000)
	entries := make([]EntryInfo, 0, min(limit, 100))
	err := s.scan(ctx, func(r *record) error {
		if len(entries) >= limit {
			return errListingLimit
		}
		if !filter.matches(r) {
			return nil
		}
		for _, m := range r.Entries {
			if len(entries) >= limit {
				break
			}
			at := s.lastAccess(m)
			entries = append(entries, EntryInfo{Key: r.Key, SiteID: r.SiteID, Host: r.Host, Path: r.Path,
				Size: m.Size, Status: m.Status, StoredAt: m.StoredAt, AccessedAt: at, FreshUntil: m.FreshUntil, Complete: m.complete()})
		}
		return nil
	})
	if err == errListingLimit {
		err = nil
	}
	return entries, err
}

var errListingLimit = errors.New("cache: listing limit reached")

func (s *Store) Purge(ctx context.Context, filter Filter) (int, error) {
	if err := s.begin(); err != nil {
		return 0, err
	}
	defer s.wg.Done()
	count := 0
	var err error
	// Fence in-process acquisitions before opening the persisted snapshot. A
	// fill completing during this walk is then either fenced or visible to that
	// snapshot, even if its hot metadata has already been evicted.
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		keys := make([]*keyState, 0)
		for _, k := range sh.keys {
			k.refs++
			if k.idle != nil {
				sh.idle.Remove(k.idle)
				k.idle = nil
			}
			keys = append(keys, k)
		}
		sh.mu.Unlock()
		for _, k := range keys {
			if err == nil && ctx.Err() == nil {
				k.mu.Lock()
				matching := false
				for _, f := range k.fills {
					if filter.matches(&record{SiteID: f.req.SiteID, Host: f.req.Host, Path: f.req.Path}) {
						matching = true
						break
					}
				}
				if matching {
					if loadErr := s.load(k); loadErr != nil {
						err = loadErr
					} else {
						n := len(k.rec.Entries)
						err = s.removeEntries(k, func(metadata) bool { return true }, true)
						if err == nil {
							count += n
						}
					}
				}
				k.mu.Unlock()
			}
			s.releaseKey(k)
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return count, err
	}
	// Per-resource linearization, not a globally atomic namespace transaction.
	err = s.scan(ctx, func(r *record) error {
		if !filter.matches(r) {
			return nil
		}
		k := s.key(r.Key)
		defer s.releaseKey(k)
		k.mu.Lock()
		defer k.mu.Unlock()
		if err := s.load(k); err != nil {
			return err
		}
		if !filter.matches(k.rec) {
			return nil
		}
		n := len(k.rec.Entries)
		if err := s.removeEntries(k, func(metadata) bool { return true }, true); err != nil {
			return err
		}
		count += n
		return nil
	})
	return count, err
}

func (s *Store) removeEntries(k *keyState, remove func(metadata) bool, fence bool) error {
	r := k.rec.copy()
	if fence {
		r.Epoch++
	}
	kept := make([]metadata, 0, len(r.Entries))
	var retired []metadata
	for _, m := range r.Entries {
		if remove(m) {
			retired = append(retired, m)
		} else {
			kept = append(kept, m)
		}
	}
	if len(retired) == 0 && !fence {
		return nil
	}
	r.Entries = kept
	b := s.db.NewBatch()
	defer b.Close()
	for _, m := range retired {
		if m.Inline {
			if err := b.Delete(bodyKey(m.ID), nil); err != nil {
				return err
			}
		} else {
			if err := b.Delete(fileKey(m.ID), nil); err != nil {
				return err
			}
		}
	}
	if len(kept) == 0 && len(k.fills) == 0 {
		if err := b.Delete(recordKey(k.key), nil); err != nil {
			return err
		}
		if err := b.Commit(pebble.Sync); err != nil {
			s.count.storageErrors.Add(1)
			return err
		}
		s.setRecord(k, r, 0)
	} else {
		if err := s.persist(k, r, b); err != nil {
			return err
		}
	}
	for _, m := range retired {
		s.count.entries.Add(-1)
		s.count.bytes.Add(-m.Bytes)
		s.retire(m)
	}
	k.signal()
	return nil
}

func (s *Store) touch(key, id string, now time.Time) {
	s.recentMu.Lock()
	// Drop a hint, never content identity. Bound the coalescing table explicitly.
	cost := int64(len(key) + 128)
	_, exists := s.recent[id]
	if exists || s.recentBytes+cost <= max(4096, s.opt.MemoryBytes/8) {
		if !exists {
			s.recentBytes += cost
		}
		s.recent[id] = accessHint{key, now}
	}
	s.recentMu.Unlock()
}

func (s *Store) lastAccess(m metadata) time.Time {
	s.recentMu.Lock()
	h := s.recent[m.ID]
	s.recentMu.Unlock()
	if h.at.After(m.AccessedAt) {
		return h.at
	}
	return m.AccessedAt
}

func (s *Store) flushAccess() {
	s.recentMu.Lock()
	hints := make(map[string]accessHint, len(s.recent))
	for id, h := range s.recent {
		hints[id] = h
	}
	s.recentMu.Unlock()
	byKey := make(map[string]map[string]time.Time)
	for id, h := range hints {
		if byKey[h.key] == nil {
			byKey[h.key] = make(map[string]time.Time)
		}
		byKey[h.key][id] = h.at
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// One bounded metadata batch, not one fsync per touched resource. Locks stay
	// held through commit so a delayed access write cannot undo purge/replacement.
	for start := 0; start < len(keys); start += 32 {
		batch := s.db.NewBatch()
		type update struct {
			key    *keyState
			record *record
			cost   int64
		}
		pending := make([]update, 0, 32)
		for _, key := range keys[start:min(start+32, len(keys))] {
			k := s.key(key)
			k.mu.Lock()
			if s.load(k) != nil {
				k.mu.Unlock()
				s.releaseKey(k)
				continue
			}
			r := k.rec.copy()
			changed := false
			for i := range r.Entries {
				m := &r.Entries[i]
				if at := byKey[key][m.ID]; at.After(m.AccessedAt) {
					m.AccessedAt = at
					changed = true
				}
			}
			if !changed {
				k.mu.Unlock()
				s.releaseKey(k)
				continue
			}
			data, encodeErr := encodeRecord(r)
			if encodeErr != nil {
				k.mu.Unlock()
				s.releaseKey(k)
				continue
			}
			batch.Set(recordKey(key), data, nil)
			pending = append(pending, update{k, r, int64(len(data)) + 256})
		}
		commitErr := batch.Commit(pebble.NoSync) // Access hints may be lost on crash.
		if commitErr != nil {
			s.count.storageErrors.Add(1)
		}
		for _, u := range pending {
			if commitErr == nil {
				s.setRecord(u.key, u.record, u.cost)
			}
			u.key.mu.Unlock()
			s.releaseKey(u.key)
		}
		batch.Close()
	}
	s.recentMu.Lock()
	for id, h := range hints {
		if s.recent[id] == h {
			s.recentBytes -= int64(len(h.key) + 128)
			delete(s.recent, id)
		}
	}
	s.recentMu.Unlock()
}

func (s *Store) maintain() {
	defer s.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.flushAccess()
			if s.count.resident.Load() > s.opt.MaxBytes-s.opt.MaxBytes/10 {
				s.evict(s.opt.MaxBytes - s.opt.MaxBytes/5)
			}
		case <-s.wake:
			s.evict(s.opt.MaxBytes - s.opt.MaxBytes/4)
		case <-s.ctx.Done():
			s.flushAccess()
			return
		}
	}
}

type candidate struct {
	key, id string
	at      time.Time
}
type candidates []candidate

func (c candidates) Len() int           { return len(c) }
func (c candidates) Less(i, j int) bool { return c[i].at.After(c[j].at) }
func (c candidates) Swap(i, j int)      { c[i], c[j] = c[j], c[i] }
func (c *candidates) Push(v any)        { *c = append(*c, v.(candidate)) }
func (c *candidates) Pop() any          { n := len(*c); v := (*c)[n-1]; *c = (*c)[:n-1]; return v }

func (s *Store) evict(target int64) {
	var old candidates
	err := s.scan(s.ctx, func(r *record) error {
		for _, m := range r.Entries {
			c := candidate{r.Key, m.ID, s.lastAccess(m)}
			if len(old) < 256 {
				heap.Push(&old, c)
			} else if c.at.Before(old[0].at) {
				heap.Pop(&old)
				heap.Push(&old, c)
			}
		}
		return nil
	})
	if err != nil {
		return
	}
	sort.Slice(old, func(i, j int) bool { return old[i].at.Before(old[j].at) })
	for _, c := range old {
		if s.count.resident.Load() <= target || s.ctx.Err() != nil {
			return
		}
		k := s.key(c.key)
		k.mu.Lock()
		if s.load(k) == nil {
			var selected *metadata
			for i := range k.rec.Entries {
				if k.rec.Entries[i].ID == c.id {
					selected = &k.rec.Entries[i]
				}
			}
			// Recheck current hot hints under the key lock. A batched access write
			// can never resurrect the removed generation.
			if selected != nil && !s.lastAccess(*selected).After(c.at) {
				if err := s.removeEntries(k, func(m metadata) bool { return m.ID == c.id }, false); err == nil {
					s.count.evictions.Add(1)
				}
			}
		}
		k.mu.Unlock()
		s.releaseKey(k)
	}
}

func (s *Store) recover() error {
	// A process never resumes an upstream acquisition. Only metadata-referenced,
	// fully durable coverage is recoverable; temporary/orphan files are garbage.
	err := s.scan(context.Background(), func(r *record) error {
		if len(r.Entries) == 0 {
			return s.db.Delete(recordKey(r.Key), pebble.NoSync)
		}
		kept := make([]metadata, 0, len(r.Entries))
		var lost []metadata
		for _, m := range r.Entries {
			if len(m.ID) != 32 || m.Size < 0 || m.Bytes < 0 || m.Bytes != spanBytes(m.Coverage) || len(m.Vary) != len(m.Values) {
				return errors.New("cache: invalid persisted generation")
			}
			var end int64 = -1
			for _, part := range m.Coverage {
				if part.Start < 0 || part.Start >= part.End || part.Start <= end || part.End > m.Size {
					return errors.New("cache: invalid persisted coverage")
				}
				end = part.End
			}
			present, err := s.storedBodyPresent(m)
			if err != nil {
				return err
			}
			if !present {
				lost = append(lost, m)
				continue
			}
			kept = append(kept, m)
			// A rebuildable reverse index makes orphan collection bounded-memory.
			if !m.Inline {
				if err := s.db.Set(fileKey(m.ID), []byte(r.Key), pebble.NoSync); err != nil {
					return err
				}
			}
			s.count.entries.Add(1)
			s.count.bytes.Add(m.Bytes)
			s.count.resident.Add(m.Bytes)
		}
		if len(lost) > 0 {
			// Repair only the variants with lost body data. Persist the logical
			// deletion before orphan collection removes any remaining files.
			b := s.db.NewBatch()
			defer b.Close()
			for _, m := range lost {
				key := fileKey(m.ID)
				if m.Inline {
					key = bodyKey(m.ID)
				}
				if err := b.Delete(key, nil); err != nil {
					return err
				}
			}
			if len(kept) == 0 {
				if err := b.Delete(recordKey(r.Key), nil); err != nil {
					return err
				}
			} else {
				r.Entries = kept
				data, err := encodeRecord(r)
				if err != nil {
					return err
				}
				if err := b.Set(recordKey(r.Key), data, nil); err != nil {
					return err
				}
			}
			return b.Commit(pebble.Sync)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, name := range []string{"tmp", "bodies"} {
		dir, err := os.Open(filepath.Join(s.opt.Dir, name))
		if err != nil {
			return err
		}
		for {
			entries, readErr := dir.ReadDir(256)
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				live := false
				if name == "bodies" {
					key, c, getErr := s.db.Get(fileKey(e.Name()))
					if getErr == nil {
						data, closer, recordErr := s.db.Get(recordKey(string(key)))
						if recordErr == nil {
							r, decodeErr := decodeRecord(data)
							closer.Close()
							if decodeErr != nil {
								c.Close()
								dir.Close()
								return decodeErr
							}
							for _, m := range r.Entries {
								if m.ID == e.Name() && !m.Inline {
									live = true
								}
							}
						} else if !errors.Is(recordErr, pebble.ErrNotFound) {
							c.Close()
							dir.Close()
							return recordErr
						}
						c.Close()
					} else if !errors.Is(getErr, pebble.ErrNotFound) {
						dir.Close()
						return getErr
					}
				}
				if !live {
					if err := os.Remove(filepath.Join(s.opt.Dir, name, e.Name())); err != nil {
						dir.Close()
						return err
					}
					if name == "bodies" {
						if err := s.db.Delete(fileKey(e.Name()), pebble.NoSync); err != nil {
							dir.Close()
							return err
						}
					}
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				dir.Close()
				return readErr
			}
		}
		dir.Close()
	}
	return nil
}

func (s *Store) storedBodyPresent(m metadata) (bool, error) {
	if m.Inline {
		if !m.complete() {
			return false, errors.New("cache: invalid inline coverage")
		}
		v, c, err := s.db.Get(bodyKey(m.ID))
		if errors.Is(err, pebble.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		length := int64(len(v))
		c.Close()
		if length > m.Size {
			return false, errors.New("cache: invalid inline body length")
		}
		return length == m.Size, nil
	}
	info, err := os.Stat(s.bodyPath(m.ID))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(m.Coverage) == 0 || info.Size() >= m.Coverage[len(m.Coverage)-1].End, nil
}
