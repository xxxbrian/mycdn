package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
)

// All metadata is immutable after publication. Access hints live separately.
type metadata struct {
	ID                   string
	Header               http.Header
	Status               int
	Size, Bytes          int64
	Inline               bool
	Coverage             []span
	Vary, Values         []string
	StoredAt, AccessedAt time.Time
	FreshUntil           time.Time
	InitialAge, SWR, SIE time.Duration
	MustRevalidate       bool
}

type record struct {
	Key, SiteID, Host, Path string
	Epoch                   uint64
	Entries                 []metadata
}

func (r *record) copy() *record {
	n := *r
	n.Entries = append([]metadata(nil), r.Entries...)
	return &n
}

func (f Filter) matches(r *record) bool {
	return (f.SiteID == "" || f.SiteID == r.SiteID) &&
		(f.Host == "" || strings.EqualFold(f.Host, r.Host)) &&
		(f.Path == "" || f.Path == r.Path) &&
		(f.PathPrefix == "" || strings.HasPrefix(r.Path, f.PathPrefix))
}

func cacheDirectives(h http.Header) map[string]string {
	d := make(map[string]string)
	for _, line := range h.Values("Cache-Control") {
		for _, part := range strings.Split(line, ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
			d[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return d
}

func has(d map[string]string, key string) bool { _, ok := d[key]; return ok }

func seconds(s string) time.Duration {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	// Bound arithmetic on untrusted HTTP dates/durations.
	if n > int64((365*24*time.Hour)/time.Second) {
		n = int64((365 * 24 * time.Hour) / time.Second)
	}
	return time.Duration(n) * time.Second
}

func bypassRequest(r Request) bool {
	d := cacheDirectives(r.Header)
	return r.Policy.Mode == "bypass" || r.Policy.Mode == "off" ||
		(r.Method != http.MethodGet && r.Method != http.MethodHead) ||
		len(r.Header.Values("Authorization")) > 0 || len(r.Header.Values("Cookie")) > 0 ||
		r.Header.Get("Upgrade") != "" || has(d, "no-store") ||
		strings.Contains(r.Header.Get("Range"), ",")
}

func wantsRevalidation(h http.Header) bool {
	d := cacheDirectives(h)
	return has(d, "no-cache") || (has(d, "max-age") && d["max-age"] == "0") ||
		strings.EqualFold(h.Get("Pragma"), "no-cache")
}

func requestFresh(m *metadata, h http.Header, now time.Time) bool {
	if wantsRevalidation(h) || !now.Before(m.FreshUntil) {
		return false
	}
	d := cacheDirectives(h)
	if age, ok := d["max-age"]; ok && m.age(now) > seconds(age) {
		return false
	}
	if minFresh, ok := d["min-fresh"]; ok && now.Add(seconds(minFresh)).After(m.FreshUntil) {
		return false
	}
	return true
}

// Accept-Encoding is always part of identity, even for origins that omit Vary.
// Arbitrary end-to-end fields can vary; protocol-owned or sensitive fields
// cannot, since acquisition may replace/remove them or sharing bypasses them.
func varyFields(h http.Header) ([]string, bool) {
	set := map[string]bool{"Accept-Encoding": true}
	var namesBytes int
	for _, line := range h.Values("Vary") {
		for _, v := range strings.Split(line, ",") {
			v = http.CanonicalHeaderKey(strings.TrimSpace(v))
			if v == "" {
				continue
			}
			if len(v) > 256 || !httpguts.ValidHeaderFieldName(v) {
				return nil, false
			}
			switch v {
			case "*", "Authorization", "Cookie", "Proxy-Authorization", "Proxy-Authenticate",
				"Connection", "Proxy-Connection", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
				"Host", "Content-Length", "Range", "If-Range", "If-Match", "If-None-Match",
				"If-Modified-Since", "If-Unmodified-Since":
				return nil, false
			}
			if !set[v] {
				set[v] = true
				namesBytes += len(v)
			}
			if len(set) > 16 || namesBytes > 2048 {
				return nil, false
			}
		}
	}
	fields := make([]string, 0, len(set))
	for k := range set {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	return fields, true
}

func headerValue(h http.Header, k string) string {
	if values := h.Values(k); len(values) > 0 {
		return "1" + strings.Join(values, "\x00")
	}
	return "0"
}

func (m *metadata) matches(h http.Header) bool {
	for i, k := range m.Vary {
		if m.Values[i] != headerValue(h, k) {
			return false
		}
	}
	return true
}

func (r *record) match(h http.Header) *metadata {
	for i := len(r.Entries) - 1; i >= 0; i-- {
		if r.Entries[i].matches(h) {
			m := r.Entries[i]
			return &m
		}
	}
	return nil
}

func (m *metadata) age(now time.Time) time.Duration {
	return m.InitialAge + now.Sub(m.StoredAt)
}

func (m *metadata) complete() bool { return covers(m.Coverage, span{0, m.Size}) }

func (m *metadata) staleAllowed(now time.Time, window time.Duration) bool {
	return !m.MustRevalidate && window > 0 && now.Before(m.FreshUntil.Add(window))
}

func responseMetadata(resp *http.Response, req Request, p Policy, now time.Time) (*metadata, bool) {
	h := cleanHeader(resp.Header)
	d := cacheDirectives(h)
	vary, validVary := varyFields(h)
	allowedStatus := resp.StatusCode == 200 || resp.StatusCode == 203 || resp.StatusCode == 204 ||
		resp.StatusCode == 206 || resp.StatusCode == 301 || resp.StatusCode == 308 ||
		resp.StatusCode == 404 || resp.StatusCode == 410
	if _, ok := p.StatusTTL[resp.StatusCode]; ok && resp.StatusCode >= 200 && resp.StatusCode != 304 && resp.StatusCode != 412 && resp.StatusCode != 416 {
		allowedStatus = true
	}
	ct := strings.ToLower(h.Get("Content-Type"))
	if !allowedStatus || !validVary || resp.Uncompressed || has(d, "private") || has(d, "no-store") ||
		len(h.Values("Set-Cookie")) > 0 || strings.HasPrefix(ct, "text/event-stream") ||
		strings.HasPrefix(ct, "application/grpc") || p.Mode == "bypass" || p.Mode == "off" {
		return nil, false
	}
	if h.Get("Content-Range") != "" && resp.StatusCode != 206 {
		return nil, false
	}
	if strings.HasPrefix(ct, "multipart/byteranges") {
		return nil, false
	}
	var headerBytes int
	for k, values := range h {
		headerBytes += len(k)
		for _, v := range values {
			headerBytes += len(v)
		}
	}
	if headerBytes > 64<<10 {
		return nil, false
	}
	ttl := p.TTL
	explicit := ttl > 0
	if s, ok := d["s-maxage"]; ok {
		ttl, explicit = seconds(s), true
	} else if s, ok := d["max-age"]; ok {
		ttl, explicit = seconds(s), true
	} else if expires, err := http.ParseTime(h.Get("Expires")); err == nil {
		date, err := http.ParseTime(h.Get("Date"))
		if err != nil {
			date = now
		}
		ttl, explicit = expires.Sub(date), true
	}
	if p.Mode == "force" {
		ttl, explicit = p.TTL, true
	}
	if t, ok := p.StatusTTL[resp.StatusCode]; ok {
		ttl, explicit = t, true
	}
	if !explicit && h.Get("ETag") == "" && h.Get("Last-Modified") == "" {
		return nil, false
	}
	if has(d, "no-cache") || ttl < 0 {
		ttl = 0
	}
	age := seconds(h.Get("Age"))
	if date, err := http.ParseTime(h.Get("Date")); err == nil && now.Sub(date) > age {
		age = now.Sub(date)
	}
	if age < 0 {
		age = 0
	}
	remaining := ttl - age
	swr, sie := seconds(d["stale-while-revalidate"]), seconds(d["stale-if-error"])
	if p.StaleWhileRevalidate > 0 {
		swr = p.StaleWhileRevalidate
	}
	if p.StaleIfError > 0 {
		sie = p.StaleIfError
	}
	values := make([]string, len(vary))
	var varyBytes int
	for i, v := range vary {
		values[i] = headerValue(req.Header, v)
		varyBytes += len(values[i])
	}
	if varyBytes > 16<<10 {
		return nil, false
	}
	return &metadata{
		Header: h, Status: resp.StatusCode, Vary: vary, Values: values,
		StoredAt: now, AccessedAt: now, FreshUntil: now.Add(remaining),
		InitialAge: age, SWR: swr, SIE: sie,
		MustRevalidate: has(d, "must-revalidate") || has(d, "proxy-revalidate") || has(d, "no-cache"),
	}, true
}

func cleanHeader(h http.Header) http.Header {
	n := cloneHeader(h)
	for _, line := range n.Values("Connection") {
		for _, name := range strings.Split(line, ",") {
			n.Del(strings.TrimSpace(name))
		}
	}
	for _, k := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		n.Del(k)
	}
	return n
}

func cloneHeader(h http.Header) http.Header {
	n := make(http.Header, len(h))
	for key, values := range h {
		name := http.CanonicalHeaderKey(key)
		n[name] = append(n[name], values...)
	}
	return n
}

// Before Vary is known, only requests with identical end-to-end input can join.
// Conditions remain in identity because the first origin response may itself
// be a client-specific 304/412. Revalidation overrides them only with a base.
func requestSignature(r Request) string {
	h := cloneHeader(r.Header)
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\x00", r.Method)
	for _, k := range keys {
		fmt.Fprintf(sum, "%d:%s%d:%s", len(k), k, len(headerValue(h, k)), headerValue(h, k))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func strongETag(h http.Header) string {
	v := strings.TrimSpace(h.Get("ETag"))
	if !validETag(v) {
		return ""
	}
	return v
}

func validETag(v string) bool {
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return false
	}
	for i := 1; i < len(v)-1; i++ {
		if v[i] == '"' || v[i] < 0x21 || v[i] == 0x7f {
			return false
		}
	}
	return true
}

func etagMatches(list, tag string, weak bool) bool {
	if strings.TrimSpace(list) == "*" {
		return true
	}
	if tag == "" {
		return false
	}
	for list != "" {
		list = strings.TrimSpace(list)
		quoted := strings.TrimPrefix(list, "W/")
		if len(quoted) < 2 || quoted[0] != '"' {
			return false
		}
		end := strings.IndexByte(quoted[1:], '"')
		if end < 0 {
			return false
		}
		t := list[:len(list)-len(quoted)+end+2]
		list = strings.TrimSpace(list[len(t):])
		if list != "" {
			if list[0] != ',' {
				return false
			}
			list = list[1:]
		}
		if weak {
			t = strings.TrimPrefix(t, "W/")
			tag = strings.TrimPrefix(tag, "W/")
		}
		if validETag(t) && t == tag && (weak || !strings.HasPrefix(t, "W/")) {
			return true
		}
	}
	return false
}

func precondition(h, stored http.Header) int {
	if v := h.Get("If-Match"); v != "" {
		if !etagMatches(v, stored.Get("ETag"), false) {
			return http.StatusPreconditionFailed
		}
	} else if t, err := http.ParseTime(h.Get("If-Unmodified-Since")); err == nil {
		if modified, err := http.ParseTime(stored.Get("Last-Modified")); err == nil && modified.After(t) {
			return http.StatusPreconditionFailed
		}
	}
	if v := h.Get("If-None-Match"); v != "" {
		if etagMatches(v, stored.Get("ETag"), true) {
			return http.StatusNotModified
		}
	} else if t, err := http.ParseTime(h.Get("If-Modified-Since")); err == nil {
		if modified, err := http.ParseTime(stored.Get("Last-Modified")); err == nil && !modified.After(t) {
			return http.StatusNotModified
		}
	}
	return 0
}

func ifRangeMatches(value string, h http.Header) bool {
	if value == "" {
		return true
	}
	if strings.HasPrefix(value, `"`) || strings.HasPrefix(value, "W/") {
		return value == strongETag(h) && strongETag(h) != ""
	}
	t, err := http.ParseTime(value)
	modified, me := http.ParseTime(h.Get("Last-Modified"))
	return err == nil && me == nil && !modified.After(t)
}
