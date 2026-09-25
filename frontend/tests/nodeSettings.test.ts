// Run: npm test (node --test with built-in TypeScript type stripping).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { nodeHAProxyLogs, nodeSettingsPayload } from '../src/features/node-detail/nodeSettings.ts';

test('HAProxy connection logs default to on', () => {
  assert.equal(nodeHAProxyLogs({ metadata: null }), true);
  assert.equal(nodeHAProxyLogs({ metadata: {} }), true);
  assert.equal(nodeHAProxyLogs({ metadata: { haproxy_logs: 'false' } }), true);
  assert.equal(nodeHAProxyLogs({ metadata: { haproxy_logs: false } }), false);
  assert.equal(nodeHAProxyLogs({ metadata: {}, haproxy_logs: false }), false);
  assert.equal(nodeHAProxyLogs({ metadata: { haproxy_logs: false }, haproxy_logs: true }), true);
});

test('settings payload keeps other metadata and sends the toggle', () => {
  const node = { metadata: { agent_port: 4317, ssh_port: 2222, traffic_limit_bytes: 10 } };
  assert.deepEqual(nodeSettingsPayload(node, 'edge', '192.0.2.1', false), {
    name: 'edge',
    address: '192.0.2.1',
    metadata: { agent_port: 4317, ssh_port: 2222, traffic_limit_bytes: 10, haproxy_logs: false },
    haproxy_logs: false,
  });
  assert.deepEqual(node.metadata, { agent_port: 4317, ssh_port: 2222, traffic_limit_bytes: 10 });
  assert.equal(nodeSettingsPayload({ metadata: null }, 'a', '192.0.2.2', true).metadata.haproxy_logs, true);
});
