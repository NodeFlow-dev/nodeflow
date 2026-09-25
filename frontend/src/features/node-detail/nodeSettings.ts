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
 * logging key, so other node metadata (ports, traffic limits) is preserved.
 */
export function nodeSettingsPayload(node: Pick<NodeRecord, 'metadata'>, name: string, address: string, haproxyLogs: boolean) {
  return {
    name,
    address,
    metadata: { ...(node.metadata ?? {}), haproxy_logs: haproxyLogs },
    haproxy_logs: haproxyLogs,
  };
}
