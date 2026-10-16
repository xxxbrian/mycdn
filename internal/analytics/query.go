package analytics

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
)

type queryPlan struct {
	from, to  time.Time
	interval  int
	where     string
	args      []any
	upstream  bool
	uniqueIPs bool
}

func plan(from, to string, filters Filters) (queryPlan, error) {
	var p queryPlan
	var err error
	if p.from, err = time.Parse(time.RFC3339Nano, from); err != nil {
		return p, &ValidationError{"from", "must be RFC3339"}
	}
	if p.to, err = time.Parse(time.RFC3339Nano, to); err != nil {
		return p, &ValidationError{"to", "must be RFC3339"}
	}
	if p.from.UnixMilli() < 0 || p.to.UnixMilli() <= 0 {
		return p, &ValidationError{"from", "must be on or after the Unix epoch"}
	}
	if !p.to.After(p.from) || p.to.Sub(p.from) > 367*day {
		return p, &ValidationError{"to", "must be after from, at most 367 days apart"}
	}
	if p.from.Nanosecond()%1_000_000 != 0 || p.to.Nanosecond()%1_000_000 != 0 {
		return p, &ValidationError{"from", "query times require millisecond precision or coarser"}
	}
	where, args, upstream, err := filterSQL(filters)
	if err != nil {
		return p, err
	}
	p.where = "ts>=? AND ts<?" + where
	p.args = append([]any{p.from.UnixMilli(), p.to.UnixMilli()}, args...)
	p.upstream = upstream
	return p, nil
}

func filterSQL(f Filters) (string, []any, bool, error) {
	var clauses []string
	var args []any
	count := 0
	addStrings := func(field, col string, values []string, normalize func(string) string) error {
		if len(values) > 64 {
			return &ValidationError{field, "at most 64 values"}
		}
		count += len(values)
		if len(values) == 0 {
			return nil
		}
		for _, v := range values {
			if len(v) > 4096 {
				return &ValidationError{field, "value exceeds 4096 bytes"}
			}
			if normalize != nil {
				v = normalize(v)
			}
			args = append(args, v)
		}
		clauses = append(clauses, col+" IN ("+placeholders(len(values))+")")
		return nil
	}
	for _, v := range []struct {
		field, col string
		values     []string
		normalize  func(string) string
	}{
		{"filters.site_ids", "site_id", f.SiteIDs, nil},
		{"filters.hosts", "host", f.Hosts, strings.ToLower},
		{"filters.methods", "method", f.Methods, strings.ToUpper},
		{"filters.cache_results", "cache_result", f.CacheResults, strings.ToUpper},
		{"filters.content_types", "content_type", f.ContentTypes, nil},
		{"filters.countries", "country", f.Countries, strings.ToUpper},
	} {
		if err := addStrings(v.field, v.col, v.values, v.normalize); err != nil {
			return "", nil, false, err
		}
	}
	if len(f.Statuses) > 64 || len(f.ASNs) > 64 || len(f.ClientIPs) > 64 {
		return "", nil, false, &ValidationError{"filters", "at most 64 values per filter"}
	}
	count += len(f.Statuses) + len(f.ASNs) + len(f.ClientIPs)
	if count > 256 {
		return "", nil, false, &ValidationError{"filters", "at most 256 values total"}
	}
	if len(f.Statuses) > 0 {
		clauses = append(clauses, "status IN ("+placeholders(len(f.Statuses))+")")
		for _, v := range f.Statuses {
			if !validStatus(v) {
				return "", nil, false, &ValidationError{"filters.statuses", "must be 0 or 100..599"}
			}
			args = append(args, v)
		}
	}
	if len(f.ASNs) > 0 {
		clauses = append(clauses, "asn IN ("+placeholders(len(f.ASNs))+")")
		for _, v := range f.ASNs {
			args = append(args, v)
		}
	}
	if len(f.ClientIPs) > 0 {
		var ips []string
		for _, value := range f.ClientIPs {
			prefix, err := parsePrefix(value)
			if err != nil {
				return "", nil, false, &ValidationError{"filters.client_ips", "invalid IP or CIDR: " + value}
			}
			low, family := packedIP(prefix.Addr())
			high := append([]byte(nil), low...)
			bits := prefix.Bits()
			if family == 4 {
				bits += 96
			}
			for bit := bits; bit < 128; bit++ {
				high[bit/8] |= 1 << (7 - uint(bit%8))
			}
			ips = append(ips, "(ip_family=? AND ip_key>=? AND ip_key<=?)")
			args = append(args, family, low, high)
		}
		clauses = append(clauses, "("+strings.Join(ips, " OR ")+")")
	}
	if len(f.PathPrefix) > 4096 {
		return "", nil, false, &ValidationError{"filters.path_prefix", "maximum 4096 bytes"}
	}
	if f.PathPrefix != "" {
		clauses = append(clauses, "starts_with(path,?)")
		args = append(args, f.PathPrefix)
	}
	upstream := len(f.ClientIPs)+len(f.CacheResults)+len(f.ContentTypes)+len(f.Countries)+len(f.ASNs) == 0
	if !upstream {
		clauses = append(clauses, "NOT physical")
	}
	if len(clauses) == 0 {
		return "", args, upstream, nil
	}
	return " AND " + strings.Join(clauses, " AND "), args, upstream, nil
}

func parsePrefix(value string) (netip.Prefix, error) {
	if strings.Contains(value, "/") {
		p, err := netip.ParsePrefix(value)
		if err != nil {
			return p, err
		}
		if p.Addr().Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		return p.Masked(), nil
	}
	ip, err := netip.ParseAddr(value)
	if err != nil || ip.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("invalid unscoped address")
	}
	ip = ip.Unmap()
	return netip.PrefixFrom(ip, ip.BitLen()), nil
}

func lowCardinality(group string) bool {
	return group != "client_ip" && group != "path"
}

type population uint8

const (
	allEvents population = iota
	consumers
	origins
)

func dimensionPopulation(group string) population {
	switch group {
	case "client_ip", "cache_result", "content_type", "country", "asn":
		return consumers
	case "origin_id", "protocol":
		return origins
	}
	return allEvents
}

func (s *Store) Query(ctx context.Context, q Query) (*Result, error) {
	p, err := plan(q.From, q.To, q.Filters)
	if err != nil {
		return nil, err
	}
	if q.Timezone != "" {
		if _, err = time.LoadLocation(q.Timezone); err != nil {
			return nil, &ValidationError{"timezone", "unknown IANA timezone"}
		}
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 100 {
		return nil, &ValidationError{"limit", "must be 1..100"}
	}
	groups := q.GroupBy
	if len(groups) == 0 {
		groups = []string{"host", "cache_result", "content_type", "country", "asn"}
	}
	if len(groups) > len(dimensions) {
		return nil, &ValidationError{"group_by", "too many dimensions"}
	}
	seen := map[string]bool{}
	for _, g := range groups {
		valid := false
		for _, allowed := range dimensions {
			if g == allowed {
				valid = true
				break
			}
		}
		if !valid || seen[g] {
			return nil, &ValidationError{"group_by", "unknown or repeated dimension: " + g}
		}
		seen[g] = true
	}
	p.interval = q.IntervalSeconds
	if p.interval == 0 {
		p.interval = max(10, int((p.to.Sub(p.from).Seconds()/600+9)/10)*10)
	}
	if p.interval < 10 || p.interval%10 != 0 || p.interval > int(367*day/time.Second) {
		return nil, &ValidationError{"interval_seconds", "must be a positive multiple of 10"}
	}
	span := (p.to.UnixMilli()-1)/(int64(p.interval)*1000) - p.from.UnixMilli()/(int64(p.interval)*1000) + 1
	if span > maxPoints {
		return nil, &ValidationError{"interval_seconds", "would produce more than 4096 points"}
	}
	needsDetails := len(q.Filters.ClientIPs) > 0 || q.Filters.PathPrefix != ""
	for _, g := range groups {
		needsDetails = needsDetails || !lowCardinality(g)
	}
	if needsDetails && p.from.Before(s.detailFrom(s.options.Now())) {
		return nil, ErrDetailRetention
	}
	snapshot, err := s.snapshot(ctx, p, needsDetails, false)
	if err != nil {
		return nil, err
	}
	defer snapshot.close()
	conn, ctx, p := snapshot.conn, snapshot.ctx, snapshot.plan
	r := &Result{Series: []Point{}, Breakdowns: map[string][]Breakdown{}, Geo: []Geography{}, Coverage: snapshot.coverage}
	if p.uniqueIPs {
		zero := int64(0)
		r.Summary.UniqueIPs = &zero
	}
	rows, err := aggregate(ctx, conn, "''", allEvents, p.uniqueIPs, 1, false)
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		r.Summary = rows[0].Metrics
	}
	r.Summary.rates(p.to.Sub(p.from).Seconds())
	intervalMS := int64(p.interval) * 1000
	rows, err = aggregate(ctx, conn, "CAST((ts//"+strconv.FormatInt(intervalMS, 10)+")*"+strconv.FormatInt(intervalMS, 10)+" AS VARCHAR)", allEvents, p.uniqueIPs, maxPoints, false)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		ms, _ := strconv.ParseInt(row.Value, 10, 64)
		start := time.UnixMilli(ms)
		seconds := min(p.to.UnixMilli(), ms+intervalMS) - max(p.from.UnixMilli(), ms)
		row.Metrics.rates(float64(seconds) / 1000)
		r.Series = append(r.Series, Point{stamp(start), row.Metrics})
	}
	// Aggregate rankings are byte-sorted; time series are chronological.
	sortPoints(r.Series)
	geo, err := aggregate(ctx, conn, "CAST(country AS VARCHAR)", consumers, p.uniqueIPs, 300, true)
	if err != nil {
		return nil, err
	}
	for i := range geo {
		geo[i].rates(p.to.Sub(p.from).Seconds())
		r.Geo = append(r.Geo, Geography{geo[i].Value, geo[i].Metrics})
	}
	for _, g := range groups {
		if g == "country" {
			r.Breakdowns[g] = geo[:min(q.Limit, len(geo))]
			continue
		}
		rows, err = aggregate(ctx, conn, "CAST("+g+" AS VARCHAR)", dimensionPopulation(g), p.uniqueIPs, q.Limit, true)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			rows[i].rates(p.to.Sub(p.from).Seconds())
		}
		r.Breakdowns[g] = rows
	}
	return r, nil
}

func aggregate(ctx context.Context, conn *sql.Conn, group string, population population, ipHistory bool, limit int, ranking bool) ([]Breakdown, error) {
	where := ""
	order := "rout DESC,n DESC,grp"
	switch population {
	case consumers:
		where = " WHERE NOT physical"
	case origins:
		where = " WHERE physical"
		order = "oout DESC,onum DESC,grp"
	}
	var sums []string
	for _, col := range sumColumns {
		sums = append(sums, "CAST(sum("+col+") AS "+metricSQLType(col)+") AS "+col)
	}
	uniqueIPs := "NULL::BIGINT AS unique_ips"
	if ipHistory && population != origins {
		uniqueIPs = "count(DISTINCT client_ip) FILTER (WHERE n>0 AND client_ip<>'') AS unique_ips"
	}
	var quantiles []string
	for _, who := range []string{"r", "o", "t", "ot"} {
		for _, percentile := range []int{50, 95, 99} {
			quantiles = append(quantiles, fmt.Sprintf("coalesce(min(v) FILTER (WHERE who='%s' AND cumulative>=ceil(total*%d/100.0)),0) AS %s%d", who, percentile, who, percentile))
		}
	}
	// Only the grouping key, metric inputs and client identity are needed after
	// scoping. Do not copy every path/host/etc into each aggregate's CTE.
	fields := "ts,client_ip," + strings.Join(sumColumns, ",") + ",latency_n,origin_latency_n,latency_value,origin_latency_value,ttfb_value,origin_ttfb_value"
	prefix := `WITH scoped AS MATERIALIZED (SELECT ` + fields + `,` + group + ` AS grp FROM ` + snapshotTable + where + `)`
	values := []any{}
	if ranking {
		keyOrder := "sum(rout) DESC,sum(n) DESC,grp"
		if population == origins {
			keyOrder = "sum(oout) DESC,sum(onum) DESC,grp"
		}
		prefix = `WITH candidates AS NOT MATERIALIZED (SELECT ` + fields + `,` + group + ` AS grp FROM ` + snapshotTable + where + `),
 winners AS MATERIALIZED (SELECT grp FROM candidates GROUP BY grp ORDER BY ` + keyOrder + ` LIMIT ?),
 scoped AS MATERIALIZED (SELECT candidates.* FROM candidates SEMI JOIN winners USING(grp))`
		values = append(values, limit)
	}
	q := prefix + `,
 totals AS (SELECT grp,` + strings.Join(sums, ",") + `,` + uniqueIPs + ` FROM scoped GROUP BY grp),
 traffic AS (SELECT grp,ts//10000 AS bucket,sum(rout) AS r,sum(oout) AS o FROM scoped GROUP BY grp,bucket),
 peaks AS (SELECT grp,max(r)/10.0 AS rp,max(o)/10.0 AS op FROM traffic GROUP BY grp),
 samples AS (SELECT grp,'r' AS who,latency_value AS v,latency_n AS weight FROM scoped WHERE latency_n>0
 UNION ALL SELECT grp,'o',origin_latency_value,origin_latency_n FROM scoped WHERE origin_latency_n>0
 UNION ALL SELECT grp,'t',ttfb_value,ttfb_n FROM scoped WHERE ttfb_n>0
 UNION ALL SELECT grp,'ot',origin_ttfb_value,origin_ttfb_n FROM scoped WHERE origin_ttfb_n>0),
 histogram AS (SELECT grp,who,v,sum(weight) AS weight FROM samples GROUP BY grp,who,v),
 cdf AS (SELECT *,sum(weight) OVER (PARTITION BY grp,who ORDER BY v ROWS UNBOUNDED PRECEDING) AS cumulative,
 sum(weight) OVER (PARTITION BY grp,who) AS total FROM histogram),
 latencies AS (SELECT grp,` + strings.Join(quantiles, ",") + ` FROM cdf GROUP BY grp)
 SELECT totals.*,coalesce(r50,0),coalesce(r95,0),coalesce(r99,0),
 coalesce(o50,0),coalesce(o95,0),coalesce(o99,0),
 coalesce(t50,0),coalesce(t95,0),coalesce(t99,0),
 coalesce(ot50,0),coalesce(ot95,0),coalesce(ot99,0),rp,op
 FROM totals JOIN peaks USING(grp) LEFT JOIN latencies USING(grp)
 ORDER BY ` + order + ` LIMIT ?`
	values = append(values, limit)
	rows, err := conn.QueryContext(ctx, q, values...)
	if err != nil {
		return nil, fmt.Errorf("aggregate analytics: %w", err)
	}
	defer rows.Close()
	result := []Breakdown{}
	for rows.Next() {
		var b Breakdown
		m := &b.Metrics
		err = rows.Scan(&b.Value, &m.Requests, &m.RequestErrors, &m.Status4xx, &m.Status5xx, &m.CacheHits,
			&m.CacheEligibleRequests, &m.CoalescedRequests, &m.RequestBytes, &m.ResponseBytes, &m.CachedBytes,
			&m.OriginAttempts, &m.OriginErrors, &m.OriginRequestBytes, &m.OriginResponseBytes, &m.DurationSumMS, &m.OriginDurationSumMS,
			&m.TTFBSamples, &m.OriginTTFBSamples, &m.UniqueIPs,
			&m.Latency.P50MS, &m.Latency.P95MS, &m.Latency.P99MS, &m.OriginLatency.P50MS, &m.OriginLatency.P95MS, &m.OriginLatency.P99MS,
			&m.TTFB.P50MS, &m.TTFB.P95MS, &m.TTFB.P99MS, &m.OriginTTFB.P50MS, &m.OriginTTFB.P95MS, &m.OriginTTFB.P99MS,
			&m.PeakResponseBytesPerSecond, &m.PeakOriginResponseBytesPerSecond)
		if err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

func (m *Metrics) rates(seconds float64) {
	if seconds > 0 {
		m.RequestsPerSecond = float64(m.Requests) / seconds
		m.ResponseBytesPerSecond = float64(m.ResponseBytes) / seconds
		m.OriginResponseBytesPerSecond = float64(m.OriginResponseBytes) / seconds
	}
	if m.Requests > 0 {
		m.ErrorRate = float64(m.RequestErrors) / float64(m.Requests)
	}
	if m.CacheEligibleRequests > 0 {
		m.CacheHitRate = float64(m.CacheHits) / float64(m.CacheEligibleRequests)
	}
	if m.ResponseBytes > 0 {
		m.ByteHitRate = float64(m.CachedBytes) / float64(m.ResponseBytes)
	}
}

func sortPoints(points []Point) {
	// RFC3339 strings with optional fractions are not lexicographically chronological.
	sort.Slice(points, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, points[i].Time)
		b, _ := time.Parse(time.RFC3339Nano, points[j].Time)
		return a.Before(b)
	})
}

func snapshotCoverage(status Status, p queryPlan, rolled bool, now time.Time, retention time.Duration, evictedBefore int64) Coverage {
	c := Coverage{
		RequestedFrom: stamp(p.from), RequestedTo: stamp(p.to), DetailFrom: status.DetailFrom, RollupFrom: status.RollupFrom,
		DiskEvictedBefore: status.DiskEvictedBefore, UsesRollups: rolled, LatencyApproximate: rolled,
		UniqueIPsAvailable:   p.uniqueIPs,
		UpstreamAttributable: p.upstream, BaseBucketSeconds: 10, IntervalSeconds: p.interval, Dropped: status.Dropped,
		QueuedEvents: status.PendingEvents, IngestLagMS: status.IngestLagMS, GeoCountryAvailable: status.GeoCountryAvailable, GeoASNAvailable: status.GeoASNAvailable,
	}
	c.Partial = p.from.Before(horizon(now, retention)) ||
		(evictedBefore != 0 && p.from.UnixMilli() < evictedBefore) ||
		status.Dropped.Queue+status.Dropped.Invalid+status.Dropped.Expired+status.Dropped.Storage > 0 || status.PendingEvents > 0
	return c
}

func (s *Store) Details(ctx context.Context, q DetailsQuery) (*DetailsResult, error) {
	p, err := plan(q.From, q.To, q.Filters)
	if err != nil {
		return nil, err
	}
	if p.from.Before(s.detailFrom(s.options.Now())) {
		return nil, ErrDetailRetention
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > 500 {
		return nil, &ValidationError{"limit", "must be 1..500"}
	}
	if q.Offset < 0 || q.Offset > 10000 {
		return nil, &ValidationError{"offset", "must be 0..10000"}
	}
	snapshot, err := s.snapshot(ctx, p, true, true)
	if err != nil {
		return nil, err
	}
	defer snapshot.close()
	conn, ctx := snapshot.conn, snapshot.ctx
	r := &DetailsResult{Rows: []RequestDetail{}, Coverage: snapshot.coverage}
	stmt := `SELECT id,site_id,host,client_ip,path,method,content_type,
 status,cache_result,started_at,ts,duration_ms,first_byte_ms,request_bytes,response_bytes,cached_bytes,error,country,asn
 FROM ` + snapshotTable + ` ORDER BY ts DESC,id LIMIT ? OFFSET ?`
	rows, err := conn.QueryContext(ctx, stmt, q.Limit+1, q.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d RequestDetail
		err = rows.Scan(&d.RequestID, &d.SiteID, &d.Host, &d.ClientIP, &d.Path, &d.Method, &d.ContentType, &d.Status, &d.CacheResult,
			&d.StartedAt, &d.CompletedAt, &d.DurationMS, &d.FirstByteMS, &d.RequestBytes, &d.ResponseBytes, &d.CachedBytes, &d.Error, &d.Country, &d.ASN)
		if err != nil {
			return nil, err
		}
		r.Rows = append(r.Rows, d)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(r.Rows) > q.Limit {
		r.HasMore = true
		r.Rows = r.Rows[:q.Limit]
		next := q.Offset + q.Limit
		if next <= 10000 {
			r.NextOffset = &next
		}
	}
	return r, nil
}
