package gateway

import "time"

// Both elapsed measurements use the same upward rounding so a sub-millisecond
// first byte remains positive without exceeding a zero-truncated duration.
func elapsedMS(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	return int64((duration-1)/time.Millisecond) + 1
}

// Observer implementations must be concurrency-safe and nonblocking. Event
// values are owned by the receiver. No bodies, headers, or raw queries are sent.
type Observer interface {
	RequestCompleted(RequestEvent)
	OriginCompleted(OriginEvent)
	Transfer(TransferEvent)
}

// Timestamps are Unix milliseconds; durations are milliseconds; sizes are bytes.
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
	FirstByteMS   int64  `json:"first_byte_ms"`
	RequestBytes  int64  `json:"request_bytes"`
	ResponseBytes int64  `json:"response_bytes"`
	CachedBytes   int64  `json:"cached_bytes"`
	Error         string `json:"error,omitempty"`
}

// Origin records describe actual transport attempts, not cache consumers. A
// fill/refresh has no client identity; RequestID is deliberately not included.
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
	FirstByteMS   int64  `json:"first_byte_ms"`
	RequestBytes  int64  `json:"request_bytes"`
	ResponseBytes int64  `json:"response_bytes"`
	Error         string `json:"error,omitempty"`
}

// Transfer is a delta, periodically drained by one sampler and once on close.
// Kind is "request" or "origin". Only request deltas carry ClientIP/RequestID.
// IntervalStart/End delimit the observation period, not the request lifetime.
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
