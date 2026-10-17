export const number = (value: number | undefined, digits = 0) =>
  value === undefined || !Number.isFinite(value)
    ? '—'
    : new Intl.NumberFormat('en', { maximumFractionDigits: digits }).format(value);
export function bytes(value: number | undefined, suffix = ''): string {
  if (value === undefined || !Number.isFinite(value)) return '—';
  const unit = Math.min(Math.floor(Math.log(Math.max(1, Math.abs(value))) / Math.log(1024)), 4);
  return `${number(value / 1024 ** unit, unit ? 2 : 0)} ${['B', 'KiB', 'MiB', 'GiB', 'TiB'][unit]}${suffix}`;
}
export function bandwidth(value: number | undefined): string {
  if (value === undefined || !Number.isFinite(value)) return '—';
  const unit = Math.min(Math.floor(Math.log(Math.max(1, Math.abs(value))) / Math.log(1000)), 4);
  return `${number(value / 1000 ** unit, unit ? 2 : 0)} ${['bit/s', 'kbit/s', 'Mbit/s', 'Gbit/s', 'Tbit/s'][unit]}`;
}
export const percent = (value: number | undefined) =>
  value === undefined ? '—' : `${number(value * 100, 1)}%`;
export const milliseconds = (value: number | undefined) =>
  value === undefined ? '—' : `${number(value, 1)} ms`;
export const dateTime = (value: string | undefined) =>
  value && !Number.isNaN(Date.parse(value)) ? new Date(value).toLocaleString() : '—';
export function duration(seconds: number | undefined): string {
  if (seconds === undefined) return '—';
  if (seconds >= 86400) return `${number(seconds / 86400, 1)} d`;
  if (seconds >= 3600) return `${number(seconds / 3600, 1)} h`;
  if (seconds >= 60) return `${number(seconds / 60, 1)} min`;
  return `${number(seconds)} s`;
}
export const splitLines = (value: string) =>
  value
    .split(/[,\n]/)
    .map((v) => v.trim())
    .filter(Boolean);
export function uid(): string {
  if (typeof crypto.randomUUID === 'function') return crypto.randomUUID();
  // getRandomValues is available on private HTTP admin listeners, too.
  const value = crypto.getRandomValues(new Uint8Array(16));
  value[6] = (value[6] & 0x0f) | 0x40;
  value[8] = (value[8] & 0x3f) | 0x80;
  const hex = [...value].map((byte) => byte.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
