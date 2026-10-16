// Package analytics provides bounded asynchronous access analytics backed by local DuckDB.
package analytics

import (
	"errors"
	"time"
)

var (
	ErrClosed          = errors.New("analytics is closed")
	ErrBusy            = errors.New("analytics query concurrency limit reached")
	ErrDetailRetention = errors.New("query requires detail history outside its retention window")
	ErrStoragePressure = errors.New("analytics disk admission limit reached")
)

type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

type Options struct {
	Dir              string // Exclusively analytics-owned; Open makes it private (0700).
	MemoryLimitBytes int64
	TempLimitBytes   int64
	DiskBudgetBytes  int64
	DetailRetention  time.Duration
	RollupRetention  time.Duration
	QueueCapacity    int
	BatchSize        int
	FlushInterval    time.Duration
	QueryConcurrency int
	QueryTimeout     time.Duration
	Threads          int
	CountryMMDB      string
	ASNMMDB          string
	Now              func() time.Time // Optional clock; must be concurrency-safe.
}

// Event values are copied and immutable. Times are Unix milliseconds, sizes bytes.
type RequestEvent struct {
	RequestID     string `json:"request_id"`
	SiteID        string `json:"site_id"`
	Host          string `json:"host"`
	ClientIP      string `json:"client_ip"`
	Path          string `json:"path"`
	Method        string `json:"method"`
	ContentType   string `json:"content_type"`
	Status        int    `json:"status"`
	CacheResult   string `json:"cache_result"`
	StartedAt     int64  `json:"started_at"`
	CompletedAt   int64  `json:"completed_at"`
	DurationMS    int64  `json:"duration_ms"`
	FirstByteMS   int64  `json:"first_byte_ms"` // Positive measurement; 0 means unavailable.
	RequestBytes  int64  `json:"request_bytes"`
	ResponseBytes int64  `json:"response_bytes"`
	CachedBytes   int64  `json:"cached_bytes"`
	Error         string `json:"error,omitempty"`
}

// OriginEvent is one physical attempt, never a copy per cache consumer.
type OriginEvent struct {
	AttemptID     string `json:"attempt_id"`
	SiteID        string `json:"site_id"`
	OriginID      string `json:"origin_id"`
	Host          string `json:"host"`
	Path          string `json:"path"`
	Method        string `json:"method"`
	Protocol      string `json:"protocol"`
	Status        int    `json:"status"`
	StartedAt     int64  `json:"started_at"`
	CompletedAt   int64  `json:"completed_at"`
	DurationMS    int64  `json:"duration_ms"`
	FirstByteMS   int64  `json:"first_byte_ms"` // Positive measurement; 0 means unavailable.
	RequestBytes  int64  `json:"request_bytes"`
	ResponseBytes int64  `json:"response_bytes"`
	Error         string `json:"error,omitempty"`
}

// TransferEvent is an observed delta attributed at IntervalEnd, not at request completion.
type TransferEvent struct {
	Kind          string `json:"kind"`
	RequestID     string `json:"request_id,omitempty"`
	AttemptID     string `json:"attempt_id,omitempty"`
	SiteID        string `json:"site_id"`
	OriginID      string `json:"origin_id,omitempty"`
	Host          string `json:"host"`
	ClientIP      string `json:"client_ip,omitempty"`
	Path          string `json:"path"`
	Method        string `json:"method"`
	ContentType   string `json:"content_type,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
	Status        int    `json:"status"`
	CacheResult   string `json:"cache_result,omitempty"`
	IntervalStart int64  `json:"interval_start"`
	IntervalEnd   int64  `json:"interval_end"`
	RequestBytes  int64  `json:"request_bytes"`
	ResponseBytes int64  `json:"response_bytes"`
	CachedBytes   int64  `json:"cached_bytes"`
}

type Filters struct {
	SiteIDs      []string `json:"site_ids,omitempty"`
	Hosts        []string `json:"hosts,omitempty"`
	ClientIPs    []string `json:"client_ips,omitempty"`
	Methods      []string `json:"methods,omitempty"`
	Statuses     []int    `json:"statuses,omitempty"`
	CacheResults []string `json:"cache_results,omitempty"`
	ContentTypes []string `json:"content_types,omitempty"`
	Countries    []string `json:"countries,omitempty"`
	ASNs         []uint32 `json:"asns,omitempty"`
	PathPrefix   string   `json:"path_prefix,omitempty"`
}

type Query struct {
	From            string   `json:"from"`
	To              string   `json:"to"`
	Timezone        string   `json:"timezone,omitempty"`
	IntervalSeconds int      `json:"interval_seconds,omitempty"`
	Filters         Filters  `json:"filters"`
	GroupBy         []string `json:"group_by,omitempty"`
	Limit           int      `json:"limit,omitempty"`
}

type Percentiles struct {
	P50MS float64 `json:"p50_ms"`
	P95MS float64 `json:"p95_ms"`
	P99MS float64 `json:"p99_ms"`
}

type Metrics struct {
	Requests                         int64       `json:"requests"`
	UniqueIPs                        *int64      `json:"unique_ips"` // Exact completed-request clients; nil without full IP history or for origin rankings.
	RequestErrors                    int64       `json:"request_errors"`
	Status4xx                        int64       `json:"status_4xx"`
	Status5xx                        int64       `json:"status_5xx"`
	CacheHits                        int64       `json:"cache_hits"`
	CacheEligibleRequests            int64       `json:"cache_eligible_requests"`
	CoalescedRequests                int64       `json:"coalesced_requests"`
	RequestBytes                     int64       `json:"request_bytes"`
	ResponseBytes                    int64       `json:"response_bytes"`
	CachedBytes                      int64       `json:"cached_bytes"`
	OriginAttempts                   int64       `json:"origin_attempts"`
	OriginErrors                     int64       `json:"origin_errors"`
	OriginRequestBytes               int64       `json:"origin_request_bytes"`
	OriginResponseBytes              int64       `json:"origin_response_bytes"`
	DurationSumMS                    float64     `json:"duration_sum_ms"`
	OriginDurationSumMS              float64     `json:"origin_duration_sum_ms"`
	RequestsPerSecond                float64     `json:"requests_per_second"`
	ErrorRate                        float64     `json:"error_rate"`
	CacheHitRate                     float64     `json:"cache_hit_rate"`
	ByteHitRate                      float64     `json:"byte_hit_rate"`
	ResponseBytesPerSecond           float64     `json:"response_bytes_per_second"`
	OriginResponseBytesPerSecond     float64     `json:"origin_response_bytes_per_second"`
	PeakResponseBytesPerSecond       float64     `json:"peak_response_bytes_per_second"`
	PeakOriginResponseBytesPerSecond float64     `json:"peak_origin_response_bytes_per_second"`
	Latency                          Percentiles `json:"latency"`
	OriginLatency                    Percentiles `json:"origin_latency"`
	TTFB                             Percentiles `json:"ttfb"`
	OriginTTFB                       Percentiles `json:"origin_ttfb"`
	TTFBSamples                      int64       `json:"ttfb_samples"`
	OriginTTFBSamples                int64       `json:"origin_ttfb_samples"`
}

type Point struct {
	Time string `json:"time"`
	Metrics
}

type Breakdown struct {
	Value string `json:"value"`
	Metrics
}

type Geography struct {
	Country string `json:"country"`
	Metrics
}

type DropCounts struct {
	Queue   uint64 `json:"queue"`
	Invalid uint64 `json:"invalid"`
	Expired uint64 `json:"expired"`
	Storage uint64 `json:"storage"`
}

type Coverage struct {
	RequestedFrom        string     `json:"requested_from"`
	RequestedTo          string     `json:"requested_to"`
	DetailFrom           string     `json:"detail_from"`
	RollupFrom           string     `json:"rollup_from"`
	DataFrom             *string    `json:"data_from"`
	DataTo               *string    `json:"data_to"`
	DiskEvictedBefore    *string    `json:"disk_evicted_before"`
	UsesRollups          bool       `json:"uses_rollups"`
	LatencyApproximate   bool       `json:"latency_approximate"`
	UniqueIPsAvailable   bool       `json:"unique_ips_available"`
	Partial              bool       `json:"partial"`
	UpstreamAttributable bool       `json:"upstream_attributable"`
	BaseBucketSeconds    int        `json:"base_bucket_seconds"`
	IntervalSeconds      int        `json:"interval_seconds"`
	Dropped              DropCounts `json:"dropped"`
	QueuedEvents         int        `json:"queued_events"`
	IngestLagMS          int64      `json:"ingest_lag_ms"`
	GeoCountryAvailable  bool       `json:"geo_country_available"`
	GeoASNAvailable      bool       `json:"geo_asn_available"`
}

type Result struct {
	Summary    Metrics                `json:"summary"`
	Series     []Point                `json:"series"`
	Breakdowns map[string][]Breakdown `json:"breakdowns"`
	Geo        []Geography            `json:"geo"`
	Coverage   Coverage               `json:"coverage"`
}

type DetailsQuery struct {
	From    string  `json:"from"`
	To      string  `json:"to"`
	Filters Filters `json:"filters"`
	Limit   int     `json:"limit,omitempty"`
	Offset  int     `json:"offset,omitempty"`
}

type RequestDetail struct {
	RequestEvent
	Country string `json:"country"`
	ASN     uint32 `json:"asn"`
}

type DetailsResult struct {
	Rows       []RequestDetail `json:"rows"`
	HasMore    bool            `json:"has_more"`
	NextOffset *int            `json:"next_offset"`
	Coverage   Coverage        `json:"coverage"`
}

type Status struct {
	Since               int64      `json:"since"`
	Accepted            uint64     `json:"accepted"`
	Ingested            uint64     `json:"ingested"`
	Dropped             DropCounts `json:"dropped"`
	QueueDepth          int        `json:"queue_depth"`
	QueueCapacity       int        `json:"queue_capacity"`
	PendingEvents       int        `json:"pending_events"`
	IngestLagMS         int64      `json:"ingest_lag_ms"`
	LastIngestAt        int64      `json:"last_ingest_at"`
	LastError           string     `json:"last_error,omitempty"`
	DiskBytes           int64      `json:"disk_bytes"`
	DiskBudgetBytes     int64      `json:"disk_budget_bytes"`
	StoragePressure     bool       `json:"storage_pressure"`
	DetailFrom          string     `json:"detail_from"`
	RollupFrom          string     `json:"rollup_from"`
	DiskEvictedBefore   *string    `json:"disk_evicted_before"`
	GeoCountryAvailable bool       `json:"geo_country_available"`
	GeoASNAvailable     bool       `json:"geo_asn_available"`
	GeoError            string     `json:"geo_error,omitempty"`
	Closed              bool       `json:"closed"`
}
