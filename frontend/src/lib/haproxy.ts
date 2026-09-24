import type { HAProxyServerStats } from './contracts';

const unassignedServerAddress = /^(?:-|0\.0\.0\.0(?::0)?|\[?::\]?(?::0)?)$/;

// HAProxy exposes every server-template slot in runtime stats. Slots that DNS
// has not assigned yet use an empty/wildcard address and are capacity, not
// actual backend members. Older Agents did not report address, so preserve
// their status rows for backwards compatibility.
export function isAssignedHAProxyServer(server: HAProxyServerStats) {
  const status = String(server.status ?? '').trim().toUpperCase();
  if (/^MAINT\s*\(RESOLUTION\)$/.test(status)) return false;
  if (server.address === undefined) return true;
  const address = server.address.trim().toLowerCase();
  return address !== '' && !unassignedServerAddress.test(address);
}
