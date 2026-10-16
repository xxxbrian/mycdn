package analytics

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"
	geoip2 "github.com/oschwald/geoip2-golang/v2"
)

type geoReaders struct {
	country *geoip2.Reader
	asn     *geoip2.Reader
	err     string
}

func openGeo(countryPath, asnPath string) geoReaders {
	var g geoReaders
	probe := netip.MustParseAddr("1.1.1.1")
	open := func(path, label string, isASN bool) *geoip2.Reader {
		if path == "" {
			return nil
		}
		r, err := geoip2.Open(path)
		if err == nil {
			if isASN {
				_, err = r.ASN(probe)
			} else {
				_, err = r.Country(probe)
			}
			if err != nil {
				_ = r.Close()
			}
		}
		if err != nil {
			if g.err != "" {
				g.err += "; "
			}
			g.err += label + ": " + err.Error()
			return nil
		}
		return r
	}
	g.country = open(countryPath, "country MMDB", false)
	g.asn = open(asnPath, "ASN MMDB", true)
	return g
}

func (g geoReaders) close() error {
	var a, b error
	if g.country != nil {
		a = g.country.Close()
	}
	if g.asn != nil {
		b = g.asn.Close()
	}
	return errors.Join(a, b)
}

func (s *Store) locate(ip netip.Addr) (string, uint32) {
	var country string
	var asn uint32
	if !ip.IsValid() {
		return country, asn
	}
	if s.geo.country != nil {
		v, err := s.geo.country.Country(ip)
		if err != nil {
			s.noteError(fmt.Errorf("GeoIP country lookup: %w", err))
		} else {
			country = v.Country.ISOCode
		}
	}
	if s.geo.asn != nil {
		v, err := s.geo.asn.ASN(ip)
		if err != nil {
			s.noteError(fmt.Errorf("GeoIP ASN lookup: %w", err))
		} else {
			asn = uint32(v.AutonomousSystemNumber)
		}
	}
	return country, asn
}

func bounded(values ...string) bool {
	total := 0
	for _, v := range values {
		if len(v) > 4096 {
			return false
		}
		total += len(v)
	}
	return total <= 8192
}

func validTimes(start, end, duration int64) bool {
	return start >= 0 && end > 0 && end >= start && duration >= 0 && duration <= int64(366*day/time.Millisecond)
}

func validSizes(in, out, cached int64) bool {
	return in >= 0 && out >= 0 && cached >= 0 && cached <= out
}

func validStatus(status int) bool { return status == 0 || status >= 100 && status <= 599 }

func validRequest(e RequestEvent) bool {
	return validTimes(e.StartedAt, e.CompletedAt, e.DurationMS) && validStatus(e.Status) &&
		e.FirstByteMS >= 0 && e.FirstByteMS <= e.DurationMS &&
		validSizes(e.RequestBytes, e.ResponseBytes, e.CachedBytes) && len(e.Error) <= 512 &&
		len(e.Host) <= 253 && len(e.ClientIP) <= 64 && len(e.Method) <= 32 &&
		bounded(e.RequestID, e.SiteID, e.Host, e.ClientIP, e.Path, e.Method, e.ContentType, e.CacheResult, e.Error)
}

func validOrigin(e OriginEvent) bool {
	return validTimes(e.StartedAt, e.CompletedAt, e.DurationMS) && validStatus(e.Status) &&
		e.FirstByteMS >= 0 && e.FirstByteMS <= e.DurationMS &&
		validSizes(e.RequestBytes, e.ResponseBytes, 0) && len(e.Error) <= 512 && len(e.Host) <= 253 &&
		bounded(e.AttemptID, e.SiteID, e.OriginID, e.Host, e.Path, e.Method, e.Protocol, e.Error)
}

func validTransfer(e TransferEvent) bool {
	return (e.Kind == "request" || e.Kind == "origin") && validTimes(e.IntervalStart, e.IntervalEnd, 0) &&
		validStatus(e.Status) && validSizes(e.RequestBytes, e.ResponseBytes, e.CachedBytes) &&
		len(e.Host) <= 253 && len(e.ClientIP) <= 64 &&
		bounded(e.RequestID, e.AttemptID, e.SiteID, e.OriginID, e.Host, e.ClientIP, e.Path, e.Method, e.ContentType, e.CacheResult, e.Protocol)
}

// rawRow matches rawSchema exactly. Kinds: request, origin, request transfer, origin transfer.
type rawRow struct {
	time, started, duration, firstByte                                                       int64
	kind                                                                                     int8
	id, site, origin, host, client, path, method, content, protocol, cache, country, failure string
	status                                                                                   int32
	asn                                                                                      uint32
	ipFamily                                                                                 int8
	ipKey                                                                                    []byte
	in, out, cached                                                                          int64
}

func (s *Store) normalize(value any) (rawRow, error) {
	var r rawRow
	switch e := value.(type) {
	case RequestEvent:
		r = rawRow{time: e.CompletedAt, started: e.StartedAt, duration: e.DurationMS, firstByte: e.FirstByteMS,
			id: e.RequestID, site: e.SiteID, host: e.Host, client: e.ClientIP, path: e.Path,
			method: e.Method, content: e.ContentType, cache: e.CacheResult, status: int32(e.Status),
			in: e.RequestBytes, out: e.ResponseBytes, cached: e.CachedBytes, failure: e.Error}
	case OriginEvent:
		r = rawRow{kind: 1, time: e.CompletedAt, started: e.StartedAt, duration: e.DurationMS, firstByte: e.FirstByteMS,
			id: e.AttemptID, site: e.SiteID, origin: e.OriginID, host: e.Host, path: e.Path,
			method: e.Method, protocol: e.Protocol, status: int32(e.Status),
			in: e.RequestBytes, out: e.ResponseBytes, failure: e.Error}
	case TransferEvent:
		kind, id := int8(2), e.RequestID
		if e.Kind == "origin" {
			kind, id = 3, e.AttemptID
		}
		r = rawRow{kind: kind, time: e.IntervalEnd, started: e.IntervalStart,
			id: id, site: e.SiteID, origin: e.OriginID, host: e.Host, path: e.Path,
			method: e.Method, status: int32(e.Status), in: e.RequestBytes, out: e.ResponseBytes}
		if kind == 2 {
			r.client, r.content, r.cache, r.cached = e.ClientIP, e.ContentType, e.CacheResult, e.CachedBytes
		} else {
			r.protocol = e.Protocol
		}
	}
	r.host = strings.ToLower(r.host)
	r.method = strings.ToUpper(r.method)
	r.cache = strings.ToUpper(r.cache)
	r.path, _, _ = strings.Cut(r.path, "?")
	r.path, _, _ = strings.Cut(r.path, "#")
	if r.client != "" {
		ip, err := netip.ParseAddr(r.client)
		if err != nil || ip.Zone() != "" {
			return r, &ValidationError{"client_ip", "must be an unscoped IP address"}
		}
		ip = ip.Unmap()
		r.client = ip.String()
		r.ipKey, r.ipFamily = packedIP(ip)
		r.country, r.asn = s.locate(ip)
	}
	return r, nil
}

func packedIP(ip netip.Addr) ([]byte, int8) {
	a := ip.As16()
	family := int8(6)
	if ip.Is4() {
		family = 4
	}
	return a[:], family
}

func (r rawRow) values() []driver.Value {
	return []driver.Value{r.time, r.kind, r.id, r.site, r.origin, r.host, r.client, r.path, r.method,
		r.content, r.protocol, r.status, r.cache, r.country, r.asn, r.ipFamily, r.ipKey,
		r.started, r.duration, r.in, r.out, r.cached, r.failure, r.firstByte}
}

func (s *Store) appendBatch(ctx context.Context, batch []message) error {
	if err := s.enforceBudget(ctx); err != nil {
		return s.rejectStorage(len(batch), err)
	}
	s.statsMu.RLock()
	pressure := s.pressure
	s.statsMu.RUnlock()
	if pressure {
		return s.rejectStorage(len(batch), ErrStoragePressure)
	}
	byDay := make(map[string][]rawRow)
	var lastErr error
	now := s.options.Now()
	floor := max(horizon(now, s.options.DetailRetention).UnixMilli(), s.meta.DetailBefore)
	for _, m := range batch {
		r, err := s.normalize(m.value)
		if err != nil {
			s.invalidDrop.Add(1)
			lastErr = err
			continue
		}
		if r.time < floor {
			s.expiredDrop.Add(1)
			lastErr = ErrDetailRetention
			continue
		}
		if r.time > now.Add(day).UnixMilli() {
			s.invalidDrop.Add(1)
			lastErr = errors.New("analytics event is more than one day in the future")
			continue
		}
		if r.time < s.meta.EvictedBefore {
			lastErr = s.rejectStorage(1, ErrStoragePressure)
			continue
		}
		key := time.UnixMilli(r.time).UTC().Format("20060102")
		if p := s.parts[key]; p != nil && p.roll {
			s.expiredDrop.Add(1)
			lastErr = ErrDetailRetention
			continue
		}
		byDay[key] = append(byDay[key], r)
	}
	keys := make([]string, 0, len(byDay))
	for key := range byDay {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		rows := byDay[key]
		if err := s.enforceBudget(ctx); err != nil {
			lastErr = s.rejectStorage(len(rows), err)
			continue
		}
		s.statsMu.RLock()
		pressure = s.pressure
		s.statsMu.RUnlock()
		if pressure || rows[0].time < s.meta.EvictedBefore {
			lastErr = s.rejectStorage(len(rows), ErrStoragePressure)
			continue
		}
		p, err := s.ensurePartition(ctx, key)
		if err == nil {
			err = s.appendRows(ctx, p, rows)
		}
		if err != nil {
			lastErr = s.rejectStorage(len(rows), err)
			continue
		}
		s.ingested.Add(uint64(len(rows)))
		s.lastIngest.Store(s.options.Now().UnixMilli())
	}
	s.lagMillis.Store(max(0, time.Since(batch[0].enqueued).Milliseconds()))
	return lastErr
}

func (s *Store) rejectStorage(count int, err error) error {
	s.storageDrop.Add(uint64(count))
	return fmt.Errorf("analytics dropped %d admitted events: %w", count, err)
}

func (s *Store) appendRows(ctx context.Context, p *partition, rows []rawRow) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	err = conn.Raw(func(raw any) error {
		app, err := duckdb.NewAppender(raw.(driver.Conn), p.alias, "main", "events")
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err = app.AppendRow(row.values()...); err != nil {
				return errors.Join(err, app.Clear(), app.CloseWithCancel(ctx))
			}
		}
		return app.CloseWithCancel(ctx)
	})
	if err != nil {
		return err
	}
	return tx.Commit()
}
