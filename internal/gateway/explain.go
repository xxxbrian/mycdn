package gateway

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"

	"mycdn/internal/rules"
)

type ExplainInput struct {
	Method      string             `json:"method"`
	URL         string             `json:"url"`
	Host        string             `json:"host,omitempty"`
	Peer        string             `json:"peer"`
	HTTPVersion string             `json:"http_version,omitempty"`
	Header      http.Header        `json:"headers,omitempty"`
	Response    *SimulatedResponse `json:"response,omitempty"`
}

type SimulatedResponse struct {
	Status      int         `json:"status"`
	Header      http.Header `json:"headers,omitempty"`
	CacheResult string      `json:"cache_result,omitempty"`
}

type ExplainResult struct {
	SiteID          string            `json:"site_id"`
	OriginID        string            `json:"origin_id"`
	OriginURL       string            `json:"origin_url"`
	Authority       string            `json:"authority"`
	Protocol        string            `json:"protocol"`
	CacheKey        string            `json:"cache_key"`
	CachePolicy     rules.CachePolicy `json:"cache_policy"`
	ClientIP        string            `json:"client_ip"`
	PeerIP          string            `json:"peer_ip"`
	HTTPVersion     string            `json:"http_version"`
	Scheme          string            `json:"scheme"`
	TerminalStatus  int               `json:"terminal_status,omitempty"`
	Trace           []rules.Trace     `json:"trace"`
	ResponseHeaders http.Header       `json:"response_headers,omitempty"`
}

// Explain executes the same compiler/executor and plans without cache access or
// network activity. Source-response/delivery stages use optional simulated facts.
func (g *Gateway) Explain(ctx context.Context, input ExplainInput) (ExplainResult, error) {
	if err := ctx.Err(); err != nil {
		return ExplainResult{}, err
	}
	if input.Method == "" {
		input.Method = http.MethodGet
	}
	if input.HTTPVersion != "" {
		switch input.HTTPVersion {
		case "HTTP/1.0", "HTTP/1.1", "HTTP/2.0", "HTTP/3.0":
		default:
			return ExplainResult{}, fmt.Errorf("unsupported HTTP version")
		}
	}
	u, err := url.Parse(input.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return ExplainResult{}, fmt.Errorf("url must be an absolute HTTP(S) URL")
	}
	r, err := http.NewRequestWithContext(ctx, input.Method, input.URL, nil)
	if err != nil {
		return ExplainResult{}, err
	}
	r.RemoteAddr = input.Peer
	if input.HTTPVersion != "" {
		r.Proto = input.HTTPVersion
	}
	r.Header, err = explainHeaders(input.Header)
	if err != nil {
		return ExplainResult{}, err
	}
	if input.Host != "" {
		r.Host = input.Host
	}
	// The URL's HTTPS scheme models a TLS connection for explain only.
	if u.Scheme == "https" {
		r.TLS = &tls.ConnectionState{}
	}
	trace := []rules.Trace{}
	p, _, err := prepare(g.config.Get(), r, &trace)
	if err != nil {
		return ExplainResult{}, err
	}
	result := ExplainResult{SiteID: p.site.ID, OriginID: p.origin.ID, OriginURL: p.target.String(), Authority: p.plans.Fetch.Authority, Protocol: p.plans.Fetch.Upstream.Protocol, CacheKey: p.key, CachePolicy: p.plans.Content.Cache, ClientIP: p.clientIP, PeerIP: p.facts.PeerIP, HTTPVersion: p.facts.HTTPVersion, Scheme: p.scheme}
	status := http.StatusOK
	headers := make(http.Header)
	outcome := "MISS"
	if input.Response != nil {
		if input.Response.Status < 200 || input.Response.Status > 599 {
			return result, fmt.Errorf("response status must be 200..599")
		}
		status = input.Response.Status
		headers, err = explainHeaders(input.Response.Header)
		if err != nil {
			return ExplainResult{}, fmt.Errorf("simulated response: %w", err)
		}
		if input.Response.CacheResult != "" {
			outcome = input.Response.CacheResult
		}
	}
	if p.plans.Terminal == nil && input.Response != nil {
		facts := p.facts
		facts.Status = status
		facts.ResponseHeader = headers.Clone()
		p.plans.Content.ResponseHeader = headers.Clone()
		p.snapshot.Program().Run(rules.OriginResponse, facts, &p.plans, &trace)
		headers = p.plans.Content.ResponseHeader
		result.CachePolicy = p.plans.Content.Cache
	}
	if p.plans.Terminal != nil {
		status = p.plans.Terminal.Status
		headers = terminalResponse(p.plans.Terminal, r).Header
		outcome = "BYPASS"
	}
	facts := p.facts
	facts.Status = status
	facts.ResponseHeader = headers.Clone()
	facts.CacheResult = outcome
	initialTerminal := p.plans.Terminal
	p.plans.Terminal = nil
	p.snapshot.Program().Run(rules.Delivery, facts, &p.plans, &trace)
	if p.plans.Terminal != nil {
		result.TerminalStatus = p.plans.Terminal.Status
		headers = terminalResponse(p.plans.Terminal, r).Header
	} else if initialTerminal != nil {
		result.TerminalStatus = initialTerminal.Status
	}
	for _, mutation := range p.plans.Delivery.Headers {
		rules.ApplyHeader(headers, mutation)
	}
	if p.plans.Delivery.CORS != nil {
		applyCORS(headers, p.original.Header, *p.plans.Delivery.CORS)
	}
	if p.plans.Delivery.BrowserTTL != nil {
		headers.Set("Cache-Control", fmt.Sprintf("public, max-age=%d", *p.plans.Delivery.BrowserTTL))
		headers.Del("Expires")
	}
	result.Trace = trace
	result.ResponseHeaders = headers
	return result, nil
}

func explainHeaders(input http.Header) (http.Header, error) {
	header := make(http.Header, len(input))
	for name, values := range input {
		if !rules.ValidHeader(name, "") {
			return nil, fmt.Errorf("invalid header name")
		}
		for _, value := range values {
			if !rules.ValidHeader(name, value) {
				return nil, fmt.Errorf("invalid header value")
			}
			header.Add(name, value)
		}
	}
	return header, nil
}
