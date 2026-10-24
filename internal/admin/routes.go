package admin

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"mycdn/internal/analytics"
	"mycdn/internal/cache"
	"mycdn/internal/gateway"
	"mycdn/internal/rules"
)

type entriesInput struct {
	SiteID     string `query:"site_id" maxLength:"256"`
	Host       string `query:"host" maxLength:"1024"`
	Path       string `query:"path" maxLength:"8192"`
	PathPrefix string `query:"path_prefix" maxLength:"8192"`
	Limit      int    `query:"limit" default:"100" minimum:"1" maximum:"500"`
}

type entriesResult struct {
	Entries   []cache.EntryInfo `json:"entries"`
	Limit     int               `json:"limit"`
	Truncated bool              `json:"truncated"`
}

type purgeSelection struct {
	SiteID     string `json:"site_id,omitempty" maxLength:"256"`
	Host       string `json:"host,omitempty" maxLength:"1024"`
	Path       string `json:"path,omitempty" maxLength:"8192"`
	PathPrefix string `json:"path_prefix,omitempty" maxLength:"8192"`
	All        bool   `json:"all,omitempty" doc:"Explicitly authorize an unfiltered purge. Filters, if given, still narrow the operation."`
}

type purgeResult struct {
	Purged int `json:"purged"`
}

type refreshSelection struct {
	URLs   []string `json:"urls" minItems:"1" maxItems:"32"`
	SiteID string   `json:"site_id,omitempty" maxLength:"256"`
}

type refreshBatch struct {
	Results []gateway.RefreshResult `json:"results"`
}

type healthResult struct {
	Status string `json:"status"`
}

func (s *Server) routes(api huma.API) {
	prepareSchemas(api)
	s.configRoutes(api)
	huma.Register(api, huma.Operation{
		OperationID: "get-capabilities", Method: http.MethodGet, Path: "/api/capabilities",
		Summary: "Get the rule compiler's field, action, and parameter catalog",
	}, func(_ context.Context, _ *struct{}) (*struct{ Body rules.Catalog }, error) {
		return &struct{ Body rules.Catalog }{Body: rules.Capabilities()}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "explain", Method: http.MethodPost, Path: "/api/explain",
		Summary:      "Explain a request with the real saved-config executor, without fetching",
		MaxBodyBytes: 1 << 20, Errors: []int{400, 403},
	}, func(ctx context.Context, input *struct{ Body gateway.ExplainInput }) (*struct{ Body gateway.ExplainResult }, error) {
		result, err := s.gateway.Explain(ctx, input.Body)
		if err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		return &struct{ Body gateway.ExplainResult }{Body: result}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "list-cache-entries", Method: http.MethodGet, Path: "/api/cache/entries",
		Summary:                      "Inspect up to 500 cache entries with intersecting filters",
		RejectUnknownQueryParameters: true, Errors: []int{422, 503},
	}, s.entries)
	huma.Register(api, huma.Operation{
		OperationID: "purge-cache", Method: http.MethodPost, Path: "/api/cache/purge",
		Summary:      "Purge entries matching a nonempty selection, or explicitly all entries",
		MaxBodyBytes: 64 << 10, Errors: []int{400, 403, 422, 503},
	}, s.purge)
	huma.Register(api, huma.Operation{
		OperationID: "refresh-cache", Method: http.MethodPost, Path: "/api/cache/refresh",
		Summary:      "Refresh concrete URLs through the gateway's content acquisition path",
		Description:  "Validates the entire batch before acquisition. Results report each URL as refreshed, skipped, or error; HTTP 200 alone does not imply every URL was refreshed.",
		MaxBodyBytes: 1 << 20, Errors: []int{400, 403, 422, 503},
	}, s.refresh)
	huma.Register(api, huma.Operation{
		OperationID: "query-analytics", Method: http.MethodPost, Path: "/api/analytics/query",
		Summary:      "Query retained traffic with consistent filters across all sections",
		MaxBodyBytes: 1 << 20, Errors: []int{400, 403, 422, 503, 504},
	}, s.queryAnalytics)
	huma.Register(api, huma.Operation{
		OperationID: "query-request-details", Method: http.MethodPost, Path: "/api/analytics/details",
		Summary:      "Inspect a bounded page of retained request outcomes",
		MaxBodyBytes: 1 << 20, Errors: []int{400, 403, 422, 503, 504},
	}, s.requestDetails)
	huma.Register(api, huma.Operation{
		OperationID: "get-status", Method: http.MethodGet, Path: "/api/status",
		Summary: "Get observed process, cache, ingestion, and restart-required state",
	}, s.status)
	huma.Register(api, huma.Operation{
		OperationID: "health", Method: http.MethodGet, Path: "/healthz",
		Summary: "Check that the management listener is serving requests", Security: []map[string][]string{},
	}, func(_ context.Context, _ *struct{}) (*struct{ Body healthResult }, error) {
		return &struct{ Body healthResult }{Body: healthResult{Status: "ok"}}, nil
	})
}

func (s *Server) entries(ctx context.Context, input *entriesInput) (*struct{ Body entriesResult }, error) {
	filter := cache.Filter{SiteID: input.SiteID, Host: input.Host, Path: input.Path, PathPrefix: input.PathPrefix}
	entries, err := s.cache.Entries(ctx, filter, input.Limit+1)
	if err != nil {
		return nil, managementError("list cache entries", err)
	}
	truncated := len(entries) > input.Limit
	if truncated {
		entries = entries[:input.Limit]
	}
	return &struct{ Body entriesResult }{Body: entriesResult{Entries: entries, Limit: input.Limit, Truncated: truncated}}, nil
}

func (s *Server) purge(ctx context.Context, input *struct{ Body purgeSelection }) (*struct{ Body purgeResult }, error) {
	selection := input.Body
	if !selection.All && strings.TrimSpace(selection.SiteID) == "" && strings.TrimSpace(selection.Host) == "" && strings.TrimSpace(selection.Path) == "" && strings.TrimSpace(selection.PathPrefix) == "" {
		return nil, fieldError("body", "provide a nonempty site_id, host, path, or path_prefix filter, or explicitly set all to true")
	}
	count, err := s.cache.Purge(ctx, cache.Filter{SiteID: selection.SiteID, Host: selection.Host, Path: selection.Path, PathPrefix: selection.PathPrefix})
	if err != nil {
		return nil, managementError("purge cache", err)
	}
	return &struct{ Body purgeResult }{Body: purgeResult{Purged: count}}, nil
}

func (s *Server) refresh(ctx context.Context, input *struct{ Body refreshSelection }) (*struct{ Body refreshBatch }, error) {
	results, err := s.gateway.Refresh(ctx, input.Body.URLs, input.Body.SiteID)
	if err != nil {
		return nil, managementError("refresh cache", err)
	}
	return &struct{ Body refreshBatch }{Body: refreshBatch{Results: results}}, nil
}

func (s *Server) queryAnalytics(ctx context.Context, input *struct{ Body analytics.Query }) (*struct{ Body *analytics.Result }, error) {
	if s.analytics == nil {
		return nil, huma.Error503ServiceUnavailable("analytics collection is disabled; enable it and restart to query retained traffic")
	}
	result, err := s.analytics.Query(ctx, input.Body)
	if err != nil {
		return nil, managementError("query analytics", err)
	}
	return &struct{ Body *analytics.Result }{Body: result}, nil
}

func (s *Server) requestDetails(ctx context.Context, input *struct{ Body analytics.DetailsQuery }) (*struct{ Body *analytics.DetailsResult }, error) {
	if s.analytics == nil {
		return nil, huma.Error503ServiceUnavailable("analytics collection is disabled; enable it and restart to inspect retained requests")
	}
	result, err := s.analytics.Details(ctx, input.Body)
	if err != nil {
		return nil, managementError("query request details", err)
	}
	return &struct{ Body *analytics.DetailsResult }{Body: result}, nil
}
