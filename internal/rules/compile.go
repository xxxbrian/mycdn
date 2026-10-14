package rules

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type Program struct {
	stages       map[Stage][]compiledRule
	dependencies map[Stage][]factReference
}
type factReference struct{ field, key string }
type compiledRule struct {
	id    string
	steps []compiledStep
}
type compiledStep struct {
	id       string
	action   *Action
	branches []compiledBranch
}
type compiledBranch struct {
	id        string
	condition *matcher
	steps     []compiledStep
}
type matcher struct {
	typ, field, key, op string
	children            []*matcher
	values              []string
	set                 map[string]struct{}
	regex               *regexp.Regexp
	prefixes            []netip.Prefix
	number              int64
}

// Compile owns a deep copy. A Program is safe for concurrent use and does no
// parsing/compilation while matching. Disabled rules are validated as well.
func Compile(input []Rule) (*Program, error) {
	p := &Program{stages: make(map[Stage][]compiledRule), dependencies: make(map[Stage][]factReference)}
	if len(input) > 2048 {
		return nil, invalid("rules", "at most 2048 rules are supported")
	}
	ids := make(map[string]bool)
	for i, rule := range input {
		at := fmt.Sprintf("rules[%d]", i)
		if rule.ID == "" || ids[rule.ID] {
			return nil, invalid(at+".id", "must be nonempty and unique")
		}
		ids[rule.ID] = true
		if !stageAllowed(rule.Stage, Capabilities().Stages) {
			return nil, invalid(at+".stage", "unknown stage")
		}
		steps, err := compileSteps(rule.Steps, rule.Stage, at+".steps", 0)
		if err != nil {
			return nil, err
		}
		if rule.Enabled {
			p.stages[rule.Stage] = append(p.stages[rule.Stage], compiledRule{rule.ID, steps})
		}
	}
	for stage, list := range p.stages {
		seen := make(map[factReference]bool)
		var condition func(*matcher)
		condition = func(m *matcher) {
			if m == nil {
				return
			}
			if m.typ == "match" && m.field != "status" && m.field != "response_header" && m.field != "cache_result" {
				seen[factReference{m.field, m.key}] = true
			}
			for _, child := range m.children {
				condition(child)
			}
		}
		var visit func([]compiledStep)
		visit = func(steps []compiledStep) {
			for _, step := range steps {
				for _, branch := range step.branches {
					condition(branch.condition)
					visit(branch.steps)
				}
			}
		}
		for _, rule := range list {
			visit(rule.steps)
		}
		for reference := range seen {
			p.dependencies[stage] = append(p.dependencies[stage], reference)
		}
		slices.SortFunc(p.dependencies[stage], func(a, b factReference) int {
			if n := strings.Compare(a.field, b.field); n != 0 {
				return n
			}
			return strings.Compare(a.key, b.key)
		})
	}
	return p, nil
}

// FactsKey partitions acquisitions by the request fields read by source rules.
// It serializes length-prefixed values directly, with no per-request JSON work.
func (p *Program) FactsKey(stage Stage, facts Facts) string {
	if len(p.dependencies[stage]) == 0 {
		return ""
	}
	facts = prepareFacts(facts)
	h := sha256.New()
	var size [8]byte
	part := func(value string) {
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		h.Write(size[:])
		h.Write([]byte(value))
	}
	for _, reference := range p.dependencies[stage] {
		part(reference.field)
		part(reference.key)
		values := fieldValues(facts, reference.field, reference.key)
		binary.BigEndian.PutUint64(size[:], uint64(len(values)))
		h.Write(size[:])
		for _, value := range values {
			part(value)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CaptureFacts keeps independent copies of only the request values actually
// read by this stage. Shared acquisitions need not retain an entire consumer's
// headers or identity just because a response-stage executor might be used.
func (p *Program) CaptureFacts(stage Stage, facts Facts) Facts {
	facts = prepareFacts(facts)
	copy := Facts{captured: make(map[factReference][]string, len(p.dependencies[stage]))}
	for _, reference := range p.dependencies[stage] {
		copy.captured[reference] = slices.Clone(fieldValues(facts, reference.field, reference.key))
	}
	return copy
}

func invalid(at, message string) error { return &ValidationError{at, message} }

func compileSteps(input []Step, stage Stage, at string, depth int) ([]compiledStep, error) {
	if depth > 16 || len(input) > 512 {
		return nil, invalid(at, "rule tree exceeds size/depth limit")
	}
	out := make([]compiledStep, 0, len(input))
	ids := make(map[string]bool)
	for i, step := range input {
		p := fmt.Sprintf("%s[%d]", at, i)
		if step.ID == "" || ids[step.ID] {
			return nil, invalid(p+".id", "must be nonempty and unique among siblings")
		}
		ids[step.ID] = true
		c := compiledStep{id: step.ID}
		switch step.Type {
		case "action":
			if step.Action == nil || len(step.Branches) != 0 {
				return nil, invalid(p, "action requires action and no branches")
			}
			if err := ValidateAction(*step.Action, stage); err != nil {
				return nil, invalid(p+".action", err.Error())
			}
			a := cloneAction(*step.Action)
			c.action = &a
		case "if":
			if step.Action != nil || len(step.Branches) == 0 || len(step.Branches) > 512 {
				return nil, invalid(p, "if requires branches and no action")
			}
			branchIDs := make(map[string]bool)
			for j, b := range step.Branches {
				bp := fmt.Sprintf("%s.branches[%d]", p, j)
				if b.ID == "" || branchIDs[b.ID] {
					return nil, invalid(bp+".id", "must be nonempty and unique")
				}
				branchIDs[b.ID] = true
				if b.Condition == nil && (j != len(step.Branches)-1 || j == 0) {
					return nil, invalid(bp+".condition", "ELSE must be last and follow IF")
				}
				var m *matcher
				var err error
				if b.Condition != nil {
					m, err = compileCondition(*b.Condition, stage, bp+".condition", depth+1)
				}
				if err != nil {
					return nil, err
				}
				steps, err := compileSteps(b.Steps, stage, bp+".steps", depth+1)
				if err != nil {
					return nil, err
				}
				c.branches = append(c.branches, compiledBranch{b.ID, m, steps})
			}
		default:
			return nil, invalid(p+".type", "expected action or if")
		}
		out = append(out, c)
	}
	return out, nil
}

func compileCondition(c Condition, stage Stage, at string, depth int) (*matcher, error) {
	if depth > 24 || len(c.Children) > 256 {
		return nil, invalid(at, "condition exceeds size/depth limit")
	}
	m := &matcher{typ: c.Type, field: c.Field, key: c.Key, op: c.Op}
	if c.Type != "match" {
		if c.Field != "" || c.Key != "" || c.Op != "" || len(c.Values) != 0 {
			return nil, invalid(at, "logical conditions only accept children")
		}
		if c.Type != "all" && c.Type != "any" && c.Type != "not" {
			return nil, invalid(at+".type", "unknown condition")
		}
		if c.Type == "not" && len(c.Children) != 1 {
			return nil, invalid(at+".children", "not requires exactly one child")
		}
		for i, child := range c.Children {
			x, err := compileCondition(child, stage, fmt.Sprintf("%s.children[%d]", at, i), depth+1)
			if err != nil {
				return nil, err
			}
			m.children = append(m.children, x)
		}
		return m, nil
	}
	if len(c.Children) != 0 {
		return nil, invalid(at+".children", "match cannot have children")
	}
	var field *FieldCapability
	catalog := Capabilities()
	for i := range catalog.Fields {
		if catalog.Fields[i].Name == c.Field {
			field = &catalog.Fields[i]
			break
		}
	}
	if field == nil || !stageAllowed(stage, field.Stages) {
		return nil, invalid(at+".field", "field unavailable at this stage")
	}
	if !slices.Contains(field.Operators, c.Op) {
		return nil, invalid(at+".op", "operator unavailable for field")
	}
	if field.Key != (c.Key != "") {
		return nil, invalid(at+".key", "key required only for query/header/cookie fields")
	}
	if strings.Contains(c.Field, "header") && !validToken(c.Key) {
		return nil, invalid(at+".key", "invalid header name")
	}
	if c.Op == "exists" || c.Op == "not_exists" {
		if len(c.Values) != 0 {
			return nil, invalid(at+".values", "existence operators take no values")
		}
		return m, nil
	}
	if len(c.Values) == 0 || len(c.Values) > 4096 {
		return nil, invalid(at+".values", "requires 1..4096 values")
	}
	if !slices.Contains([]string{"in", "not_in", "cidr"}, c.Op) && len(c.Values) != 1 {
		return nil, invalid(at+".values", "operator requires one value")
	}
	m.values = slices.Clone(c.Values)
	m.set = make(map[string]struct{}, len(c.Values))
	for _, v := range c.Values {
		if len(v) > 8192 {
			return nil, invalid(at+".values", "value too long")
		}
		if field.Type == "integer" {
			if n, err := strconv.ParseInt(v, 10, 64); err != nil {
				return nil, invalid(at+".values", "expected integer")
			} else {
				v = strconv.FormatInt(n, 10)
			}
		}
		if field.Type == "ip" && c.Op != "cidr" {
			if ip, err := netip.ParseAddr(v); err != nil {
				return nil, invalid(at+".values", "expected IP address")
			} else {
				v = ip.Unmap().String()
			}
		}
		m.set[v] = struct{}{}
		if c.Op == "cidr" {
			prefix, err := netip.ParsePrefix(v)
			if err != nil {
				return nil, invalid(at+".values", "expected CIDR")
			}
			m.prefixes = append(m.prefixes, prefix.Masked())
		}
	}
	if field.Type == "integer" {
		m.number, _ = strconv.ParseInt(c.Values[0], 10, 64)
	}
	if c.Op == "regex" {
		r, err := regexp.Compile(c.Values[0])
		if err != nil {
			return nil, invalid(at+".values", err.Error())
		}
		m.regex = r
	}
	if c.Op == "glob" {
		if _, err := path.Match(c.Values[0], ""); err != nil {
			return nil, invalid(at+".values", err.Error())
		}
	}
	return m, nil
}

// ValidateAction validates the tagged payload and protected protocol fields.
func ValidateAction(a Action, stage Stage) error {
	var capability *ActionCapability
	catalog := Capabilities()
	for i := range catalog.Actions {
		if catalog.Actions[i].Type == a.Type {
			capability = &catalog.Actions[i]
			break
		}
	}
	if capability == nil || !stageAllowed(stage, capability.Stages) {
		return fmt.Errorf("action %q unavailable at %s", a.Type, stage)
	}
	count := 0
	for _, set := range []bool{a.Cache != nil, a.CacheKey != nil, a.Origin != nil, a.Path != nil, a.Query != nil, a.Cookie != nil, a.Header != nil, a.Authority != nil, a.Upstream != nil, a.Identity != nil, a.Limits != nil, a.BrowserTTL != nil, a.CORS != nil, a.Reply != nil, a.Redirect != nil} {
		if set {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("action must have exactly one typed parameter")
	}
	switch a.Type {
	case "cache":
		if a.Cache == nil {
			break
		}
		return ValidateCachePatch(*a.Cache)
	case "cache_key":
		if a.CacheKey == nil {
			break
		}
		return ValidateCacheKey(*a.CacheKey)
	case "origin":
		if a.Origin == nil {
			break
		}
		if *a.Origin == "" {
			return fmt.Errorf("origin must be nonempty")
		}
		return nil
	case "path":
		if a.Path == nil {
			break
		}
		if !strings.HasPrefix(*a.Path, "/") || strings.ContainsAny(*a.Path, "?#\r\n") {
			return fmt.Errorf("path must be absolute without query/fragment")
		}
		return nil
	case "query":
		if a.Query == nil {
			break
		}
		if a.Query.Name == "" || !slices.Contains([]string{"set", "add", "delete"}, a.Query.Operation) {
			return fmt.Errorf("query needs name and set/add/delete operation")
		}
		return nil
	case "cookie":
		if a.Cookie == nil {
			break
		}
		if !slices.Contains([]string{"set", "delete"}, a.Cookie.Operation) {
			return fmt.Errorf("cookie operation must be set/delete")
		}
		c := http.Cookie{Name: a.Cookie.Name, Value: a.Cookie.Value}
		return c.Valid()
	case "request_header", "source_header", "response_header":
		if a.Header == nil {
			break
		}
		return ValidateHeader(*a.Header, a.Type)
	case "authority":
		if a.Authority == nil {
			break
		}
		return ValidateAuthority(*a.Authority)
	case "upstream":
		if a.Upstream == nil {
			break
		}
		return ValidateUpstreamPatch(*a.Upstream)
	case "identity":
		if a.Identity == nil {
			break
		}
		return ValidateIdentityPatch(*a.Identity)
	case "limits":
		if a.Limits == nil {
			break
		}
		if a.Limits.MaxUploadBytes != nil && *a.Limits.MaxUploadBytes < 0 {
			return fmt.Errorf("max_upload_bytes must be nonnegative")
		}
		return nil
	case "browser_ttl":
		if a.BrowserTTL == nil {
			break
		}
		if *a.BrowserTTL < 0 || *a.BrowserTTL > 315360000 {
			return fmt.Errorf("browser TTL out of range")
		}
		return nil
	case "cors":
		if a.CORS == nil {
			break
		}
		return ValidateCORS(*a.CORS)
	case "reply":
		if a.Reply == nil {
			break
		}
		if a.Reply.Status < 200 || a.Reply.Status > 599 || len(a.Reply.Body) > 1<<20 || !validHeaderValue(a.Reply.ContentType) {
			return fmt.Errorf("invalid reply status/type or body exceeds 1 MiB")
		}
		return nil
	case "redirect":
		if a.Redirect == nil {
			break
		}
		if !slices.Contains([]int{301, 302, 303, 307, 308}, a.Redirect.Status) {
			return fmt.Errorf("invalid redirect status")
		}
		if a.Redirect.Location == "" || !validHeaderValue(a.Redirect.Location) {
			return fmt.Errorf("invalid redirect location")
		}
		_, err := url.Parse(a.Redirect.Location)
		return err
	}
	return fmt.Errorf("wrong parameter for action %q", a.Type)
}

func ValidateCachePatch(p CachePatch) error {
	if p.Mode != nil && !slices.Contains([]string{"respect", "force", "bypass"}, *p.Mode) {
		return fmt.Errorf("cache mode must be respect, force, or bypass")
	}
	for _, n := range []*int64{p.TTLSeconds, p.StaleWhileRevalidateSeconds, p.StaleIfErrorSeconds} {
		if n != nil && (*n < 0 || *n > 315360000) {
			return fmt.Errorf("cache duration out of range")
		}
	}
	if p.StatusTTL != nil {
		seen := map[int]bool{}
		for _, s := range *p.StatusTTL {
			if s.Status < 200 || s.Status > 599 || s.TTLSeconds < 0 || s.TTLSeconds > 315360000 || seen[s.Status] {
				return fmt.Errorf("invalid or duplicate status TTL")
			}
			seen[s.Status] = true
		}
	}
	return nil
}

func ValidateCacheKey(k CacheKey) error {
	if !slices.Contains([]string{"all", "ignore", "include", "exclude"}, k.QueryMode) {
		return fmt.Errorf("query_mode must be all/ignore/include/exclude")
	}
	for _, name := range k.Headers {
		if !validToken(name) || protectedHeader(name, true) {
			return fmt.Errorf("invalid cache key header %q", name)
		}
	}
	for _, name := range k.Cookies {
		if !validToken(name) {
			return fmt.Errorf("invalid cache key cookie")
		}
	}
	return nil
}

func ValidateHeader(h HeaderMutation, kind string) error {
	if !ValidHeader(h.Name, h.Value) || !slices.Contains([]string{"set", "add", "delete"}, h.Operation) {
		return fmt.Errorf("invalid header mutation")
	}
	if protectedHeader(h.Name, kind == "request_header") {
		return fmt.Errorf("protocol-owned header %q cannot be edited", h.Name)
	}
	if kind == "source_header" && strings.EqualFold(h.Name, "Set-Cookie") {
		return fmt.Errorf("Set-Cookie must be changed at delivery, after cache eligibility is decided")
	}
	return nil
}

// ValidHeader checks wire syntax only, without rule-specific ownership checks.
func ValidHeader(name, value string) bool { return validToken(name) && validHeaderValue(value) }

func protectedHeader(name string, request bool) bool {
	switch strings.ToLower(name) {
	case "connection", "proxy-connection", "keep-alive", "transfer-encoding", "trailer", "content-length", "upgrade", "te", "host", "proxy-authenticate", "proxy-authorization", "content-range", "accept-ranges", "etag", "last-modified", "age", "vary", "if-none-match", "if-modified-since", "if-match", "if-unmodified-since", "if-range", "range":
		return true
	case "forwarded", "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto":
		return request
	}
	return false
}

func ValidateAuthority(s string) error {
	if s == "" || strings.ContainsAny(s, " /\\?#@\r\n\t") {
		return fmt.Errorf("invalid HTTP authority")
	}
	u, err := url.Parse("http://" + s)
	if err != nil || u.Host != s || u.Hostname() == "" {
		return fmt.Errorf("invalid HTTP authority")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("invalid authority port")
		}
	}
	if strings.Contains(u.Hostname(), ":") {
		if ip, err := netip.ParseAddr(u.Hostname()); err != nil || ip.Zone() != "" {
			return fmt.Errorf("invalid authority IP")
		}
	} else if strings.Contains(s, ":") {
		if _, port, err := net.SplitHostPort(s); err != nil || port == "" {
			return fmt.Errorf("invalid authority port")
		}
	}
	return nil
}

func validHeaderValue(s string) bool {
	for _, c := range []byte(s) {
		if c < 32 && c != '\t' || c == 127 {
			return false
		}
	}
	return true
}

func ValidateUpstreamPatch(p UpstreamPatch) error {
	if p.Protocol != nil && !slices.Contains([]string{"auto", "h1", "h2", "h2c", "h3"}, *p.Protocol) {
		return fmt.Errorf("protocol must be auto/h1/h2/h2c/h3")
	}
	if p.Timeouts != nil {
		for _, n := range []*int64{p.Timeouts.ConnectMS, p.Timeouts.TLSHandshakeMS, p.Timeouts.ResponseHeaderMS, p.Timeouts.IdleMS, p.Timeouts.RequestMS} {
			if n != nil && (*n < 0 || *n > 86400000) {
				return fmt.Errorf("timeout out of range")
			}
		}
	}
	return nil
}

func ValidateIdentityPatch(p IdentityPatch) error {
	if p.ClientIPSource != nil && !slices.Contains([]string{"peer", "trusted_x_forwarded_for", "trusted_forwarded"}, *p.ClientIPSource) {
		return fmt.Errorf("unknown client IP source")
	}
	if p.SchemeSource != nil && !slices.Contains([]string{"connection", "trusted_x_forwarded_proto", "trusted_forwarded"}, *p.SchemeSource) {
		return fmt.Errorf("unknown scheme source")
	}
	if p.ClientIPHeaders != nil {
		if len(*p.ClientIPHeaders) > 16 {
			return fmt.Errorf("at most 16 client IP headers are supported")
		}
		for _, source := range *p.ClientIPHeaders {
			if !validToken(source.Name) || !slices.Contains([]string{"single", "xff", "forwarded"}, source.Format) {
				return fmt.Errorf("client IP header requires a valid name and single/xff/forwarded format")
			}
		}
	}
	return nil
}

func ValidateCORS(c CORS) error {
	if len(c.Origins) == 0 || c.MaxAge < 0 || c.MaxAge > 315360000 {
		return fmt.Errorf("invalid CORS origins/max age")
	}
	for _, o := range c.Origins {
		if o == "*" {
			if c.Credentials {
				return fmt.Errorf("wildcard CORS cannot allow credentials")
			}
			continue
		}
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.User != nil {
			return fmt.Errorf("invalid CORS origin")
		}
	}
	for _, group := range [][]string{c.Methods, c.Headers, c.Expose} {
		for _, s := range group {
			if !validToken(s) {
				return fmt.Errorf("invalid CORS token")
			}
		}
	}
	return nil
}

func validToken(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c))) {
			return false
		}
	}
	return true
}
