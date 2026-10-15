package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mycdn/internal/config"
	"mycdn/internal/rules"
)

func TestCompletedHTTPRequestsHaveSuccessfulValidObserverEvents(t *testing.T) {
	payload := strings.Repeat("mycdn shared origin\n", 4096)
	releaseShared := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseShared) }) }
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Cache-Control", "public, max-age=600")
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		if r.URL.Path == "/shared" {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			select {
			case <-releaseShared:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = io.WriteString(w, payload)
	}))
	defer origin.Close()
	defer release()
	observer := &recorder{}
	objects := newCache(t)
	g, _ := newGateway(t, origin.URL, nil, observer, objects)
	var lateCancellations atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.ServeHTTP(w, r)
		if r.Context().Err() != nil {
			lateCancellations.Add(1)
		}
	}))
	defer proxy.Close()
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	fetch := func(path string) {
		request, _ := http.NewRequest("GET", proxy.URL+path, nil)
		request.Host = "cdn.test"
		response, err := client.Do(request)
		if err != nil {
			t.Error(err)
			return
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != payload || response.StatusCode != 200 {
			t.Errorf("HTTP delivery = %d, %d bytes, %v", response.StatusCode, len(body), err)
		}
	}
	fetch("/asset")
	fetch("/asset")
	var clients sync.WaitGroup
	for range 24 {
		clients.Go(func() { fetch("/shared") })
	}
	deadline := time.Now().Add(5 * time.Second)
	for objects.Stats().Coalesced < 23 {
		if time.Now().After(deadline) {
			t.Fatal("HTTP clients did not join shared fill")
		}
		time.Sleep(time.Millisecond)
	}
	release()
	clients.Wait()
	deadline = time.Now().Add(5 * time.Second)
	for {
		observer.mu.Lock()
		completed := len(observer.requests)
		observer.mu.Unlock()
		if completed == 26 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("request events = %d, want 26", completed)
		}
		time.Sleep(time.Millisecond)
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.origins) != 2 {
		t.Fatalf("physical attempts = %d, want 2", len(observer.origins))
	}
	for _, event := range observer.requests {
		if event.Error != "" || event.ResponseBytes != int64(len(payload)) {
			t.Errorf("completed request reported failure: %+v", event)
		}
		if event.FirstByteMS <= 0 || event.FirstByteMS > event.DurationMS {
			t.Errorf("invalid request timing: %+v", event)
		}
	}
	for _, event := range observer.origins {
		if event.Error != "" || event.ResponseBytes != int64(len(payload)) {
			t.Errorf("completed origin reported failure: %+v", event)
		}
		if event.FirstByteMS <= 0 || event.FirstByteMS > event.DurationMS {
			t.Errorf("invalid origin timing: %+v", event)
		}
	}
	t.Logf("successful HTTP deliveries observed with late request cancellation: %d", lateCancellations.Load())
}

func TestDeliveryResultOnlyIgnoresCancellationAfterProvenCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name            string
		length, written int64
		readErr, want   error
	}{
		{"complete known length", 10, 10, context.Canceled, nil},
		{"complete chunked EOF", -1, 10, io.EOF, nil},
		{"early cancel", 10, 9, context.Canceled, context.Canceled},
		{"unknown length cancel", -1, 10, context.Canceled, context.Canceled},
		{"complete length but corrupt source", 10, 10, io.ErrUnexpectedEOF, io.ErrUnexpectedEOF},
		{"complete length but source failure", 10, 10, io.ErrClosedPipe, io.ErrClosedPipe},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := deliveryBody{length: test.length, readErr: test.readErr}
			if got := body.result(ctx, test.written); !errors.Is(got, test.want) {
				t.Fatalf("delivery result = %v, want %v", got, test.want)
			}
		})
	}
	body := deliveryBody{length: 10, readErr: context.Canceled}
	if got := body.result(context.Background(), 10); !errors.Is(got, context.Canceled) {
		t.Fatalf("independent source cancellation suppressed: %v", got)
	}
}

func TestElapsedMillisecondsRoundUpConsistently(t *testing.T) {
	for _, test := range []struct {
		elapsed time.Duration
		want    int64
	}{{0, 0}, {time.Nanosecond, 1}, {999 * time.Microsecond, 1}, {time.Millisecond, 1}, {time.Millisecond + time.Nanosecond, 2}} {
		if got := elapsedMS(test.elapsed); got != test.want {
			t.Fatalf("elapsedMS(%s) = %d, want %d", test.elapsed, got, test.want)
		}
	}
}

func TestTruncatedHTTPResponseRetainsObserverErrors(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Header().Set("Cache-Control", "public, max-age=600")
		_, _ = io.WriteString(w, "short")
	}))
	defer origin.Close()
	observer := &recorder{}
	g, _ := newGateway(t, origin.URL, nil, observer, newCache(t))
	proxy := httptest.NewServer(g)
	defer proxy.Close()
	request, _ := http.NewRequest("GET", proxy.URL+"/truncated", nil)
	request.Host = "cdn.test"
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "short" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated HTTP delivery = %q %v", body, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		observer.mu.Lock()
		completed := len(observer.requests)
		observer.mu.Unlock()
		if completed == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("truncated request event missing")
		}
		time.Sleep(time.Millisecond)
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if event := observer.requests[0]; event.Error == "" || event.ResponseBytes != 5 {
		t.Fatalf("truncated request = %+v", event)
	}
	if len(observer.origins) != 1 || observer.origins[0].Error == "" {
		t.Fatalf("truncated origin = %+v", observer.origins)
	}
}

func TestCanceledHTTPResponseBeforeCompletionRetainsObserverError(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Header().Set("Cache-Control", "public, max-age=600")
		_, _ = io.WriteString(w, "start")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer origin.Close()
	observer := &recorder{}
	g, _ := newGateway(t, origin.URL, nil, observer, newCache(t))
	proxy := httptest.NewServer(g)
	defer proxy.Close()
	request, _ := http.NewRequest("GET", proxy.URL+"/canceled", nil)
	request.Host = "cdn.test"
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	prefix := make([]byte, 5)
	if _, err := io.ReadFull(response.Body, prefix); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		observer.mu.Lock()
		completed := len(observer.requests)
		observer.mu.Unlock()
		if completed == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled request event missing")
		}
		time.Sleep(time.Millisecond)
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if event := observer.requests[0]; event.Error == "" || event.ResponseBytes >= 100 {
		t.Fatalf("early canceled request = %+v", event)
	}
}

func TestSubMillisecondRequestAndOriginEventsHaveValidTiming(t *testing.T) {
	observer := &recorder{}
	g, store := newGateway(t, "http://127.0.0.1:1", nil, observer, nil)
	response := &http.Response{StatusCode: 204, Header: make(http.Header), Body: http.NoBody, ContentLength: 0, Proto: "HTTP/1.1"}
	g.proxy.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		copy := *response
		copy.Request = r
		copy.Header = response.Header.Clone()
		return &copy, nil
	})
	if result := serve(g, "GET", "/fast", nil); result.Code != 204 {
		t.Fatal(result.Code)
	}
	request := httptest.NewRequest("GET", "http://cdn.test/fast", nil)
	p, _, err := prepare(store.Get(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, err := g.acquisition(p, request)
	if err != nil {
		t.Fatal(err)
	}
	a.transport = transportFunc(func(*http.Request) (*http.Response, error) {
		copy := *response
		copy.Header = response.Header.Clone()
		return &copy, nil
	})
	result, err := g.fetch(*a, nil, 0, nil)(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result.Body.Close()
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.requests) != 1 || len(observer.origins) != 1 {
		t.Fatalf("events = %+v %+v", observer.requests, observer.origins)
	}
	if event := observer.requests[0]; event.FirstByteMS <= 0 || event.FirstByteMS > event.DurationMS || event.Error != "" {
		t.Fatalf("fast request timing = %+v", event)
	}
	if event := observer.origins[0]; event.FirstByteMS <= 0 || event.FirstByteMS > event.DurationMS || event.Error != "" {
		t.Fatalf("fast origin timing = %+v", event)
	}
}

func TestClientWriteFailureRemainsAnErrorAfterFullBodyRead(t *testing.T) {
	observer := &recorder{}
	g, _ := newGateway(t, "http://127.0.0.1:1", func(d *config.Document) {
		d.Rules = []rules.Rule{{ID: "reply", Enabled: true, Stage: rules.Request, Steps: []rules.Step{{ID: "local", Type: "action", Action: &rules.Action{Type: "reply", Reply: &rules.Reply{Status: 200, Body: "body"}}}}}}
	}, observer, nil)
	writer := &failingWriter{ResponseRecorder: httptest.NewRecorder()}
	g.ServeHTTP(writer, httptest.NewRequest("GET", "http://cdn.test/local", nil))
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.requests) != 1 || observer.requests[0].Error != io.ErrClosedPipe.Error() {
		t.Fatalf("client write error suppressed: %+v", observer.requests)
	}
}

type failingWriter struct{ *httptest.ResponseRecorder }

func (w *failingWriter) Write(p []byte) (int, error) { return len(p), io.ErrClosedPipe }
