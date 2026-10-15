package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mycdn/internal/cache"
	"mycdn/internal/config"
	"mycdn/internal/rules"
)

func TestManualRefreshReplansPreservesOldAndRevalidates(t *testing.T) {
	var version atomic.Int64
	version.Store(1)
	var calls atomic.Int64
	refreshStarted, releaseRefresh := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRefresh) }) }
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if r.URL.Path != "/base/asset" || r.URL.Query().Get("token") != "private-query" {
			t.Error("request did not use the planned origin URL")
		}
		if call == 1 {
			if r.Header.Get("X-Consumer") != "first" || r.Header.Get("X-Plan") != "old" {
				t.Error("initial request headers differ")
			}
		} else {
			if r.Header.Get("X-Consumer") != "" || r.Header.Get("X-Request-Id") != "" || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Plan") != "new" {
				t.Errorf("manual refresh retained old consumer/plan headers: %+v", r.Header)
			}
			if (r.Header.Get("If-None-Match") != `"v1"` && call == 2) || (r.Header.Get("If-None-Match") != `"v2"` && call == 3) {
				t.Errorf("manual refresh validator = %q", r.Header.Get("If-None-Match"))
			}
		}
		if call == 2 {
			close(refreshStarted)
			select {
			case <-releaseRefresh:
			case <-r.Context().Done():
				return
			}
		}
		etag := fmt.Sprintf(`"v%d"`, version.Load())
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/plain")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = fmt.Fprintf(w, "body-%d", version.Load())
	}))
	defer origin.Close()
	defer release()
	obs := &recorder{}
	objects := newCache(t)
	g, store := newGateway(t, origin.URL+"/base", func(d *config.Document) {
		d.Rules = []rules.Rule{
			{ID: "request", Enabled: true, Stage: rules.Request, Steps: []rules.Step{
				{ID: "path", Type: "action", Action: &rules.Action{Type: "path", Path: stringPointer("/asset")}},
				{ID: "header", Type: "action", Action: &rules.Action{Type: "request_header", Header: &rules.HeaderMutation{Operation: "set", Name: "X-Plan", Value: "old"}}},
			}},
			{ID: "source", Enabled: true, Stage: rules.OriginResponse, Steps: []rules.Step{
				{ID: "freshness", Type: "action", Action: &rules.Action{Type: "source_header", Header: &rules.HeaderMutation{Operation: "set", Name: "Cache-Control", Value: "public, max-age=600"}}},
				{ID: "source", Type: "action", Action: &rules.Action{Type: "source_header", Header: &rules.HeaderMutation{Operation: "set", Name: "X-Source", Value: "old"}}},
			}},
		}
	}, obs, objects)
	// Complete the first consumer, then cancel it. Manual refresh cannot depend
	// on that canceled context or on any of its arbitrary request headers.
	firstContext, cancelFirst := context.WithCancel(context.Background())
	request := httptest.NewRequest("GET", "http://cdn.test/original?token=private-query", nil).WithContext(firstContext)
	request.RemoteAddr = "192.0.2.40:1234"
	request.Header.Set("X-Consumer", "first")
	request.Header.Set("X-Request-Id", "first-client")
	first := httptest.NewRecorder()
	g.ServeHTTP(first, request)
	if first.Code != 200 || first.Body.String() != "body-1" {
		t.Fatalf("initial response = %d %q", first.Code, first.Body)
	}
	waitForPublication(t, objects)
	cancelFirst()
	d := store.Get().Document()
	d.Rules[0].Steps[1].Action.Header.Value = "new"
	d.Rules[1].Steps[1].Action.Header.Value = "new"
	if err := store.Save(d); err != nil {
		t.Fatal(err)
	}
	version.Store(2)
	url := "http://cdn.test/original?token=private-query"
	type completion struct {
		results []RefreshResult
		err     error
	}
	done := make(chan completion, 1)
	go func() {
		var inheritedTrace atomic.Int64
		ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{GetConn: func(string) { inheritedTrace.Add(1) }})
		results, err := g.Refresh(ctx, []string{url}, "site")
		if inheritedTrace.Load() != 0 {
			t.Error("manual origin inherited caller trace")
		}
		done <- completion{results, err}
	}()
	select {
	case <-refreshStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("manual refresh did not start while old entry was fresh")
	}
	old := serve(g, "GET", "/original?token=private-query", nil)
	if old.Code != 200 || old.Body.String() != "body-1" {
		t.Fatalf("old entry unavailable during refresh: %d %q", old.Code, old.Body)
	}
	release()
	var finished completion
	select {
	case finished = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("manual refresh did not complete")
	}
	if finished.err != nil || len(finished.results) != 1 || finished.results[0].Status != "refreshed" || finished.results[0].Error != "" {
		t.Fatalf("refresh result = %+v %v", finished.results, finished.err)
	}
	updated := serve(g, "GET", "/original?token=private-query", nil)
	if updated.Code != 200 || updated.Body.String() != "body-2" || updated.Header().Get("X-Source") != "new" || calls.Load() != 2 {
		t.Fatalf("updated entry = %d %q %v calls=%d", updated.Code, updated.Body, updated.Header(), calls.Load())
	}
	results, err := g.Refresh(context.Background(), []string{url}, "site")
	if err != nil || len(results) != 1 || results[0].Status != "refreshed" || calls.Load() != 3 {
		t.Fatalf("conditional refresh = %+v %v calls=%d", results, err, calls.Load())
	}
	validated := serve(g, "GET", "/original?token=private-query", nil)
	if validated.Body.String() != "body-2" || calls.Load() != 3 {
		t.Fatalf("conditional response = %q calls=%d", validated.Body, calls.Load())
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.requests) != 4 || len(obs.origins) != 3 || obs.origins[2].Status != 304 {
		t.Fatalf("manual refresh accounting: requests=%+v origins=%+v", obs.requests, obs.origins)
	}
	for _, event := range obs.transfers {
		if event.Kind == "origin" && (event.RequestID != "" || event.ClientIP != "") {
			t.Fatal("manual origin analytics gained consumer identity")
		}
	}
}

func waitForPublication(t *testing.T, objects *cache.Store) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for objects.Stats().ActiveFills != 0 {
		if time.Now().After(deadline) {
			t.Fatal("fill did not publish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRefreshValidatesWholeBatchBeforeAnyOriginWork(t *testing.T) {
	var calls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "max-age=600")
		_, _ = io.WriteString(w, "ok")
	}))
	defer origin.Close()
	g, _ := newGateway(t, origin.URL, nil, nil, newCache(t))
	for _, test := range []struct {
		name string
		urls []string
		site string
	}{
		{"empty", nil, ""},
		{"batch limit", make([]string, 33), ""},
		{"unknown host", []string{"http://cdn.test/valid", "http://unknown.test/?token=secret"}, ""},
		{"relative", []string{"http://cdn.test/valid", "/relative?token=secret"}, ""},
		{"credentials", []string{"http://cdn.test/valid", "http://user:secret@cdn.test/"}, ""},
		{"fragment", []string{"http://cdn.test/valid", "http://cdn.test/#secret"}, ""},
		{"query", []string{"http://cdn.test/valid", "http://cdn.test/?secret=%zz"}, ""},
		{"site binding", []string{"http://cdn.test/valid"}, "different-site"},
		{"scheme", []string{"http://cdn.test/valid", "file://cdn.test/secret"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			results, err := g.Refresh(context.Background(), test.urls, test.site)
			if err == nil || results != nil {
				t.Fatalf("invalid batch result = %+v %v", results, err)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "token") {
				t.Fatalf("validation error leaked input: %s", err)
			}
			if calls.Load() != 0 {
				t.Fatal("invalid batch had acquisition side effects")
			}
		})
	}
}

func TestRefreshSkipsLocalAndBypassPlansAndReportsFailures(t *testing.T) {
	g, _ := newGateway(t, "http://127.0.0.1:1", func(d *config.Document) {
		d.Rules = []rules.Rule{{ID: "skip", Enabled: true, Stage: rules.Request, Steps: []rules.Step{{ID: "if", Type: "if", Branches: []rules.Branch{
			{ID: "local", Condition: &rules.Condition{Type: "match", Field: "path", Op: "eq", Values: []string{"/local"}}, Steps: []rules.Step{{ID: "reply", Type: "action", Action: &rules.Action{Type: "reply", Reply: &rules.Reply{Status: 200, Body: "local"}}}}},
			{ID: "bypass", Condition: &rules.Condition{Type: "match", Field: "path", Op: "eq", Values: []string{"/bypass"}}, Steps: []rules.Step{{ID: "cache", Type: "action", Action: &rules.Action{Type: "cache", Cache: &rules.CachePatch{Mode: stringPointer("bypass")}}}}},
		}}}}}
	}, nil, newCache(t))
	results, err := g.Refresh(context.Background(), []string{"http://cdn.test/local?token=secret", "http://cdn.test/bypass", "http://cdn.test/error?token=secret"}, "site")
	if err != nil || len(results) != 3 {
		t.Fatalf("valid batch = %+v %v", results, err)
	}
	for i := 0; i < 2; i++ {
		if results[i].Status != "skipped" || results[i].Reason == "" || results[i].Error != "" {
			t.Fatalf("skipped result = %+v", results[i])
		}
	}
	if results[2].Status != "error" || results[2].Error == "" || strings.Contains(results[2].Error, "secret") || strings.Contains(results[2].Error, "http") {
		t.Fatalf("failure result = %+v", results[2])
	}
	if stats := g.Stats(); stats.Requests != 0 || stats.ActiveRequests != 0 {
		t.Fatalf("manual refresh entered public handler: %+v", stats)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := g.Refresh(ctx, []string{"http://cdn.test/error"}, ""); !errors.Is(err, context.Canceled) || result != nil {
		t.Fatalf("pre-canceled batch = %+v %v", result, err)
	}
	for _, err := range []error{errors.New("https://origin.test/?token=secret"), fmt.Errorf("wrapped %w", errors.New("https://origin.test/?token=secret"))} {
		if message := refreshError(err); strings.Contains(message, "secret") {
			t.Fatalf("operational error leaked query: %s", message)
		}
	}
}

func TestRefreshDoesNotClaimSuccessForUnstoredResponse(t *testing.T) {
	var calls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Cache-Control", "public, max-age=600")
			_, _ = io.WriteString(w, "old")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "private")
	}))
	defer origin.Close()
	objects := newCache(t)
	g, _ := newGateway(t, origin.URL, nil, nil, objects)
	if response := serve(g, "GET", "/unstored", nil); response.Body.String() != "old" {
		t.Fatal(response.Body)
	}
	waitForPublication(t, objects)
	results, err := g.Refresh(context.Background(), []string{"http://cdn.test/unstored?"}, "site")
	if err != nil || len(results) != 1 || results[0].Status != "error" || results[0].Error != "refresh did not publish reusable content" {
		t.Fatalf("unstored result = %+v %v", results, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("refresh acquisitions = %d", calls.Load())
	}
}

func TestRefreshCancellationReportsRemainingURLsWithoutAcquisition(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-r.Context().Done()
	}))
	defer origin.Close()
	objects := newCache(t)
	g, _ := newGateway(t, origin.URL, nil, nil, objects)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type completion struct {
		results []RefreshResult
		err     error
	}
	done := make(chan completion, 1)
	go func() {
		results, err := g.Refresh(ctx, []string{"http://cdn.test/first", "http://cdn.test/second"}, "site")
		done <- completion{results, err}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not start")
	}
	cancel()
	var result completion
	select {
	case result = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not honor cancellation")
	}
	if result.err != nil || len(result.results) != 2 {
		t.Fatalf("canceled result = %+v %v", result.results, result.err)
	}
	for _, item := range result.results {
		if item.Status != "error" || item.Error != "refresh canceled" {
			t.Fatalf("canceled item = %+v", item)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("unstarted URL was acquired after cancellation: %d", calls.Load())
	}
	waitForPublication(t, objects)
}
