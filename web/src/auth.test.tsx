import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import App from './App';
import { Login } from './auth';

describe('management login boundary', () => {
  it('asks for one management key and no account', () => {
    const html = renderToStaticMarkup(<Login onLogin={() => {}} />);
    expect(html).toContain('Management key');
    expect(html).toContain('type="password"');
    expect(html).toContain('autoComplete="current-password"');
    expect(html).toContain('method="post"');
    expect(html).not.toContain('MYCDN_ADMIN_KEY');
    expect(html).toContain('Sign in');
    expect(html).not.toContain('type="email"');
    expect(html).not.toContain('name="username"');
  });
  it('limits visible copy to the brand and sign-in controls', () => {
    const html = renderToStaticMarkup(<Login onLogin={() => {}} />);
    const text = html
      .replace(/<[^>]*>/g, ' ')
      .replace(/\s+/g, ' ')
      .trim();
    expect(text).toBe('mycdn / console Sign in Management key Sign in');
  });
  it('does not mount the console or configuration editors before checking the session', () => {
    const html = renderToStaticMarkup(<App />);
    expect(html).toContain('role="status"');
    expect(html).not.toContain('Main navigation');
    expect(html).not.toContain('config-editor');
    expect(html).not.toContain('Save configuration');
  });
});
