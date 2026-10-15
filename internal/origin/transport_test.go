package origin

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/quic-go/quic-go/http3"
	"mycdn/internal/config"
)

func validatedOrigin(t *testing.T, origin config.Origin, cert []byte) config.Origin {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}), 0600); err != nil {
		t.Fatal(err)
	}
	origin.CAFile = path
	d := config.Default()
	d.Sites = []config.Site{{ID: "test", Enabled: true, Domains: []string{"example.test"}, Origins: []config.Origin{origin}, DefaultOrigin: origin.ID}}
	s, err := config.Validate(d)
	if err != nil {
		t.Fatal(err)
	}
	site, _ := s.MatchSite("example.test")
	return site.Origins[0]
}

func TestVerifiedTLSProtocolAndReuse(t *testing.T) {
	var connections atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Authority", r.Host)
		w.Header().Set("X-Protocol", r.Proto)
		_, _ = io.WriteString(w, "ok")
	}))
	server.EnableHTTP2 = true
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.StartTLS()
	defer server.Close()
	m := New()
	defer m.Close()
	settings := config.Default().Defaults.Upstream
	o := validatedOrigin(t, config.Origin{ID: "one", URL: "https://example.com", DialAddress: server.Listener.Addr().String(), TLSServerName: "example.com"}, server.Certificate().Raw)
	t1, err := m.Transport(o, settings)
	if err != nil {
		t.Fatal(err)
	}
	t2, _ := m.Transport(o, settings)
	if t1 != t2 {
		t.Fatal("transport not reused")
	}
	for i := 0; i < 2; i++ {
		r, _ := http.NewRequest("GET", o.URL, nil)
		r.Host = "authority.test"
		response, err := t1.RoundTrip(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.ProtoMajor != 2 || response.Header.Get("X-Authority") != "authority.test" {
			t.Fatalf("protocol/authority = %s %s", response.Proto, response.Header.Get("X-Authority"))
		}
	}
	if connections.Load() != 1 {
		t.Fatalf("connections = %d", connections.Load())
	}
	settings.Protocol = "h1"
	h1, err := m.Transport(o, settings)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest("GET", o.URL, nil)
	response, err := h1.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.ProtoMajor != 1 {
		t.Fatal(response.Proto)
	}
	o.TLSServerName = "wrong.invalid"
	bad, _ := m.Transport(o, settings)
	r, _ = http.NewRequest("GET", o.URL, nil)
	if response, err := bad.RoundTrip(r); err == nil {
		response.Body.Close()
		t.Fatal("TLS verification accepted wrong name")
	}
}

func TestExplicitHTTP3VerifiedTLS(t *testing.T) {
	certificateServer := httptest.NewTLSServer(http.NotFoundHandler())
	cert := certificateServer.TLS.Certificates[0]
	raw := certificateServer.Certificate().Raw
	certificateServer.Close()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	server := &http3.Server{TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.Proto) })}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(udp) }()
	defer func() { _ = server.Close(); <-done }()
	o := validatedOrigin(t, config.Origin{ID: "h3", URL: "https://example.com", DialAddress: udp.LocalAddr().String(), TLSServerName: "example.com", Protocol: "h3"}, raw)
	m := New()
	defer m.Close()
	settings := config.Default().Defaults.Upstream
	settings.Protocol = "h3"
	transport, err := m.Transport(o, settings)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequestWithContext(context.Background(), "GET", o.URL, nil)
	response, err := transport.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "HTTP/3.0" {
		t.Fatalf("upstream = %q", body)
	}
	o.TLSServerName = "wrong.invalid"
	bad, err := m.Transport(o, settings)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest("GET", o.URL, nil)
	if response, err := bad.RoundTrip(request); err == nil {
		response.Body.Close()
		t.Fatal("HTTP/3 accepted wrong TLS verification name")
	}
}

func TestExplicitHTTP2DoesNotFallBack(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.Proto) }))
	defer server.Close()
	o := validatedOrigin(t, config.Origin{ID: "one", URL: server.URL}, server.Certificate().Raw)
	m := New()
	defer m.Close()
	settings := config.Default().Defaults.Upstream
	settings.Protocol = "h2"
	transport, err := m.Transport(o, settings)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest("GET", o.URL, nil)
	response, err := transport.RoundTrip(request)
	if err == nil {
		response.Body.Close()
		t.Fatalf("explicit h2 silently used %s", response.Proto)
	}
}

func TestExplicitH2CPriorKnowledgeAndReuse(t *testing.T) {
	var connections atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil || r.Proto != "HTTP/2.0" || r.Header.Get("Upgrade") != "" {
			t.Errorf("not prior-knowledge h2c: %s TLS=%v headers=%v", r.Proto, r.TLS, r.Header)
		}
		_, _ = io.WriteString(w, r.Proto)
	}))
	server.Config.Protocols = new(http.Protocols)
	server.Config.Protocols.SetUnencryptedHTTP2(true)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	m := New()
	defer m.Close()
	settings := config.Default().Defaults.Upstream
	settings.Protocol = "h2c"
	o := config.Origin{ID: "h2c", URL: server.URL}
	transport, err := m.Transport(o, settings)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		request, _ := http.NewRequest("GET", o.URL+"/cleartext", nil)
		response, err := transport.RoundTrip(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.ProtoMajor != 2 || string(body) != "HTTP/2.0" {
			t.Fatalf("h2c response = %s %q %v", response.Proto, body, err)
		}
	}
	if connections.Load() != 1 {
		t.Fatalf("h2c connections = %d", connections.Load())
	}
	if _, err := m.Transport(config.Origin{ID: "bad", URL: "https://example.test"}, settings); err == nil {
		t.Fatal("h2c accepted HTTPS")
	}
}
