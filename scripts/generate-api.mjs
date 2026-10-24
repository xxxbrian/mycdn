// Generate the browser contract from the same typed API the Go server exposes.
// Usage: node scripts/generate-api.mjs [openapi-url]
// Remote export uses MYCDN_ADMIN_KEY and a temporary, revoked login session.
import { spawnSync } from 'node:child_process';
import { mkdir, writeFile } from 'node:fs/promises';

const directory = new URL('../web/src/generated/', import.meta.url);
await mkdir(directory, { recursive: true });
const schema = new URL('openapi.json', directory);
if (process.argv[2]) {
  const url = new URL(process.argv[2]);
  const key = process.env.MYCDN_ADMIN_KEY;
  if (!key) throw new Error('Set MYCDN_ADMIN_KEY for authenticated remote OpenAPI export.');
  const login = await fetch(new URL('/api/auth/login', url), {
    method: 'POST',
    redirect: 'error',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ key }),
  });
  if (!login.ok) throw new Error(`Management login failed: ${login.status}`);
  const cookie = login.headers.getSetCookie().map((value) => value.split(';', 1)[0]).join('; ');
  try {
    const response = await fetch(url, { headers: { Cookie: cookie }, redirect: 'error' });
    if (!response.ok) throw new Error(`OpenAPI request failed: ${response.status}`);
    await writeFile(schema, await response.text());
  } finally {
    await fetch(new URL('/api/auth/logout', url), {
      method: 'POST', headers: { Cookie: cookie }, redirect: 'error',
    }).catch(() => {});
  }
} else {
  const result = spawnSync('go', ['run', './cmd/mycdn', '-openapi'], {
    encoding: 'utf8',
    cwd: new URL('../', import.meta.url),
    env: {
      ...process.env,
      ...(process.platform === 'darwin' ? { CC: '/usr/bin/clang', CXX: '/usr/bin/clang++' } : {}),
    },
  });
  if (result.status !== 0) throw new Error(result.stderr || 'OpenAPI generation failed');
  JSON.parse(result.stdout);
  await writeFile(schema, result.stdout);
}
const result = spawnSync(
  'npx',
  ['--yes', 'openapi-typescript@7.13.0', schema.pathname, '-o', new URL('api.d.ts', directory).pathname],
  { stdio: 'inherit' },
);
if (result.status !== 0) process.exitCode = result.status || 1;
