package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func (s *Store) Do(ctx context.Context, req Request) (*Response, error) {
	if err := s.begin(); err != nil {
		return nil, err
	}
	defer s.wg.Done()
	select {
	case s.requests <- struct{}{}:
		defer func() { <-s.requests }()
	default:
		return nil, ErrBusy
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.Fetch == nil {
		return nil, errors.New("cache: Fetch is required")
	}
	if req.Method == "" {
		req.Method = http.MethodGet
	}
	if req.Key == "" || len(req.Key) > 8<<10 {
		return nil, errors.New("cache: Key must contain 1..8192 bytes")
	}
	// Callbacks outlive the request. Freeze all cache-owned inputs.
	req.Header = cloneHeader(req.Header)
	if req.Policy.StatusTTL != nil {
		ttls := make(map[int]time.Duration, len(req.Policy.StatusTTL))
		for k, v := range req.Policy.StatusTTL {
			ttls[k] = v
		}
		req.Policy.StatusTTL = ttls
	}
	if bypassRequest(req) {
		if has(cacheDirectives(req.Header), "only-if-cached") {
			s.count.bypasses.Add(1)
			return &Response{HTTP: makeHTTP(504, make(http.Header), 0), Outcome: "BYPASS", CachedBytes: func() int64 { return 0 }, CachedPrefix: noCachedBytes}, nil
		}
		return s.direct(ctx, req)
	}
	k := s.key(req.Key)
	defer s.releaseKey(k)
	signature := requestSignature(req)
	for {
		if s.closed.Load() {
			return nil, ErrClosed
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		k.mu.Lock()
		if err := s.load(k); err != nil {
			k.mu.Unlock()
			return nil, err
		}
		m := k.rec.match(req.Header)
		now := time.Now()
		usable := m != nil && canServe(m, req)
		fresh := !req.Revalidate && usable && requestFresh(m, req.Header, now)
		if fresh {
			resp, err := s.cached(ctx, req, m, "HIT", nil)
			if err == nil {
				s.count.hits.Add(1)
				s.touch(k.key, m.ID, now)
				if req.Method == http.MethodGet && req.Policy.Prefetch && now.After(m.StoredAt.Add(m.FreshUntil.Sub(m.StoredAt)*4/5)) && len(k.fills) == 0 {
					s.startFill(k, req, m, true)
				}
			}
			k.mu.Unlock()
			return resp, err
		}
		if has(cacheDirectives(req.Header), "only-if-cached") {
			k.mu.Unlock()
			s.count.bypasses.Add(1)
			return &Response{HTTP: makeHTTP(504, make(http.Header), 0), Outcome: "BYPASS", CachedBytes: func() int64 { return 0 }, CachedPrefix: noCachedBytes}, nil
		}
		if !req.Revalidate && usable && !now.Before(m.FreshUntil) && !wantsRevalidation(req.Header) && m.staleAllowed(now, m.SWR) {
			resp, err := s.cached(ctx, req, m, "STALE", nil)
			if err == nil {
				s.touch(k.key, m.ID, now)
				if req.Method == http.MethodGet && len(k.fills) == 0 {
					s.startFill(k, req, m, true)
				}
			}
			k.mu.Unlock()
			return resp, err
		}
		for _, f := range k.fills {
			if f.signature == signature && f.targetCurrent() && f.ctx.Err() == nil {
				sub, err := s.subscribe(ctx, f)
				if errors.Is(err, errFillOrphaned) {
					continue
				}
				k.mu.Unlock()
				if err != nil {
					return nil, err
				}
				resp, err := s.fromFill(ctx, req, sub, "COALESCED")
				if errors.Is(err, errRetry) {
					return s.direct(ctx, req)
				}
				if err == nil {
					s.outcome(resp.Outcome)
				}
				return resp, err
			}
		}
		plan := s.plan(k, req, m, false)
		if !k.canStart(plan, m) {
			// One consumer uses one fill plus its published snapshot. Overlap,
			// discovery and revalidation wait instead of composing subscriptions.
			changed := k.changed
			k.mu.Unlock()
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-s.ctx.Done():
				return nil, ErrClosed
			}
		}
		// A HEAD miss cannot publish a body, and must not trigger a GET implicitly.
		if req.Method == http.MethodHead && m == nil {
			k.mu.Unlock()
			return s.direct(ctx, req)
		}
		f, sub, err := s.startSubscribedFill(ctx, k, req, m)
		k.mu.Unlock()
		if err != nil {
			if f != nil {
				f.cancel()
			}
			return nil, err
		}
		resp, err := s.fromFill(ctx, req, sub, "MISS")
		if errors.Is(err, errRetry) {
			return s.direct(ctx, req)
		}
		if err == nil {
			s.outcome(resp.Outcome)
		}
		return resp, err
	}
}

// The caller holds the resource lock until initial admission is settled. A fill
// with no admitted consumer must become nonjoinable before that lock is released;
// cancellation itself stays outside both the resource and fill mutexes.
func (s *Store) startSubscribedFill(ctx context.Context, k *keyState, req Request, base *metadata) (*fill, *subscription, error) {
	f, err := s.startFill(k, req, base, false)
	if err != nil {
		return nil, nil, err
	}
	sub, err := s.subscribe(ctx, f)
	if err != nil {
		f.mu.Lock()
		f.orphaned = true
		f.mu.Unlock()
	}
	return f, sub, err
}

func canServe(m *metadata, r Request) bool {
	if r.Method == http.MethodHead || precondition(r.Header, m.Header) != 0 {
		return true
	}
	if r.Header.Get("Range") == "" || !ifRangeMatches(r.Header.Get("If-Range"), m.Header) {
		return m.complete()
	}
	want, err := parseRange(r.Header.Get("Range"), m.Size)
	if err == errMalformedRange {
		return m.complete()
	}
	return err == errUnsatisfiable || (err == nil && covers(m.Coverage, want))
}

func (s *Store) outcome(outcome string) {
	switch outcome {
	case "COALESCED":
		s.count.coalesced.Add(1)
	case "MISS":
		s.count.misses.Add(1)
	case "BYPASS":
		s.count.bypasses.Add(1)
	}
}

func (s *Store) readerSlot() error {
	select {
	case s.readers <- struct{}{}:
		s.count.readers.Add(1)
		return nil
	default:
		return ErrBusy
	}
}

func (s *Store) cached(ctx context.Context, req Request, m *metadata, outcome string, sub *subscription) (*Response, error) {
	status, h, wanted := deliveryHead(req, m.Header, m.Status, m.Size)
	age := m.age(time.Now())
	h.Set("Age", strconv.FormatInt(max(0, int64(age/time.Second)), 10))
	resp := &Response{HTTP: makeHTTP(status, h, wanted.End-wanted.Start), Outcome: outcome, Age: age, CachedBytes: func() int64 { return 0 }, CachedPrefix: noCachedBytes}
	if noBody(req.Method, status) || wanted.Start == wanted.End {
		return resp, nil
	}
	if sub == nil {
		if err := s.readerSlot(); err != nil {
			return nil, err
		}
	}
	o, err := s.acquire(m)
	if err != nil {
		if sub == nil {
			<-s.readers
			s.count.readers.Add(-1)
		}
		return nil, err
	}
	r := &bodyReader{s: s, ctx: ctx, object: o, sub: sub, off: wanted.Start, end: wanted.End,
		provenance: []span{wanted}, done: make(chan struct{}), slotOwned: sub == nil, release: func() { s.release(o) }}
	if sub != nil {
		r.release = func() { s.release(o); sub.close() }
	}
	r.watch()
	resp.HTTP.Body, resp.CachedBytes = r, r.cached.Load
	resp.CachedPrefix = cachedPrefix(wanted.Start, wanted.End, r.provenance)
	return resp, nil
}

func deliveryHead(req Request, source http.Header, status int, size int64) (int, http.Header, span) {
	h := source.Clone()
	wanted := span{0, size}
	if status == 200 || status == 203 {
		if code := precondition(req.Header, h); code != 0 {
			status = code
			wanted = span{}
		} else if size >= 0 && req.Method == http.MethodGet && req.Header.Get("Range") != "" && ifRangeMatches(req.Header.Get("If-Range"), h) {
			if r, err := parseRange(req.Header.Get("Range"), size); err == errUnsatisfiable {
				status, wanted = 416, span{}
				h.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			} else if err == nil && strings.HasPrefix(req.Header.Get("Range"), "bytes=") {
				status, wanted = 206, r
				h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", r.Start, r.End-1, size))
			}
		}
	}
	if noBody(req.Method, status) {
		if status != 304 && req.Method != http.MethodHead {
			h.Set("Content-Length", "0")
		}
		if status == 304 {
			h.Del("Content-Length")
			h.Del("Content-Range")
		}
		return status, h, wanted
	}
	if size >= 0 {
		h.Set("Content-Length", strconv.FormatInt(wanted.End-wanted.Start, 10))
	}
	return status, h, wanted
}

func noBody(method string, status int) bool {
	return method == http.MethodHead || status < 200 || status == 204 || status == 304
}

func makeHTTP(status int, header http.Header, length int64) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: header, Body: http.NoBody, ContentLength: length}
}

type directBody struct {
	io.ReadCloser
	once sync.Once
	done func()
	err  error
}

func (b *directBody) Close() error {
	b.once.Do(func() { b.err = b.ReadCloser.Close(); b.done() })
	return b.err
}

func (b *directBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.Close()
	}
	return n, err
}

func (s *Store) direct(ctx context.Context, req Request) (*Response, error) {
	select {
	case s.fills <- struct{}{}:
	default:
		return nil, ErrBusy
	}
	if err := s.readerSlot(); err != nil {
		<-s.fills
		return nil, err
	}
	s.count.fills.Add(1)
	s.wg.Add(1)
	callCtx, cancel := context.WithTimeout(ctx, s.opt.FillTimeout)
	stop := context.AfterFunc(s.ctx, cancel)
	var once sync.Once
	done := func() {
		once.Do(func() {
			stop()
			cancel()
			<-s.fills
			<-s.readers
			s.count.fills.Add(-1)
			s.count.readers.Add(-1)
			s.wg.Done()
		})
	}
	resp, err := req.Fetch(callCtx, nil)
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		done()
		return nil, err
	}
	if resp == nil {
		done()
		return nil, errors.New("cache: Fetch returned nil response")
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	if req.Transform != nil {
		p := req.Policy
		if err := req.Transform(resp, &p); err != nil {
			resp.Body.Close()
			done()
			return nil, err
		}
	}
	resp.Header = cleanHeader(resp.Header)
	b := &directBody{ReadCloser: resp.Body, done: done}
	resp.Body = b
	context.AfterFunc(callCtx, func() { b.Close() })
	s.count.bypasses.Add(1)
	return &Response{HTTP: resp, Outcome: "BYPASS", CachedBytes: func() int64 { return 0 }, CachedPrefix: noCachedBytes}, nil
}

var errRetry = errors.New("cache: retry after incompatible shared response")
var errFillOrphaned = errors.New("cache: fill no longer accepts consumers")

type subscription struct {
	f        *fill
	once     sync.Once
	hookMu   sync.Mutex
	stop     func() bool
	closed   atomic.Bool
	mu       sync.Mutex
	reads    int
	released bool
}

func (s *Store) subscribe(ctx context.Context, f *fill) (*subscription, error) {
	if err := s.readerSlot(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	if f.orphaned {
		f.mu.Unlock()
		<-s.readers
		s.count.readers.Add(-1)
		return nil, errFillOrphaned
	}
	f.subs++
	f.consumers++
	f.mu.Unlock()
	sub := &subscription{f: f}
	// Assign before registering: callback may run immediately on a canceled ctx.
	sub.hookMu.Lock()
	sub.stop = context.AfterFunc(ctx, func() { sub.close() })
	sub.hookMu.Unlock()
	return sub, nil
}

func (sub *subscription) close() {
	sub.once.Do(func() {
		sub.mu.Lock()
		sub.closed.Store(true)
		sub.mu.Unlock()
		sub.hookMu.Lock()
		if sub.stop != nil {
			sub.stop()
		}
		sub.hookMu.Unlock()
		f := sub.f
		f.mu.Lock()
		f.consumers--
		cancel := f.consumers == 0 && !f.background && !f.finished && !f.acquired
		if cancel {
			f.orphaned = true
		}
		f.mu.Unlock()
		if cancel {
			f.cancel()
		}
		sub.release()
	})
}

func (sub *subscription) pin() bool {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.closed.Load() {
		return false
	}
	sub.reads++
	return true
}

func (sub *subscription) unpin() {
	sub.mu.Lock()
	sub.reads--
	sub.mu.Unlock()
	sub.release()
}

func (sub *subscription) release() {
	sub.mu.Lock()
	release := sub.closed.Load() && sub.reads == 0 && !sub.released
	if release {
		sub.released = true
	}
	sub.mu.Unlock()
	if !release {
		return
	}
	f := sub.f
	f.mu.Lock()
	f.subs--
	cleanup := f.subs == 0 && f.finished
	f.mu.Unlock()
	if cleanup {
		f.dropReferences()
	}
	<-f.s.readers
	f.s.count.readers.Add(-1)
}

func (s *Store) fromFill(ctx context.Context, req Request, sub *subscription, outcome string) (*Response, error) {
	// Cancellation closes the subscription independently. Pin head construction
	// as well as ReadAt so it cannot reclaim the body's reference underneath us.
	if !sub.pin() {
		return nil, ctx.Err()
	}
	defer sub.unpin()
	f := sub.f
	select {
	case <-f.ready:
	case <-ctx.Done():
		sub.close()
		return nil, ctx.Err()
	case <-s.ctx.Done():
		sub.close()
		return nil, ErrClosed
	}
	if sub.closed.Load() {
		return nil, ctx.Err()
	}
	if f.headErr != nil {
		sub.close()
		return nil, f.headErr
	}
	if !f.shareable && outcome == "COALESCED" {
		sub.close()
		return nil, errRetry
	}
	if f.reused != nil {
		// The fill pins this generation until this acquisition is complete.
		k := f.key
		k.mu.Lock()
		// Pin before releasing the subscription: purge may already have removed
		// the persisted body, but the fill still owns the readable generation.
		reusedOutcome := f.reuseOutcome
		if outcome == "COALESCED" {
			reusedOutcome = outcome
		}
		resp, err := s.cached(ctx, req, f.reused, reusedOutcome, sub)
		if err == nil {
			resp.wait = f.wait
			s.touch(k.key, f.reused.ID, time.Now())
		}
		k.mu.Unlock()
		if err != nil || resp.HTTP.Body == http.NoBody {
			sub.close()
		}
		return resp, err
	}
	if f.directHTTP != nil {
		if f.directHTTP.StatusCode == 200 || f.directHTTP.StatusCode == 203 {
			if status := precondition(req.Header, f.directHTTP.Header); status != 0 {
				h := f.directHTTP.Header.Clone()
				h.Del("Content-Length")
				h.Del("Content-Range")
				sub.close()
				return &Response{HTTP: makeHTTP(status, h, 0), Outcome: "BYPASS", CachedBytes: func() int64 { return 0 }, CachedPrefix: noCachedBytes, wait: f.wait}, nil
			}
		}
		h := *f.directHTTP
		h.Header = h.Header.Clone()
		h.Request = nil
		h.Body = &directBody{ReadCloser: f.directHTTP.Body, done: func() { close(f.directDone); sub.close() }}
		return &Response{HTTP: &h, Outcome: "BYPASS", CachedBytes: func() int64 { return 0 }, CachedPrefix: noCachedBytes, wait: f.wait}, nil
	}
	status, h, wanted := f.status, f.header.Clone(), f.delivery
	if f.logical != nil {
		status, h, wanted = deliveryHead(req, f.logical.Header, f.logical.Status, f.logical.Size)
	}
	resp := &Response{HTTP: makeHTTP(status, h, wanted.End-wanted.Start), Outcome: outcome, CachedBytes: func() int64 { return 0 }, CachedPrefix: noCachedBytes, wait: f.wait}
	if !f.cacheable {
		resp.Outcome = "BYPASS"
	}
	if noBody(req.Method, status) || wanted.Start == wanted.End {
		if !f.background && (status == 304 || status == 412) && (f.status == 200 || f.status == 203 || f.status == 206) {
			// The origin supplied body bytes; a locally synthesized condition
			// must not make its acquisition disappear before publication.
			select {
			case <-f.done:
			case <-ctx.Done():
				sub.close()
				return nil, ctx.Err()
			case <-s.ctx.Done():
				sub.close()
				return nil, ErrClosed
			}
		}
		sub.close()
		return resp, nil
	}
	r := &bodyReader{s: s, ctx: ctx, object: f.object, fill: f, sub: sub, off: wanted.Start, end: wanted.End,
		provenance: f.provenance, done: make(chan struct{}), release: sub.close}
	// Subscription already owns the reader slot; bodyReader.Close must not
	// release it a second time.
	resp.HTTP.Body, resp.CachedBytes = r, r.cached.Load
	resp.CachedPrefix = cachedPrefix(wanted.Start, wanted.End, r.provenance)
	return resp, nil
}
