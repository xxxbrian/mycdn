import { cp, mkdir, readdir, rm } from 'node:fs/promises';

const target = new URL('../internal/admin/dist/', import.meta.url);
await mkdir(target, { recursive: true });
// Only generated embedded assets are removed; retain the tracked build marker.
for (const entry of await readdir(target)) {
  if (entry !== '.keep') await rm(new URL(entry, target), { recursive: true });
}
await cp(new URL('../web/dist/', import.meta.url), target, { recursive: true });
