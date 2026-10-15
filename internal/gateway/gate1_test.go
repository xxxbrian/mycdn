package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"mycdn/internal/config"
	"mycdn/internal/rules"
)

func TestGate1BareHostRefreshAndExplainUseRootPath(t *testing.T) {
	var version, calls atomic.Int64
	version.Store(1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/rewritten" {
			t.Errorf("root path rule did not match: %q", r.URL.Path)
		}
		w.Header().Set("Cache-Control", "public, max-age=600")
		w.Header().Set("ETag", fmt.Sprintf(`"v%d"`, version.Load()))
		_, _ = fmt.Fprintf(w, "version-%d", version.Load())
	}))
	defer origin.Close()
	objects := newCache(t)
	g, store := newGateway(t, origin.URL, func(d *config.Document) {
		d.Rules = []rules.Rule{{ID: "root", Enabled: true, Stage: rules.Request, Steps: []rules.Step{{ID: "if", Type: "if", Branches: []rules.Branch{{ID: "root", Condition: &rules.Condition{Type: "all", Children: []rules.Condition{
			{Type: "match", Field: "path", Op: "eq", Values: []string{"/"}},
			{Type: "match", Field: "original.path", Op: "eq", Values: []string{"/"}},
		}}, Steps: []rules.Step{{ID: "rewrite", Type: "action", Action: &rules.Action{Type: "path", Path: stringPointer("/rewritten")}}}}}}}}}
	}, nil, objects)
	bare, err := g.Explain(context.Background(), ExplainInput{URL: "http://cdn.test"})
	if err != nil {
		t.Fatal(err)
	}
	slash, err := g.Explain(context.Background(), ExplainInput{URL: "http://cdn.test/"})
	if err != nil {
		t.Fatal(err)
	}
	if bare.CacheKey != slash.CacheKey || bare.OriginURL != slash.OriginURL || !strings.HasSuffix(bare.OriginURL, "/rewritten") {
		t.Fatalf("bare/slash plans differ: %+v %+v", bare, slash)
	}
	request, _ := http.NewRequest("GET", "http://cdn.test", nil)
	if _, _, err := prepare(store.Get(), request, nil); err != nil {
		t.Fatal(err)
	}
	if request.URL.Path != "" {
		t.Fatal("prepare mutated caller URL")
	}
	if response := serve(g, "GET", "/", nil); response.Body.String() != "version-1" {
		t.Fatal(response.Body)
	}
	waitForPublication(t, objects)
	version.Store(2)
	results, err := g.Refresh(context.Background(), []string{"http://cdn.test"}, "site")
	if err != nil || len(results) != 1 || results[0].Status != "refreshed" {
		t.Fatalf("bare host refresh = %+v %v", results, err)
	}
	if response := serve(g, "GET", "/", nil); response.Body.String() != "version-2" || calls.Load() != 2 {
		t.Fatalf("root cache not refreshed: %q calls=%d", response.Body, calls.Load())
	}
}

func TestGate1ExplainCanonicalizesSimulatedResponseHeaders(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "body")
	}))
	defer origin.Close()
	g, _ := newGateway(t, origin.URL, func(d *config.Document) {
		d.Rules = []rules.Rule{
			{ID: "source", Enabled: true, Stage: rules.OriginResponse, Steps: []rules.Step{{ID: "if", Type: "if", Branches: []rules.Branch{{ID: "plain", Condition: &rules.Condition{Type: "match", Field: "response_header", Key: "Content-Type", Op: "eq", Values: []string{"text/plain"}}, Steps: []rules.Step{{ID: "header", Type: "action", Action: &rules.Action{Type: "source_header", Header: &rules.HeaderMutation{Operation: "set", Name: "X-Source", Value: "matched"}}}}}}}}},
			{ID: "delivery", Enabled: true, Stage: rules.Delivery, Steps: []rules.Step{{ID: "if", Type: "if", Branches: []rules.Branch{{ID: "source", Condition: &rules.Condition{Type: "match", Field: "response_header", Key: "X-Source", Op: "eq", Values: []string{"matched"}}, Steps: []rules.Step{{ID: "header", Type: "action", Action: &rules.Action{Type: "response_header", Header: &rules.HeaderMutation{Operation: "set", Name: "X-Delivery", Value: "matched"}}}}}}}}},
			{ID: "outcome", Enabled: true, Stage: rules.Delivery, Steps: []rules.Step{{ID: "if", Type: "if", Branches: []rules.Branch{{ID: "miss", Condition: &rules.Condition{Type: "match", Field: "cache_result", Op: "eq", Values: []string{"MISS"}}, Steps: []rules.Step{{ID: "header", Type: "action", Action: &rules.Action{Type: "response_header", Header: &rules.HeaderMutation{Operation: "set", Name: "X-Default-Outcome", Value: "MISS"}}}}}}}}},
		}
	}, nil, nil)
	input := ExplainInput{URL: "http://cdn.test/file", Response: &SimulatedResponse{Status: 200, Header: http.Header{"content-type": {"text/plain"}}}}
	result, err := g.Explain(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	actual := serve(g, "GET", "/file", nil)
	for _, header := range []string{"Content-Type", "X-Source", "X-Delivery"} {
		if result.ResponseHeaders.Get(header) != actual.Header().Get(header) || result.ResponseHeaders.Get(header) == "" {
			t.Fatalf("header %s: explain=%v actual=%v", header, result.ResponseHeaders, actual.Header())
		}
	}
	if result.ResponseHeaders.Get("X-Default-Outcome") != "MISS" {
		t.Fatalf("omitted simulated outcome did not default to MISS: %v", result.ResponseHeaders)
	}
	if _, ok := input.Response.Header["Content-Type"]; ok {
		t.Fatal("explain mutated supplied header keys")
	}
	input.Response.Header = http.Header{"content-type": {"bad\r\nInjected: yes"}}
	if _, err := g.Explain(context.Background(), input); err == nil {
		t.Fatal("simulated response accepted invalid header value")
	}
}

func TestGate1ExplicitCORSAlwaysVariesOnOrigin(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Accept-Encoding, origin, ORIGIN")
		_, _ = io.WriteString(w, "body")
	}))
	defer origin.Close()
	g, _ := newGateway(t, origin.URL, func(d *config.Document) {
		d.Defaults.Delivery.CORS = &rules.CORS{Origins: []string{"https://allowed.test"}}
	}, nil, nil)
	for _, origin := range []string{"", "https://disallowed.test", "https://allowed.test"} {
		t.Run(origin, func(t *testing.T) {
			response := serve(g, "GET", "/", http.Header{"Origin": {origin}})
			if vary := response.Header().Get("Vary"); vary != "Accept-Encoding, Origin" {
				t.Fatalf("normalized vary = %q", vary)
			}
			want := ""
			if origin == "https://allowed.test" {
				want = origin
			}
			if response.Header().Get("Access-Control-Allow-Origin") != want {
				t.Fatal(response.Header())
			}
		})
	}
	for _, origin := range []string{"", "https://disallowed.test", "https://allowed.test"} {
		header := make(http.Header)
		applyCORS(header, http.Header{"Origin": {origin}}, rules.CORS{Origins: []string{"https://allowed.test"}})
		if header.Get("Vary") != "Origin" {
			t.Fatalf("missing Vary for %q: %v", origin, header)
		}
	}
	header := http.Header{"Vary": {"Accept-Encoding"}}
	applyCORS(header, http.Header{"Origin": {"https://any.test"}}, rules.CORS{Origins: []string{"*"}})
	if header.Get("Vary") != "Accept-Encoding" || header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("wildcard changed vary: %v", header)
	}
}
