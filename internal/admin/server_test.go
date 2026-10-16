package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mycdn/internal/analytics"
	"mycdn/internal/cache"
	"mycdn/internal/config"
	"mycdn/internal/gateway"
	"mycdn/internal/rules"
)

type fixture struct {
	server *Server
	config *config.Store
	cache  *cache.Store
	path   string
}

func setup(t *testing.T, enabled bool) fixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	configuration, err := config.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	document := configuration.Get().Document()
	document.Analytics.Enabled = enabled
	document.DataDir = dir
	document.Sites = []config.Site{{ID: "site-a", Name: "Test", Enabled: true, Domains: []string{"cdn.example"}, Origins: []config.Origin{{ID: "origin", URL: "http://127.0.0.1:1"}}, DefaultOrigin: "origin"}}
	if err := configuration.Save(document); err != nil {
		t.Fatal(err)
	}
	objects, err := cache.Open(cache.Options{Dir: filepath.Join(dir, "cache")})
	if err != nil {
		t.Fatal(err)
	}
	var events *analytics.Store
	if enabled {
		events, err = analytics.Open(analytics.Options{Dir: filepath.Join(dir, "analytics")})
		if err != nil {
			objects.Close()
			t.Fatal(err)
		}
	}
	proxy := gateway.New(configuration, objects, nil)
	management := New(configuration, objects, proxy, events, Options{Version: "test"})
	t.Cleanup(func() {
		for _, close := range []func() error{management.Close, objects.Close, proxy.Close} {
			if err := close(); err != nil {
				t.Error(err)
			}
		}
		if events != nil {
			if err := events.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return fixture{server: management, config: configuration, cache: objects, path: path}
}

func request(t *testing.T, handler http.Handler, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, "http://admin.example"+path, bytes.NewReader(encoded))
	r.Header.Set("Accept", "application/json")
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if strings.HasPrefix(path, "/api") && !strings.Contains(w.Header().Get("Content-Type"), "json") {
		t.Fatalf("%s %s returned non-JSON (%d): %s", method, path, w.Code, w.Body.String())
	}
	return w
}

func decode[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var result T
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode %d response: %v\n%s", response.Code, err, response.Body.String())
	}
	return result
}

func expectStatus(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status %d, expected %d: %s", response.Code, status, response.Body.String())
	}
}

func TestConfigETagAtomicSaveAndRestartState(t *testing.T) {
	f := setup(t, false)
	get := request(t, f.server, "GET", "/api/config", nil, nil)
	expectStatus(t, get, 200)
	etag := get.Header().Get("ETag")
	if !strings.HasPrefix(etag, `"`) || strings.Contains(get.Body.String(), "$schema") {
		t.Fatalf("expected plain configuration with a strong ETag: %s %s", etag, get.Body.String())
	}
	document := decode[config.Document](t, get)
	document.Sites[0].Name = "First editor"
	put := request(t, f.server, "PUT", "/api/config", document, map[string]string{"If-Match": etag})
	expectStatus(t, put, 200)
	if put.Header().Get("ETag") == etag {
		t.Fatal("successful edit did not change the ETag")
	}
	expectStatus(t, request(t, f.server, "PUT", "/api/config", document, map[string]string{"If-Match": etag}), 412)
	expectStatus(t, request(t, f.server, "PUT", "/api/config", document, nil), 422)
	expectStatus(t, request(t, f.server, "PUT", "/api/config", document, map[string]string{"If-Match": "*"}), 412)
	file, err := os.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := config.Decode(file)
	file.Close()
	if err != nil || persisted.Sites[0].Name != "First editor" {
		t.Fatalf("save not persisted: %v, %+v", err, persisted.Sites)
	}
	status := decode[RuntimeStatus](t, request(t, f.server, "GET", "/api/status", nil, nil))
	if status.RestartRequired || status.Version != "test" || status.GoVersion == "" || status.MemoryBytes == 0 || len(status.History) == 0 {
		t.Fatalf("unexpected running status: %+v", status)
	}
	document.Listen.Admin = ":18081"
	document.DataDir = filepath.Join(document.DataDir, "other")
	document.Storage.MaxFills++
	document.Analytics.Enabled = true
	put = request(t, f.server, "PUT", "/api/config", document, map[string]string{"If-Match": put.Header().Get("ETag")})
	expectStatus(t, put, 200)
	status = decode[RuntimeStatus](t, request(t, f.server, "GET", "/api/status", nil, nil))
	if !status.RestartRequired || !slices.Equal(status.RestartFields, []string{"listen.admin", "data_dir", "storage", "analytics"}) {
		t.Fatalf("missing restart-required fields: %+v", status)
	}
	if status.Analytics.Enabled || status.AdminListen == document.Listen.Admin || status.DataDir == document.DataDir {
		t.Fatal("saved process settings were incorrectly reported as active")
	}
}

func TestConfigConcurrentEditorsHaveOneWinner(t *testing.T) {
	f := setup(t, false)
	get := request(t, f.server, "GET", "/api/config", nil, nil)
	document := decode[config.Document](t, get)
	etag := get.Header().Get("ETag")
	results := make(chan int, 2)
	var editors sync.WaitGroup
	for _, name := range []string{"editor one", "editor two"} {
		editors.Go(func() {
			copy := document
			copy.DataDir = name
			results <- request(t, f.server, "PUT", "/api/config", copy, map[string]string{"If-Match": etag}).Code
		})
	}
	editors.Wait()
	close(results)
	statuses := []int{}
	for status := range results {
		statuses = append(statuses, status)
	}
	slices.Sort(statuses)
	if !slices.Equal(statuses, []int{200, 412}) {
		t.Fatalf("lost-update protection failed: %v", statuses)
	}
}

func TestTypedConfigValidationAndRecursiveRules(t *testing.T) {
	f := setup(t, false)
	get := request(t, f.server, "GET", "/api/config", nil, nil)
	document := decode[config.Document](t, get)
	headers := map[string]string{"If-Match": get.Header().Get("ETag")}
	document.Sites[0].DefaultOrigin = "missing"
	bad := request(t, f.server, "PUT", "/api/config", document, headers)
	expectStatus(t, bad, 422)
	problem := decode[huma.ErrorModel](t, bad)
	if len(problem.Errors) != 1 || problem.Errors[0].Location != "sites[0].default_origin" {
		t.Fatalf("lost typed field path: %s", bad.Body.String())
	}
	document.Sites[0].DefaultOrigin = "origin"
	document.Rules = []rules.Rule{{ID: "delivery", Name: "recursive", Enabled: true, Stage: rules.Delivery, Steps: []rules.Step{{ID: "if", Type: "if", Branches: []rules.Branch{
		{ID: "conditional", Condition: &rules.Condition{Type: "all", Children: []rules.Condition{{Type: "not", Children: []rules.Condition{{Type: "match", Field: "path", Op: "eq", Values: []string{"/no"}}}}}}, Steps: []rules.Step{{ID: "nested", Type: "if", Branches: []rules.Branch{{ID: "yes", Condition: &rules.Condition{Type: "all"}, Steps: []rules.Step{{ID: "action", Type: "action", Action: &rules.Action{Type: "response_header", Header: &rules.HeaderMutation{Operation: "set", Name: "X-Rule", Value: "nested"}}}}}}}}},
		{ID: "else", Condition: nil, Steps: []rules.Step{}},
	}}}}}
	put := request(t, f.server, "PUT", "/api/config", document, headers)
	expectStatus(t, put, 200)
	var unknown map[string]any
	encoded, _ := json.Marshal(document)
	_ = json.Unmarshal(encoded, &unknown)
	unknown["unknown"] = true
	expectStatus(t, request(t, f.server, "PUT", "/api/config", unknown, map[string]string{"If-Match": put.Header().Get("ETag")}), 422)
	input := gateway.ExplainInput{URL: "http://cdn.example/yes", Method: "GET", Peer: "127.0.0.1:1000"}
	explained := request(t, f.server, "POST", "/api/explain", input, nil)
	expectStatus(t, explained, 200)
	result := decode[gateway.ExplainResult](t, explained)
	if result.SiteID != "site-a" || result.CacheKey == "" || result.ResponseHeaders.Get("X-Rule") != "nested" {
		t.Fatalf("explain did not use the saved executor: %+v", result)
	}
	input.URL = "http://unknown.example/"
	expectStatus(t, request(t, f.server, "POST", "/api/explain", input, nil), 400)
}

func putEntry(t *testing.T, store *cache.Store, key, site, host, path string) {
	t.Helper()
	response, err := store.Do(context.Background(), cache.Request{
		Key: key, SiteID: site, Host: host, Path: path, Method: "GET", Header: make(http.Header),
		Policy: cache.Policy{Mode: "force", TTL: time.Hour},
		Fetch: func(context.Context, http.Header) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"public, max-age=3600"}}, Body: io.NopCloser(strings.NewReader("cached content")), ContentLength: 14}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, response.HTTP.Body)
	response.HTTP.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
}

func TestCacheScopeSafetyListingAndJSONBoundary(t *testing.T) {
	f := setup(t, false)
	putEntry(t, f.cache, "a", "site-a", "cdn.example", "/a")
	putEntry(t, f.cache, "b", "site-b", "other.example", "/b")
	for _, selection := range []any{map[string]any{}, map[string]any{"key": "a"}, map[string]any{"host": " "}, map[string]any{"all": false}} {
		expectStatus(t, request(t, f.server, "POST", "/api/cache/purge", selection, nil), 422)
	}
	all := request(t, f.server, "GET", "/api/cache/entries?limit=1", nil, nil)
	expectStatus(t, all, 200)
	listing := decode[entriesResult](t, all)
	if len(listing.Entries) != 1 || !listing.Truncated || listing.Limit != 1 {
		t.Fatalf("listing envelope incorrect: %+v", listing)
	}
	for _, query := range []string{"limit=501", "limit=0", "limit=bad", "key=a"} {
		expectStatus(t, request(t, f.server, "GET", "/api/cache/entries?"+query, nil, nil), 422)
	}
	scoped := request(t, f.server, "GET", "/api/cache/entries?site_id=site-b&path_prefix=/b", nil, nil)
	expectStatus(t, scoped, 200)
	listing = decode[entriesResult](t, scoped)
	if len(listing.Entries) != 1 || listing.Entries[0].Key != "b" || listing.Truncated {
		t.Fatalf("filters did not intersect: %+v", listing)
	}
	purged := request(t, f.server, "POST", "/api/cache/purge", purgeSelection{SiteID: "site-a", Host: "other.example"}, nil)
	expectStatus(t, purged, 200)
	if decode[purgeResult](t, purged).Purged != 0 {
		t.Fatal("conflicting selection broadened the purge")
	}
	purged = request(t, f.server, "POST", "/api/cache/purge", purgeSelection{SiteID: "site-a", Host: "cdn.example", Path: "/a"}, nil)
	expectStatus(t, purged, 200)
	if decode[purgeResult](t, purged).Purged != 1 {
		t.Fatal("exact selected entry not purged")
	}
	remaining := decode[entriesResult](t, request(t, f.server, "GET", "/api/cache/entries", nil, nil))
	if len(remaining.Entries) != 1 || remaining.Entries[0].SiteID != "site-b" {
		t.Fatal("entry outside purge scope was changed")
	}
	failedRefresh := request(t, f.server, "POST", "/api/cache/refresh", refreshSelection{URLs: []string{"http://cdn.example/a"}}, nil)
	expectStatus(t, failedRefresh, 200)
	refresh := decode[refreshBatch](t, failedRefresh)
	if len(refresh.Results) != 1 || refresh.Results[0].Status != "error" {
		t.Fatalf("unreachable origin reported as refreshed: %+v", refresh)
	}
	expectStatus(t, request(t, f.server, "POST", "/api/cache/refresh", refreshSelection{URLs: []string{"http://unknown.example/a"}}, nil), 422)
	expectStatus(t, request(t, f.server, "POST", "/api/cache/refresh", map[string]any{"key": "b"}, nil), 422)
	for _, path := range []string{"/api", "/api/no-such-route", "/api/cache/purge/not-a-route"} {
		expectStatus(t, request(t, f.server, "GET", path, nil, nil), 404)
	}
	blocked := request(t, f.server, "POST", "/api/cache/purge", purgeSelection{All: true}, map[string]string{"Origin": "https://evil.example"})
	expectStatus(t, blocked, 403)
	if blocked.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("management must not allow wildcard CORS")
	}
	expectStatus(t, request(t, f.server, "POST", "/api/cache/purge", purgeSelection{All: true}, map[string]string{"Origin": "http://admin.example:9000"}), 403)
	purged = request(t, f.server, "POST", "/api/cache/purge", purgeSelection{All: true}, map[string]string{"Origin": "http://admin.example"})
	expectStatus(t, purged, 200)
	if decode[purgeResult](t, purged).Purged != 1 {
		t.Fatal("explicit all purge failed")
	}
	expectStatus(t, request(t, f.server, "GET", "/healthz", nil, nil), 200)
	capabilities := request(t, f.server, "GET", "/api/capabilities", nil, nil)
	expectStatus(t, capabilities, 200)
	if len(decode[rules.Catalog](t, capabilities).Actions) == 0 {
		t.Fatal("real rules catalog not served")
	}
	openapi := request(t, f.server, "GET", "/api/openapi.json", nil, nil)
	expectStatus(t, openapi, 200)
	if decode[map[string]any](t, openapi)["openapi"] == nil {
		t.Fatal("generated OpenAPI not served")
	}
	metrics := request(t, f.server, "GET", "/metrics", nil, nil)
	expectStatus(t, metrics, 200)
	if !strings.Contains(metrics.Body.String(), "mycdn_cache_entries") {
		t.Fatal("metrics helper not served")
	}
}

func TestAnalyticsDirectTypesDisabledAndValidation(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			f := setup(t, enabled)
			query := analytics.Query{From: time.Now().Add(-time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339), To: time.Now().UTC().Truncate(time.Second).Format(time.RFC3339), Filters: analytics.Filters{}}
			response := request(t, f.server, "POST", "/api/analytics/query", query, nil)
			details := request(t, f.server, "POST", "/api/analytics/details", analytics.DetailsQuery{From: query.From, To: query.To, Filters: query.Filters}, nil)
			if !enabled {
				expectStatus(t, response, 503)
				expectStatus(t, details, 503)
				return
			}
			expectStatus(t, response, 200)
			expectStatus(t, details, 200)
			result := decode[analytics.Result](t, response)
			if result.Summary.Requests != 0 || len(result.Series) != 0 || result.Coverage.RequestedFrom == "" {
				t.Fatalf("unexpected empty store result: %+v", result)
			}
			query.IntervalSeconds = 11
			response = request(t, f.server, "POST", "/api/analytics/query", query, nil)
			expectStatus(t, response, 422)
			problem := decode[huma.ErrorModel](t, response)
			if len(problem.Errors) == 0 || problem.Errors[0].Location != "interval_seconds" {
				t.Fatalf("analytics field path lost: %s", response.Body.String())
			}
		})
	}
}

func TestRefreshUsesGatewayAcquisitionAndPublishesNewContent(t *testing.T) {
	f := setup(t, false)
	var mu sync.Mutex
	content := "old content"
	attempts := 0
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = io.WriteString(w, content)
	}))
	defer origin.Close()
	document := f.config.Get().Document()
	document.Sites[0].Origins[0].URL = origin.URL
	if err := f.config.Save(document); err != nil {
		t.Fatal(err)
	}
	readProxy := func() string {
		r := httptest.NewRequest("GET", "http://cdn.example/refresh", nil)
		w := httptest.NewRecorder()
		f.server.gateway.ServeHTTP(w, r)
		expectStatus(t, w, 200)
		return w.Body.String()
	}
	if got := readProxy(); got != "old content" {
		t.Fatalf("initial fill: %q", got)
	}
	mu.Lock()
	content = "new content"
	mu.Unlock()
	response := request(t, f.server, "POST", "/api/cache/refresh", refreshSelection{URLs: []string{"http://cdn.example/refresh"}, SiteID: "site-a"}, nil)
	expectStatus(t, response, 200)
	result := decode[refreshBatch](t, response)
	if len(result.Results) != 1 || result.Results[0].Status != "refreshed" {
		t.Fatalf("manual refresh did not publish: %s", response.Body.String())
	}
	if got := readProxy(); got != "new content" {
		t.Fatalf("refresh left old content active: %q", got)
	}
	mu.Lock()
	count := attempts
	mu.Unlock()
	if count != 2 {
		t.Fatalf("manual refresh should perform one acquisition and leave a HIT, got %d attempts", count)
	}
	response = request(t, f.server, "POST", "/api/cache/refresh", refreshSelection{URLs: []string{"http://cdn.example/refresh"}, SiteID: "different-site"}, nil)
	expectStatus(t, response, 422)
	if got := readProxy(); got != "new content" {
		t.Fatal("invalid refresh selection invalidated cached content")
	}
}

func TestGeneratedOpenAPIUsesTypedRecursiveSchemas(t *testing.T) {
	spec := OpenAPI()
	if spec.Paths["/api/config"].Put.RequestBody == nil || spec.Paths["/api/cache/refresh"].Post == nil {
		t.Fatal("typed routes not documented")
	}
	schemas := spec.Components.Schemas.Map()
	for _, name := range []string{"Document", "Rule", "Step", "Branch", "Condition", "Action", "Query", "Result", "DetailsResult", "RuntimeStatus"} {
		if schemas[name] == nil {
			t.Fatalf("missing schema %s", name)
		}
	}
	if schemas["Step"].Properties["branches"].Items.Ref != "#/components/schemas/Branch" || schemas["Branch"].Properties["steps"].Items.Ref != "#/components/schemas/Step" || schemas["Condition"].Properties["children"].Items.Ref != "#/components/schemas/Condition" {
		t.Fatal("recursive rule tree became an untyped object")
	}
	condition := schemas["Branch"].Properties["condition"]
	if len(condition.AnyOf) != 2 || condition.AnyOf[0].Ref != "#/components/schemas/Condition" || condition.AnyOf[1].Type != "null" {
		t.Fatal("ELSE's explicit null condition missing from schema")
	}
	if schemas["PurgeSelection"].AdditionalProperties != false {
		t.Fatal("purge accepts unknown fields")
	}
	if _, ok := schemas["PurgeSelection"].Properties["key"]; ok {
		t.Fatal("purge must not advertise unsupported key selection")
	}
	if _, err := json.Marshal(spec); err != nil {
		t.Fatal(err)
	}
}
