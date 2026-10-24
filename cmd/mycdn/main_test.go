package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"mycdn/internal/cache"
	"mycdn/internal/config"
)

func TestOpenAPIDoesNotCreateConfigOrOpenStores(t *testing.T) {
	t.Setenv("MYCDN_ADMIN_KEY", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "must-not-exist", "config.json")
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--openapi", "-config", path, "-data", filepath.Join(dir, "must-not-exist")}, &output); err != nil {
		t.Fatal(err)
	}
	var spec struct {
		OpenAPI string                     `json:"openapi"`
		Paths   map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(output.Bytes(), &spec); err != nil || spec.OpenAPI == "" || spec.Paths["/api/config"] == nil {
		t.Fatalf("not generated OpenAPI: %v\n%s", err, output.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("--openapi touched storage: %v %v", entries, err)
	}
}

func TestInvalidAuthenticationEnvironmentDoesNotTouchStorage(t *testing.T) {
	for _, key := range []string{"", "short", strings.Repeat(" ", 32), strings.Repeat("a", 4097)} {
		t.Run(fmt.Sprintf("length-%d", len(key)), func(t *testing.T) {
			t.Setenv("MYCDN_ADMIN_KEY", key)
			dir := t.TempDir()
			err := run(context.Background(), []string{"-config", filepath.Join(dir, "config.json")}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "MYCDN_ADMIN_KEY") {
				t.Fatalf("invalid authentication settings did not fail closed: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatal("invalid authentication settings touched storage")
			}
		})
	}
}

func TestInitialConfigUsesItsParentAndContainerListener(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data", "config.json")
	if err := createInitialConfig(path); err != nil {
		t.Fatal(err)
	}
	store, err := config.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	document := store.Get().Document()
	if document.Listen.Admin != ":8081" || document.DataDir != filepath.Dir(path) {
		t.Fatalf("initial runtime paths/listener incorrect: %+v", document)
	}
	if mode, err := os.Stat(path); err != nil || mode.Mode().Perm() != 0600 {
		t.Fatalf("initial config permissions: %v %v", mode, err)
	}
	document.Listen.Admin = "127.0.0.1:18081"
	if err := store.Save(document); err != nil {
		t.Fatal(err)
	}
	if err := createInitialConfig(path); err != nil {
		t.Fatal(err)
	}
	reopened, err := config.NewStore(path)
	if err != nil || reopened.Get().Document().Listen.Admin != "127.0.0.1:18081" {
		t.Fatal("bootstrap overwrote an existing config")
	}
	cache := cacheOptions(document.DataDir, document.Storage)
	analytics := analyticsOptions(document.DataDir, document.Analytics)
	if cache.Dir != filepath.Join(filepath.Dir(path), "cache") || analytics.Dir != filepath.Join(filepath.Dir(path), "analytics") {
		t.Fatal("storage directories nested an extra data directory")
	}
	if cache.MaxReaders != document.Storage.MaxReaders || analytics.Threads != document.Analytics.Threads || analytics.CountryMMDB != document.Analytics.CountryMMDB {
		t.Fatal("configuration options not adapted to the real stores")
	}
}

func TestBootstrapOverridesH2CAndGracefulDrain(t *testing.T) {
	const adminKey = "test-management-key-at-least-32-bytes"
	t.Setenv("MYCDN_ADMIN_KEY", adminKey)
	dir := t.TempDir()
	path := filepath.Join(dir, "config", "config.json")
	data := filepath.Join(dir, "runtime")
	if err := createInitialConfig(path); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: stream\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = io.WriteString(w, "real origin")
	}))
	defer origin.Close()
	document := configuration.Get().Document()
	document.Analytics.Enabled = false
	document.Sites = []config.Site{{ID: "site", Enabled: true, Domains: []string{"cdn.example"}, Origins: []config.Origin{{ID: "origin", URL: origin.URL}}, DefaultOrigin: "origin"}}
	if err := configuration.Save(document); err != nil {
		t.Fatal(err)
	}
	freeAddress := func() string {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		return listener.Addr().String()
	}
	proxyAddress, adminAddress := freeAddress(), freeAddress()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-config", path, "-proxy", proxyAddress, "-admin", adminAddress, "-data", data}, io.Discard)
	}()
	t.Cleanup(func() { cancel(); finish() })
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 5 * time.Second, Jar: jar}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := client.Get("http://" + adminAddress + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		select {
		case err := <-done:
			t.Fatalf("application exited during startup: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("management listener did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, address := range []string{proxyAddress, adminAddress} {
		connection, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, &tls.Config{InsecureSkipVerify: true})
		if err == nil {
			connection.Close()
			t.Fatalf("listener %s accepted TLS instead of HTTP only", address)
		}
	}
	response, err := client.Get("http://" + adminAddress + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("binary exposed management without a session")
	}
	response, err = client.Post("http://"+adminAddress+"/api/auth/login", "application/json", strings.NewReader(`{"key":"`+adminKey+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("binary did not accept environment-supplied management key")
	}
	response, err = client.Get("http://" + adminAddress + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		ProxyListen string `json:"proxy_listen"`
		AdminListen string `json:"admin_listen"`
		DataDir     string `json:"data_dir"`
	}
	err = json.NewDecoder(response.Body).Decode(&status)
	response.Body.Close()
	if err != nil || response.ProtoMajor != 1 || status.ProxyListen != proxyAddress || status.AdminListen != adminAddress || status.DataDir != data {
		t.Fatalf("overrides/admin HTTP1 not active: %+v %v", status, err)
	}
	transport := &http2.Transport{AllowHTTP: true, DialTLSContext: func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	defer transport.CloseIdleConnections()
	h2client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, _ := http.NewRequest("GET", "http://"+proxyAddress+"/value", nil)
	request.Host = "cdn.example"
	response, err = h2client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.ProtoMajor != 2 || string(body) != "real origin" {
		t.Fatalf("h2c proxy failed: protocol=%s body=%q err=%v", response.Proto, body, err)
	}
	request, _ = http.NewRequest("GET", "http://"+proxyAddress+"/stream", nil)
	request.Host = "cdn.example"
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first := make([]byte, len("data: stream\n\n"))
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatal(err)
	}
	statusResponse, err := client.Get("http://" + adminAddress + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	var active struct {
		ActiveRequests int64 `json:"active_requests"`
	}
	err = json.NewDecoder(statusResponse.Body).Decode(&active)
	statusResponse.Body.Close()
	if err != nil || active.ActiveRequests != 1 {
		t.Fatalf("runtime did not observe the real active stream: %+v %v", active, err)
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("shutdown did not drain the active stream: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	finish()
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("graceful shutdown interrupted a stream: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("application did not finish graceful shutdown")
	}
	if _, err := os.Stat(filepath.Join(data, "cache")); err != nil {
		t.Fatal("cache was not opened under the data override")
	}
	if _, err := os.Stat(filepath.Join(data, "analytics")); !os.IsNotExist(err) {
		t.Fatalf("disabled analytics unexpectedly opened storage: %v", err)
	}
	// Cache ownership must be released after the drain, not leaked at exit.
	reopened, err := cache.Open(cacheOptions(data, document.Storage))
	if err != nil {
		t.Fatal(fmt.Errorf("reopen cache after shutdown: %w", err))
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
