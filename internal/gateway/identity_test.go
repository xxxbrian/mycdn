package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"mycdn/internal/config"
	"mycdn/internal/rules"
)

func TestOrderedTrustedClientIPHeaders(t *testing.T) {
	d := config.Default()
	d.TrustedProxies = []string{"10.0.0.0/8"}
	snapshot, err := config.Validate(d)
	if err != nil {
		t.Fatal(err)
	}
	settings := d.Defaults.Identity
	settings.ClientIPHeaders = []rules.ClientIPHeader{{Name: "X-Visitor-IP", Format: "single"}, {Name: "X-Real-IP", Format: "single"}, {Name: "X-Proxy-Chain", Format: "xff"}, {Name: "X-Forwarded-Chain", Format: "forwarded"}}
	for _, test := range []struct {
		name, peer, want string
		headers          http.Header
	}{
		{"first valid", "10.0.0.2:443", "192.0.2.1", http.Header{"X-Visitor-Ip": {"192.0.2.1"}, "X-Real-Ip": {"192.0.2.2"}}},
		{"invalid first", "10.0.0.2:443", "192.0.2.2", http.Header{"X-Visitor-Ip": {"unknown"}, "X-Real-Ip": {"192.0.2.2"}}},
		{"single chain rejected", "10.0.0.2:443", "192.0.2.2", http.Header{"X-Visitor-Ip": {"192.0.2.1, 192.0.2.9"}, "X-Real-Ip": {"192.0.2.2"}}},
		{"duplicate single rejected", "10.0.0.2:443", "192.0.2.2", http.Header{"X-Visitor-Ip": {"192.0.2.1", "192.0.2.9"}, "X-Real-Ip": {"192.0.2.2"}}},
		{"trust boundary", "10.0.0.2:443", "198.51.100.4", http.Header{"X-Proxy-Chain": {"203.0.113.99, 198.51.100.4, 10.0.0.1"}}},
		{"forwarded fallback", "10.0.0.2:443", "2001:db8::8", http.Header{"X-Proxy-Chain": {"unknown"}, "X-Forwarded-Chain": {`for="[2001:db8::8]";proto=https, for=10.0.0.1`}}},
		{"invalid all", "10.0.0.2:443", "10.0.0.2", http.Header{"X-Visitor-Ip": {"unknown"}, "X-Proxy-Chain": {"192.0.2.3, bad"}}},
		{"untrusted immediate peer", "198.51.100.4:443", "198.51.100.4", http.Header{"X-Visitor-Ip": {"192.0.2.1"}}},
		{"mapped address", "10.0.0.2:443", "192.0.2.1", http.Header{"X-Real-Ip": {"::ffff:192.0.2.1"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "http://cdn.test/", nil)
			request.RemoteAddr = test.peer
			request.Header = test.headers
			got, _ := identity(snapshot, settings, request)
			if got != test.want {
				t.Fatalf("client = %q, want %q", got, test.want)
			}
		})
	}
	request := httptest.NewRequest("GET", "http://cdn.test/", nil)
	request.RemoteAddr = "10.0.0.2:443"
	request.Header.Set("X-Forwarded-For", "192.0.2.3")
	settings.ClientIPHeaders = []rules.ClientIPHeader{}
	if got, _ := identity(snapshot, settings, request); got != "10.0.0.2" {
		t.Fatalf("empty list did not force peer: %s", got)
	}
	settings.ClientIPHeaders = nil
	if got, _ := identity(snapshot, settings, request); got != "192.0.2.3" {
		t.Fatalf("unset list did not use preset: %s", got)
	}
}

func TestIngressPeerProtocolSelectIdentityAndStripCustomSource(t *testing.T) {
	g, _ := newGateway(t, "http://127.0.0.1:1", func(d *config.Document) {
		d.TrustedProxies = []string{"10.0.0.0/8"}
		d.Rules = []rules.Rule{{ID: "identity", Enabled: true, Stage: rules.Ingress, Steps: []rules.Step{{ID: "if", Type: "if", Branches: []rules.Branch{{ID: "proxy", Condition: &rules.Condition{Type: "all", Children: []rules.Condition{{Type: "match", Field: "peer_ip", Op: "cidr", Values: []string{"10.0.0.0/8"}}, {Type: "match", Field: "http_version", Op: "eq", Values: []string{"HTTP/2.0"}}}}, Steps: []rules.Step{{ID: "custom", Type: "action", Action: &rules.Action{Type: "identity", Identity: &rules.IdentityPatch{ClientIPHeaders: &[]rules.ClientIPHeader{{Name: "X-Visitor-IP", Format: "single"}}}}}}}}}}}}
	}, nil, nil)
	input := ExplainInput{URL: "http://cdn.test/", Peer: "10.0.0.2:100", HTTPVersion: "HTTP/2.0", Header: http.Header{"X-Visitor-Ip": {"192.0.2.4"}, "X-Forwarded-For": {"198.51.100.4"}}}
	result, err := g.Explain(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.ClientIP != "192.0.2.4" || result.PeerIP != "10.0.0.2" || result.HTTPVersion != "HTTP/2.0" {
		t.Fatalf("identity explain = %+v", result)
	}
	r := httptest.NewRequest("GET", input.URL, nil)
	r.RemoteAddr = input.Peer
	r.Proto = input.HTTPVersion
	r.Header = input.Header
	plans, _, err := prepare(g.config.Get(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plans.plans.Fetch.Header.Get("X-Visitor-IP") != "" {
		t.Fatal("client identity retained by outbound plan")
	}
	input.HTTPVersion = "HTTP/1.1"
	result, err = g.Explain(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.ClientIP != "198.51.100.4" {
		t.Fatalf("unmatched ingress identity = %s", result.ClientIP)
	}
}
