// Package origin owns reusable, immutable upstream transports. URL authority,
// HTTP Host, dial address, and TLS verification name are separate inputs.
package origin

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"mycdn/internal/config"
	"mycdn/internal/rules"
)

type Manager struct {
	mu         sync.Mutex
	transports map[transportKey]*http.Transport
	h3         map[transportKey]*http3.Transport
	closed     bool
	closeErr   error
}

type transportKey struct {
	scheme, authority, dial, serverName, protocol string
	ca                                            [32]byte
	timeouts                                      rules.Timeouts
}

func New() *Manager {
	return &Manager{transports: make(map[transportKey]*http.Transport), h3: make(map[transportKey]*http3.Transport)}
}

// Transport returns a shared transport. Never change fields on the returned
// value. RequestMS is a whole-acquisition deadline applied by gateway, not a
// transport setting. A changed config gets a different transport as needed.
func (m *Manager) Transport(o config.Origin, settings rules.UpstreamSettings) (http.RoundTripper, error) {
	u, err := url.Parse(o.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid origin URL")
	}
	protocol := settings.Protocol
	if protocol != "auto" && protocol != "h1" && protocol != "h2" && protocol != "h2c" && protocol != "h3" {
		return nil, fmt.Errorf("unsupported upstream protocol %q", protocol)
	}
	if (protocol == "h2" || protocol == "h3") && u.Scheme != "https" {
		return nil, fmt.Errorf("explicit HTTP/2 and HTTP/3 require HTTPS")
	}
	if protocol == "h2c" && u.Scheme != "http" {
		return nil, fmt.Errorf("explicit h2c requires HTTP")
	}
	ca := o.CAPEM()
	key := transportKey{scheme: u.Scheme, authority: u.Host, dial: o.DialAddress, serverName: o.TLSServerName, protocol: protocol, ca: sha256.Sum256(ca), timeouts: settings.Timeouts}
	key.timeouts.RequestMS = 0
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, fmt.Errorf("origin transports are closed")
	}
	if t := m.transports[key]; t != nil {
		return t, nil
	}
	if t := m.h3[key]; t != nil {
		return t, nil
	}
	if len(m.transports)+len(m.h3) >= 256 {
		return nil, fmt.Errorf("origin transport limit reached (256 distinct configurations; restart to reclaim retired configurations)")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: o.TLSServerName}
	if len(ca) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(ca) {
			return nil, fmt.Errorf("invalid origin CA certificates")
		}
		tlsConfig.RootCAs = pool
	}
	if protocol == "h3" {
		config := &quic.Config{HandshakeIdleTimeout: milliseconds(settings.Timeouts.TLSHandshakeMS), MaxIdleTimeout: milliseconds(settings.Timeouts.IdleMS)}
		tlsConfig.MinVersion = tls.VersionTLS13
		t := &http3.Transport{TLSClientConfig: tlsConfig, QUICConfig: config, DisableCompression: true}
		address := o.DialAddress
		t.Dial = func(ctx context.Context, authority string, tlsConfig *tls.Config, config *quic.Config) (*quic.Conn, error) {
			if address != "" {
				authority = address
			}
			if settings.Timeouts.ConnectMS > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, milliseconds(settings.Timeouts.ConnectMS))
				defer cancel()
			}
			return quic.DialAddr(ctx, authority, tlsConfig, config)
		}
		m.h3[key] = t
		return t, nil
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(protocol != "h2" && protocol != "h2c")
	protocols.SetHTTP2(u.Scheme == "https" && protocol != "h1")
	protocols.SetUnencryptedHTTP2(protocol == "h2c")
	dialer := &net.Dialer{Timeout: milliseconds(settings.Timeouts.ConnectMS), KeepAlive: 30 * time.Second}
	dialAddress := o.DialAddress
	t := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if dialAddress != "" {
				address = dialAddress
			}
			return dialer.DialContext(ctx, network, address)
		},
		TLSClientConfig: tlsConfig, Protocols: protocols,
		TLSHandshakeTimeout: milliseconds(settings.Timeouts.TLSHandshakeMS), ResponseHeaderTimeout: milliseconds(settings.Timeouts.ResponseHeaderMS), IdleConnTimeout: milliseconds(settings.Timeouts.IdleMS),
		ExpectContinueTimeout: time.Second, MaxIdleConns: 256, MaxIdleConnsPerHost: 32,
		DisableCompression: true,
	}
	m.transports[key] = t
	return t, nil
}

func milliseconds(n int64) time.Duration { return time.Duration(n) * time.Millisecond }

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return m.closeErr
	}
	m.closed = true
	for _, t := range m.transports {
		t.CloseIdleConnections()
	}
	for _, t := range m.h3 {
		if err := t.Close(); err != nil && m.closeErr == nil {
			m.closeErr = err
		}
	}
	return m.closeErr
}
