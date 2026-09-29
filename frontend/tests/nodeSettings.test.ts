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

test('HAProxy tuning: validation, omitted empties, reset and metadata preservation', async () => {
  const { haproxyTuningForm, haproxyTuningPayload, validateHAProxyTuning } = await import('../src/features/node-detail/nodeSettings.ts');
  const node = { metadata: { agent_port: 4317, haproxy_settings: { max_connections: 5000, timeout_client: '30m' } } };
  const form = haproxyTuningForm(node);
  assert.deepEqual(form, { maxconn: '5000', nbthread: '', timeoutConnect: '', timeoutClient: '30m', timeoutServer: '' });
  assert.deepEqual(haproxyTuningPayload(form), { max_connections: 5000, timeout_client: '30m' });
  assert.deepEqual(haproxyTuningPayload({ maxconn: '', nbthread: '', timeoutConnect: '', timeoutClient: '', timeoutServer: '' }), {});
  assert.deepEqual(validateHAProxyTuning(form), {});
  assert.deepEqual(Object.keys(validateHAProxyTuning({ maxconn: '0', nbthread: '257', timeoutConnect: '5', timeoutClient: '05s', timeoutServer: '1d' })).sort(), ['maxconn', 'nbthread', 'timeoutClient', 'timeoutConnect', 'timeoutServer']);
  const payload = nodeSettingsPayload(node, 'edge', '192.0.2.1', true, { threads: 2 });
  assert.deepEqual(payload.metadata, { agent_port: 4317, haproxy_settings: { threads: 2 }, haproxy_logs: true });
});
