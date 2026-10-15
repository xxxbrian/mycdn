package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mycdn/internal/config"
)

func TestStatsTrackActiveRequestsWithoutObserver(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			_, _ = io.WriteString(w, "ok")
		case <-r.Context().Done():
		}
	}))
	defer origin.Close()
	g, _ := newGateway(t, origin.URL, nil, nil, nil)
	initial := g.Stats()
	if initial.StartedAt <= 0 || initial.ActiveRequests != 0 || initial.Requests != 0 || initial.MaxUpstreams != config.Default().Storage.MaxFills {
		t.Fatalf("initial stats = %+v", initial)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- serve(g, "GET", "/active", nil) }()
	<-started
	active := g.Stats()
	if active.ActiveRequests != 1 || active.Requests != 1 || active.ActiveUpstreams != 1 {
		t.Fatalf("active stats = %+v", active)
	}
	close(release)
	if response := <-done; response.Code != 200 {
		t.Fatal(response.Code)
	}
	finished := g.Stats()
	if finished.ActiveRequests != 0 || finished.ActiveUpstreams != 0 || finished.Requests != 1 {
		t.Fatalf("finished stats = %+v", finished)
	}
	r := httptest.NewRequest("GET", "http://unknown.test/", nil)
	g.ServeHTTP(httptest.NewRecorder(), r)
	if rejected := g.Stats(); rejected.ActiveRequests != 0 || rejected.Requests != 2 {
		t.Fatalf("rejected stats = %+v", rejected)
	}
}

func TestDefaultFetchHasNoDeadlineButExplicitTimeoutDoes(t *testing.T) {
	g, store := newGateway(t, "http://127.0.0.1:1", nil, nil, nil)
	r := httptest.NewRequest("GET", "http://cdn.test/", nil)
	p, _, err := prepare(store.Get(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, err := g.acquisition(p, r)
	if err != nil {
		t.Fatal(err)
	}
	check := func(want bool) http.RoundTripper {
		return transportFunc(func(r *http.Request) (*http.Response, error) {
			_, got := r.Context().Deadline()
			if got != want {
				t.Errorf("fetch deadline = %v, want %v", got, want)
			}
			return &http.Response{StatusCode: 204, Header: make(http.Header), Body: http.NoBody, Proto: "HTTP/1.1"}, nil
		})
	}
	a.transport = check(false)
	response, err := g.fetch(*a, nil, 0, nil)(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	a.timeout = time.Second
	a.transport = check(true)
	response, err = g.fetch(*a, nil, 0, nil)(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
