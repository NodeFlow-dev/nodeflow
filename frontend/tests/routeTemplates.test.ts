import { test } from 'node:test';
import assert from 'node:assert/strict';
import { model } from './routeMatrix.ts';
const { templateDraft } = await import('../src/features/routes/templateModel.ts');

test('template intent opens in the route editor without deployment identity', () => {
  const draft = model.emptyRouteDraft();
  draft.name = 'Reusable';
  draft.snis = ['route.example.com'];
  draft.listenerPort = 8443;
  draft.servers = [
    { ...model.emptyServerDraft('primary'), _key: 'a', host: 'pool.example.com', dnsPool: true, weight: 2 },
    { ...model.emptyServerDraft('secondary'), _key: 'b', host: '192.0.2.20', weight: 3 },
  ];
  draft.stickyMode = 'source_table';
  draft.stickyTTL = '24h';
  draft.balanceAlgorithm = 'leastconn';
  draft.stickyTableEntries = '200k';
  draft.stickyIPv6Prefix = 96;
  draft.clientBandwidthEnabled = true;
  draft.clientUploadMbps = 10;
  const config = model.routePayload(draft, false);
  const loaded = templateDraft({ id: 'template', name: 'Reusable', description: '', version: 1, config });
  assert.deepEqual(model.routePayload(loaded, false), config);
  assert.equal(loaded.name, draft.name);
  assert.deepEqual(model.validateRouteDraft(loaded, []), []);
});
