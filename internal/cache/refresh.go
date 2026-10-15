package cache

import (
	"context"
	"errors"
	"io"
	"net/http"
)

// Refresh uses the caller's current immutable GET plan, retaining the published
// entry until a replacement or conditional revalidation commits. It drains the
// response and waits for storage, not just delivery. Bypass, stale fallback, and
// publication rejected by purge return ErrNotStored, never false success.
func (s *Store) Refresh(ctx context.Context, req Request) error {
	if req.Method != "" && req.Method != http.MethodGet {
		return errors.New("cache: Refresh requires a GET plan")
	}
	req.Revalidate = true
	resp, err := s.Do(ctx, req)
	if err != nil {
		return err
	}
	defer resp.HTTP.Body.Close()
	if _, err := io.Copy(io.Discard, resp.HTTP.Body); err != nil {
		return err
	}
	if resp.wait == nil {
		return ErrNotStored
	}
	return resp.wait(ctx)
}

func (f *fill) wait(ctx context.Context) error {
	select {
	case <-f.done:
		if f.err != nil {
			return f.err
		}
		if f.publishErr != nil {
			return f.publishErr
		}
		if !f.published {
			return ErrNotStored
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
