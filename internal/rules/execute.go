package rules

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
)

// Run uses only the supplied stage-entry facts. Passing trace enables explain
// without a second implementation or any change to matching/action semantics.
func (p *Program) Run(stage Stage, facts Facts, plans *Plans, trace *[]Trace) {
	if len(p.stages[stage]) == 0 {
		return
	}
	facts = prepareFacts(facts)
	for _, rule := range p.stages[stage] {
		if runSteps(rule.steps, stage, rule.id, facts, plans, trace) {
			return
		}
	}
}

func prepareFacts(f Facts) Facts {
	if f.captured != nil {
		return f
	}
	f.query, _ = url.ParseQuery(f.RawQuery)
	f.cookies = make(map[string][]string)
	r := http.Request{Header: f.Header}
	for _, c := range r.Cookies() {
		f.cookies[c.Name] = append(f.cookies[c.Name], c.Value)
	}
	if f.Original != nil {
		original := *f.Original
		original.Original = nil
		original = prepareFacts(original)
		f.Original = &original
	}
	return f
}

func runSteps(steps []compiledStep, stage Stage, ruleID string, facts Facts, plans *Plans, trace *[]Trace) bool {
	for _, step := range steps {
		if step.action != nil {
			before := ""
			if trace != nil {
				before = actionValue(*step.action, plans)
			}
			terminal := apply(*step.action, plans)
			if trace != nil {
				*trace = append(*trace, Trace{Stage: stage, RuleID: ruleID, StepID: step.id, Action: step.action.Type, Before: before, After: actionValue(*step.action, plans)})
			}
			if terminal {
				return true
			}
			continue
		}
		for _, branch := range step.branches {
			matched := branch.condition == nil || branch.condition.match(facts)
			if trace != nil {
				*trace = append(*trace, Trace{Stage: stage, RuleID: ruleID, StepID: step.id, BranchID: branch.id, Matched: &matched})
			}
			if matched {
				if runSteps(branch.steps, stage, ruleID, facts, plans, trace) {
					return true
				}
				break
			}
		}
	}
	return false
}

func (m *matcher) match(f Facts) bool {
	switch m.typ {
	case "all":
		for _, c := range m.children {
			if !c.match(f) {
				return false
			}
		}
		return true
	case "any":
		for _, c := range m.children {
			if c.match(f) {
				return true
			}
		}
		return false
	case "not":
		return !m.children[0].match(f)
	}
	values := fieldValues(f, m.field, m.key)
	if m.op == "exists" {
		return len(values) != 0
	}
	if m.op == "not_exists" {
		return len(values) == 0
	}
	negative := m.op == "ne" || m.op == "not_in"
	for _, value := range values {
		matched := false
		switch m.op {
		case "eq", "ne", "in", "not_in":
			_, matched = m.set[value]
		case "prefix":
			matched = strings.HasPrefix(value, m.values[0])
		case "suffix":
			matched = strings.HasSuffix(value, m.values[0])
		case "contains":
			matched = strings.Contains(value, m.values[0])
		case "glob":
			matched, _ = path.Match(m.values[0], value)
		case "regex":
			matched = m.regex.MatchString(value)
		case "cidr":
			ip, err := netip.ParseAddr(value)
			if err == nil {
				for _, prefix := range m.prefixes {
					if prefix.Contains(ip.Unmap()) {
						matched = true
						break
					}
				}
			}
		case "gt", "gte", "lt", "lte":
			n, err := strconv.ParseInt(value, 10, 64)
			if err == nil {
				switch m.op {
				case "gt":
					matched = n > m.number
				case "gte":
					matched = n >= m.number
				case "lt":
					matched = n < m.number
				case "lte":
					matched = n <= m.number
				}
			}
		}
		if matched {
			return !negative
		}
	}
	return negative
}

func fieldValues(f Facts, field, key string) []string {
	if f.captured != nil && field != "status" && field != "response_header" && field != "cache_result" {
		return f.captured[factReference{field, key}]
	}
	if strings.HasPrefix(field, "original.") {
		if f.Original == nil {
			return nil
		}
		f = *f.Original
		field = strings.TrimPrefix(field, "original.")
	}
	var value string
	switch field {
	case "site":
		value = f.SiteID
	case "host":
		value = f.Host
	case "path":
		value = f.Path
	case "url":
		value = f.URL
	case "method":
		value = f.Method
	case "scheme":
		value = f.Scheme
	case "client_ip":
		value = f.ClientIP
	case "peer_ip":
		value = f.PeerIP
	case "http_version":
		value = f.HTTPVersion
	case "extension":
		value = strings.TrimPrefix(path.Ext(f.Path), ".")
	case "filename":
		value = path.Base(f.Path)
	case "status":
		if f.Status != 0 {
			value = strconv.Itoa(f.Status)
		}
	case "cache_result":
		value = f.CacheResult
	case "header":
		return f.Header.Values(key)
	case "response_header":
		return f.ResponseHeader.Values(key)
	case "query":
		if f.query == nil {
			f.query, _ = url.ParseQuery(f.RawQuery)
		}
		return f.query[key]
	case "cookie":
		if f.cookies != nil {
			return f.cookies[key]
		}
		r := http.Request{Header: f.Header}
		var out []string
		for _, c := range r.Cookies() {
			if c.Name == key {
				out = append(out, c.Value)
			}
		}
		return out
	}
	if value == "" {
		return nil
	}
	return []string{value}
}

func apply(a Action, p *Plans) bool {
	switch a.Type {
	case "cache":
		ApplyCache(&p.Content.Cache, *a.Cache)
	case "cache_key":
		p.Content.Key = cloneKey(*a.CacheKey)
	case "origin":
		p.Fetch.OriginID = *a.Origin
	case "path":
		p.Fetch.Path = *a.Path
		p.Fetch.RawPath = ""
	case "query":
		q, _ := url.ParseQuery(p.Fetch.RawQuery)
		switch a.Query.Operation {
		case "set":
			q.Set(a.Query.Name, a.Query.Value)
		case "add":
			q.Add(a.Query.Name, a.Query.Value)
		case "delete":
			q.Del(a.Query.Name)
		}
		p.Fetch.RawQuery = q.Encode()
	case "cookie":
		r := http.Request{Header: p.Fetch.Header}
		cookies := r.Cookies()
		p.Fetch.Header.Del("Cookie")
		for _, c := range cookies {
			if c.Name != a.Cookie.Name {
				r.AddCookie(c)
			}
		}
		if a.Cookie.Operation == "set" {
			r.AddCookie(&http.Cookie{Name: a.Cookie.Name, Value: a.Cookie.Value})
		}
	case "request_header":
		ApplyHeader(p.Fetch.Header, *a.Header)
	case "source_header":
		ApplyHeader(p.Content.ResponseHeader, *a.Header)
	case "response_header":
		p.Delivery.Headers = append(p.Delivery.Headers, *a.Header)
	case "authority":
		p.Fetch.Authority = *a.Authority
	case "upstream":
		ApplyUpstream(&p.Fetch.Upstream, *a.Upstream)
		mergeUpstream(&p.Fetch.Overrides, *a.Upstream)
	case "identity":
		if a.Identity.ClientIPSource != nil {
			p.Request.Identity.ClientIPSource = *a.Identity.ClientIPSource
		}
		if a.Identity.SchemeSource != nil {
			p.Request.Identity.SchemeSource = *a.Identity.SchemeSource
		}
		if a.Identity.ClientIPHeaders != nil {
			p.Request.Identity.ClientIPHeaders = slices.Clone(*a.Identity.ClientIPHeaders)
		}
	case "limits":
		if a.Limits.MaxUploadBytes != nil {
			p.Request.Limits.MaxUploadBytes = *a.Limits.MaxUploadBytes
		}
		if a.Limits.AllowUpgrade != nil {
			p.Request.Limits.AllowUpgrade = *a.Limits.AllowUpgrade
		}
	case "browser_ttl":
		n := *a.BrowserTTL
		p.Delivery.BrowserTTL = &n
	case "cors":
		c := cloneCORS(*a.CORS)
		p.Delivery.CORS = &c
	case "reply":
		p.Terminal = &Terminal{Status: a.Reply.Status, ContentType: a.Reply.ContentType, Body: a.Reply.Body}
		return true
	case "redirect":
		p.Terminal = &Terminal{Status: a.Redirect.Status, Location: a.Redirect.Location}
		return true
	}
	return false
}

func ApplyCache(p *CachePolicy, patch CachePatch) {
	if patch.Mode != nil {
		p.Mode = *patch.Mode
	}
	if patch.TTLSeconds != nil {
		p.TTLSeconds = *patch.TTLSeconds
	}
	if patch.StaleWhileRevalidateSeconds != nil {
		p.StaleWhileRevalidateSeconds = *patch.StaleWhileRevalidateSeconds
	}
	if patch.StaleIfErrorSeconds != nil {
		p.StaleIfErrorSeconds = *patch.StaleIfErrorSeconds
	}
	if patch.Prefetch != nil {
		p.Prefetch = *patch.Prefetch
	}
	if patch.StatusTTL != nil {
		p.StatusTTL = slices.Clone(*patch.StatusTTL)
	}
}

func ApplyUpstream(p *UpstreamSettings, patch UpstreamPatch) {
	if patch.Protocol != nil {
		p.Protocol = *patch.Protocol
	}
	if patch.Timeouts == nil {
		return
	}
	x := patch.Timeouts
	if x.ConnectMS != nil {
		p.Timeouts.ConnectMS = *x.ConnectMS
	}
	if x.TLSHandshakeMS != nil {
		p.Timeouts.TLSHandshakeMS = *x.TLSHandshakeMS
	}
	if x.ResponseHeaderMS != nil {
		p.Timeouts.ResponseHeaderMS = *x.ResponseHeaderMS
	}
	if x.IdleMS != nil {
		p.Timeouts.IdleMS = *x.IdleMS
	}
	if x.RequestMS != nil {
		p.Timeouts.RequestMS = *x.RequestMS
	}
}

func mergeUpstream(p *UpstreamPatch, patch UpstreamPatch) {
	if patch.Protocol != nil {
		value := *patch.Protocol
		p.Protocol = &value
	}
	if patch.Timeouts == nil {
		return
	}
	if p.Timeouts == nil {
		p.Timeouts = &TimeoutPatch{}
	}
	for _, pair := range []struct {
		source *int64
		target **int64
	}{{patch.Timeouts.ConnectMS, &p.Timeouts.ConnectMS}, {patch.Timeouts.TLSHandshakeMS, &p.Timeouts.TLSHandshakeMS}, {patch.Timeouts.ResponseHeaderMS, &p.Timeouts.ResponseHeaderMS}, {patch.Timeouts.IdleMS, &p.Timeouts.IdleMS}, {patch.Timeouts.RequestMS, &p.Timeouts.RequestMS}} {
		if pair.source != nil {
			n := *pair.source
			*pair.target = &n
		}
	}
}

func ApplyHeader(h http.Header, m HeaderMutation) {
	switch m.Operation {
	case "set":
		h.Set(m.Name, m.Value)
	case "add":
		h.Add(m.Name, m.Value)
	case "delete":
		h.Del(m.Name)
	}
}

func cloneAction(a Action) Action {
	// Used only at compile time, never on the request path.
	b, _ := json.Marshal(a)
	var copy Action
	_ = json.Unmarshal(b, &copy)
	return copy
}

func cloneKey(k CacheKey) CacheKey {
	k.QueryNames = slices.Clone(k.QueryNames)
	k.Headers = slices.Clone(k.Headers)
	k.Cookies = slices.Clone(k.Cookies)
	return k
}

func cloneCORS(c CORS) CORS {
	c.Origins = slices.Clone(c.Origins)
	c.Methods = slices.Clone(c.Methods)
	c.Headers = slices.Clone(c.Headers)
	c.Expose = slices.Clone(c.Expose)
	return c
}

func (p Plans) Clone() Plans {
	p.Request.Identity.ClientIPHeaders = slices.Clone(p.Request.Identity.ClientIPHeaders)
	p.Content.Cache.StatusTTL = slices.Clone(p.Content.Cache.StatusTTL)
	p.Content.Key = cloneKey(p.Content.Key)
	p.Content.ResponseHeader = p.Content.ResponseHeader.Clone()
	p.Fetch.Header = p.Fetch.Header.Clone()
	var overrides UpstreamPatch
	mergeUpstream(&overrides, p.Fetch.Overrides)
	p.Fetch.Overrides = overrides
	p.Delivery.Headers = slices.Clone(p.Delivery.Headers)
	if p.Delivery.BrowserTTL != nil {
		n := *p.Delivery.BrowserTTL
		p.Delivery.BrowserTTL = &n
	}
	if p.Delivery.CORS != nil {
		c := cloneCORS(*p.Delivery.CORS)
		p.Delivery.CORS = &c
	}
	if p.Terminal != nil {
		t := *p.Terminal
		p.Terminal = &t
	}
	return p
}

func actionValue(a Action, p *Plans) string {
	switch a.Type {
	case "cache":
		return fmt.Sprintf("%+v", p.Content.Cache)
	case "cache_key":
		return fmt.Sprintf("%+v", p.Content.Key)
	case "origin":
		return p.Fetch.OriginID
	case "path":
		return p.Fetch.Path
	case "query":
		return p.Fetch.RawQuery
	case "cookie", "request_header":
		return "[headers redacted]"
	case "source_header":
		return strings.Join(p.Content.ResponseHeader.Values(a.Header.Name), ", ")
	case "response_header":
		return fmt.Sprintf("%v", p.Delivery.Headers)
	case "authority":
		return p.Fetch.Authority
	case "upstream":
		return fmt.Sprintf("%+v", p.Fetch.Upstream)
	case "identity":
		return fmt.Sprintf("%+v", p.Request.Identity)
	case "limits":
		return fmt.Sprintf("%+v", p.Request.Limits)
	case "browser_ttl":
		if p.Delivery.BrowserTTL != nil {
			return strconv.FormatInt(*p.Delivery.BrowserTTL, 10)
		}
	case "cors":
		return fmt.Sprintf("%+v", p.Delivery.CORS)
	case "reply", "redirect":
		if p.Terminal != nil {
			return strconv.Itoa(p.Terminal.Status)
		}
	}
	return ""
}
