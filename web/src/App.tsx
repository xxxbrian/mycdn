import { Component, lazy, Suspense } from 'react';
import type { ErrorInfo, ReactNode } from 'react';
import {
  Activity,
  ArrowUpRight,
  Braces,
  ChartNoAxesCombined,
  Check,
  Database,
  Globe2,
  Loader2,
  Menu,
  Save,
  SearchCode,
  Settings2,
  X,
} from 'lucide-react';
import { api } from './api';
import { ConfigProvider, useConfig } from './config';
import { NavigationProvider, useNavigation } from './lib/navigation';
import { useAsync } from './lib/useAsync';
import { Alert, Badge, Button, ConfirmButton, Loading, LoadError } from './components/ui';
import { useState } from 'react';

const Analytics = lazy(() => import('./pages/Analytics'));
const Sites = lazy(() => import('./pages/Sites'));
const Rules = lazy(() => import('./pages/Rules'));
const Cache = lazy(() => import('./pages/Cache'));
const Debug = lazy(() => import('./pages/Debug'));
const Settings = lazy(() => import('./pages/Settings'));
const navigation = [
  { id: 'analytics', label: 'Analytics', icon: ChartNoAxesCombined, number: '01' },
  { id: 'sites', label: 'Sites', icon: Globe2, number: '02' },
  { id: 'rules', label: 'Rules', icon: Braces, number: '03' },
  { id: 'cache', label: 'Cache', icon: Database, number: '04' },
  { id: 'debug', label: 'Debug', icon: SearchCode, number: '05' },
  { id: 'settings', label: 'Settings', icon: Settings2, number: '06' },
];

class ErrorBoundary extends Component<{ children: ReactNode }, { error?: string }> {
  state: { error?: string } = {};
  static getDerivedStateFromError(error: Error) {
    return { error: error.message };
  }
  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('mycdn UI render failure', error, info.componentStack);
  }
  render() {
    return this.state.error ? (
      <main className="main-content">
        <Alert>
          The console could not render this response: {this.state.error}. Your saved server
          configuration is unchanged.
        </Alert>
        <Button onClick={() => window.location.reload()}>Reload console</Button>
      </main>
    ) : (
      this.props.children
    );
  }
}
function Shell() {
  const { params, update } = useNavigation();
  const config = useConfig();
  const status = useAsync((signal) => api.status(signal), 'shell-status', 30000);
  const [menu, setMenu] = useState(false);
  const page = navigation.some((item) => item.id === params.get('page'))
    ? params.get('page')!
    : 'analytics';
  const editing = ['sites', 'rules', 'settings'].includes(page);
  const renderPage = () =>
    page === 'sites' ? (
      <Sites />
    ) : page === 'rules' ? (
      <Rules />
    ) : page === 'cache' ? (
      <Cache />
    ) : page === 'debug' ? (
      <Debug />
    ) : page === 'settings' ? (
      <Settings />
    ) : (
      <Analytics />
    );
  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-content">
        Skip to content
      </a>
      <header className="site-header">
        <div className="header-inner">
          <button
            className="brand"
            aria-label="mycdn analytics home"
            onClick={() => update({ page: 'analytics' })}
          >
            <span className="brand-mark">
              <Braces size={20} strokeWidth={2.4} />
            </span>
            <span>
              mycdn<span className="brand-divider">/</span>
              <span className="brand-console">console</span>
            </span>
          </button>
          <button
            className="mobile-menu icon-button button"
            aria-label="Toggle navigation"
            aria-expanded={menu}
            onClick={() => setMenu(!menu)}
          >
            {menu ? <X size={18} /> : <Menu size={18} />}
          </button>
          <nav className={menu ? 'primary-nav open' : 'primary-nav'} aria-label="Main navigation">
            {navigation.map((item) => (
              <a
                key={item.id}
                href={`?${new URLSearchParams({ ...Object.fromEntries(params), page: item.id })}`}
                onClick={(event) => {
                  if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
                  event.preventDefault();
                  update({ page: item.id });
                  setMenu(false);
                }}
                className={page === item.id ? 'active' : ''}
                aria-current={page === item.id ? 'page' : undefined}
              >
                <item.icon size={15} />
                <span>{item.label}</span>
              </a>
            ))}
          </nav>
          <div
            className="header-status"
            title={
              status.error || (status.data ? 'Admin API connected' : 'Connecting to admin API')
            }
          >
            <span
              className={`square-dot ${status.error ? 'error-dot' : status.data ? '' : 'muted-dot'}`}
            />
            <span>
              {status.error ? 'API UNAVAILABLE' : status.data ? 'LOCAL INSTANCE' : 'CONNECTING'}
            </span>
            <Activity size={13} />
          </div>
        </div>
      </header>
      <div className="context-bar">
        <div>
          <span className="context-label">SELF-HOSTED EDGE</span>
          <span className="context-separator">/</span>
          <span>{navigation.find((item) => item.id === page)?.label}</span>
        </div>
        <div>
          <span className="mono muted">{status.data?.version || 'mycdn'}</span>
          <Badge>ADMIN</Badge>
        </div>
      </div>
      <ErrorBoundary>
        <main id="main-content" className={`main-content ${editing ? 'with-save-bar' : ''}`}>
          {config.notice && <Alert tone="success">{config.notice}</Alert>}
          {status.data?.restart_required && page !== 'settings' && (
            <Alert
              tone="warning"
              action={
                <Button onClick={() => update({ page: 'settings' })}>
                  Runtime <ArrowUpRight size={12} />
                </Button>
              }
            >
              Saved startup settings require a restart
              {status.data.restart_fields?.length
                ? `: ${status.data.restart_fields.join(', ')}`
                : '.'}
            </Alert>
          )}
          {editing && config.loading ? (
            <Loading label="Loading configuration…" />
          ) : editing && config.error ? (
            <LoadError error={config.error} retry={config.reload} />
          ) : (
            <Suspense fallback={<Loading label="Loading console view…" />}>
              {editing ? (
                <form
                  id="config-editor"
                  onSubmit={(event) => {
                    event.preventDefault();
                    void config.save();
                  }}
                >
                  {renderPage()}
                </form>
              ) : (
                renderPage()
              )}
            </Suspense>
          )}
          {editing && config.saveError && (
            <div id="config-save-error" tabIndex={-1}>
              <Alert>{config.saveError}</Alert>
            </div>
          )}
          {editing && config.issues.length > 0 && (
            <Alert>
              <strong>Configuration validation failed</strong>
              <ul className="validation-issues">
                {config.issues.map((issue, index) => (
                  <li key={index}>
                    <code>{issue.path}</code> {issue.message}
                  </li>
                ))}
              </ul>
            </Alert>
          )}
        </main>
      </ErrorBoundary>
      {editing && config.draft && (
        <div className="save-bar">
          <div className="save-state">
            {config.saveError && (
              <button
                type="button"
                className="save-error-link"
                onClick={() => document.getElementById('config-save-error')?.focus()}
              >
                Save failed · view error
              </button>
            )}
            {config.dirty ? (
              <>
                <span className="square-dot" />
                <strong>Unsaved changes</strong>
                <span>Shared configuration draft</span>
              </>
            ) : (
              <>
                <Check size={15} />
                <strong>Configuration in sync</strong>
                <span>Routes & policy saved</span>
              </>
            )}
          </div>
          <div className="inline-actions">
            {config.dirty && (
              <ConfirmButton
                variant="default"
                disabled={config.saving}
                confirm="Discard draft"
                onConfirm={config.discard}
              >
                Discard
              </ConfirmButton>
            )}
            <Button
              onClick={() => {
                if (
                  !config.dirty ||
                  window.confirm('Reload server configuration and replace the local draft?')
                )
                  config.reload();
              }}
              disabled={config.saving}
            >
              Reload
            </Button>
            <Button
              variant="primary"
              disabled={!config.dirty || config.saving}
              onClick={() => void config.save()}
            >
              {config.saving ? <Loader2 size={14} className="spin" /> : <Save size={14} />}
              {config.saving ? 'Saving…' : 'Save configuration'}
            </Button>
          </div>
        </div>
      )}
      <footer className="site-footer">
        <span>
          <span className="footer-square" />
          MYCDN <span className="muted">/ CACHING REVERSE PROXY</span>
        </span>
        <span>
          <a href="/licenses.txt" target="_blank" rel="noreferrer">
            Asset licenses ↗
          </a>
        </span>
      </footer>
    </div>
  );
}
export default function App() {
  return (
    <NavigationProvider>
      <ConfigProvider>
        <Shell />
      </ConfigProvider>
    </NavigationProvider>
  );
}
