// Package rules compiles the small, typed rule language used by the proxy.
package rules

import "net/http"

type Stage string

const (
	Ingress        Stage = "ingress"
	Request        Stage = "request"
	OriginResponse Stage = "origin_response"
	Delivery       Stage = "delivery"
)

type Rule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Stage   Stage  `json:"stage"`
	Steps   []Step `json:"steps"`
}

type Step struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Action   *Action  `json:"action,omitempty"`
	Branches []Branch `json:"branches,omitempty"`
}

type Branch struct {
	ID        string     `json:"id"`
	Condition *Condition `json:"condition"`
	Steps     []Step     `json:"steps"`
}

type Condition struct {
	Type     string      `json:"type"`
	Children []Condition `json:"children,omitempty"`
	Field    string      `json:"field,omitempty"`
	Key      string      `json:"key,omitempty"`
	Op       string      `json:"op,omitempty"`
	Values   []string    `json:"values,omitempty"`
}

// Action is a tagged union. Exactly the parameter named by Type must be set.
type Action struct {
	Type       string          `json:"type"`
	Cache      *CachePatch     `json:"cache,omitempty"`
	CacheKey   *CacheKey       `json:"cache_key,omitempty"`
	Origin     *string         `json:"origin,omitempty"`
	Path       *string         `json:"path,omitempty"`
	Query      *QueryMutation  `json:"query,omitempty"`
	Cookie     *CookieMutation `json:"cookie,omitempty"`
	Header     *HeaderMutation `json:"header,omitempty"`
	Authority  *string         `json:"authority,omitempty"`
	Upstream   *UpstreamPatch  `json:"upstream,omitempty"`
	Identity   *IdentityPatch  `json:"identity,omitempty"`
	Limits     *LimitsPatch    `json:"limits,omitempty"`
	BrowserTTL *int64          `json:"browser_ttl_seconds,omitempty"`
	CORS       *CORS           `json:"cors,omitempty"`
	Reply      *Reply          `json:"reply,omitempty"`
	Redirect   *Redirect       `json:"redirect,omitempty"`
}

type CachePolicy struct {
	Mode                        string      `json:"mode"`
	TTLSeconds                  int64       `json:"ttl_seconds"`
	StaleWhileRevalidateSeconds int64       `json:"stale_while_revalidate_seconds"`
	StaleIfErrorSeconds         int64       `json:"stale_if_error_seconds"`
	Prefetch                    bool        `json:"prefetch"`
	StatusTTL                   []StatusTTL `json:"status_ttl,omitempty"`
}

type StatusTTL struct {
	Status     int   `json:"status"`
	TTLSeconds int64 `json:"ttl_seconds"`
}

type CachePatch struct {
	Mode                        *string      `json:"mode,omitempty"`
	TTLSeconds                  *int64       `json:"ttl_seconds,omitempty"`
	StaleWhileRevalidateSeconds *int64       `json:"stale_while_revalidate_seconds,omitempty"`
	StaleIfErrorSeconds         *int64       `json:"stale_if_error_seconds,omitempty"`
	Prefetch                    *bool        `json:"prefetch,omitempty"`
	StatusTTL                   *[]StatusTTL `json:"status_ttl,omitempty"`
}

type CacheKey struct {
	Namespace   string   `json:"namespace,omitempty"`
	IncludeHost bool     `json:"include_host"`
	QueryMode   string   `json:"query_mode"`
	QueryNames  []string `json:"query_names,omitempty"`
	Headers     []string `json:"headers,omitempty"`
	Cookies     []string `json:"cookies,omitempty"`
}

type Timeouts struct {
	ConnectMS        int64 `json:"connect_ms"`
	TLSHandshakeMS   int64 `json:"tls_handshake_ms"`
	ResponseHeaderMS int64 `json:"response_header_ms"`
	IdleMS           int64 `json:"idle_ms"`
	RequestMS        int64 `json:"request_ms"`
}

type TimeoutPatch struct {
	ConnectMS        *int64 `json:"connect_ms,omitempty"`
	TLSHandshakeMS   *int64 `json:"tls_handshake_ms,omitempty"`
	ResponseHeaderMS *int64 `json:"response_header_ms,omitempty"`
	IdleMS           *int64 `json:"idle_ms,omitempty"`
	RequestMS        *int64 `json:"request_ms,omitempty"`
}

type UpstreamSettings struct {
	Protocol string   `json:"protocol"`
	Timeouts Timeouts `json:"timeouts"`
}

type UpstreamPatch struct {
	Protocol *string       `json:"protocol,omitempty"`
	Timeouts *TimeoutPatch `json:"timeouts,omitempty"`
}

type IdentitySettings struct {
	ClientIPSource string `json:"client_ip_source"`
	SchemeSource   string `json:"scheme_source"`
	// nil uses ClientIPSource; a non-nil empty slice deliberately means peer.
	ClientIPHeaders []ClientIPHeader `json:"client_ip_headers,omitzero"`
}

type IdentityPatch struct {
	ClientIPSource  *string           `json:"client_ip_source,omitempty"`
	SchemeSource    *string           `json:"scheme_source,omitempty"`
	ClientIPHeaders *[]ClientIPHeader `json:"client_ip_headers,omitempty"`
}

type ClientIPHeader struct {
	Name   string `json:"name"`
	Format string `json:"format"`
}

type RequestSettings struct {
	MaxUploadBytes int64 `json:"max_upload_bytes"`
	AllowUpgrade   bool  `json:"allow_upgrade"`
}

type LimitsPatch struct {
	MaxUploadBytes *int64 `json:"max_upload_bytes,omitempty"`
	AllowUpgrade   *bool  `json:"allow_upgrade,omitempty"`
}

type HeaderMutation struct {
	Operation string `json:"operation"`
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
}

type QueryMutation struct {
	Operation string `json:"operation"`
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
}

type CookieMutation struct {
	Operation string `json:"operation"`
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
}

type CORS struct {
	Origins     []string `json:"origins"`
	Methods     []string `json:"methods,omitempty"`
	Headers     []string `json:"headers,omitempty"`
	Expose      []string `json:"expose,omitempty"`
	Credentials bool     `json:"credentials"`
	MaxAge      int64    `json:"max_age_seconds,omitempty"`
}

type DeliverySettings struct {
	Headers    []HeaderMutation `json:"headers,omitempty"`
	BrowserTTL *int64           `json:"browser_ttl_seconds,omitempty"`
	CORS       *CORS            `json:"cors,omitempty"`
}

type Reply struct {
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Body        string `json:"body"`
}

type Redirect struct {
	Status   int    `json:"status"`
	Location string `json:"location"`
}

// Facts is a stage-entry snapshot, never modified by the executor. Original is
// the original request snapshot; response facts exist only in the last stages.
type Facts struct {
	SiteID, Host, Path, RawQuery, URL, Method string
	ClientIP, Scheme                          string
	PeerIP, HTTPVersion                       string
	Header                                    http.Header
	Status                                    int
	ResponseHeader                            http.Header
	CacheResult                               string
	Original                                  *Facts
	query                                     map[string][]string
	cookies                                   map[string][]string
	captured                                  map[factReference][]string
}

// Plans keep consumer settings, content identity/storage, acquisition, and
// delivery separate. Clone before reusing a plan across consumers or fills.
type Plans struct {
	Request  RequestPlan
	Content  ContentPlan
	Fetch    FetchPlan
	Delivery DeliveryPlan
	Terminal *Terminal
}

type RequestPlan struct {
	Identity IdentitySettings
	Limits   RequestSettings
}

type ContentPlan struct {
	Cache          CachePolicy
	Key            CacheKey
	ResponseHeader http.Header
}

type FetchPlan struct {
	OriginID  string
	Path      string
	RawPath   string
	RawQuery  string
	Header    http.Header
	Authority string
	Upstream  UpstreamSettings
	// Overrides records only explicit rule fields, so a selected named origin's
	// defaults can be overlaid before these rule changes.
	Overrides UpstreamPatch
}

type DeliveryPlan struct {
	DeliverySettings
}

type Terminal struct {
	Status      int
	ContentType string
	Body        string
	Location    string
}

type Trace struct {
	Stage    Stage  `json:"stage"`
	RuleID   string `json:"rule_id"`
	StepID   string `json:"step_id"`
	BranchID string `json:"branch_id,omitempty"`
	Matched  *bool  `json:"matched,omitempty"`
	Action   string `json:"action,omitempty"`
	Before   string `json:"before,omitempty"`
	After    string `json:"after,omitempty"`
}

type ValidationError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e *ValidationError) Error() string { return e.Path + ": " + e.Message }
