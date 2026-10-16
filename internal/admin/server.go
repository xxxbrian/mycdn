// Package admin serves the local management API and embedded console.
package admin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mycdn/internal/analytics"
	"mycdn/internal/cache"
	"mycdn/internal/config"
	"mycdn/internal/gateway"
)

// Options describes the running process, including command-line overrides.
// Saved configuration changes never re-open stores or move live listeners.
type Options struct {
	Version     string
	StartedAt   time.Time
	ProxyListen string
	AdminListen string
	DataDir     string
}

type Server struct {
	handler   http.Handler
	config    *config.Store
	cache     *cache.Store
	gateway   *gateway.Gateway
	analytics *analytics.Store
	startup   config.Document
	options   Options
	monitor   *runtimeMonitor
	saveMu    sync.Mutex
	closeOnce sync.Once
}

func New(configuration *config.Store, objects *cache.Store, proxy *gateway.Gateway, events *analytics.Store, options Options) *Server {
	startup := configuration.Get().Document()
	if options.Version == "" {
		options.Version = "dev"
	}
	if options.StartedAt.IsZero() {
		options.StartedAt = time.Now()
	}
	if options.ProxyListen == "" {
		options.ProxyListen = startup.Listen.Proxy
	}
	if options.AdminListen == "" {
		options.AdminListen = startup.Listen.Admin
	}
	if options.DataDir == "" {
		options.DataDir = startup.DataDir
	}
	s := &Server{config: configuration, cache: objects, gateway: proxy, analytics: events, startup: startup, options: options, monitor: newRuntimeMonitor()}
	mux := http.NewServeMux()
	api := newAPI(mux)
	s.routes(api)
	mux.Handle("GET /metrics", metrics(objects))
	mux.HandleFunc("/api", apiNotFound)
	mux.HandleFunc("/api/", apiNotFound)
	static := frontend()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeProblem(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		static.ServeHTTP(w, r)
	})
	s.handler = sameOrigin(mux)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// Close stops only the management runtime sampler. The composition root owns
// listeners, gateway, cache, and analytics and closes them in that order.
func (s *Server) Close() error {
	s.closeOnce.Do(s.monitor.close)
	return nil
}

// OpenAPI uses the same route registration as the live server, but opens no
// configuration, cache, or analytics store and starts no background work.
func OpenAPI() *huma.OpenAPI {
	api := newAPI(http.NewServeMux())
	(&Server{}).routes(api)
	return api.OpenAPI()
}

func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
			w.Header().Set("Cache-Control", "no-store")
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			origin := r.Header.Get("Origin")
			if origin != "" {
				u, err := url.Parse(origin)
				// Compare browser authorities (including ports), not forwarding
				// headers. The outer TLS proxy can legitimately change the scheme.
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
					writeProblem(w, http.StatusForbidden, "cross-origin management writes are not allowed")
					return
				}
			} else if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeProblem(w, http.StatusForbidden, "cross-origin management writes are not allowed")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func apiNotFound(w http.ResponseWriter, _ *http.Request) {
	writeProblem(w, http.StatusNotFound, "management endpoint not found")
}

func writeProblem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(huma.NewError(status, detail))
}
