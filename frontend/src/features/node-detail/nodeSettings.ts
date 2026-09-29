// Node-level settings shared by the node settings dialog and its tests.
// Keep this module free of runtime imports: tests load it with node's
// built-in type stripping.
import type { NodeRecord } from '../../lib/contracts';

/**
 * Effective «Логи соединений HAProxy» setting. The Panel reports
 * `haproxy_logs`; older Panels only have metadata.haproxy_logs. Anything but
 * an explicit false means on (the default).
 */
export function nodeHAProxyLogs(node: Pick<NodeRecord, 'metadata'> & { haproxy_logs?: boolean }): boolean {
  if (typeof node.haproxy_logs === 'boolean') return node.haproxy_logs;
  return node.metadata?.haproxy_logs !== false;
}

/**
 * PUT /api/v1/nodes/{id} body. metadata is sent back unchanged apart from the
 * logging key (and haproxy_settings when given), so other node metadata
 * (ports, traffic limits) is preserved.
 */
export function nodeSettingsPayload(node: Pick<NodeRecord, 'metadata'>, name: string, address: string, haproxyLogs: boolean, haproxySettings?: HAProxyTuning) {
  return {
    name,
    address,
    metadata: { ...(node.metadata ?? {}), haproxy_logs: haproxyLogs, ...(haproxySettings ? { haproxy_settings: haproxySettings } : {}) },
    haproxy_logs: haproxyLogs,
  };
}

/** metadata.haproxy_settings; every field optional (= renderer default). */
export interface HAProxyTuning {
  max_connections?: number;
  threads?: number;
  timeout_connect?: string;
  timeout_client?: string;
  timeout_server?: string;
}

/** Form state of the «HAProxy» block: raw strings, empty = default. */
export interface HAProxyTuningForm {
  maxconn: string;
  nbthread: string;
  timeoutConnect: string;
  timeoutClient: string;
  timeoutServer: string;
}

const TIMEOUT_PATTERN = /^[1-9][0-9]{0,7}(ms|s|m|h)$/;

export function haproxyTuningForm(node: Pick<NodeRecord, 'metadata'>): HAProxyTuningForm {
  const raw = node.metadata?.haproxy_settings;
  const value = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>;
  const text = (key: string) => (typeof value[key] === 'string' ? value[key] as string : typeof value[key] === 'number' && value[key] ? String(value[key]) : '');
  return {
    maxconn: text('max_connections'),
    nbthread: text('threads'),
    timeoutConnect: text('timeout_connect'),
    timeoutClient: text('timeout_client'),
    timeoutServer: text('timeout_server'),
  };
}

/** Field errors (Russian) keyed like the form; empty object = valid. */
export function validateHAProxyTuning(form: HAProxyTuningForm): Partial<Record<keyof HAProxyTuningForm, string>> {
  const errors: Partial<Record<keyof HAProxyTuningForm, string>> = {};
  const integer = (key: 'maxconn' | 'nbthread', max: number) => {
    const value = form[key].trim();
    if (!value) return;
    if (!/^\d+$/.test(value) || Number(value) < 1 || Number(value) > max) errors[key] = `Целое число 1–${max.toLocaleString('ru-RU')}`;
  };
  integer('maxconn', 10_000_000);
  integer('nbthread', 256);
  for (const key of ['timeoutConnect', 'timeoutClient', 'timeoutServer'] as const) {
    const value = form[key].trim();
    if (value && !TIMEOUT_PATTERN.test(value)) errors[key] = 'Формат: 5s, 15m';
  }
  return errors;
}

/** metadata.haproxy_settings value: empty fields omitted, {} resets to defaults. */
export function haproxyTuningPayload(form: HAProxyTuningForm): HAProxyTuning {
  const out: HAProxyTuning = {};
  if (form.maxconn.trim()) out.max_connections = Number(form.maxconn.trim());
  if (form.nbthread.trim()) out.threads = Number(form.nbthread.trim());
  if (form.timeoutConnect.trim()) out.timeout_connect = form.timeoutConnect.trim();
  if (form.timeoutClient.trim()) out.timeout_client = form.timeoutClient.trim();
  if (form.timeoutServer.trim()) out.timeout_server = form.timeoutServer.trim();
  return out;
}
