package cache

import (
	"context"
	"net/http"
	"time"
)

// Policy controls reuse, not content identity. Durations are measured from the
// origin's freshness metadata or the explicit TTL when Mode is "force".
type Policy struct {
	Mode                 string
	TTL                  time.Duration
	StaleWhileRevalidate time.Duration
	StaleIfError         time.Duration
	Prefetch             bool
	StatusTTL            map[int]time.Duration
}

type Request struct {
	Key    string
	SiteID string
	Host   string
	Path   string
	Method string
	Header http.Header
	Policy Policy
	// Revalidate skips fresh/SWR reuse for this consumer. It does not alter the
	// cache key, acquisition signature, or headers sent by Fetch.
	Revalidate bool
	// Fetch retains an immutable outbound plan and honors the supplied context.
	// Override fields replace outgoing fields; a nil value removes a field.
	Fetch func(context.Context, http.Header) (*http.Response, error)
	// Transform runs source-response rules before cache eligibility and storage.
	// It may update policy and safe headers, never consume the body.
	Transform func(*http.Response, *Policy) error
}

type Response struct {
	HTTP    *http.Response
	Outcome string
	Age     time.Duration
	// CachedBytes counts read bytes originating in published coverage, not
	// newly acquired bytes read from a shared fill. Safe to sample concurrently.
	CachedBytes func() int64
	// CachedPrefix counts previously published bytes in the first n bytes of
	// this response body, independently of read-ahead. Immutable and concurrent-
	// safe; callers pass the number of bytes successfully delivered so far.
	CachedPrefix func(int64) int64
	wait         func(context.Context) error
}

type Options struct {
	Dir            string
	MaxBytes       int64
	MemoryBytes    int64
	MaxFills       int
	MaxBackground  int
	MaxObjectBytes int64
	InlineBytes    int64
	BlockBytes     int64
	MaxReaders     int
	FillTimeout    time.Duration
}

type Filter struct {
	SiteID     string `json:"site_id,omitempty"`
	Host       string `json:"host,omitempty"`
	Path       string `json:"path,omitempty"`
	PathPrefix string `json:"path_prefix,omitempty"`
}

type EntryInfo struct {
	Key        string    `json:"key"`
	SiteID     string    `json:"site_id"`
	Host       string    `json:"host"`
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	Status     int       `json:"status"`
	StoredAt   time.Time `json:"stored_at"`
	AccessedAt time.Time `json:"accessed_at"`
	FreshUntil time.Time `json:"fresh_until"`
	Complete   bool      `json:"complete"`
}

type Stats struct {
	Entries     int64 `json:"entries"`
	Bytes       int64 `json:"bytes"`
	ActiveFills int64 `json:"active_fills"`
	Readers     int64 `json:"readers"`
	Hits        int64 `json:"hits"`
	Misses      int64 `json:"misses"`
	Evictions   int64 `json:"evictions"`
	Coalesced   int64 `json:"coalesced"`
	Bypasses    int64 `json:"bypasses"`
	// ResidentBytes includes unpublished fills and generations held by old readers.
	// Pebble's WAL/LSM overhead is not part of this body-byte budget.
	ResidentBytes int64 `json:"resident_bytes"`
	StorageErrors int64 `json:"storage_errors"`
}
