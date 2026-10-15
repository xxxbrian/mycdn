package cache

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// End is exclusive. Empty bodies are represented by [0,0).
type span struct{ Start, End int64 }

var errUnsatisfiable = errors.New("cache: unsatisfiable range")
var errMalformedRange = errors.New("cache: malformed range")

func parseRange(value string, size int64) (span, error) {
	whole := span{0, size}
	if value == "" || !strings.HasPrefix(value, "bytes=") {
		return whole, errMalformedRange
	}
	start, end, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(value, "bytes=")), "-")
	if !ok || strings.Contains(end, ",") {
		return whole, errMalformedRange // ignored for delivery; multipart bypasses
	}
	if start == "" {
		n, err := strconv.ParseInt(end, 10, 64)
		if err != nil || n < 0 {
			return whole, errMalformedRange
		}
		if n == 0 || size == 0 {
			return span{}, errUnsatisfiable
		}
		if n > size {
			n = size
		}
		return span{size - n, size}, nil
	}
	a, err := strconv.ParseInt(start, 10, 64)
	if err != nil || a < 0 {
		return whole, errMalformedRange
	}
	b := size - 1
	if end != "" {
		b, err = strconv.ParseInt(end, 10, 64)
		if err != nil || b < a {
			return whole, errMalformedRange
		}
	}
	if a >= size {
		return span{}, errUnsatisfiable
	}
	if b >= size {
		b = size - 1
	}
	return span{a, b + 1}, nil
}

func contentRange(value string) (span, int64, error) {
	var a, b, size int64
	var extra string
	n, err := fmt.Sscanf(value, "bytes %d-%d/%d%s", &a, &b, &size, &extra)
	if (err != nil && n != 3) || n != 3 || a < 0 || b < a || b >= size || size <= 0 {
		return span{}, 0, errors.New("cache: invalid Content-Range")
	}
	return span{a, b + 1}, size, nil
}

func rangeHeader(s span) string { return fmt.Sprintf("bytes=%d-%d", s.Start, s.End-1) }

func mergeSpans(a []span, b span) []span {
	return unionSpans(a, []span{b})
}

func unionSpans(sets ...[]span) []span {
	var out []span
	for _, set := range sets {
		out = append(out, set...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	n := 0
	for _, v := range out {
		if v.Start == v.End {
			continue
		}
		if n > 0 && v.Start <= out[n-1].End {
			if v.End > out[n-1].End {
				out[n-1].End = v.End
			}
		} else {
			out[n] = v
			n++
		}
	}
	return out[:n]
}

func covers(coverage []span, want span) bool {
	if want.Start == want.End {
		return true
	}
	for _, v := range coverage {
		if v.Start <= want.Start && v.End >= want.End {
			return true
		}
	}
	return false
}

// One continuous upstream request spans the holes. Published overlap is consumed
// from the origin, but never written again.
func missing(coverage []span, want span) span {
	first, last := int64(-1), want.Start
	p := want.Start
	for _, v := range coverage {
		if v.End <= p || v.Start >= want.End {
			continue
		}
		if v.Start > p {
			if first < 0 {
				first = p
			}
			last = v.Start
		}
		if v.End > p {
			p = v.End
		}
	}
	if p < want.End {
		if first < 0 {
			first = p
		}
		last = want.End
	}
	return span{first, last}
}

func spanBytes(spans []span) int64 {
	var n int64
	for _, v := range spans {
		n += v.End - v.Start
	}
	return n
}

func uncovered(coverage []span, wanted span) []span {
	var out []span
	pos := wanted.Start
	for _, v := range coverage {
		if v.End <= pos || v.Start >= wanted.End {
			continue
		}
		if pos < v.Start {
			out = append(out, span{pos, v.Start})
		}
		pos = max(pos, v.End)
	}
	if pos < wanted.End {
		out = append(out, span{pos, wanted.End})
	}
	return out
}

func intersect(spans []span, wanted span) []span {
	var out []span
	for _, v := range spans {
		a, b := max(v.Start, wanted.Start), min(v.End, wanted.End)
		if a < b {
			out = append(out, span{a, b})
		}
	}
	return out
}

func overlaps(a, b []span) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i].Start < b[j].End && b[j].Start < a[i].End {
			return true
		}
		if a[i].End <= b[j].Start {
			i++
		} else {
			j++
		}
	}
	return false
}
