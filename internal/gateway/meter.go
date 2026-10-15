package gateway

import (
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type sampler struct {
	observer Observer
	mu       sync.Mutex
	meters   map[*meter]struct{}
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

type meter struct {
	sampler                      *sampler
	mu                           sync.Mutex
	event                        TransferEvent
	upload                       atomic.Int64
	download                     atomic.Int64
	cachedPrefix                 func(int64) int64
	lastUp, lastDown, lastCached int64
	closed                       bool
}

func newSampler(observer Observer) *sampler {
	s := &sampler{observer: observer, meters: make(map[*meter]struct{}), stop: make(chan struct{}), done: make(chan struct{})}
	if observer == nil {
		close(s.done)
		return s
	}
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				s.flush(now.UnixMilli())
			case <-s.stop:
				s.flush(time.Now().UnixMilli())
				return
			}
		}
	}()
	return s
}

func (s *sampler) add(event TransferEvent) *meter {
	event.IntervalStart = time.Now().UnixMilli()
	m := &meter{sampler: s, event: event}
	if s.observer != nil {
		s.mu.Lock()
		s.meters[m] = struct{}{}
		s.mu.Unlock()
	}
	return m
}

func (s *sampler) flush(now int64) {
	s.mu.Lock()
	meters := make([]*meter, 0, len(s.meters))
	for m := range s.meters {
		meters = append(meters, m)
	}
	s.mu.Unlock()
	for _, m := range meters {
		m.emit(now, false)
	}
}

func (m *meter) emit(now int64, finish bool) {
	if m.sampler.observer == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	u, d := m.upload.Load(), m.download.Load()
	c := int64(0)
	if m.cachedPrefix != nil {
		c = m.cachedPrefix(d)
	}
	e := m.event
	e.IntervalEnd = now
	e.RequestBytes = u - m.lastUp
	e.ResponseBytes = d - m.lastDown
	e.CachedBytes = c - m.lastCached
	if e.RequestBytes != 0 || e.ResponseBytes != 0 || e.CachedBytes != 0 {
		m.sampler.observer.Transfer(e)
	}
	m.lastUp, m.lastDown, m.lastCached = u, d, c
	m.event.IntervalStart = now
	if finish {
		m.closed = true
		m.sampler.mu.Lock()
		delete(m.sampler.meters, m)
		m.sampler.mu.Unlock()
	}
}

func (m *meter) response(status int, contentType, cacheResult string, cachedPrefix func(int64) int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.event.Status = status
	m.event.ContentType = contentType
	m.event.CacheResult = cacheResult
	m.cachedPrefix = cachedPrefix
}

func (s *sampler) close() { s.once.Do(func() { close(s.stop); <-s.done }) }

type countedBody struct {
	io.ReadCloser
	counter *atomic.Int64
}

func (b *countedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.counter.Add(int64(n))
	return n, err
}

// responseWriter only observes successful client operations. ReverseProxy still
// owns delivery/framing; no response content is captured for caching here.
type responseWriter struct {
	http.ResponseWriter
	meter     *meter
	status    int
	started   time.Time
	firstByte atomic.Int64
	writeErr  error
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseWriter) WriteHeader(status int) {
	if status >= 200 && w.status == 0 {
		w.status = status
		w.firstByte.CompareAndSwap(0, elapsedMS(time.Since(w.started)))
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.meter.download.Add(int64(n))
	if err != nil {
		w.writeErr = err
	} else if n != len(p) {
		w.writeErr = io.ErrShortWrite
	}
	return n, err
}
func (w *responseWriter) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }
