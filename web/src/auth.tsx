import { createContext, useCallback, useContext, useEffect, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { Braces, Loader2, LogIn } from 'lucide-react';
import { api, errorMessage, onUnauthorized } from './api';
import { Alert, Button, Field, Input, Loading } from './components/ui';

const AuthContext = createContext<{ logout: () => Promise<void> } | null>(null);

function LoginFrame({ children }: { children: ReactNode }) {
  return (
    <main className="auth-page">
      <section className="auth-panel" aria-labelledby="login-title">
        <header className="auth-brand">
          <span className="brand-mark">
            <Braces size={20} strokeWidth={2.4} />
          </span>
          <span>
            mycdn<span className="brand-divider">/</span>
            <span className="muted">console</span>
          </span>
        </header>
        <div className="auth-body">
          <h1 id="login-title">Sign in</h1>
          {children}
        </div>
      </section>
    </main>
  );
}

export function Login({ onLogin }: { onLogin: () => void }) {
  const [key, setKey] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(undefined);
    const submitted = key;
    setKey('');
    try {
      await api.login(submitted);
      const session = await api.session();
      if (!session.authenticated) {
        throw new Error('Sign-in failed. Try again.');
      }
      onLogin();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <LoginFrame>
      <form method="post" onSubmit={(event) => void submit(event)} className="auth-form">
        <Field label="Management key">
          <Input
            type="password"
            name="management-key"
            autoComplete="current-password"
            autoCapitalize="none"
            spellCheck={false}
            required
            maxLength={4096}
            value={key}
            onChange={(event) => setKey(event.target.value)}
            disabled={busy}
            autoFocus
          />
        </Field>
        {error && <Alert>{error}</Alert>}
        <Button type="submit" variant="primary" disabled={busy || !key}>
          {busy ? <Loader2 size={15} className="spin" /> : <LogIn size={15} />}
          {busy ? 'Signing in...' : 'Sign in'}
        </Button>
      </form>
    </LoginFrame>
  );
}

export function AuthBoundary({ children }: { children: ReactNode }) {
  const [authenticated, setAuthenticated] = useState(false);
  const [checking, setChecking] = useState(true);
  const [error, setError] = useState<string>();
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    let expired = false;
    const unsubscribe = onUnauthorized(() => {
      expired = true;
      setAuthenticated(false);
    });
    setChecking(true);
    setError(undefined);
    api
      .session(controller.signal)
      .then((session) => {
        if (!controller.signal.aborted && !expired) setAuthenticated(session.authenticated);
      })
      .catch((err) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      })
      .finally(() => {
        if (!controller.signal.aborted) setChecking(false);
      });
    return () => {
      controller.abort();
      unsubscribe();
    };
  }, [revision]);
  const logout = useCallback(async () => {
    await api.logout();
    setAuthenticated(false);
  }, []);
  if (checking)
    return (
      <LoginFrame>
        <Loading />
      </LoginFrame>
    );
  if (error)
    return (
      <LoginFrame>
        <Alert>{error}</Alert>
        <Button onClick={() => setRevision((value) => value + 1)}>Retry</Button>
      </LoginFrame>
    );
  if (!authenticated) return <Login onLogin={() => setAuthenticated(true)} />;
  return <AuthContext.Provider value={{ logout }}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const auth = useContext(AuthContext);
  if (!auth) throw new Error('AuthBoundary missing');
  return auth;
}
