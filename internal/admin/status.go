package admin

import (
	"context"
	"time"

	"mycdn/internal/analytics"
	"mycdn/internal/cache"
	"mycdn/internal/gateway"
)

type analyticsRuntime struct {
	Enabled        bool   `json:"enabled"`
	QueueDepth     int    `json:"queue_depth"`
	DroppedEvents  uint64 `json:"dropped_events"`
	LagMS          int64  `json:"lag_ms"`
	GeoIPAvailable bool   `json:"geoip_available"`
}

// A named value type prevents the two independent packages' Stats types from
// colliding in the generated schema registry.
type gatewayRuntime gateway.Stats

type RuntimeStatus struct {
	Version         string            `json:"version"`
	StartedAt       time.Time         `json:"started_at"`
	UptimeSeconds   float64           `json:"uptime_seconds"`
	GoVersion       string            `json:"go_version"`
	Goroutines      int               `json:"goroutines"`
	MemoryBytes     uint64            `json:"memory_bytes"`
	HeapBytes       uint64            `json:"heap_bytes"`
	CPUPercent      float64           `json:"cpu_percent"`
	CPUs            int               `json:"cpus"`
	ActiveRequests  int64             `json:"active_requests"`
	ActiveFills     int64             `json:"active_fills"`
	Cache           cache.Stats       `json:"cache"`
	Gateway         gatewayRuntime    `json:"gateway"`
	Analytics       analyticsRuntime  `json:"analytics"`
	AnalyticsStatus *analytics.Status `json:"analytics_status,omitempty"`
	History         []runtimeInfo     `json:"history"`
	RestartRequired bool              `json:"restart_required"`
	RestartFields   []string          `json:"restart_fields"`
	ProxyListen     string            `json:"proxy_listen"`
	AdminListen     string            `json:"admin_listen"`
	DataDir         string            `json:"data_dir"`
}

func (s *Server) status(_ context.Context, _ *struct{}) (*struct{ Body RuntimeStatus }, error) {
	runtime := s.monitor.snapshot()
	objects := s.cache.Stats()
	proxy := s.gateway.Stats()
	fields := s.restartFields()
	status := RuntimeStatus{
		Version: s.options.Version, StartedAt: s.options.StartedAt,
		UptimeSeconds: time.Since(s.options.StartedAt).Seconds(),
		GoVersion:     runtime.GoVersion, Goroutines: runtime.Goroutines,
		MemoryBytes: runtime.GoBytes, HeapBytes: runtime.HeapBytes,
		CPUPercent: runtime.CPUPercent, CPUs: runtime.CPUs,
		ActiveRequests: proxy.ActiveRequests, ActiveFills: objects.ActiveFills,
		Cache: objects, Gateway: gatewayRuntime(proxy), History: s.monitor.samples(),
		RestartRequired: len(fields) > 0, RestartFields: fields,
		ProxyListen: s.options.ProxyListen, AdminListen: s.options.AdminListen, DataDir: s.options.DataDir,
	}
	if s.analytics != nil {
		observed := s.analytics.Status()
		status.Analytics = analyticsRuntime{
			Enabled: true, QueueDepth: observed.QueueDepth,
			DroppedEvents: observed.Dropped.Queue + observed.Dropped.Invalid + observed.Dropped.Expired + observed.Dropped.Storage,
			LagMS:         observed.IngestLagMS, GeoIPAvailable: observed.GeoCountryAvailable || observed.GeoASNAvailable,
		}
		status.AnalyticsStatus = &observed
	}
	return &struct{ Body RuntimeStatus }{Body: status}, nil
}
