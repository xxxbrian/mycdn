package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// A reference pins the backing storage, not a seek position. Even replacement
// and purge cannot invalidate a reader that already acquired a reference.
type object struct {
	id        string
	file      *os.File
	data      []byte
	path      string
	bytes     int64
	refs      int
	obsolete  bool
	temporary bool
	written   atomic.Int64
	readFile  func([]byte, int64) (int, error) // test boundary; installed before readers start
}

func (s *Store) bodyPath(id string) string { return filepath.Join(s.opt.Dir, "bodies", id) }

func (s *Store) acquire(m *metadata) (*object, error) {
	s.objectsMu.Lock()
	if o := s.objects[m.ID]; o != nil {
		o.refs++
		s.objectsMu.Unlock()
		return o, nil
	}
	s.objectsMu.Unlock()
	o := &object{id: m.ID, bytes: m.Bytes, refs: 1}
	if m.Inline {
		b, c, err := s.db.Get(bodyKey(m.ID))
		if err != nil {
			return nil, err
		}
		o.data = append([]byte(nil), b...)
		c.Close()
	} else {
		o.path = s.bodyPath(m.ID)
		f, err := os.OpenFile(o.path, os.O_RDWR, 0)
		if err != nil {
			return nil, err
		}
		o.file = f
	}
	// Callers own the resource lock, so no second opener can race this insert.
	s.objectsMu.Lock()
	s.objects[m.ID] = o
	s.objectsMu.Unlock()
	return o, nil
}

func (s *Store) newObject() (*object, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(token[:])
	path := filepath.Join(s.opt.Dir, "tmp", id)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o640)
	if err != nil {
		return nil, err
	}
	o := &object{id: id, file: f, path: path, refs: 1, temporary: true, obsolete: true}
	s.objectsMu.Lock()
	s.objects[id] = o
	s.objectsMu.Unlock()
	return o, nil
}

func (s *Store) release(o *object) {
	if o == nil {
		return
	}
	s.objectsMu.Lock()
	o.refs--
	if o.refs > 0 {
		s.objectsMu.Unlock()
		return
	}
	delete(s.objects, o.id)
	s.objectsMu.Unlock()
	if o.file != nil {
		o.file.Close()
	}
	if o.obsolete || o.temporary {
		os.Remove(o.path)
	}
	if o.obsolete {
		s.count.resident.Add(-o.bytes)
	}
}

// Pebble body deletion is part of the metadata batch. Files wait for pins.
func (s *Store) retire(m metadata) {
	s.objectsMu.Lock()
	if o := s.objects[m.ID]; o != nil {
		o.obsolete = true
		s.objectsMu.Unlock()
	} else {
		s.objectsMu.Unlock()
		if !m.Inline {
			os.Remove(s.bodyPath(m.ID))
		}
		s.count.resident.Add(-m.Bytes)
	}
}

func (o *object) readAt(p []byte, off int64) (int, error) {
	if o.file != nil {
		readFile := o.readFile
		if readFile != nil {
			return readFile(p, off)
		}
		return o.file.ReadAt(p, off)
	}
	if off >= int64(len(o.data)) {
		return 0, io.EOF
	}
	n := copy(p, o.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (o *object) writeAt(p []byte, off int64) (int, error) {
	n, err := o.file.WriteAt(p, off)
	o.written.Add(int64(n))
	return n, err
}

func noCachedBytes(int64) int64 { return 0 }

// start/end and provenance are the frozen delivery snapshot, not the reader's
// moving offset or the generation's subsequently expanded published coverage.
func cachedPrefix(start, end int64, provenance []span) func(int64) int64 {
	if len(provenance) == 0 {
		return noCachedBytes
	}
	return func(n int64) int64 {
		if n <= 0 {
			return 0
		}
		if end >= 0 {
			n = min(n, end-start)
		}
		var cached int64
		for _, v := range provenance {
			a := max(int64(0), v.Start-start)
			b := min(n, v.End-start)
			if b > a {
				cached += b - a
			}
		}
		return cached
	}
}

type bodyReader struct {
	s          *Store
	ctx        context.Context
	object     *object
	fill       *fill
	sub        *subscription
	off, end   int64
	provenance []span
	cached     atomic.Int64
	closed     atomic.Bool
	once       sync.Once
	done       chan struct{}
	slotOwned  bool
	hookMu     sync.Mutex
	stop       func() bool
	pinMu      sync.Mutex
	reads      int
	released   bool
	// A response owns either an object reference or a fill subscription.
	release func()
}

func (r *bodyReader) Read(p []byte) (int, error) {
	if !r.pinRead() {
		return 0, r.closedError()
	}
	defer r.unpinRead()
	if r.sub != nil {
		if !r.sub.pin() {
			return 0, r.closedError()
		}
		defer r.sub.unpin()
	}
	if err := r.ctx.Err(); err != nil {
		r.Close()
		return 0, err
	}
	if err := r.s.ctx.Err(); err != nil {
		r.Close()
		return 0, ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.end >= 0 && r.off >= r.end {
		// The last byte becoming readable is not publication/EOF. Let the
		// writer check framing and finish before dropping the last subscription.
		if r.fill != nil {
			select {
			case <-r.fill.done:
				r.fill.mu.Lock()
				err := r.fill.err
				r.fill.mu.Unlock()
				r.Close()
				if err != nil {
					return 0, err
				}
			case <-r.ctx.Done():
				r.Close()
				return 0, r.ctx.Err()
			case <-r.s.ctx.Done():
				r.Close()
				return 0, ErrClosed
			case <-r.done:
				return 0, r.closedError()
			}
		}
		r.Close()
		return 0, io.EOF
	}
	available := int64(len(p))
	if r.end >= 0 {
		available = min(available, r.end-r.off)
	}
	if r.fill != nil {
		for {
			if r.closed.Load() {
				return 0, r.closedError()
			}
			f := r.fill
			f.mu.Lock()
			coverage, finished, err, notify := f.coverage, f.finished, f.err, f.notify
			var limit int64
			for _, v := range coverage {
				if v.Start <= r.off && r.off < v.End {
					limit = v.End - r.off
					break
				}
			}
			f.mu.Unlock()
			if limit > 0 {
				available = min(available, limit)
				break
			}
			if finished {
				r.Close()
				if err != nil {
					return 0, err
				}
				return 0, io.EOF
			}
			select {
			case <-notify:
			case <-r.ctx.Done():
				r.Close()
				return 0, r.ctx.Err()
			case <-r.s.ctx.Done():
				r.Close()
				return 0, ErrClosed
			case <-r.done:
				return 0, r.closedError()
			}
		}
	}
	if r.closed.Load() {
		return 0, r.closedError()
	}
	n, err := r.object.readAt(p[:available], r.off)
	if n > 0 {
		for _, v := range r.provenance {
			a, b := max(v.Start, r.off), min(v.End, r.off+int64(n))
			if b > a {
				r.cached.Add(b - a)
			}
		}
		r.off += int64(n)
	}
	if err != nil {
		r.Close()
	}
	return n, err
}

func (r *bodyReader) closedError() error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if r.s.closed.Load() {
		return ErrClosed
	}
	return io.ErrClosedPipe
}

func (r *bodyReader) Close() error {
	r.once.Do(func() {
		r.closed.Store(true)
		close(r.done)
		r.hookMu.Lock()
		if r.stop != nil {
			r.stop()
		}
		r.hookMu.Unlock()
		// Stop acquisition promptly even when ReadAt is still unwinding. The
		// subscription keeps its lease/reference pinned until that read returns.
		if r.sub != nil {
			r.sub.close()
		}
		r.releaseIfDrained()
	})
	return nil
}

// Close signals blocked reads immediately; references/leases outlive any ReadAt
// already in progress. No mutex is held while waiting or performing file I/O.
func (r *bodyReader) pinRead() bool {
	r.pinMu.Lock()
	defer r.pinMu.Unlock()
	if r.closed.Load() {
		return false
	}
	r.reads++
	return true
}

func (r *bodyReader) unpinRead() {
	r.pinMu.Lock()
	r.reads--
	r.pinMu.Unlock()
	r.releaseIfDrained()
}

func (r *bodyReader) releaseIfDrained() {
	r.pinMu.Lock()
	release := r.closed.Load() && r.reads == 0 && !r.released
	if release {
		r.released = true
	}
	r.pinMu.Unlock()
	if release {
		r.release()
		if r.slotOwned {
			<-r.s.readers
			r.s.count.readers.Add(-1)
		}
	}
}

func (r *bodyReader) watch() {
	r.hookMu.Lock()
	r.stop = context.AfterFunc(r.ctx, func() { r.Close() })
	r.hookMu.Unlock()
}
