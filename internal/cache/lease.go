package cache

import "net/http"

const (
	maxKeyFills = 4
	maxExtents  = 128
)

// A lease reserves only unpublished positions. The upstream permit ends at
// acquisition completion; this reservation lasts until every subscriber and
// in-progress ReadAt has drained. bytes belongs to the acquisition writer until
// finish; published and key-list membership use the resource lock.
type rangeLease struct {
	object    *object
	spans     []span
	bytes     int64
	published bool
}

type acquisition struct {
	extend bool
	part   span
	writes []span
}

func (k *keyState) signal() {
	close(k.changed)
	k.changed = make(chan struct{})
}

func (s *Store) plan(k *keyState, req Request, base *metadata, background bool) acquisition {
	if base == nil || base.Inline || base.complete() || strongETag(base.Header) == "" || req.Method != http.MethodGet {
		return acquisition{}
	}
	want := span{0, base.Size}
	if !background && req.Header.Get("If-Range") != "" && !ifRangeMatches(req.Header.Get("If-Range"), base.Header) {
		return acquisition{}
	}
	if !background {
		if requested, err := parseRange(req.Header.Get("Range"), base.Size); err == nil {
			want = requested
		}
	}
	if covers(base.Coverage, want) {
		return acquisition{}
	}
	part := missing(base.Coverage, want)
	block := s.opt.BlockBytes
	part.Start -= part.Start % block
	if tail := part.End % block; tail != 0 {
		part.End += min(block-tail, base.Size-part.End)
	}
	p := acquisition{extend: true, part: part, writes: uncovered(base.Coverage, part)}
	leaseExtents := 0
	for _, l := range k.leases {
		if l.object.id == base.ID && !l.published {
			leaseExtents += len(l.spans)
		}
	}
	// Fragmentation fallback acquires an independent generation without copying.
	if len(base.Coverage)+leaseExtents+len(p.writes) > maxExtents {
		return acquisition{}
	}
	return p
}

func (k *keyState) canStart(p acquisition, base *metadata) bool {
	if len(k.fills) >= maxKeyFills {
		return false
	}
	if !p.extend {
		return len(k.fills) == 0
	}
	for _, l := range k.leases {
		if l.object.id == base.ID && overlaps(l.spans, p.writes) {
			return false
		}
	}
	for _, f := range k.fills {
		if f.lease == nil || f.exclusive || f.base.ID != base.ID || f.epoch != k.rec.Epoch {
			return false
		}
	}
	return true
}

func (f *fill) targetCurrent() bool {
	current := f.key.rec.match(f.identity)
	if f.key.rec.Epoch != f.epoch {
		return false
	}
	if f.base == nil {
		return current == nil
	}
	return current != nil && current.ID == f.base.ID
}

func (f *fill) dropReferences() {
	f.dropOnce.Do(func() {
		if l := f.lease; l != nil {
			k := f.key
			k.mu.Lock()
			for i, active := range k.leases {
				if active == l {
					k.leases = append(k.leases[:i], k.leases[i+1:]...)
					break
				}
			}
			if !l.published {
				// Abandoned extents remain unreferenced disk bytes, but no longer
				// need resident reader protection or count as recoverable coverage.
				f.s.objectsMu.Lock()
				l.object.bytes -= l.bytes
				f.s.objectsMu.Unlock()
				f.s.count.resident.Add(-l.bytes)
			}
			k.signal()
			k.mu.Unlock()
		}
		if f.object != f.baseObject {
			f.s.release(f.object)
		}
		f.s.release(f.baseObject)
		f.s.releaseKey(f.key)
	})
}

// Extension metadata starts from the latest published snapshot; an older origin
// head cannot undo newer freshness or access merely by finishing last.
func extendMetadata(current, response metadata, additions []span) metadata {
	next := current
	if response.StoredAt.After(current.StoredAt) {
		next = response
		next.ID, next.Size, next.Inline = current.ID, current.Size, current.Inline
	}
	if current.AccessedAt.After(next.AccessedAt) {
		next.AccessedAt = current.AccessedAt
	}
	next.Coverage = unionSpans(current.Coverage, additions)
	next.Bytes = spanBytes(next.Coverage)
	return next
}
