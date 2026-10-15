package cache

func (f *fill) publish(m *metadata) error {
	if f.extending {
		return f.publishExtension(m)
	}
	return f.publishReplacement(m)
}

// A compatible extension never changes generation identity, backing storage,
// or reference ownership. Sync only establishes new byte
// durability; the resource lock decides whether they still belong to this entry.
func (f *fill) publishExtension(response *metadata) error {
	if err := f.object.file.Sync(); err != nil {
		return err
	}
	k := f.key
	k.mu.Lock()
	defer k.mu.Unlock()
	if !f.targetCurrent() || f.s.closed.Load() {
		return nil
	}
	r := k.rec.copy()
	for i, current := range r.Entries {
		if current.ID != f.base.ID {
			continue
		}
		next := extendMetadata(current, *response, f.lease.spans)
		r.Entries[i] = next
		b := f.s.db.NewBatch()
		defer b.Close()
		if err := f.s.persist(k, r, b); err != nil {
			return err
		}
		f.s.count.bytes.Add(next.Bytes - current.Bytes)
		f.lease.published = true
		f.published = true
		return nil
	}
	return nil
}
