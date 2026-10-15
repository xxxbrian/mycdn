package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

type acquisition struct {
	transport                    http.RoundTripper
	url                          url.URL
	authority, method            string
	header                       http.Header
	timeout                      time.Duration
	headerTimeout                time.Duration
	siteID, originID, host, path string
	upgrade                      bool
}

func (g *Gateway) acquisition(p *prepared, request *http.Request) (*acquisition, error) {
	transport, err := g.origins.Transport(p.origin, p.plans.Fetch.Upstream)
	if err != nil {
		return nil, err
	}
	header := p.plans.Fetch.Header.Clone()
	stripHopHeaders(header, wantsUpgrade(request))
	// Shared acquisition cannot claim a particular client identity. This also
	// makes conditional refresh independent of the request that populated it.
	a := &acquisition{transport: transport, url: *p.target, authority: p.plans.Fetch.Authority, method: request.Method, header: header, timeout: time.Duration(p.plans.Fetch.Upstream.Timeouts.RequestMS) * time.Millisecond, siteID: p.site.ID, originID: p.origin.ID, host: p.host, path: p.original.Path, upgrade: wantsUpgrade(request)}
	if p.plans.Fetch.Upstream.Protocol == "h3" {
		a.headerTimeout = time.Duration(p.plans.Fetch.Upstream.Timeouts.ResponseHeaderMS) * time.Millisecond
	}
	return a, nil
}

func (g *Gateway) fetch(a acquisition, body io.ReadCloser, contentLength int64, trailer http.Header) func(context.Context, http.Header) (*http.Response, error) {
	// a contains values and independent copies, not the first request/context.
	a.header = a.header.Clone()
	return func(ctx context.Context, overrides http.Header) (*http.Response, error) {
		select {
		case g.upstream <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { <-g.upstream }) }
		cancel := func() {}
		if a.timeout > 0 && !a.upgrade {
			ctx, cancel = context.WithTimeout(ctx, a.timeout)
		}
		request := &http.Request{Method: a.method, URL: new(url.URL), Host: a.authority, Header: a.header.Clone(), Body: body, ContentLength: contentLength, Trailer: trailer.Clone()}
		*request.URL = a.url
		for name, values := range overrides {
			request.Header.Del(name)
			for _, value := range values {
				request.Header.Add(name, value)
			}
		}
		request = request.WithContext(ctx)
		attempts := &attempts{gateway: g, acquisition: a}
		// A reused connection can fail and net/http may retry transparently. The
		// GetConn/WroteRequest trace marks each physical attempt, including failed
		// connection attempts, instead of counting one logical Fetch as one attempt.
		trace := &httptrace.ClientTrace{GetConn: func(string) { attempts.start() }, WroteRequest: func(info httptrace.WroteRequestInfo) { attempts.wrote(info.Err) }, GotFirstResponseByte: func() { attempts.firstByte() }}
		request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
		if body != nil && body != http.NoBody {
			request.Body = &attemptUpload{ReadCloser: body, attempts: attempts, sourceTrailer: trailer, trailer: request.Trailer}
		}
		var headerTimer *time.Timer
		var headerExpired atomic.Bool
		if a.headerTimeout > 0 {
			headerCtx, headerCancel := context.WithCancel(request.Context())
			previous := cancel
			cancel = func() { headerCancel(); previous() }
			request = request.WithContext(headerCtx)
			headerTimer = time.AfterFunc(a.headerTimeout, func() { headerExpired.Store(true); headerCancel() })
		}
		response, err := a.transport.RoundTrip(request)
		if headerTimer != nil {
			headerTimer.Stop()
		}
		if headerExpired.Load() && err != nil {
			err = context.DeadlineExceeded
		}
		current := attempts.last()
		if err != nil {
			if current != nil {
				current.finish(err)
			}
			cancel()
			release()
			return nil, err
		}
		if current == nil {
			current = attempts.currentAttempt()
		}
		current.status = response.StatusCode
		current.protocol = response.Proto
		if current.firstByteMS == 0 {
			current.firstByteMS = elapsedMS(time.Since(current.start))
		}
		current.meter.response(response.StatusCode, category(response.Header.Get("Content-Type")), "", nil)
		current.meter.mu.Lock()
		current.meter.event.Protocol = response.Proto
		current.meter.mu.Unlock()
		if response.Body == nil {
			response.Body = http.NoBody
		}
		measured := &originBody{ReadCloser: response.Body, attempt: current, cancel: cancel, release: release}
		if response.StatusCode != 101 && (request.Method == http.MethodHead || response.StatusCode == 204 || response.StatusCode == 304 || response.ContentLength == 0) {
			current.finish(nil)
			release()
		}
		if duplex, ok := response.Body.(io.ReadWriteCloser); ok {
			response.Body = &originDuplex{originBody: measured, writer: duplex}
		} else {
			response.Body = measured
		}
		return response, nil
	}
}

type attempts struct {
	mu          sync.Mutex
	gateway     *Gateway
	acquisition acquisition
	current     *attempt
}
type attempt struct {
	once        sync.Once
	gateway     *Gateway
	acquisition acquisition
	id          string
	start       time.Time
	status      int
	protocol    string
	meter       *meter
	wroteErr    error
	firstByteMS int64
}

func (a *attempts) start() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current != nil {
		err := a.current.wroteErr
		if err == nil {
			err = fmt.Errorf("transport retried upstream attempt")
		}
		a.current.finish(err)
	}
	a.current = a.newAttempt()
}

func (a *attempts) newAttempt() *attempt {
	id := a.gateway.nextID()
	p := a.acquisition
	return &attempt{gateway: a.gateway, acquisition: p, id: id, start: time.Now(), meter: a.gateway.sampler.add(TransferEvent{Kind: "origin", AttemptID: id, SiteID: p.siteID, OriginID: p.originID, Host: p.host, Path: p.path, Method: p.method})}
}
func (a *attempts) currentAttempt() *attempt {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current == nil {
		a.current = a.newAttempt()
	}
	return a.current
}
func (a *attempts) last() *attempt { a.mu.Lock(); defer a.mu.Unlock(); return a.current }
func (a *attempts) wrote(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current != nil {
		a.current.wroteErr = err
	}
}
func (a *attempts) firstByte() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current != nil && a.current.firstByteMS == 0 {
		a.current.firstByteMS = elapsedMS(time.Since(a.current.start))
	}
}
func (a *attempts) upload(n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current != nil {
		a.current.meter.upload.Add(int64(n))
	}
}

func (a *attempt) finish(err error) {
	a.once.Do(func() {
		now := time.Now()
		a.meter.emit(now.UnixMilli(), true)
		if a.gateway.observer == nil {
			return
		}
		p := a.acquisition
		e := OriginEvent{AttemptID: a.id, SiteID: p.siteID, OriginID: p.originID, Host: p.host, Path: p.path, Method: p.method, Protocol: a.protocol, Status: a.status, StartedAt: a.start.UnixMilli(), CompletedAt: now.UnixMilli(), DurationMS: elapsedMS(now.Sub(a.start)), FirstByteMS: a.firstByteMS, RequestBytes: a.meter.upload.Load(), ResponseBytes: a.meter.download.Load()}
		if err != nil {
			e.Error = err.Error()
		}
		a.gateway.observer.OriginCompleted(e)
	})
}

type attemptUpload struct {
	io.ReadCloser
	attempts               *attempts
	sourceTrailer, trailer http.Header
}

func (b *attemptUpload) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.attempts.upload(n)
	if errors.Is(err, io.EOF) {
		for name := range b.trailer {
			b.trailer[name] = append([]string(nil), b.sourceTrailer.Values(name)...)
		}
	}
	return n, err
}

type originBody struct {
	io.ReadCloser
	attempt *attempt
	cancel  context.CancelFunc
	ended   atomic.Bool
	release func()
}

func (b *originBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.attempt.meter.download.Add(int64(n))
	if err != nil {
		b.ended.Store(true)
		if errors.Is(err, io.EOF) {
			b.attempt.finish(nil)
		} else {
			b.attempt.finish(err)
		}
		b.cancel()
		b.release()
	}
	return n, err
}
func (b *originBody) Close() error {
	err := b.ReadCloser.Close()
	if !b.ended.Load() {
		reason := err
		if reason == nil {
			reason = context.Canceled
		}
		b.attempt.finish(reason)
	}
	b.cancel()
	b.release()
	return err
}

type originDuplex struct {
	*originBody
	writer io.Writer
}

func (b *originDuplex) Write(p []byte) (int, error) {
	n, err := b.writer.Write(p)
	b.attempt.meter.upload.Add(int64(n))
	return n, err
}
