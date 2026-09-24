import type { APIErrorPayload, DNSResolveResult } from './contracts';

export class APIError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
  ) {
    super(message);
    this.name = 'APIError';
  }
}

export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json');

  let response: Response;
  try {
    response = await fetch(path, { ...init, headers, credentials: 'same-origin' });
  } catch {
    throw new APIError('Panel API недоступен. Проверьте соединение.', 0);
  }

  const payload = (await response.json().catch(() => null)) as T | APIErrorPayload | null;
  if (!response.ok) {
    if (response.status === 401 && path !== '/auth/login') {
      window.dispatchEvent(new Event('nodeflow:unauthorized'));
    }
    const body = payload as APIErrorPayload | null;
    const structured = typeof body?.error === 'object' ? body.error : null;
    const message = structured?.message ?? (typeof body?.error === 'string' ? body.error : `HTTP ${response.status}`);
    throw new APIError(message, response.status, structured?.code);
  }
  return payload as T;
}

export function isUnauthorized(error: unknown): boolean {
  return error instanceof APIError && error.status === 401;
}

/** Resolves a hostname on the panel host (suggestions for the preferred IP picker). */
export function resolveDNS(host: string): Promise<DNSResolveResult> {
  return api<DNSResolveResult>(`/api/v1/dns/resolve?host=${encodeURIComponent(host.trim())}`);
}
