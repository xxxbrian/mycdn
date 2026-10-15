package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"mycdn/internal/cache"
	"mycdn/internal/rules"
)

type RefreshResult struct {
	URL    string `json:"url"`
	Status string `json:"status"` // refreshed, skipped, or error
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Refresh validates the entire batch before acquisition, then refreshes in input
// order against one current snapshot. An overall error means no acquisition was
// started. After validation, operational failures (including cancellation) are
// per-URL results; they never masquerade as successful refreshes.
func (g *Gateway) Refresh(ctx context.Context, urls []string, siteID string) ([]RefreshResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if g.closed.Load() {
		return nil, fmt.Errorf("gateway closed")
	}
	if g.cache == nil {
		return nil, fmt.Errorf("cache is unavailable")
	}
	if len(urls) == 0 || len(urls) > 32 {
		return nil, &rules.ValidationError{Path: "urls", Message: "requires 1..32 absolute client URLs"}
	}
	snapshot := g.config.Get()
	plans := make([]*prepared, len(urls))
	requests := make([]*http.Request, len(urls))
	for i, value := range urls {
		invalid := func(message string) ([]RefreshResult, error) {
			return nil, &rules.ValidationError{Path: fmt.Sprintf("urls[%d]", i), Message: message}
		}
		if len(value) > 8192 {
			return invalid("client URL exceeds 8192 bytes")
		}
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" || strings.Contains(value, "#") {
			return invalid("expected absolute HTTP(S) URL without credentials or fragment (at most 8192 bytes)")
		}
		// This is planning input, not a public/self HTTP request. Do not inherit an
		// admin request's headers, peer, context values, tracing, or protocol facts.
		request, err := http.NewRequest(http.MethodGet, value, nil)
		if err != nil {
			return invalid("invalid client URL")
		}
		request.Proto, request.ProtoMajor, request.ProtoMinor = "", 0, 0
		if u.Scheme == "https" {
			request.TLS = &tls.ConnectionState{}
		}
		p, status, err := prepare(snapshot, request, nil)
		if err != nil {
			switch status {
			case http.StatusMisdirectedRequest:
				return invalid("host does not match an enabled site")
			case http.StatusBadRequest:
				return invalid("invalid client host or query")
			default:
				return invalid("current rules cannot resolve an origin for this URL")
			}
		}
		if siteID != "" && p.site.ID != siteID {
			return invalid("host does not belong to the requested site")
		}
		plans[i], requests[i] = p, request
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	results := make([]RefreshResult, len(urls))
	for i, p := range plans {
		result := RefreshResult{URL: urls[i]}
		switch {
		case ctx.Err() != nil:
			result.Status, result.Error = "error", refreshError(ctx.Err())
		case g.closed.Load():
			result.Status, result.Error = "error", "gateway closed"
		case p.plans.Terminal != nil:
			result.Status, result.Reason = "skipped", "current rules produce a local reply or redirect"
		case p.plans.Content.Cache.Mode == "bypass":
			result.Status, result.Reason = "skipped", "current rules bypass the cache"
		default:
			a, err := g.acquisition(p, requests[i])
			if err == nil {
				err = g.cache.Refresh(ctx, cache.Request{
					Key: p.key, SiteID: p.site.ID, Host: p.host, Path: p.original.Path,
					Method: http.MethodGet, Header: a.header.Clone(), Policy: cachePolicy(p.plans.Content.Cache),
					Revalidate: true,
					Fetch:      g.fetch(*a, nil, 0, nil),
					Transform:  sourceTransform(snapshot.Program(), p.facts, p.plans.Content),
				})
			}
			if err != nil {
				if ctx.Err() != nil {
					err = ctx.Err()
				}
				result.Status, result.Error = "error", refreshError(err)
			} else {
				result.Status = "refreshed"
			}
		}
		results[i] = result
	}
	return results, nil
}

// Do not return raw parser/transport errors: they may contain a client URL,
// query, upstream location, or other credential-bearing input. The URL field is
// explicitly caller-supplied; diagnostic messages contain only safe categories.
func refreshError(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "refresh canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "refresh deadline exceeded"
	case errors.Is(err, cache.ErrClosed):
		return "cache closed"
	case errors.Is(err, cache.ErrBusy):
		return "cache concurrency limit reached"
	case errors.Is(err, cache.ErrCapacity):
		return "cache capacity exhausted"
	case errors.Is(err, cache.ErrObjectTooLarge):
		return "origin object exceeds cache size limit"
	case errors.Is(err, cache.ErrNotStored):
		return "refresh did not publish reusable content"
	}
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return "origin TLS verification failed"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "origin request timed out"
	}
	return "cache refresh failed"
}
