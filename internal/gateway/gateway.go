// Package gateway joins immutable planning, reusable transports, the object
// cache, and net/http's protocol/delivery implementation.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mycdn/internal/cache"
	"mycdn/internal/config"
	"mycdn/internal/origin"
	"mycdn/internal/rules"
)

type Gateway struct {
	config         *config.Store
	cache          *cache.Store
	origins        *origin.Manager
	observer       Observer
	sampler        *sampler
	proxy          *httputil.ReverseProxy
	sequence       atomic.Uint64
	prefix         string
	upstream       chan struct{}
	closed         atomic.Bool
	closeOnce      sync.Once
	closeErr       error
	startedAt      int64
	activeRequests atomic.Int64
	requests       atomic.Uint64
}

type requestContext struct {
	prepared     *prepared
	event        *RequestEvent
	meter        *meter
	cachedPrefix func(int64) int64
	trailer      http.Header
	started      time.Time
	delivery     *deliveryBody
}
type contextKey struct{}

func New(store *config.Store, objects *cache.Store, observer Observer) *Gateway {
	started := time.Now()
	g := &Gateway{config: store, cache: objects, origins: origin.New(), observer: observer, sampler: newSampler(observer), prefix: strconv.FormatInt(started.UnixNano(), 36), startedAt: started.UnixMilli()}
	g.upstream = make(chan struct{}, store.Get().Document().Storage.MaxFills)
	g.proxy = &httputil.ReverseProxy{
		Rewrite:        func(p *httputil.ProxyRequest) {},
		FlushInterval:  -1,
		BufferPool:     &proxyBufferPool{},
		Transport:      roundTripper{g},
		ModifyResponse: g.deliver,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			state := r.Context().Value(contextKey{}).(*requestContext)
			status := http.StatusBadGateway
			var max *http.MaxBytesError
			if errors.As(err, &max) {
				status = http.StatusRequestEntityTooLarge
			} else if errors.Is(err, context.DeadlineExceeded) {
				status = http.StatusGatewayTimeout
			}
			if r.Context().Err() != nil {
				state.event.Error = r.Context().Err().Error()
				state.event.Status = 499
				return
			}
			state.event.Error = err.Error()
			state.event.Status = status
			response := terminalResponse(&rules.Terminal{Status: status, ContentType: "text/plain; charset=utf-8", Body: http.StatusText(status) + "\n"}, r)
			_ = g.deliver(response)
			copyHeaders(w.Header(), response.Header)
			w.WriteHeader(response.StatusCode)
			if r.Method != http.MethodHead {
				_, _ = io.Copy(w, response.Body)
			}
			response.Body.Close()
		},
	}
	return g
}

func (g *Gateway) nextID() string { return g.prefix + "-" + strconv.FormatUint(g.sequence.Add(1), 36) }

// Close stops the shared transfer sampler and closes idle origin connections.
// The caller shuts down listeners first; cache/config lifetimes remain theirs.
func (g *Gateway) Close() error {
	g.closeOnce.Do(func() { g.closed.Store(true); g.sampler.close(); g.closeErr = g.origins.Close() })
	return g.closeErr
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.activeRequests.Add(1)
	g.requests.Add(1)
	defer g.activeRequests.Add(-1)
	started := time.Now()
	event := &RequestEvent{RequestID: g.nextID(), Host: r.Host, Path: r.URL.Path, Method: r.Method, StartedAt: started.UnixMilli(), ClientIP: peerAddress(r.RemoteAddr).String(), CacheResult: "BYPASS"}
	m := g.sampler.add(TransferEvent{Kind: "request", RequestID: event.RequestID, Host: event.Host, ClientIP: event.ClientIP, Path: event.Path, Method: event.Method, Protocol: r.Proto})
	writer := &responseWriter{ResponseWriter: w, meter: m, started: started}
	var state *requestContext
	defer func() {
		if event.Error == "" {
			err := writer.writeErr
			if err == nil {
				if state != nil && state.delivery != nil {
					err = state.delivery.result(r.Context(), m.download.Load())
				} else {
					err = r.Context().Err()
				}
			}
			if err != nil {
				event.Error = err.Error()
			}
		}
		now := time.Now()
		event.CompletedAt = now.UnixMilli()
		event.DurationMS = elapsedMS(now.Sub(started))
		event.RequestBytes = m.upload.Load()
		event.ResponseBytes = m.download.Load()
		if measured := writer.firstByte.Load(); measured > 0 {
			event.FirstByteMS = measured
		}
		if state != nil && state.cachedPrefix != nil {
			event.CachedBytes = state.cachedPrefix(event.ResponseBytes)
		}
		if event.Status == 0 {
			event.Status = writer.status
		}
		if event.Status == 0 {
			event.Status = 200
		}
		m.mu.Lock()
		m.event.Status = event.Status
		m.event.ContentType = event.ContentType
		m.event.CacheResult = event.CacheResult
		m.mu.Unlock()
		m.emit(now.UnixMilli(), true)
		if g.observer != nil {
			g.observer.RequestCompleted(*event)
		}
	}()
	if g.closed.Load() {
		http.Error(writer, "gateway closed", http.StatusServiceUnavailable)
		return
	}
	p, status, err := prepare(g.config.Get(), r, nil)
	if err != nil {
		event.Error = err.Error()
		http.Error(writer, http.StatusText(status), status)
		return
	}
	event.SiteID, event.Host, event.ClientIP = p.site.ID, p.host, p.clientIP
	m.mu.Lock()
	m.event.SiteID = p.site.ID
	m.event.Host = p.host
	m.event.ClientIP = p.clientIP
	m.mu.Unlock()
	state = &requestContext{prepared: p, event: event, meter: m, trailer: r.Trailer, started: started}
	request := r.Clone(context.WithValue(r.Context(), contextKey{}, state))
	if request.Body != nil && request.Body != http.NoBody {
		request.Body = &countedBody{ReadCloser: request.Body, counter: &m.upload}
	}
	if limit := p.plans.Request.Limits.MaxUploadBytes; limit > 0 {
		if r.ContentLength > limit {
			p.plans.Terminal = &rules.Terminal{Status: 413, ContentType: "text/plain; charset=utf-8", Body: "request body too large\n"}
		} else if request.Body != nil {
			request.Body = http.MaxBytesReader(writer, request.Body, limit)
		}
	}
	if wantsUpgrade(request) && !p.plans.Request.Limits.AllowUpgrade {
		p.plans.Terminal = &rules.Terminal{Status: 403, ContentType: "text/plain; charset=utf-8", Body: "upgrades disabled\n"}
	}
	g.proxy.ServeHTTP(writer, request)
}

type roundTripper struct{ gateway *Gateway }

func (t roundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	g := t.gateway
	state := request.Context().Value(contextKey{}).(*requestContext)
	p := state.prepared
	if p.plans.Terminal != nil {
		return terminalResponse(p.plans.Terminal, request), nil
	}
	a, err := g.acquisition(p, request)
	if err != nil {
		return nil, err
	}
	transform := sourceTransform(p.snapshot.Program(), p.facts, p.plans.Content)
	policy := cachePolicy(p.plans.Content.Cache)
	if g.cache == nil || uncacheable(request) || policy.Mode == "bypass" {
		response, err := g.fetch(*a, request.Body, request.ContentLength, state.trailer)(request.Context(), nil)
		if err != nil {
			return nil, err
		}
		if err = transform(response, &policy); err != nil {
			response.Body.Close()
			return nil, err
		}
		response.Request = request
		state.event.CacheResult = "BYPASS"
		return response, nil
	}
	response, err := g.cache.Do(request.Context(), cache.Request{Key: p.key, SiteID: p.site.ID, Host: p.host, Path: p.original.Path, Method: request.Method, Header: a.header.Clone(), Policy: policy, Fetch: g.fetch(*a, nil, 0, nil), Transform: transform})
	if err != nil {
		return nil, err
	}
	state.event.CacheResult = response.Outcome
	state.cachedPrefix = response.CachedPrefix
	response.HTTP.Request = request
	return response.HTTP, nil
}

func (g *Gateway) deliver(response *http.Response) error {
	state := response.Request.Context().Value(contextKey{}).(*requestContext)
	p := state.prepared
	plans := p.plans.Clone()
	plans.Terminal = nil
	facts := p.facts
	facts.Status = response.StatusCode
	facts.ResponseHeader = response.Header.Clone()
	facts.CacheResult = state.event.CacheResult
	p.snapshot.Program().Run(rules.Delivery, facts, &plans, nil)
	if plans.Terminal != nil {
		response.Body.Close()
		state.cachedPrefix = nil
		replacement := terminalResponse(plans.Terminal, response.Request)
		*response = *replacement
	}
	response.Header = response.Header.Clone()
	for _, mutation := range plans.Delivery.Headers {
		rules.ApplyHeader(response.Header, mutation)
	}
	if plans.Delivery.BrowserTTL != nil {
		response.Header.Set("Cache-Control", "public, max-age="+strconv.FormatInt(*plans.Delivery.BrowserTTL, 10))
		response.Header.Del("Expires")
	}
	if plans.Delivery.CORS != nil {
		applyCORS(response.Header, p.original.Header, *plans.Delivery.CORS)
	}
	state.event.Status = response.StatusCode
	state.event.ContentType = category(response.Header.Get("Content-Type"))
	state.meter.response(response.StatusCode, state.event.ContentType, state.event.CacheResult, state.cachedPrefix)
	if response.StatusCode == http.StatusSwitchingProtocols {
		state.event.FirstByteMS = elapsedMS(time.Since(state.started))
		if duplex, ok := response.Body.(io.ReadWriteCloser); ok {
			response.Body = &clientDuplex{ReadWriteCloser: duplex, meter: state.meter}
		}
	} else {
		length := response.ContentLength
		if response.Request.Method == http.MethodHead || response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusNotModified {
			length = 0
		}
		state.delivery = &deliveryBody{ReadCloser: response.Body, length: length}
		response.Body = state.delivery
	}
	return nil
}

func terminalResponse(t *rules.Terminal, request *http.Request) *http.Response {
	header := make(http.Header)
	if t.ContentType != "" {
		header.Set("Content-Type", t.ContentType)
	}
	if t.Location != "" {
		header.Set("Location", t.Location)
	}
	body := t.Body
	if t.Status == 204 || t.Status == 304 {
		body = ""
	}
	length := int64(len(body))
	var reader io.ReadCloser = io.NopCloser(strings.NewReader(body))
	if request.Method == http.MethodHead {
		reader = http.NoBody
		header.Set("Content-Length", strconv.FormatInt(length, 10))
	}
	return &http.Response{StatusCode: t.Status, Status: fmt.Sprintf("%d %s", t.Status, http.StatusText(t.Status)), Header: header, Body: reader, ContentLength: length, Request: request, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1}
}

func copyHeaders(to, from http.Header) {
	for name, values := range from {
		to[name] = append([]string(nil), values...)
	}
}
func category(value string) string {
	typ, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "unknown"
	}
	return strings.ToLower(typ)
}

func applyCORS(header, request http.Header, c rules.CORS) {
	for _, candidate := range c.Origins {
		if candidate != "*" {
			mergeVary(header, "Origin")
			break
		}
	}
	origin := request.Get("Origin")
	if origin == "" {
		return
	}
	allowed := ""
	for _, candidate := range c.Origins {
		if candidate == "*" || candidate == origin {
			allowed = candidate
			break
		}
	}
	if allowed == "" {
		return
	}
	header.Set("Access-Control-Allow-Origin", allowed)
	if c.Credentials {
		header.Set("Access-Control-Allow-Credentials", "true")
	}
	if len(c.Methods) > 0 {
		header.Set("Access-Control-Allow-Methods", strings.Join(c.Methods, ", "))
	}
	if len(c.Headers) > 0 {
		header.Set("Access-Control-Allow-Headers", strings.Join(c.Headers, ", "))
	}
	if len(c.Expose) > 0 {
		header.Set("Access-Control-Expose-Headers", strings.Join(c.Expose, ", "))
	}
	if c.MaxAge > 0 {
		header.Set("Access-Control-Max-Age", strconv.FormatInt(c.MaxAge, 10))
	}
}

func mergeVary(header http.Header, name string) {
	var values []string
	seen := make(map[string]bool)
	for _, line := range header.Values("Vary") {
		for _, value := range strings.Split(line, ",") {
			value = http.CanonicalHeaderKey(strings.TrimSpace(value))
			if value != "" && !seen[value] {
				values = append(values, value)
				seen[value] = true
			}
		}
	}
	if !seen[name] {
		values = append(values, name)
	}
	header.Set("Vary", strings.Join(values, ", "))
}

type clientDuplex struct {
	io.ReadWriteCloser
	meter *meter
}

type deliveryBody struct {
	io.ReadCloser
	length  int64
	readErr error
}

func (b *deliveryBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.readErr = err
	}
	return n, err
}

func (b *deliveryBody) result(ctx context.Context, written int64) error {
	complete := b.length >= 0 && written == b.length || b.length < 0 && b.readErr == io.EOF
	if b.readErr != nil && b.readErr != io.EOF {
		// A client can close as soon as Content-Length bytes arrive, before
		// ReverseProxy's final read observes EOF/publication. That late cancel
		// does not undo successful delivery. All other read errors stay errors.
		if !(complete && ctx.Err() != nil && errors.Is(b.readErr, context.Canceled)) {
			return b.readErr
		}
	}
	if !complete {
		if err := ctx.Err(); err != nil {
			return err
		}
		if b.readErr == io.EOF && b.length >= 0 {
			return io.ErrUnexpectedEOF
		}
	}
	return nil
}

func (b *clientDuplex) Read(p []byte) (int, error) {
	n, err := b.ReadWriteCloser.Read(p)
	b.meter.download.Add(int64(n))
	return n, err
}
func (b *clientDuplex) Write(p []byte) (int, error) {
	n, err := b.ReadWriteCloser.Write(p)
	b.meter.upload.Add(int64(n))
	return n, err
}
