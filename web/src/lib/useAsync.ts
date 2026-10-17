import { useCallback, useEffect, useRef, useState } from 'react';
import { errorMessage } from '../api';

export function useAsync<T>(
  load: (signal: AbortSignal) => Promise<T>,
  key: string,
  pollMs = 0,
  enabled = true,
  keepPrevious = false,
) {
  const loader = useRef(load);
  loader.current = load;
  const [revision, setRevision] = useState(0);
  const requestKey = JSON.stringify([key, revision, enabled]);
  const [state, setState] = useState<{ data?: T; error?: string; loading: boolean; key: string }>({
    loading: true,
    key: requestKey,
  });
  useEffect(() => {
    if (!enabled) {
      setState((previous) => ({
        data: keepPrevious ? previous.data : undefined,
        loading: false,
        key: requestKey,
      }));
      return;
    }
    const controller = new AbortController();
    setState((previous) => ({
      data: keepPrevious ? previous.data : undefined,
      loading: true,
      key: requestKey,
    }));
    loader
      .current(controller.signal)
      .then((data) => {
        if (!controller.signal.aborted) setState({ data, loading: false, key: requestKey });
      })
      .catch((error) => {
        if (!controller.signal.aborted)
          setState((previous) => ({
            data: keepPrevious ? previous.data : undefined,
            error: errorMessage(error),
            loading: false,
            key: requestKey,
          }));
      });
    return () => controller.abort();
  }, [key, revision, enabled, keepPrevious]);
  useEffect(() => {
    if (!pollMs || !enabled) return;
    const timer = window.setInterval(() => setRevision((r) => r + 1), pollMs);
    return () => window.clearInterval(timer);
  }, [pollMs, enabled]);
  const reload = useCallback(() => setRevision((r) => r + 1), []);
  // Retained results opt in and keep their own query snapshot. Other callers
  // never see a previous key's data, including the render before the effect.
  const current = state.key === requestKey;
  const visible = current
    ? state
    : { data: keepPrevious ? state.data : undefined, loading: enabled, key: requestKey };
  return { ...visible, error: 'error' in visible ? visible.error : undefined, reload };
}
