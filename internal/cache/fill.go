package cache

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
)

type fill struct {
	s          *Store
	key        *keyState
	req        Request
	epoch      uint64
	signature  string
	identity   http.Header
	plan       acquisition
	lease      *rangeLease
	extending  bool
	exclusive  bool // resource lock: an If-Range replacement stops parallel admission
	background bool
	ctx        context.Context
	cancel     context.CancelFunc
	ready      chan struct{}
	done       chan struct{}
	mu         sync.Mutex
	notify     chan struct{}
	coverage   []span
	subs       int
	consumers  int
	orphaned   bool // admission closes under mu before last-consumer cancellation
	finished   bool
	acquired   bool
	err        error
	dropOnce   sync.Once
	base       *metadata
	baseObject *object
	object     *object
	// Completion result is read only after done closes. Delivery can succeed
	// even when storage fails; an explicit Refresh must report that failure.
	published  bool
	publishErr error
	// Everything below is fixed before ready closes.
	headErr      error
	header       http.Header
	status       int
	delivery     span
	logical      *metadata
	cacheable    bool
	shareable    bool
	provenance   []span
	reused       *metadata
	reuseOutcome string
	directHTTP   *http.Response
	directDone   chan struct{}
}

func (s *Store) startFill(k *keyState, req Request, base *metadata, background bool) (*fill, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	plan := s.plan(k, req, base, background)
	if !k.canStart(plan, base) {
		return nil, ErrBusy
	}
	if background {
		select {
		case s.background <- struct{}{}:
		default:
			return nil, ErrBusy
		}
	}
	select {
	case s.fills <- struct{}{}:
	default:
		if background {
			<-s.background
		}
		return nil, ErrBusy
	}
	var pin *object
	var err error
	if base != nil {
		pin, err = s.acquire(base)
		if err != nil {
			<-s.fills
			if background {
				<-s.background
			}
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.opt.FillTimeout)
	f := &fill{s: s, key: k, req: req, epoch: k.rec.Epoch, signature: requestSignature(req), identity: req.Header, plan: plan,
		background: background, ctx: ctx, cancel: cancel, base: base, baseObject: pin,
		ready: make(chan struct{}), done: make(chan struct{}), notify: make(chan struct{})}
	if plan.extend {
		f.lease = &rangeLease{object: pin, spans: plan.writes}
		k.leases = append(k.leases, f.lease)
	}
	k.fills = append(k.fills, f)
	s.keep(k)
	s.count.fills.Add(1)
	s.wg.Add(1)
	go f.run()
	return f, nil
}

func (f *fill) finish(err error) {
	f.cancel()
	f.key.mu.Lock()
	f.mu.Lock()
	f.err, f.finished = err, true
	close(f.notify)
	cleanup := f.subs == 0
	f.mu.Unlock()
	for i, active := range f.key.fills {
		if active == f {
			f.key.fills = append(f.key.fills[:i], f.key.fills[i+1:]...)
			break
		}
	}
	f.req = Request{}
	// No old publisher remains after finish. An empty invalidation record can
	// now be reclaimed without weakening the fence during the fill's lifetime.
	if len(f.key.rec.Entries) == 0 && len(f.key.fills) == 0 {
		if f.key.cost == 0 || f.s.db.Delete(recordKey(f.key.key), pebble.Sync) == nil {
			f.s.setRecord(f.key, &record{Key: f.key.key, Epoch: f.key.rec.Epoch}, 0)
		} else {
			f.s.count.storageErrors.Add(1)
		}
	}
	f.key.signal()
	f.key.mu.Unlock()
	close(f.done)
	if cleanup {
		f.dropReferences()
	}
	<-f.s.fills
	if f.background {
		<-f.s.background
	}
	f.s.count.fills.Add(-1)
	f.s.wg.Done()
}

func (f *fill) progress(v span, acquired bool) {
	f.mu.Lock()
	f.coverage = mergeSpans(f.coverage, v)
	if acquired {
		f.acquired = true
	}
	close(f.notify)
	f.notify = make(chan struct{})
	f.mu.Unlock()
}

func (f *fill) overrides() http.Header {
	h := make(http.Header)
	if f.base == nil {
		return h
	}
	for _, k := range []string{"If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
		h[k] = nil
	}
	if f.background {
		h["Range"] = nil
	}
	base := f.base
	if f.plan.extend {
		h.Set("Range", rangeHeader(f.plan.part))
		h.Set("If-Range", strongETag(base.Header))
		return h
	}
	if base.complete() {
		// Revalidate the representation, not a client-specific range/condition.
		h["Range"] = nil
		h["If-Range"] = nil
		if tag := base.Header.Get("ETag"); tag != "" {
			h.Set("If-None-Match", tag)
		} else if modified := base.Header.Get("Last-Modified"); modified != "" {
			h.Set("If-Modified-Since", modified)
		}
	}
	return h
}

func (f *fill) run() {
	var finalErr error
	defer func() { f.finish(finalErr) }()
	overrides := f.overrides()
	resp, err := f.req.Fetch(f.ctx, overrides.Clone())
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		if f.useStale() {
			close(f.ready)
			return
		}
		f.headErr, finalErr = err, err
		close(f.ready)
		return
	}
	if resp == nil {
		f.headErr = errors.New("cache: Fetch returned nil response")
		finalErr = f.headErr
		close(f.ready)
		return
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	defer resp.Body.Close()
	// Cancellation must also interrupt an upstream that has already sent headers.
	stop := context.AfterFunc(f.ctx, func() { resp.Body.Close() })
	defer stop()
	p := f.req.Policy
	if f.req.Transform != nil {
		if err := f.req.Transform(resp, &p); err != nil {
			f.headErr, finalErr = err, err
			close(f.ready)
			return
		}
	}
	resp.Header = cleanHeader(resp.Header)
	if resp.StatusCode >= 500 && resp.StatusCode <= 599 && f.useStale() {
		close(f.ready)
		return
	}
	if resp.StatusCode == 304 && f.base != nil && (overrides.Get("If-None-Match") != "" || overrides.Get("If-Modified-Since") != "") {
		if err := f.revalidated(resp, p); err != nil {
			f.headErr, finalErr = err, err
		}
		close(f.ready)
		return
	}
	m, cacheable := responseMetadata(resp, f.req, p, time.Now())
	f.cacheable, f.shareable = cacheable, cacheable
	f.status, f.header = resp.StatusCode, resp.Header.Clone()
	expected := resp.ContentLength
	if n, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); err == nil {
		expected = n
	}
	if noBody(f.req.Method, resp.StatusCode) {
		expected = 0
		if f.req.Method == http.MethodHead || resp.StatusCode == 304 {
			f.cacheable = false
		}
	}
	internalRange := overrides.Get("Range") != "" && overrides.Get("If-Range") != ""
	if internalRange && (resp.StatusCode == 416 || resp.StatusCode == 304 ||
		(resp.StatusCode == 206 && (!cacheable || strongETag(resp.Header) == ""))) {
		f.headErr = errors.New("cache: origin cannot validate sparse completion")
		finalErr = f.headErr
		close(f.ready)
		return
	}
	if !f.cacheable || expected > f.s.opt.MaxObjectBytes {
		if !f.cacheable && f.base != nil && resp.StatusCode < 500 {
			if err := f.invalidateBase(); err != nil {
				f.s.count.storageErrors.Add(1)
			}
		}
		f.streamDirect(resp)
		return
	}
	writeSpan := span{0, expected}
	f.delivery = writeSpan
	var initial []span
	if resp.StatusCode == 206 {
		part, total, err := contentRange(resp.Header.Get("Content-Range"))
		if err != nil || (expected >= 0 && expected != part.End-part.Start) {
			f.headErr = errors.New("cache: inconsistent partial response")
			finalErr = f.headErr
			close(f.ready)
			return
		}
		writeSpan, expected, f.delivery = part, part.End-part.Start, part
		if total > f.s.opt.MaxObjectBytes {
			if internalRange {
				f.headErr = errors.New("cache: sparse completion changed representation size")
				finalErr = f.headErr
				close(f.ready)
				return
			}
			f.streamDirect(resp)
			return
		}
		if strongETag(resp.Header) == "" {
			if f.base != nil {
				if err := f.invalidateBase(); err != nil {
					f.s.count.storageErrors.Add(1)
				}
			}
			f.streamDirect(resp)
			return
		}
		if m != nil && f.cacheable {
			m.Size, m.Status = total, 200
			m.Header.Del("Content-Range")
			m.Header.Set("Content-Length", strconv.FormatInt(total, 10))
			if f.lease != nil && compatible(f.base, m) {
				f.extending = true
				initial = f.base.Coverage
				f.provenance = initial
			}
			// A synthetic fill response is safe only when the requested slice will
			// actually be covered by this generation. Never return sparse holes.
			want := span{0, total}
			if f.req.Header.Get("Range") != "" && ifRangeMatches(f.req.Header.Get("If-Range"), m.Header) {
				if r, err := parseRange(f.req.Header.Get("Range"), total); err == nil {
					want = r
				}
			}
			if covers(mergeSpans(initial, part), want) {
				f.logical = m
			}
		}
		requestedRange := f.req.Header.Get("Range")
		if values, replaced := overrides["Range"]; replaced {
			requestedRange = ""
			if len(values) > 0 {
				requestedRange = values[0]
			}
		}
		wantedPart, rangeErr := parseRange(requestedRange, total)
		if requestedRange == "" || rangeErr != nil || wantedPart != part ||
			(overrides.Get("If-Range") != "" && (!f.cacheable || !compatible(f.base, m) || f.logical == nil)) {
			f.headErr = errors.New("cache: partial response does not match the requested representation/range")
			finalErr = f.headErr
			close(f.ready)
			return
		}
	} else if m != nil {
		m.Size = expected
		if f.req.Method != http.MethodHead {
			f.logical = m
		}
	}
	if f.lease != nil && !f.extending {
		f.key.mu.Lock()
		f.exclusive = true
		f.key.mu.Unlock()
	}
	if f.extending {
		f.object = f.baseObject
		f.coverage = initial
	} else {
		o, err := f.s.newObject()
		if err != nil {
			f.headErr, finalErr = err, err
			close(f.ready)
			return
		}
		f.object = o
	}
	o := f.object
	// Keep the published base until replacement publication succeeds. A failed
	// acquisition must not remove usable bytes from later stale fallback.
	// The delivery snapshot is separate from metadata completed by the writer.
	if f.logical != nil {
		copy := *f.logical
		f.logical = &copy
	}
	f.shareable = f.cacheable
	close(f.ready)
	if expected == 0 {
		if m != nil && f.cacheable {
			m.Size = 0
			m.Coverage = nil
			f.publishErr = f.publish(m)
			if f.publishErr != nil {
				f.s.count.storageErrors.Add(1)
			}
		}
		return
	}
	buf := make([]byte, 32<<10)
	pos := writeSpan.Start
	var received int64
	for {
		if err := f.ctx.Err(); err != nil {
			finalErr = err
			return
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if received+int64(n) > f.s.opt.MaxObjectBytes || (expected >= 0 && received+int64(n) > expected) {
				finalErr = ErrObjectTooLarge
				return
			}
			parts := []span{{pos, pos + int64(n)}}
			if f.extending {
				parts = intersect(f.lease.spans, span{pos, pos + int64(n)})
			}
			newBytes := spanBytes(parts)
			if !f.s.reserve(newBytes) {
				finalErr = ErrCapacity
				return
			}
			f.s.objectsMu.Lock()
			o.bytes += newBytes
			f.s.objectsMu.Unlock()
			if f.extending {
				f.lease.bytes += newBytes
			}
			for _, part := range parts {
				if _, err := o.writeAt(buf[part.Start-pos:part.End-pos], part.Start); err != nil {
					finalErr = err
					return
				}
			}
			f.progress(span{pos, pos + int64(n)}, expected >= 0 && received+int64(n) == expected)
			pos += int64(n)
			received += int64(n)
		}
		if readErr != nil {
			if readErr != io.EOF {
				finalErr = readErr
				return
			}
			if expected >= 0 && received != expected {
				finalErr = io.ErrUnexpectedEOF
				return
			}
			break
		}
	}
	if m != nil && f.cacheable && f.req.Method != http.MethodHead {
		if resp.StatusCode != 206 {
			m.Size = received
		}
		m.Coverage = mergeSpans(initial, span{writeSpan.Start, pos})
		// A publication failure does not turn a successful stream into an error.
		f.publishErr = f.publish(m)
		if f.publishErr != nil {
			f.s.count.storageErrors.Add(1)
		}
	}
}

func (f *fill) streamDirect(resp *http.Response) {
	f.cacheable, f.shareable = false, false
	if f.background {
		close(f.ready)
		return
	}
	f.directHTTP, f.directDone = resp, make(chan struct{})
	close(f.ready)
	select {
	case <-f.directDone:
	case <-f.ctx.Done():
	}
}

func compatible(a, b *metadata) bool {
	if a == nil || a.Size != b.Size || strongETag(a.Header) == "" || strongETag(a.Header) != strongETag(b.Header) ||
		a.Header.Get("Content-Encoding") != b.Header.Get("Content-Encoding") || len(a.Vary) != len(b.Vary) {
		return false
	}
	for i := range a.Vary {
		if a.Vary[i] != b.Vary[i] || a.Values[i] != b.Values[i] {
			return false
		}
	}
	return true
}

func (f *fill) useStale() bool {
	if f.base != nil && f.base.staleAllowed(time.Now(), f.base.SIE) && canServe(f.base, f.req) {
		f.reused, f.reuseOutcome, f.shareable = f.base, "STALE", true
		return true
	}
	return false
}

func (f *fill) revalidated(resp *http.Response, p Policy) error {
	h := f.base.Header.Clone()
	if v := resp.Header.Get("ETag"); v != "" && v != f.base.Header.Get("ETag") {
		return errors.New("cache: 304 changed validator")
	}
	if v := resp.Header.Get("Content-Encoding"); v != "" && v != f.base.Header.Get("Content-Encoding") {
		return errors.New("cache: 304 changed content encoding")
	}
	if value := resp.Header.Get("Content-Length"); value != "" {
		length, err := strconv.ParseInt(value, 10, 64)
		if err != nil || length != f.base.Size {
			return errors.New("cache: 304 changed representation length")
		}
	}
	for k, v := range resp.Header {
		h[k] = append([]string(nil), v...)
	}
	// A 304's Content-Length is representation length, not an empty body length.
	h.Set("Content-Length", strconv.FormatInt(f.base.Size, 10))
	h.Del("Content-Range")
	if resp.Header.Get("Date") == "" {
		h.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	}
	if resp.Header.Get("Age") == "" {
		h.Del("Age")
	}
	updated := &http.Response{StatusCode: f.base.Status, Header: h}
	m, ok := responseMetadata(updated, f.req, p, time.Now())
	vary, validVary := varyFields(h)
	schemaChanged := !validVary || !sameVary(f.base.Vary, vary)
	if !ok || schemaChanged {
		copy := *f.base
		copy.Header = h
		m = &copy
		ok = false
	}
	m.ID, m.Size, m.Bytes, m.Inline, m.Coverage = f.base.ID, f.base.Size, f.base.Bytes, f.base.Inline, f.base.Coverage
	// Validation establishes delivery independently of metadata durability. A
	// failed commit leaves the published record untouched and is reported only
	// through the storage completion result, not as a corrupt response body.
	f.reused, f.reuseOutcome, f.shareable = m, "REVALIDATED", ok
	f.key.mu.Lock()
	defer f.key.mu.Unlock()
	if !f.targetCurrent() {
		return nil
	}
	current := f.key.rec.match(f.identity)
	m.Bytes, m.Coverage, m.AccessedAt = current.Bytes, current.Coverage, current.AccessedAt
	if !ok {
		// Vary defines the sharing boundary of the whole schema, not just the
		// request's variant. Retire siblings atomically; existing pins survive.
		f.publishErr = f.s.removeEntries(f.key, func(old metadata) bool {
			return old.ID == f.base.ID || (schemaChanged && sameVary(old.Vary, f.base.Vary))
		}, false)
		return nil
	}
	r := f.key.rec.copy()
	for i := range r.Entries {
		if r.Entries[i].ID == m.ID {
			r.Entries[i] = *m
		}
	}
	b := f.s.db.NewBatch()
	defer b.Close()
	f.publishErr = f.s.persist(f.key, r, b)
	f.published = f.publishErr == nil
	return nil
}

func (f *fill) invalidateBase() error {
	f.key.mu.Lock()
	defer f.key.mu.Unlock()
	if !f.targetCurrent() {
		return nil
	}
	return f.s.removeEntries(f.key, func(m metadata) bool { return m.ID == f.base.ID }, false)
}

func (f *fill) publishReplacement(m *metadata) error {
	o := f.object
	m.ID, m.Bytes = o.id, spanBytes(m.Coverage)
	m.Inline = m.complete() && m.Size <= f.s.opt.InlineBytes
	var inline []byte
	if m.Inline {
		inline = make([]byte, m.Size)
		if _, err := o.file.ReadAt(inline, 0); err != nil && !(err == io.EOF && len(inline) == 0) {
			return err
		}
	} else {
		if err := o.file.Sync(); err != nil {
			return err
		}
		path := f.s.bodyPath(o.id)
		if err := os.Rename(o.path, path); err != nil {
			return err
		}
		f.s.objectsMu.Lock()
		o.path = path
		f.s.objectsMu.Unlock()
		dir, err := os.Open(filepath.Dir(path))
		if err != nil {
			return err
		}
		err = dir.Sync()
		dir.Close()
		if err != nil {
			return err
		}
	}
	f.key.mu.Lock()
	defer f.key.mu.Unlock()
	if !f.targetCurrent() || f.s.closed.Load() {
		return nil
	}
	r := f.key.rec.copy()
	r.SiteID, r.Host, r.Path = f.req.SiteID, f.req.Host, f.req.Path
	kept := make([]metadata, 0, len(r.Entries)+1)
	var retired []metadata
	for _, old := range r.Entries {
		// A Vary schema change invalidates the old schema completely.
		if old.matches(f.req.Header) || !sameVary(old.Vary, m.Vary) {
			retired = append(retired, old)
		} else {
			kept = append(kept, old)
		}
	}
	// Bound variants for a resource independently of cache key cardinality.
	if len(kept) >= 16 {
		retired = append(retired, kept[0])
		kept = kept[1:]
	}
	r.Entries = append(kept, *m)
	b := f.s.db.NewBatch()
	defer b.Close()
	if m.Inline {
		if err := b.Set(bodyKey(m.ID), inline, nil); err != nil {
			return err
		}
	} else {
		if err := b.Set(fileKey(m.ID), []byte(r.Key), nil); err != nil {
			return err
		}
	}
	for _, old := range retired {
		if old.Inline {
			if err := b.Delete(bodyKey(old.ID), nil); err != nil {
				return err
			}
		} else {
			if err := b.Delete(fileKey(old.ID), nil); err != nil {
				return err
			}
		}
	}
	if err := f.s.persist(f.key, r, b); err != nil {
		return err
	}
	f.published = true
	f.s.objectsMu.Lock()
	o.obsolete = false
	if !m.Inline {
		o.temporary = false
	}
	// The same backing bytes move from acquisition ownership to publication.
	f.s.count.resident.Add(m.Bytes - o.bytes)
	o.bytes = m.Bytes
	f.s.objectsMu.Unlock()
	f.s.count.entries.Add(1 - int64(len(retired)))
	f.s.count.bytes.Add(m.Bytes)
	for _, old := range retired {
		f.s.count.bytes.Add(-old.Bytes)
		f.s.retire(old)
	}
	return nil
}

func sameVary(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
