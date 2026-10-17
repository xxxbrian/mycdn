import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useState,
} from 'react';
import type { ReactNode } from 'react';

type Navigation = {
  params: URLSearchParams;
  update: (values: Record<string, string | null>, replace?: boolean) => void;
};
const Context = createContext<Navigation | null>(null);
export function NavigationProvider({ children }: { children: ReactNode }) {
  const [search, setSearch] = useState(window.location.search);
  useEffect(() => {
    const pop = () => setSearch(window.location.search);
    window.addEventListener('popstate', pop);
    return () => window.removeEventListener('popstate', pop);
  }, []);
  const update = useCallback((values: Record<string, string | null>, replace = false) => {
    const next = new URLSearchParams(window.location.search);
    const previousPage = next.get('page') || 'analytics';
    for (const [key, value] of Object.entries(values)) {
      if (value === null || value === '') next.delete(key);
      else next.set(key, value);
    }
    if (next.toString() === new URLSearchParams(window.location.search).toString()) return;
    const url = `${window.location.pathname}?${next.toString()}`;
    window.history.replaceState(
      { ...window.history.state, readingY: window.scrollY },
      '',
      window.location.href,
    );
    window.history[replace ? 'replaceState' : 'pushState'](
      { readingY: values.page && values.page !== previousPage ? 0 : window.scrollY },
      '',
      url,
    );
    setSearch(window.location.search);
    if (values.page && values.page !== previousPage) window.scrollTo({ top: 0, left: 0 });
  }, []);
  const value = useMemo(() => ({ params: new URLSearchParams(search), update }), [search, update]);
  return <Context.Provider value={value}>{children}</Context.Provider>;
}
export function useNavigation() {
  const value = useContext(Context);
  if (!value) throw new Error('NavigationProvider missing');
  return value;
}

// History entries carry reading position, not transient query data. Keep the
// document mounted during a same-page update; restore only actual Back/Forward.
export function useReadingPosition(ready: boolean) {
  const { params } = useNavigation();
  useEffect(() => {
    const previous = window.history.scrollRestoration;
    window.history.scrollRestoration = 'manual';
    let frame = 0;
    const save = () => {
      if (window.history.state?.readingRestore) return;
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        if (!window.history.state?.readingRestore)
          window.history.replaceState(
            { ...window.history.state, readingY: window.scrollY },
            '',
            window.location.href,
          );
      });
    };
    const restore = () => {
      window.history.replaceState(
        { ...window.history.state, readingRestore: true },
        '',
        window.location.href,
      );
    };
    window.addEventListener('scroll', save, { passive: true });
    window.addEventListener('popstate', restore);
    return () => {
      cancelAnimationFrame(frame);
      if (!window.history.state?.readingRestore)
        window.history.replaceState(
          { ...window.history.state, readingY: window.scrollY },
          '',
          window.location.href,
        );
      window.history.scrollRestoration = previous;
      window.removeEventListener('scroll', save);
      window.removeEventListener('popstate', restore);
    };
  }, []);
  useLayoutEffect(() => {
    const state = window.history.state;
    if (ready && state?.readingRestore) {
      window.scrollTo({ top: state.readingY || 0, left: 0 });
      window.history.replaceState({ ...state, readingRestore: false }, '', window.location.href);
    }
  }, [params, ready]);
}
