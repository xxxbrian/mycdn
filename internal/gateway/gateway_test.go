package gateway

import (
	"bufio"
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mycdn/internal/cache"
	"mycdn/internal/config"
	"mycdn/internal/rules"
)

type recorder struct {
	mu        sync.Mutex
	requests  []RequestEvent
	origins   []OriginEvent
	transfers []TransferEvent
}

func (r *recorder) RequestCompleted(e RequestEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, e)
}
func (r *recorder) OriginCompleted(e OriginEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.origins = append(r.origins, e)
}
func (r *recorder) Transfer(e TransferEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transfers = append(r.transfers, e)
}
func stringPointer(s string) *string { return &s }

func newGateway(t *testing.T, originURL string, edit func(*config.Document), observer Observer, objects *cache.Store) (*Gateway, *config.Store) {
	t.Helper()
	store, err := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	d := config.Default()
	d.Sites = []config.Site{{ID: "site", Name: "Test", Enabled: true, Domains: []string{"cdn.test"}, Origins: []config.Origin{{ID: "origin", URL: originURL}}, DefaultOrigin: "origin"}}
	if edit != nil {
		edit(&d)
	}
	if err := store.Save(d); err != nil {
		t.Fatal(err)
	}
	g := New(store, objects, observer)
	t.Cleanup(func() { g.Close() })
	return g, store
}

func newCache(t *testing.T) *cache.Store {
	t.Helper()
	s, err := cache.Open(cache.Options{Dir: t.TempDir(), MaxBytes: 16 << 20, MaxObjectBytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func serve(g *Gateway, method, path string, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://cdn.test"+path, nil)
	r.RemoteAddr = "192.0.2.40:1234"
	if header != nil {
		r.Header = header.Clone()
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}

func TestCacheSourceAndDeliveryAreSeparate(t *testing.T) {
	var calls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/base/asset" || r.URL.Query().Get("v") != "1" || r.Header.Get("X-Upstream") != "yes" || r.Header.Get("X-Forwarded-For") != "" {
			t.Errorf("unexpected outbound request: %s %+v", r.URL, r.Header)
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", `"one"`)
		_, _ = io.WriteString(w, "payload")
	}))
	defer origin.Close()
	obs := &recorder{}
	g, _ := newGateway(t, origin.URL+"/base", func(d *config.Document) {
		d.Rules = []rules.Rule{
			{ID: "request", Enabled: true, Stage: rules.Request, Steps: []rules.Step{
				{ID: "path", Type: "action", Action: &rules.Action{Type: "path", Path: stringPointer("/asset")}},
				{ID: "header", Type: "action", Action: &rules.Action{Type: "request_header", Header: &rules.HeaderMutation{Operation: "set", Name: "X-Upstream", Value: "yes"}}},
			}},
			{ID: "source", Enabled: true, Stage: rules.OriginResponse, Steps: []rules.Step{{ID: "fresh", Type: "action", Action: &rules.Action{Type: "source_header", Header: &rules.HeaderMutation{Operation: "set", Name: "Cache-Control", Value: "public, max-age=600"}}}, {ID: "stored", Type: "action", Action: &rules.Action{Type: "source_header", Header: &rules.HeaderMutation{Operation: "set", Name: "X-Source", Value: "stored"}}}}},
			{ID: "delivery", Enabled: true, Stage: rules.Delivery, Steps: []rules.Step{{ID: "if-hit", Type: "if", Branches: []rules.Branch{{ID: "hit", Condition: &rules.Condition{Type: "match", Field: "cache_result", Op: "eq", Values: []string{"HIT"}}, Steps: []rules.Step{{ID: "hit-header", Type: "action", Action: &rules.Action{Type: "response_header", Header: &rules.HeaderMutation{Operation: "set", Name: "X-Delivery", Value: "hit-only"}}}}}}}}},
		}
	}, obs, newCache(t))
	first := serve(g, "GET", "/original?v=1", http.Header{"X-Forwarded-For": {"spoof"}})
	second := serve(g, "GET", "/original?v=1", nil)
	if first.Code != 200 || second.Code != 200 || first.Body.String() != "payload" || second.Body.String() != "payload" {
		t.Fatalf("responses = %d %q / %d %q", first.Code, first.Body, second.Code, second.Body)
	}
	if calls.Load() != 1 || first.Header().Get("X-Delivery") != "" || second.Header().Get("X-Delivery") != "hit-only" || second.Header().Get("X-Source") != "stored" {
		t.Fatalf("cache/delivery separation: calls=%d first=%v second=%v", calls.Load(), first.Header(), second.Header())
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.origins) != 1 || len(obs.requests) != 2 || obs.requests[0].CacheResult != "MISS" || obs.requests[1].CacheResult != "HIT" || obs.requests[1].CachedBytes != 7 {
		t.Fatalf("observer = %+v %+v", obs.requests, obs.origins)
	}
	if obs.origins[0].FirstByteMS <= 0 || obs.requests[0].FirstByteMS <= 0 || obs.requests[1].FirstByteMS <= 0 {
		t.Fatal("successful response lacks first-byte measurement")
	}
	var delivered, upstream, cached int64
	for _, delta := range obs.transfers {
		if delta.Protocol == "" {
			t.Fatal("transfer lacks protocol")
		}
		if delta.Kind == "request" {
			delivered += delta.ResponseBytes
			cached += delta.CachedBytes
		} else {
			upstream += delta.ResponseBytes
			if delta.ClientIP != "" || delta.RequestID != "" {
				t.Fatal("origin transfer leaked first consumer")
			}
		}
	}
	if delivered != 14 || upstream != 7 || cached != 7 {
		t.Fatalf("transfer accounting = delivered %d upstream %d cached %d", delivered, upstream, cached)
	}
}

func TestTrustedIdentityAndExplain(t *testing.T) {
	g, _ := newGateway(t, "http://127.0.0.1:1", func(d *config.Document) {
		d.TrustedProxies = []string{"10.0.0.0/8"}
		d.Rules = []rules.Rule{{ID: "reply", Enabled: true, Stage: rules.Request, Steps: []rules.Step{{ID: "terminal", Type: "action", Action: &rules.Action{Type: "reply", Reply: &rules.Reply{Status: 202, ContentType: "text/plain", Body: "local"}}}}}, {ID: "delivery", Enabled: true, Stage: rules.Delivery, Steps: []rules.Step{{ID: "header", Type: "action", Action: &rules.Action{Type: "response_header", Header: &rules.HeaderMutation{Operation: "set", Name: "X-Delivered", Value: "yes"}}}}}}
	}, nil, nil)
	input := ExplainInput{URL: "http://cdn.test/file", Peer: "10.0.0.2:10", Header: http.Header{"X-Forwarded-For": {"203.0.113.1, 198.51.100.7, 10.0.0.1"}, "X-Forwarded-Proto": {"https"}}}
	result, err := g.Explain(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.ClientIP != "198.51.100.7" || result.Scheme != "https" || result.TerminalStatus != 202 || result.ResponseHeaders.Get("X-Delivered") != "yes" {
		t.Fatalf("explain = %+v", result)
	}
	input.Peer = "192.0.2.2:10"
	result, err = g.Explain(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.ClientIP != "192.0.2.2" || result.Scheme != "http" {
		t.Fatalf("untrusted identity = %+v", result)
	}
	response := serve(g, "GET", "/file", nil)
	if response.Code != 202 || response.Body.String() != "local" || response.Header().Get("X-Delivered") != "yes" {
		t.Fatalf("terminal not delivered: %+v", response)
	}
	head := serve(g, "HEAD", "/file", nil)
	if head.Code != 202 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "5" {
		t.Fatalf("HEAD terminal response = %d %q %v", head.Code, head.Body, head.Header())
	}
	r := httptest.NewRequest("GET", "http://unknown.test/", nil)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 421 {
		t.Fatal(w.Code)
	}
	for _, line := range []string{"203.0.113.1, garbage", "unknown", "[2001:db8::1]"} {
		if parseXFF([]string{line}) != nil {
			t.Fatalf("accepted malformed XFF %q", line)
		}
	}
	chain, proto := parseForwarded([]string{`for=198.51.100.7;proto=https, for="[2001:db8::1]:443";proto=http`})
	if len(chain) != 2 || chain[1].String() != "2001:db8::1" || proto != "http" {
		t.Fatalf("Forwarded parsing = %v %s", chain, proto)
	}
	if chain, _ := parseForwarded([]string{`for=198.51.100.7;for=203.0.113.1`}); chain != nil {
		t.Fatal("accepted duplicate Forwarded for")
	}
}

func TestSharedFillSurvivesFirstCancellation(t *testing.T) {
	started, release, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("Content-Length", "12")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "first-")
		w.(http.Flusher).Flush()
		close(started)
		select {
		case <-release:
			_, _ = io.WriteString(w, "second")
		case <-r.Context().Done():
			close(canceled)
		}
	}))
	defer origin.Close()
	obs := &recorder{}
	objects := newCache(t)
	g, _ := newGateway(t, origin.URL, nil, obs, objects)
	proxy := httptest.NewServer(g)
	defer proxy.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r1, _ := http.NewRequestWithContext(ctx, "GET", proxy.URL+"/file", nil)
	r1.Host = "cdn.test"
	response1, err := http.DefaultClient.Do(r1)
	if err != nil {
		t.Fatal(err)
	}
	defer response1.Body.Close()
	<-started
	r2, _ := http.NewRequest("GET", proxy.URL+"/file", nil)
	r2.Host = "cdn.test"
	response2, err := http.DefaultClient.Do(r2)
	if err != nil {
		t.Fatal(err)
	}
	defer response2.Body.Close()
	buf := make([]byte, 6)
	if _, err := io.ReadFull(response2.Body, buf); err != nil {
		t.Fatal(err)
	}
	cancel()
	close(release)
	rest, err := io.ReadAll(response2.Body)
	if err != nil || string(buf)+string(rest) != "first-second" {
		t.Fatalf("second consumer = %q %q %v", buf, rest, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
	select {
	case <-canceled:
		t.Fatal("first client canceled shared fill")
	default:
	}
	deadline := time.Now().Add(time.Second)
	for {
		obs.mu.Lock()
		n := len(obs.requests)
		obs.mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("request outcomes not completed")
		}
		time.Sleep(time.Millisecond)
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.origins) != 1 {
		t.Fatalf("attempts = %+v", obs.origins)
	}
	coalesced := false
	for _, event := range obs.requests {
		if event.CacheResult == "COALESCED" {
			coalesced = true
		}
	}
	if !coalesced {
		t.Fatalf("no coalesced event: %+v", obs.requests)
	}
}

func TestStreamingSamplerAndUpgrade(t *testing.T) {
	obs := &recorder{}
	sampler := newSampler(obs)
	defer sampler.close()
	m := sampler.add(TransferEvent{Kind: "request", RequestID: "long", ClientIP: "192.0.2.1", Host: "cdn.test"})
	m.download.Add(10)
	// The periodic sampler observes a long transfer before completion.
	deadline := time.Now().Add(2 * time.Second)
	for {
		obs.mu.Lock()
		n := len(obs.transfers)
		obs.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sampler did not publish in-flight delta")
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.download.Add(5)
	m.emit(time.Now().UnixMilli(), true)
	obs.mu.Lock()
	sum := int64(0)
	for _, event := range obs.transfers {
		sum += event.ResponseBytes
	}
	obs.mu.Unlock()
	if sum != 15 {
		t.Fatal(sum)
	}

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buffer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		buffer.Flush()
		line, err := buffer.ReadString('\n')
		if err == nil {
			_, _ = conn.Write([]byte(line))
		}
	}))
	defer origin.Close()
	g, _ := newGateway(t, origin.URL, nil, nil, nil)
	proxy := httptest.NewServer(g)
	defer proxy.Close()
	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(conn, "GET /socket HTTP/1.1\r\nHost: cdn.test\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	buffer := bufio.NewReader(conn)
	response, err := http.ReadResponse(buffer, &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 101 {
		t.Fatal(response.StatusCode)
	}
	fmt.Fprint(conn, "ping\n")
	line, err := buffer.ReadString('\n')
	if err != nil || line != "ping\n" {
		t.Fatalf("upgrade = %q %v", line, err)
	}
}

func TestLimitsAndSSEBypass(t *testing.T) {
	var calls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "max-age=600")
		_, _ = io.WriteString(w, "data: hi\n\n")
	}))
	defer origin.Close()
	g, _ := newGateway(t, origin.URL, func(d *config.Document) {
		d.Defaults.Request.MaxUploadBytes = 3
		d.Defaults.Request.AllowUpgrade = false
	}, nil, newCache(t))
	for i := 0; i < 2; i++ {
		response := serve(g, "GET", "/events", http.Header{"Accept": {"text/event-stream"}})
		if response.Body.String() != "data: hi\n\n" {
			t.Fatal(response.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatal("SSE was cached")
	}
	r := httptest.NewRequest("POST", "http://cdn.test/upload", strings.NewReader("large"))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 413 || calls.Load() != 2 {
		t.Fatalf("upload limit = %d calls=%d", w.Code, calls.Load())
	}
	response := serve(g, "GET", "/upgrade", http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}})
	if response.Code != 403 {
		t.Fatal(response.Code)
	}
}

func TestBackgroundRefreshUsesAcquisitionNotConsumerMetadata(t *testing.T) {
	var calls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" || r.Header.Get("X-Request-Id") != "" || r.Header.Get("Traceparent") != "" {
			t.Error("consumer metadata forwarded into shared acquisition")
		}
		w.Header().Set("ETag", `"stable"`)
		if n == 1 {
			w.Header().Set("Cache-Control", "max-age=0, stale-while-revalidate=600")
			_, _ = io.WriteString(w, "stable")
			return
		}
		if r.Header.Get("If-None-Match") != `"stable"` {
			t.Errorf("refresh validator = %q", r.Header.Get("If-None-Match"))
		}
		w.Header().Set("Cache-Control", "max-age=600")
		w.WriteHeader(304)
	}))
	defer origin.Close()
	obs := &recorder{}
	objects := newCache(t)
	g, _ := newGateway(t, origin.URL, nil, obs, objects)
	first := serve(g, "GET", "/refresh", http.Header{"X-Forwarded-For": {"203.0.113.4"}, "X-Request-Id": {"first-consumer"}, "Traceparent": {"first-trace"}})
	if first.Body.String() != "stable" {
		t.Fatal(first.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for objects.Stats().ActiveFills != 0 {
		if time.Now().After(deadline) {
			t.Fatal("initial fill not published")
		}
		time.Sleep(time.Millisecond)
	}
	stale := serve(g, "GET", "/refresh", http.Header{"X-Forwarded-For": {"198.51.100.2"}})
	if stale.Body.String() != "stable" {
		t.Fatal(stale.Body.String())
	}
	for {
		obs.mu.Lock()
		n := len(obs.origins)
		obs.mu.Unlock()
		if n == 2 && objects.Stats().ActiveFills == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background refresh not completed")
		}
		time.Sleep(time.Millisecond)
	}
	hit := serve(g, "GET", "/refresh", nil)
	if hit.Body.String() != "stable" || calls.Load() != 2 {
		t.Fatalf("refreshed response = %q calls=%d", hit.Body.String(), calls.Load())
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.requests) != 3 || obs.requests[1].CacheResult != "STALE" || obs.requests[2].CacheResult != "HIT" || obs.origins[1].Status != 304 {
		t.Fatalf("refresh events: %+v %+v", obs.requests, obs.origins)
	}
}

func TestOriginFailureAndIndependentFetchCopy(t *testing.T) {
	obs := &recorder{}
	g, store := newGateway(t, "http://127.0.0.1:1", nil, obs, nil)
	response := serve(g, "GET", "/failure", nil)
	if response.Code != 502 {
		t.Fatal(response.Code)
	}
	obs.mu.Lock()
	if len(obs.origins) != 1 || obs.origins[0].Status != 0 || obs.origins[0].Error == "" {
		t.Fatalf("failure attempt = %+v", obs.origins)
	}
	obs.mu.Unlock()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Copied") != "original" || r.Header.Get("If-None-Match") != "new" {
			t.Errorf("fetch headers = %v", r.Header)
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer origin.Close()
	d := store.Get().Document()
	d.Sites[0].Origins[0].URL = origin.URL
	if err := store.Save(d); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://cdn.test/file", nil)
	r.Header.Set("X-Copied", "original")
	r.Header.Set("If-None-Match", "old")
	p, _, err := prepare(store.Get(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, err := g.acquisition(p, r)
	if err != nil {
		t.Fatal(err)
	}
	fetch := g.fetch(*a, nil, 0, nil)
	r.Header.Set("X-Copied", "request mutated")
	p.plans.Fetch.Header.Set("X-Copied", "plan mutated")
	a.header.Set("X-Copied", "acquisition mutated")
	result, err := fetch(context.Background(), http.Header{"If-None-Match": {"new"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, result.Body)
	result.Body.Close()
}

func TestGatewayHTTP2AttemptsAndGRPCTrailers(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("origin protocol = %s", r.Proto)
		}
		if r.URL.Path == "/grpc" {
			_, _ = io.Copy(io.Discard, r.Body)
			if r.Trailer.Get("X-Upload-End") != "complete" {
				t.Errorf("upload trailer = %v", r.Trailer)
			}
			w.Header().Set("Content-Type", "application/grpc")
			w.Header().Set("Trailer", "Grpc-Status")
			_, _ = io.WriteString(w, "frame")
			w.Header().Set("Grpc-Status", "0")
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	obs := &recorder{}
	g, _ := newGateway(t, server.URL, func(d *config.Document) { d.Sites[0].Origins[0].CAFile = caFile }, obs, nil)
	for i := 0; i < 2; i++ {
		response := serve(g, "GET", "/file", nil)
		if response.Code != 200 || response.Body.String() != "ok" {
			t.Fatalf("response = %d %q", response.Code, response.Body)
		}
	}
	obs.mu.Lock()
	if len(obs.origins) != 2 {
		t.Fatalf("H2 attempts = %+v", obs.origins)
	}
	for _, event := range obs.origins {
		if event.Error != "" || event.Protocol != "HTTP/2.0" {
			t.Errorf("H2 attempt = %+v", event)
		}
	}
	obs.mu.Unlock()
	proxy := httptest.NewUnstartedServer(g)
	proxy.EnableHTTP2 = true
	proxy.StartTLS()
	defer proxy.Close()
	client := proxy.Client()
	request, _ := http.NewRequest("POST", proxy.URL+"/grpc", strings.NewReader("upload"))
	request.Host = "cdn.test"
	request.Header.Set("Content-Type", "application/grpc")
	request.Header.Set("Te", "trailers")
	request.Trailer = http.Header{"X-Upload-End": {"complete"}}
	request.ContentLength = -1
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "frame" || response.Trailer.Get("Grpc-Status") != "0" {
		t.Fatalf("gRPC delivery = %q %v %v", body, err, response.Trailer)
	}
}

func TestRequestHeaderTimeout(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer origin.Close()
	obs := &recorder{}
	g, _ := newGateway(t, origin.URL, func(d *config.Document) { d.Defaults.Upstream.Timeouts.ResponseHeaderMS = 10 }, obs, nil)
	response := serve(g, "GET", "/slow", nil)
	if response.Code != 504 {
		t.Fatalf("header timeout = %d", response.Code)
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.origins) != 1 || obs.origins[0].Error == "" {
		t.Fatalf("timeout accounting = %+v", obs.origins)
	}
}

func TestTransparentTransportRetryCountsEveryAttemptOnce(t *testing.T) {
	var calls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 2 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer origin.Close()
	obs := &recorder{}
	g, _ := newGateway(t, origin.URL, nil, obs, nil)
	for i := 0; i < 2; i++ {
		response := serve(g, "GET", "/retry", nil)
		if response.Code != 200 || response.Body.String() != "ok" {
			t.Fatalf("retry response = %d %q", response.Code, response.Body)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("physical requests = %d", calls.Load())
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.requests) != 2 || len(obs.origins) != 3 {
		t.Fatalf("retry events requests=%+v origins=%+v", obs.requests, obs.origins)
	}
	if obs.origins[1].Error == "" || obs.origins[1].Status != 0 || obs.origins[2].Status != 200 {
		t.Fatalf("retry attempt results = %+v", obs.origins)
	}
}
