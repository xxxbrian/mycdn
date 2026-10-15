package gateway

// Stats is a lightweight live snapshot, independent of analytics ingestion and
// cache statistics. Counters are process-local and need no sampler or database.
type Stats struct {
	StartedAt       int64  `json:"started_at"`
	ActiveRequests  int64  `json:"active_requests"`
	Requests        uint64 `json:"requests"`
	ActiveUpstreams int    `json:"active_upstreams"`
	MaxUpstreams    int    `json:"max_upstreams"`
}

func (g *Gateway) Stats() Stats {
	return Stats{StartedAt: g.startedAt, ActiveRequests: g.activeRequests.Load(), Requests: g.requests.Load(), ActiveUpstreams: len(g.upstream), MaxUpstreams: cap(g.upstream)}
}
