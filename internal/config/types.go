// Package config validates, compiles, and atomically publishes configuration.
package config

import (
	"mycdn/internal/rules"
)

const SchemaVersion = 1

type Document struct {
	SchemaVersion  int          `json:"schema_version"`
	Listen         Listen       `json:"listen"`
	DataDir        string       `json:"data_dir"`
	TrustedProxies []string     `json:"trusted_proxies"`
	Sites          []Site       `json:"sites"`
	Defaults       Defaults     `json:"defaults"`
	Rules          []rules.Rule `json:"rules"`
	Storage        Storage      `json:"storage"`
	Analytics      Analytics    `json:"analytics"`
}

type Listen struct {
	Proxy string `json:"proxy"`
	Admin string `json:"admin"`
}

type Site struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Enabled       bool     `json:"enabled"`
	Domains       []string `json:"domains"`
	Origins       []Origin `json:"origins"`
	DefaultOrigin string   `json:"default_origin"`
}

type Origin struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	URL           string              `json:"url"`
	HostHeader    string              `json:"host_header,omitempty"`
	TLSServerName string              `json:"tls_server_name,omitempty"`
	DialAddress   string              `json:"dial_address,omitempty"`
	CAFile        string              `json:"ca_file,omitempty"`
	Protocol      string              `json:"protocol,omitempty"`
	Timeouts      *rules.TimeoutPatch `json:"timeouts,omitempty"`
	caPEM         []byte
}

// CAPEM is the CA material captured at validation time, not a later file read.
func (o Origin) CAPEM() []byte { return append([]byte(nil), o.caPEM...) }

type Defaults struct {
	Cache    rules.CachePolicy      `json:"cache"`
	CacheKey rules.CacheKey         `json:"cache_key"`
	Upstream rules.UpstreamSettings `json:"upstream"`
	Identity rules.IdentitySettings `json:"identity"`
	Request  rules.RequestSettings  `json:"request"`
	Delivery rules.DeliverySettings `json:"delivery"`
}

type Storage struct {
	MaxBytes       int64 `json:"max_bytes"`
	MemoryBytes    int64 `json:"memory_bytes"`
	MaxFills       int   `json:"max_fills"`
	MaxBackground  int   `json:"max_background"`
	MaxObjectBytes int64 `json:"max_object_bytes"`
	InlineBytes    int64 `json:"inline_bytes"`
	BlockBytes     int64 `json:"block_bytes"`
	MaxReaders     int   `json:"max_readers"`
	FillTimeoutMS  int64 `json:"fill_timeout_ms"`
}

type Analytics struct {
	Enabled          bool   `json:"enabled"`
	DetailDays       int    `json:"detail_days"`
	RollupDays       int    `json:"rollup_days"`
	MaxBytes         int64  `json:"max_bytes"`
	QueueSize        int    `json:"queue_size"`
	BatchSize        int    `json:"batch_size"`
	FlushIntervalMS  int64  `json:"flush_interval_ms"`
	QueryConcurrency int    `json:"query_concurrency"`
	QueryTimeoutMS   int64  `json:"query_timeout_ms"`
	MemoryBytes      int64  `json:"memory_bytes"`
	TempBytes        int64  `json:"temp_bytes"`
	Threads          int    `json:"threads"`
	CountryMMDB      string `json:"country_mmdb,omitempty"`
	ASNMMDB          string `json:"asn_mmdb,omitempty"`
}

func Default() Document {
	return Document{
		SchemaVersion: SchemaVersion,
		Listen:        Listen{Proxy: ":8080", Admin: "127.0.0.1:8081"}, DataDir: "data",
		TrustedProxies: []string{}, Sites: []Site{}, Rules: []rules.Rule{},
		Defaults: Defaults{
			Cache:    rules.CachePolicy{Mode: "respect"},
			CacheKey: rules.CacheKey{IncludeHost: true, QueryMode: "all"},
			Upstream: rules.UpstreamSettings{Protocol: "auto", Timeouts: rules.Timeouts{ConnectMS: 10000, TLSHandshakeMS: 10000, ResponseHeaderMS: 30000, IdleMS: 90000, RequestMS: 0}},
			Identity: rules.IdentitySettings{ClientIPSource: "trusted_x_forwarded_for", SchemeSource: "trusted_x_forwarded_proto"},
			Request:  rules.RequestSettings{MaxUploadBytes: 64 << 20, AllowUpgrade: true},
		},
		Storage:   Storage{MaxBytes: 10 << 30, MemoryBytes: 64 << 20, MaxFills: 64, MaxBackground: 4, MaxObjectBytes: 2 << 30, InlineBytes: 32 << 10, BlockBytes: 1 << 20, MaxReaders: 4096, FillTimeoutMS: 1800000},
		Analytics: Analytics{Enabled: true, DetailDays: 14, RollupDays: 90, MaxBytes: 2 << 30, QueueSize: 8192, BatchSize: 1024, FlushIntervalMS: 1000, QueryConcurrency: 2, QueryTimeoutMS: 15000, MemoryBytes: 256 << 20, TempBytes: 128 << 20, Threads: 2},
	}
}
