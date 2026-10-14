package config

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"mycdn/internal/rules"
)

type Store struct {
	path    string
	mu      sync.Mutex
	current atomic.Pointer[Snapshot]
}

// Snapshot is immutable. Accessors returning editable data make independent
// copies; Program can be shared because its implementation is immutable.
type Snapshot struct {
	document  Document
	program   *rules.Program
	revision  string
	exact     map[string]int
	wildcards []domainPattern
	trusted   []netip.Prefix
}
type domainPattern struct {
	suffix string
	site   int
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		if err := s.Save(Default()); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d, err := Decode(f)
	if err != nil {
		return nil, err
	}
	snapshot, _, err := compile(d)
	if err != nil {
		return nil, err
	}
	s.current.Store(snapshot)
	return s, nil
}

func (s *Store) Get() *Snapshot { return s.current.Load() }

// Decode rejects unknown members and trailing documents. Config size is bounded
// here and again at Save, so loading untrusted management input is predictable.
func Decode(r io.Reader) (Document, error) {
	b, err := io.ReadAll(io.LimitReader(r, 4<<20+1))
	if err != nil {
		return Document{}, err
	}
	if len(b) > 4<<20 {
		return Document{}, fmt.Errorf("config exceeds 4 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	var d Document
	if err := decoder.Decode(&d); err != nil {
		return d, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return d, fmt.Errorf("config must contain one JSON document")
	}
	return d, nil
}

// Validate compiles without writing or publishing; returned snapshots own their
// input, including CA contents, so callers can safely reuse/edit their document.
func Validate(d Document) (*Snapshot, error) { s, _, err := compile(d); return s, err }

func (s *Store) Save(d Document) error {
	snapshot, data, err := compile(d)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := atomicWrite(s.path, data); err != nil {
		return err
	}
	s.current.Store(snapshot)
	return nil
}

func (s *Snapshot) Document() Document      { return cloneDocument(s.document) }
func (s *Snapshot) Revision() string        { return s.revision }
func (s *Snapshot) Program() *rules.Program { return s.program }
func (s *Snapshot) IsTrusted(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, prefix := range s.trusted {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// MatchSite accepts a normalized host (without port); exact domains take priority
// over the most specific *.suffix wildcard. Wildcards do not match the apex.
func (s *Snapshot) MatchSite(host string) (Site, bool) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	index, ok := s.exact[host]
	if !ok {
		for _, w := range s.wildcards {
			if strings.HasSuffix(host, w.suffix) && len(host) > len(w.suffix) {
				index, ok = w.site, true
				break
			}
		}
	}
	if !ok {
		return Site{}, false
	}
	return cloneSite(s.document.Sites[index]), true
}

func (s *Snapshot) Plans() rules.Plans {
	d := s.document.Defaults
	p := rules.Plans{Request: rules.RequestPlan{Identity: d.Identity, Limits: d.Request}, Content: rules.ContentPlan{Cache: d.Cache, Key: d.CacheKey}, Fetch: rules.FetchPlan{Upstream: d.Upstream}, Delivery: rules.DeliveryPlan{DeliverySettings: d.Delivery}}
	return p.Clone()
}

func compile(input Document) (*Snapshot, []byte, error) {
	data, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	if len(data) > 4<<20 {
		return nil, nil, fmt.Errorf("config exceeds 4 MiB")
	}
	d := cloneDocument(input)
	bad := func(path, message string) (*Snapshot, []byte, error) {
		return nil, nil, &rules.ValidationError{Path: path, Message: message}
	}
	if d.SchemaVersion != SchemaVersion {
		return bad("schema_version", "must be 1")
	}
	for _, address := range []string{d.Listen.Proxy, d.Listen.Admin} {
		if _, _, err := net.SplitHostPort(address); err != nil {
			return bad("listen", "listeners must be host:port")
		}
	}
	if d.Listen.Proxy == d.Listen.Admin {
		return bad("listen", "proxy and admin listeners must differ")
	}
	for field, value := range map[string]string{"proxy": d.Listen.Proxy, "admin": d.Listen.Admin} {
		_, port, _ := net.SplitHostPort(value)
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return bad("listen."+field, "port must be 1..65535")
		}
	}
	if strings.TrimSpace(d.DataDir) == "" {
		return bad("data_dir", "must be nonempty")
	}
	s := &Snapshot{document: d, exact: make(map[string]int)}
	for i, value := range d.TrustedProxies {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return bad(fmt.Sprintf("trusted_proxies[%d]", i), "expected CIDR")
		}
		s.trusted = append(s.trusted, prefix.Masked())
	}
	if err := validateDefaults(d.Defaults); err != nil {
		return bad("defaults", err.Error())
	}
	for _, budget := range []struct {
		name  string
		value int64
	}{
		{"max_bytes", d.Storage.MaxBytes}, {"memory_bytes", d.Storage.MemoryBytes},
		{"max_fills", int64(d.Storage.MaxFills)}, {"max_background", int64(d.Storage.MaxBackground)},
		{"max_object_bytes", d.Storage.MaxObjectBytes}, {"inline_bytes", d.Storage.InlineBytes},
		{"block_bytes", d.Storage.BlockBytes}, {"max_readers", int64(d.Storage.MaxReaders)},
		{"fill_timeout_ms", d.Storage.FillTimeoutMS},
	} {
		if budget.value <= 0 {
			return bad("storage."+budget.name, "must be greater than zero")
		}
	}
	if d.Storage.FillTimeoutMS > 86400000 {
		return bad("storage.fill_timeout_ms", "must be at most 86400000")
	}
	if err := validateAnalytics(d.Analytics); err != nil {
		return nil, nil, err
	}
	siteIDs, domains := map[string]bool{}, map[string]bool{}
	for i := range s.document.Sites {
		site := &s.document.Sites[i]
		at := fmt.Sprintf("sites[%d]", i)
		if site.ID == "" || siteIDs[site.ID] {
			return bad(at+".id", "must be nonempty and unique")
		}
		siteIDs[site.ID] = true
		if len(site.Domains) == 0 || len(site.Origins) == 0 {
			return bad(at, "site requires domains and origins")
		}
		origins := map[string]bool{}
		for j := range site.Origins {
			o := &site.Origins[j]
			op := fmt.Sprintf("%s.origins[%d]", at, j)
			if o.ID == "" || origins[o.ID] {
				return bad(op+".id", "must be nonempty and unique in site")
			}
			origins[o.ID] = true
			u, err := url.Parse(o.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || rules.ValidateAuthority(u.Host) != nil {
				return bad(op+".url", "expected HTTP(S) URL without credentials/query/fragment")
			}
			if o.HostHeader != "" {
				if err := rules.ValidateAuthority(o.HostHeader); err != nil {
					return bad(op+".host_header", err.Error())
				}
			}
			if o.TLSServerName != "" && !validDomain(strings.ToLower(o.TLSServerName)) {
				return bad(op+".tls_server_name", "invalid verification hostname")
			}
			if o.DialAddress != "" {
				host, port, err := net.SplitHostPort(o.DialAddress)
				n, e := strconv.Atoi(port)
				if err != nil || e != nil || host == "" || n < 1 || n > 65535 {
					return bad(op+".dial_address", "expected host:port")
				}
			}
			if o.Protocol != "" {
				if err := rules.ValidateUpstreamPatch(rules.UpstreamPatch{Protocol: &o.Protocol}); err != nil {
					return bad(op+".protocol", err.Error())
				}
			}
			if err := rules.ValidateUpstreamPatch(rules.UpstreamPatch{Timeouts: o.Timeouts}); err != nil {
				return bad(op+".timeouts", err.Error())
			}
			protocol := d.Defaults.Upstream.Protocol
			if o.Protocol != "" {
				protocol = o.Protocol
			}
			if (protocol == "h2" || protocol == "h3") && u.Scheme != "https" {
				return bad(op+".protocol", "explicit h2/h3 requires HTTPS")
			}
			if protocol == "h2c" && u.Scheme != "http" {
				return bad(op+".protocol", "explicit h2c requires HTTP")
			}
			if o.CAFile != "" {
				b, err := os.ReadFile(o.CAFile)
				if err != nil {
					return bad(op+".ca_file", err.Error())
				}
				pool := x509.NewCertPool()
				if !pool.AppendCertsFromPEM(b) {
					return bad(op+".ca_file", "no CA certificates")
				}
				o.caPEM = b
			}
		}
		if !origins[site.DefaultOrigin] {
			return bad(at+".default_origin", "must refer to a named origin")
		}
		for j, value := range site.Domains {
			domain := strings.ToLower(strings.TrimSuffix(value, "."))
			wildcard := strings.HasPrefix(domain, "*.")
			base := strings.TrimPrefix(domain, "*.")
			if ip, err := netip.ParseAddr(base); err == nil {
				if wildcard {
					return bad(fmt.Sprintf("%s.domains[%d]", at, j), "IP addresses cannot be wildcard domains")
				}
				base = ip.Unmap().String()
				domain = base
			}
			if !validDomain(base) || strings.Contains(base, "*") {
				return bad(fmt.Sprintf("%s.domains[%d]", at, j), "expected hostname or *.hostname")
			}
			if domains[domain] {
				return bad(at+".domains", "duplicate domain across sites")
			}
			domains[domain] = true
			if !site.Enabled {
				continue
			}
			if wildcard {
				s.wildcards = append(s.wildcards, domainPattern{"." + base, i})
			} else {
				s.exact[domain] = i
			}
		}
	}
	slices.SortFunc(s.wildcards, func(a, b domainPattern) int { return len(b.suffix) - len(a.suffix) })
	program, err := rules.Compile(d.Rules)
	if err != nil {
		return nil, nil, err
	}
	s.program = program
	hash := sha256.Sum256(data)
	s.revision = hex.EncodeToString(hash[:])
	return s, append(data, '\n'), nil
}

func validateDefaults(d Defaults) error {
	p := d.Cache
	if err := rules.ValidateCachePatch(rules.CachePatch{Mode: &p.Mode, TTLSeconds: &p.TTLSeconds, StaleWhileRevalidateSeconds: &p.StaleWhileRevalidateSeconds, StaleIfErrorSeconds: &p.StaleIfErrorSeconds, StatusTTL: &p.StatusTTL}); err != nil {
		return err
	}
	if err := rules.ValidateCacheKey(d.CacheKey); err != nil {
		return err
	}
	u := d.Upstream
	t := u.Timeouts
	if err := rules.ValidateUpstreamPatch(rules.UpstreamPatch{Protocol: &u.Protocol, Timeouts: &rules.TimeoutPatch{ConnectMS: &t.ConnectMS, TLSHandshakeMS: &t.TLSHandshakeMS, ResponseHeaderMS: &t.ResponseHeaderMS, IdleMS: &t.IdleMS, RequestMS: &t.RequestMS}}); err != nil {
		return err
	}
	i := d.Identity
	if err := rules.ValidateIdentityPatch(rules.IdentityPatch{ClientIPSource: &i.ClientIPSource, SchemeSource: &i.SchemeSource, ClientIPHeaders: &i.ClientIPHeaders}); err != nil {
		return err
	}
	if d.Request.MaxUploadBytes < 0 {
		return fmt.Errorf("max_upload_bytes must be nonnegative")
	}
	for _, h := range d.Delivery.Headers {
		if err := rules.ValidateHeader(h, "response_header"); err != nil {
			return err
		}
	}
	if d.Delivery.BrowserTTL != nil {
		if err := rules.ValidateAction(rules.Action{Type: "browser_ttl", BrowserTTL: d.Delivery.BrowserTTL}, rules.Delivery); err != nil {
			return err
		}
	}
	if d.Delivery.CORS != nil {
		return rules.ValidateCORS(*d.Delivery.CORS)
	}
	return nil
}

// These bounds mirror analytics.Options, without coupling config compilation to
// a database package. Values are explicit here: zero is not an implicit default.
func validateAnalytics(a Analytics) error {
	checks := []struct {
		bad            bool
		field, message string
	}{
		{a.MemoryBytes < 32<<20, "memory_bytes", "minimum is 32 MiB"},
		{a.MaxBytes < 32<<20, "max_bytes", "minimum is 32 MiB"},
		{a.TempBytes < 1<<20 || a.TempBytes > a.MaxBytes/8, "temp_bytes", "must be between 1 MiB and one eighth of max_bytes"},
		{a.DetailDays < 1, "detail_days", "must be at least one day"},
		{a.RollupDays < a.DetailDays || a.RollupDays > 366, "rollup_days", "must be >= detail_days and <= 366"},
		{a.QueueSize < 1 || a.QueueSize > 1<<20, "queue_size", "must be 1..1048576"},
		{a.BatchSize < 1 || a.BatchSize > 8192, "batch_size", "must be 1..8192"},
		{a.FlushIntervalMS < 1 || a.FlushIntervalMS > 60000, "flush_interval_ms", "must be 1..60000"},
		{a.QueryConcurrency < 1 || a.QueryConcurrency > 8, "query_concurrency", "must be 1..8"},
		{a.QueryTimeoutMS < 1 || a.QueryTimeoutMS > 120000, "query_timeout_ms", "must be 1..120000"},
		{a.Threads < 1 || a.Threads > 16, "threads", "must be 1..16"},
	}
	for _, check := range checks {
		if check.bad {
			return &rules.ValidationError{Path: "analytics." + check.field, Message: check.message}
		}
	}
	return nil
}

func validDomain(s string) bool {
	if ip, err := netip.ParseAddr(s); err == nil {
		return ip.Zone() == ""
	}
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func cloneDocument(d Document) Document {
	b, _ := json.Marshal(d)
	var out Document
	_ = json.Unmarshal(b, &out)
	return out
}
func cloneSite(s Site) Site {
	s.Domains = slices.Clone(s.Domains)
	s.Origins = slices.Clone(s.Origins)
	for i := range s.Origins {
		o := &s.Origins[i]
		o.caPEM = slices.Clone(o.caPEM)
		if o.Timeouts != nil {
			old := o.Timeouts
			t := *old
			o.Timeouts = &t
			for _, pair := range []struct {
				source *int64
				target **int64
			}{{old.ConnectMS, &t.ConnectMS}, {old.TLSHandshakeMS, &t.TLSHandshakeMS}, {old.ResponseHeaderMS, &t.ResponseHeaderMS}, {old.IdleMS, &t.IdleMS}, {old.RequestMS, &t.RequestMS}} {
				if pair.source != nil {
					n := *pair.source
					*pair.target = &n
				}
			}
		}
	}
	return s
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
