package admin

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestManagementRequiresSession(t *testing.T) {
	f := setup(t, false)
	for _, path := range []string{"/api", "/api/config", "/api/status", "/api/capabilities", "/api/cache/entries", "/api/openapi.json", "/api/unknown", "/metrics"} {
		expectStatus(t, request(t, f.server, "GET", path, nil, nil), 401)
	}
	for _, path := range []string{"/api/config", "/api/cache/purge", "/api/cache/refresh", "/api/explain", "/api/analytics/query", "/api/analytics/details", "/api/auth/logout"} {
		expectStatus(t, request(t, f.server, "POST", path, map[string]any{}, nil), 401)
	}
	expectStatus(t, request(t, f.server, "GET", "/healthz", nil, nil), 200)
	session := request(t, f.server, "GET", "/api/auth/session", nil, nil)
	if decode[sessionStatus](t, session).Authenticated || len(session.Result().Cookies()) != 0 {
		t.Fatal("anonymous session check created or authenticated a session")
	}
	expectStatus(t, request(t, f.server, "GET", "/api/config", nil, map[string]string{"Authorization": "Bearer " + testAdminKey}), 401)
	expectStatus(t, request(t, f.server, "GET", "/api/config", nil, map[string]string{"Cookie": "mycdn_session=forged"}), 401)
}

func TestLoginCookieRotationLogoutAndNoStoredKey(t *testing.T) {
	f := setup(t, false)
	login := request(t, f.server, "POST", "/api/auth/login", loginCredentials{Key: testAdminKey}, nil)
	expectStatus(t, login, 200)
	first := login.Result().Cookies()[0]
	if first.Name != sessionCookieName || first.Value == testAdminKey || first.Secure || !first.HttpOnly || first.SameSite != http.SameSiteStrictMode || first.Path != "/" || first.Domain != "" || first.MaxAge < 43199 || first.MaxAge > 43201 {
		t.Fatalf("incorrect HTTP session cookie attributes: %+v", first)
	}
	headers := map[string]string{"Cookie": first.Name + "=" + first.Value}
	if !decode[sessionStatus](t, request(t, f.server, "GET", "/api/auth/session", nil, headers)).Authenticated {
		t.Fatal("valid session was not recognized")
	}
	expectStatus(t, request(t, f.server, "GET", "/api/config", nil, headers), 200)
	login = request(t, f.server, "POST", "/api/auth/login", loginCredentials{Key: testAdminKey}, headers)
	expectStatus(t, login, 200)
	second := login.Result().Cookies()[0]
	if first.Value == second.Value {
		t.Fatal("login did not rotate the session token")
	}
	expectStatus(t, request(t, f.server, "GET", "/api/config", nil, headers), 401)
	headers["Cookie"] = second.Name + "=" + second.Value
	logout := request(t, f.server, "POST", "/api/auth/logout", nil, headers)
	expectStatus(t, logout, 200)
	if decode[sessionStatus](t, logout).Authenticated || logout.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout did not expire the browser cookie")
	}
	expectStatus(t, request(t, f.server, "GET", "/api/config", nil, headers), 401)
	for _, path := range []string{"/api/config", "/api/status"} {
		response := request(t, f.authorized, "GET", path, nil, nil)
		if strings.Contains(response.Body.String(), testAdminKey) || strings.Contains(response.Body.String(), second.Value) {
			t.Fatal("management response exposed authentication secrets")
		}
	}
	persisted, err := os.ReadFile(f.path)
	if err != nil || strings.Contains(string(persisted), testAdminKey) || strings.Contains(string(persisted), second.Value) {
		t.Fatalf("authentication secrets entered saved configuration: %v", err)
	}
}

func TestSessionExpiryRestartAndHTTPLogin(t *testing.T) {
	auth, err := NewAuth(testAdminKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(auth.Close)
	mux := http.NewServeMux()
	auth.routes(newAPI(mux))
	mux.HandleFunc("GET /api/private", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	})
	handler := sameOrigin(auth.protect(mux))
	login := request(t, handler, "POST", "/api/auth/login", loginCredentials{Key: testAdminKey}, nil)
	expectStatus(t, login, 200)
	cookie := login.Result().Cookies()[0]
	headers := map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}
	expectStatus(t, request(t, handler, "GET", "/api/private", nil, headers), 200)
	for _, key := range []string{testAdminKey, "changed-management-key-at-least-32-bytes"} {
		restarted, err := NewAuth(key)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(restarted.Close)
		expectStatus(t, request(t, restarted.protect(mux), "GET", "/api/private", nil, headers), 401)
	}
	ctx, err := auth.sessions.Load(context.Background(), cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	auth.sessions.SetDeadline(ctx, time.Now().Add(-time.Second))
	if _, _, err := auth.sessions.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, request(t, handler, "GET", "/api/private", nil, headers), 401)
	if decode[sessionStatus](t, request(t, handler, "GET", "/api/auth/session", nil, headers)).Authenticated {
		t.Fatal("expired session still authenticated")
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := server.Client()
	client.Jar, _ = cookiejar.New(nil)
	for _, forwardedProto := range []string{"", "http", "https"} {
		request, err := http.NewRequest("POST", server.URL+"/api/auth/login", strings.NewReader(`{"key":"`+testAdminKey+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		if forwardedProto != "" {
			request.Header.Set("X-Forwarded-Proto", forwardedProto)
			request.Header.Set("Forwarded", "proto="+forwardedProto)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 || len(response.Cookies()) != 1 {
			t.Fatalf("HTTP login failed: %d", response.StatusCode)
		}
		cookie := response.Cookies()[0]
		if cookie.Name != sessionCookieName || cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Domain != "" || cookie.Path != "/" {
			t.Fatalf("forwarding headers changed the HTTP cookie policy: %+v", cookie)
		}
		response, err = client.Get(server.URL + "/api/private")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal("HTTP client could not reuse its session cookie")
		}
		response, err = client.Post(server.URL+"/api/auth/logout", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 || len(response.Cookies()) != 1 || response.Cookies()[0].MaxAge != -1 {
			t.Fatal("HTTP logout did not clear the session cookie")
		}
		response, err = client.Get(server.URL + "/api/private")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 401 {
			t.Fatal("HTTP logout did not revoke the session")
		}
	}
}

func TestLoginRejectsCrossSiteAndRedactsErrors(t *testing.T) {
	f := setup(t, false)
	for _, headers := range []map[string]string{
		{"Origin": "https://evil.example"},
		{"Origin": "https://admin.example:9999"},
		{"Sec-Fetch-Site": "cross-site"},
		{"Sec-Fetch-Site": "same-site"},
	} {
		expectStatus(t, request(t, f.server, "POST", "/api/auth/login", loginCredentials{Key: testAdminKey}, headers), 403)
		expectStatus(t, request(t, f.authorized, "POST", "/api/auth/logout", nil, headers), 403)
	}
	expectStatus(t, request(t, f.authorized, "GET", "/api/config", nil, nil), 200)
	for _, body := range []string{`{"key":"` + testAdminKey + `"`, `{"key":["` + testAdminKey + `"]}`, `null`, `{"key":"` + strings.Repeat("x", 9000) + `"}`} {
		r := httptest.NewRequest("POST", "http://admin.example/api/auth/login", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.server.ServeHTTP(w, r)
		if w.Code != 400 && w.Code != 413 || strings.Contains(w.Body.String(), testAdminKey) || strings.Contains(w.Body.String(), strings.Repeat("x", 100)) || len(w.Result().Cookies()) != 0 {
			t.Fatalf("login parsing leaked secrets or accepted malformed input: %d %s", w.Code, w.Body.String())
		}
	}
	bad := request(t, f.server, "POST", "/api/auth/login", loginCredentials{Key: "incorrect-management-key-at-least-32-bytes"}, nil)
	expectStatus(t, bad, 401)
	if len(bad.Result().Cookies()) != 0 || bad.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("failed login created a cookie or was cacheable")
	}
	expectStatus(t, request(t, f.server, "POST", "/api/auth/login", loginCredentials{Key: testAdminKey}, map[string]string{"Content-Type": "text/plain"}), 415)
}

func TestLoginRateLimitDoesNotBlockAuthenticatedRequests(t *testing.T) {
	f := setup(t, false)
	f.auth.loginRate = rate.NewLimiter(0, 2)
	for range 2 {
		expectStatus(t, request(t, f.server, "POST", "/api/auth/login", loginCredentials{Key: "wrong"}, nil), 401)
	}
	limited := request(t, f.server, "POST", "/api/auth/login", loginCredentials{Key: testAdminKey}, map[string]string{"X-Forwarded-For": "192.0.2.1"})
	expectStatus(t, limited, 429)
	if limited.Header().Get("Retry-After") == "" || len(limited.Result().Cookies()) != 0 {
		t.Fatal("rate limit did not advertise retry or created a session")
	}
	expectStatus(t, request(t, f.authorized, "GET", "/api/config", nil, nil), 200)
}

func TestAuthValidatesKeyAndHashesSessionTokens(t *testing.T) {
	for _, key := range []string{"", "too-short", strings.Repeat(" ", 32), strings.Repeat("a", 4097)} {
		if _, err := NewAuth(key); err == nil {
			t.Fatal("accepted an invalid management key")
		}
	}
	f := setup(t, false)
	login := request(t, f.server, "POST", "/api/auth/login", loginCredentials{Key: testAdminKey}, nil)
	token := login.Result().Cookies()[0].Value
	if _, exists, _ := f.auth.sessions.Store.Find(token); exists {
		t.Fatal("raw session token was stored")
	}
	if !decode[sessionStatus](t, request(t, f.server, "GET", "/api/auth/session", nil, map[string]string{"Cookie": "mycdn_session=" + token})).Authenticated {
		t.Fatal("hashed session token could not be loaded")
	}
}
