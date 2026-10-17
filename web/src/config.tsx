import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import { api, ApiError, errorMessage } from './api';
import type { Capabilities, Configuration, ValidationIssue } from './types';
import { useAsync } from './lib/useAsync';

interface ConfigState {
  draft?: Configuration;
  change: (update: (config: Configuration) => Configuration) => void;
  loading: boolean;
  dirty: boolean;
  saving: boolean;
  error?: string;
  saveError?: string;
  notice?: string;
  issues: ValidationIssue[];
  capabilities?: Capabilities;
  capabilitiesError?: string;
  save: () => Promise<void>;
  reload: () => void;
  discard: () => void;
}
const Context = createContext<ConfigState | null>(null);
export function ConfigProvider({ children }: { children: ReactNode }) {
  const remote = useAsync((signal) => api.config(signal), 'config');
  const capabilities = useAsync((signal) => api.capabilities(signal), 'capabilities');
  const [draft, setDraft] = useState<Configuration>();
  const [base, setBase] = useState<Configuration>();
  const [etag, setEtag] = useState('');
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string>();
  const [issues, setIssues] = useState<ValidationIssue[]>([]);
  const [notice, setNotice] = useState<string>();
  useEffect(() => {
    if (remote.data) {
      setDraft(remote.data.data);
      setBase(remote.data.data);
      setEtag(remote.data.etag);
      setSaveError(undefined);
      setIssues([]);
    }
  }, [remote.data]);
  const dirty = useMemo(() => JSON.stringify(draft) !== JSON.stringify(base), [draft, base]);
  useEffect(() => {
    if (!dirty) return;
    const warn = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [dirty]);
  const change = useCallback((update: (config: Configuration) => Configuration) => {
    setDraft((current) => (current ? update(current) : current));
    setNotice(undefined);
  }, []);
  async function save() {
    if (!draft || saving) return;
    if (!etag) {
      setSaveError(
        'The API did not return an ETag. Saving without concurrency protection is disabled.',
      );
      return;
    }
    const form = document.getElementById('config-editor') as HTMLFormElement | null;
    if (form && !form.reportValidity()) return;
    setSaving(true);
    setSaveError(undefined);
    setIssues([]);
    try {
      const result = await api.saveConfig(draft, etag);
      // Keep edits made while the save was in flight as a new local draft.
      setDraft((current) =>
        JSON.stringify(current) === JSON.stringify(draft) ? result.data : current,
      );
      setBase(result.data);
      setEtag(result.etag);
      setNotice('Configuration saved.');
    } catch (error) {
      setSaveError(
        error instanceof ApiError && (error.status === 409 || error.status === 412)
          ? 'Configuration changed on the server. Export your draft before reloading; no changes were overwritten.'
          : errorMessage(error),
      );
      if (error instanceof ApiError) setIssues(error.issues);
    } finally {
      setSaving(false);
    }
  }
  return (
    <Context.Provider
      value={{
        draft,
        change,
        loading: remote.loading,
        dirty,
        saving,
        error: remote.error,
        saveError,
        notice,
        issues,
        capabilities: capabilities.data,
        capabilitiesError: capabilities.error,
        save,
        reload: remote.reload,
        discard: () => {
          setDraft(base);
          setSaveError(undefined);
          setIssues([]);
          setNotice(undefined);
        },
      }}
    >
      {children}
    </Context.Provider>
  );
}
export function useConfig() {
  const value = useContext(Context);
  if (!value) throw new Error('ConfigProvider missing');
  return value;
}
