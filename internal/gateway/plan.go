package gateway

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"mycdn/internal/cache"
	"mycdn/internal/config"
	"mycdn/internal/rules"
)

type prepared struct {
	snapshot               *config.Snapshot
	site                   config.Site
	origin                 config.Origin
	plans                  rules.Plans
	original               rules.Facts
	facts                  rules.Facts
	target                 *url.URL
	key                    string
	clientIP, scheme, host string
}

func prepare(snapshot *config.Snapshot, request *http.Request, trace *[]rules.Trace) (*prepared, int, error) {
	clientURL := *request.URL
	if clientURL.Path == "" {
		clientURL.Path = "/"
		clientURL.RawPath = ""
	}
	host, err := normalizeHost(request.Host)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	site, ok := snapshot.MatchSite(host)
	if !ok {
		return nil, http.StatusMisdirectedRequest, fmt.Errorf("unknown host")
	}
	if _, err := url.ParseQuery(clientURL.RawQuery); err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid query")
	}
	p := &prepared{snapshot: snapshot, site: site, host: host, plans: snapshot.Plans()}
	peerIP := ""
	if peer := peerAddress(request.RemoteAddr); peer.IsValid() {
		peerIP = peer.String()
	}
	p.clientIP, p.scheme = identity(snapshot, p.plans.Request.Identity, request)
	originalURL := &url.URL{Scheme: p.scheme, Host: request.Host, Path: clientURL.Path, RawPath: clientURL.RawPath, RawQuery: clientURL.RawQuery}
	p.original = rules.Facts{SiteID: site.ID, Host: host, Path: clientURL.Path, RawQuery: clientURL.RawQuery, URL: originalURL.String(), Method: request.Method, ClientIP: p.clientIP, Scheme: p.scheme, PeerIP: peerIP, HTTPVersion: request.Proto, Header: request.Header.Clone()}
	p.facts = p.original
	p.facts.Original = &p.original
	p.plans.Fetch.OriginID = site.DefaultOrigin
	p.plans.Fetch.Path = clientURL.Path
	p.plans.Fetch.RawPath = clientURL.RawPath
	p.plans.Fetch.RawQuery = clientURL.RawQuery
	p.plans.Fetch.Header = request.Header.Clone()
	stripClientMetadata(p.plans.Fetch.Header)
	for _, source := range p.plans.Request.Identity.ClientIPHeaders {
		p.plans.Fetch.Header.Del(source.Name)
	}
	snapshot.Program().Run(rules.Ingress, p.facts, &p.plans, trace)
	p.clientIP, p.scheme = identity(snapshot, p.plans.Request.Identity, request)
	p.facts.ClientIP, p.facts.Scheme = p.clientIP, p.scheme
	originalURL.Scheme = p.scheme
	p.facts.URL = originalURL.String()
	for _, source := range p.plans.Request.Identity.ClientIPHeaders {
		p.plans.Fetch.Header.Del(source.Name)
	}
	if p.plans.Terminal == nil {
		snapshot.Program().Run(rules.Request, p.facts, &p.plans, trace)
	}
	for _, o := range site.Origins {
		if o.ID == p.plans.Fetch.OriginID {
			p.origin = o
			break
		}
	}
	if p.origin.ID == "" {
		return nil, http.StatusBadGateway, fmt.Errorf("selected origin %q does not exist in site", p.plans.Fetch.OriginID)
	}
	settings := snapshot.Plans().Fetch.Upstream
	if p.origin.Protocol != "" {
		settings.Protocol = p.origin.Protocol
	}
	rules.ApplyUpstream(&settings, rules.UpstreamPatch{Timeouts: p.origin.Timeouts})
	rules.ApplyUpstream(&settings, p.plans.Fetch.Overrides)
	p.plans.Fetch.Upstream = settings
	target, _ := url.Parse(p.origin.URL)
	target.Path, target.RawPath = joinURLPath(target, &url.URL{Path: p.plans.Fetch.Path, RawPath: p.plans.Fetch.RawPath})
	target.RawQuery = p.plans.Fetch.RawQuery
	p.target = target
	// Later stages enter with the request-stage output, while Original remains
	// the untouched ingress snapshot. The request executor itself never sees its
	// own actions change condition inputs.
	p.facts.Path = p.plans.Fetch.Path
	p.facts.RawQuery = p.plans.Fetch.RawQuery
	p.facts.Header = p.plans.Fetch.Header.Clone()
	stageURL := *originalURL
	stageURL.Path = p.facts.Path
	stageURL.RawPath = p.plans.Fetch.RawPath
	stageURL.RawQuery = p.facts.RawQuery
	p.facts.URL = stageURL.String()
	if p.plans.Fetch.Authority == "" {
		p.plans.Fetch.Authority = p.origin.HostHeader
	}
	if p.plans.Fetch.Authority == "" {
		p.plans.Fetch.Authority = target.Host
	}
	p.key = contentKey(p)
	return p, 0, nil
}

func contentKey(p *prepared) string {
	h := sha256.New()
	for _, v := range []string{"mycdn:1", p.site.ID, p.origin.ID, p.origin.URL, p.origin.DialAddress, p.origin.TLSServerName, p.plans.Fetch.Authority, p.target.EscapedPath(), p.plans.Content.Key.Namespace, p.snapshot.Program().FactsKey(rules.OriginResponse, p.facts)} {
		keyPart(h, v)
	}
	k := p.plans.Content.Key
	keyPart(h, strconv.FormatBool(k.IncludeHost))
	if k.IncludeHost {
		keyPart(h, p.host)
	} else {
		keyPart(h, "")
	}
	q, _ := url.ParseQuery(p.target.RawQuery)
	for name := range q {
		if k.QueryMode == "ignore" || k.QueryMode == "include" && !slices.Contains(k.QueryNames, name) || k.QueryMode == "exclude" && slices.Contains(k.QueryNames, name) {
			q.Del(name)
		}
	}
	keyPart(h, q.Encode())
	names := slices.Clone(k.Headers)
	sort.Strings(names)
	keyPart(h, strconv.Itoa(len(names)))
	for _, name := range names {
		keyPart(h, http.CanonicalHeaderKey(name))
		values := p.plans.Fetch.Header.Values(name)
		keyPart(h, strconv.Itoa(len(values)))
		for _, value := range values {
			keyPart(h, value)
		}
	}
	r := http.Request{Header: p.plans.Fetch.Header}
	names = slices.Clone(k.Cookies)
	sort.Strings(names)
	keyPart(h, strconv.Itoa(len(names)))
	for _, name := range names {
		keyPart(h, name)
		var values []string
		for _, cookie := range r.Cookies() {
			if cookie.Name == name {
				values = append(values, cookie.Value)
			}
		}
		keyPart(h, strconv.Itoa(len(values)))
		for _, value := range values {
			keyPart(h, value)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func keyPart(h hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	h.Write(size[:])
	h.Write([]byte(value))
}

func cachePolicy(p rules.CachePolicy) cache.Policy {
	status := make(map[int]time.Duration, len(p.StatusTTL))
	for _, s := range p.StatusTTL {
		status[s.Status] = time.Duration(s.TTLSeconds) * time.Second
	}
	return cache.Policy{Mode: p.Mode, TTL: time.Duration(p.TTLSeconds) * time.Second, StaleWhileRevalidate: time.Duration(p.StaleWhileRevalidateSeconds) * time.Second, StaleIfError: time.Duration(p.StaleIfErrorSeconds) * time.Second, Prefetch: p.Prefetch, StatusTTL: status}
}

func sourceTransform(program *rules.Program, facts rules.Facts, content rules.ContentPlan) func(*http.Response, *cache.Policy) error {
	// The closure owns copied rule inputs and a content-only plan. It does not
	// retain prepared/request state, observer IDs, cancellation, or delivery state.
	facts = program.CaptureFacts(rules.OriginResponse, facts)
	base := rules.Plans{Content: content}.Clone()
	return func(response *http.Response, policy *cache.Policy) error {
		plans := base.Clone()
		plans.Content.ResponseHeader = response.Header.Clone()
		entry := facts
		entry.Status = response.StatusCode
		entry.ResponseHeader = response.Header.Clone()
		program.Run(rules.OriginResponse, entry, &plans, nil)
		response.Header = plans.Content.ResponseHeader
		*policy = cachePolicy(plans.Content.Cache)
		return nil
	}
}

func joinURLPath(a, b *url.URL) (string, string) {
	join := func(a, b string) string {
		aslash, bslash := strings.HasSuffix(a, "/"), strings.HasPrefix(b, "/")
		switch {
		case aslash && bslash:
			return a + b[1:]
		case !aslash && !bslash:
			return a + "/" + b
		default:
			return a + b
		}
	}
	if a.RawPath == "" && b.RawPath == "" {
		return join(a.Path, b.Path), ""
	}
	return join(a.Path, b.Path), join(a.EscapedPath(), b.EscapedPath())
}

func stripHopHeaders(header http.Header, upgrade bool) {
	protocol := header.Get("Upgrade")
	te := header.Get("Te")
	for _, line := range header.Values("Connection") {
		for _, name := range strings.Split(line, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		header.Del(name)
	}
	if upgrade && protocol != "" {
		header.Set("Connection", "Upgrade")
		header.Set("Upgrade", protocol)
	}
	if strings.EqualFold(strings.TrimSpace(te), "trailers") {
		header.Set("Te", "trailers")
	}
}

// These describe a consumer, not an immutable representation. Do not keep the
// first request's tracing/client identity on a cache-scheduled refresh. Rules can
// still intentionally install literal custom headers after this boundary.
func stripClientMetadata(header http.Header) {
	for _, name := range []string{"X-Real-Ip", "X-Client-Ip", "True-Client-Ip", "Cf-Connecting-Ip", "X-Request-Id", "X-Correlation-Id", "Traceparent", "Tracestate", "Baggage"} {
		header.Del(name)
	}
}

func wantsUpgrade(r *http.Request) bool {
	if r.Header.Get("Upgrade") == "" {
		return false
	}
	for _, line := range r.Header.Values("Connection") {
		for _, token := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

func uncacheable(request *http.Request) bool {
	return (request.Method != http.MethodGet && request.Method != http.MethodHead) || request.ContentLength > 0 || request.Body != nil && request.Body != http.NoBody || wantsUpgrade(request) || strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/grpc") || strings.Contains(strings.ToLower(request.Header.Get("Accept")), "text/event-stream")
}
