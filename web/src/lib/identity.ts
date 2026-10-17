import type { ClientIPHeader, IdentitySettings } from '../types';

export const MAX_IP_HEADERS = 16;

export function withClientIPHeaders(
  value: IdentitySettings,
  headers: ClientIPHeader[] | undefined,
): IdentitySettings {
  const next = { ...value };
  if (headers === undefined) delete next.client_ip_headers;
  else next.client_ip_headers = headers;
  return next;
}
