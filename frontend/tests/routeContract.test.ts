import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { gunzipSync, gzipSync } from 'node:zlib';
import { uiPayloadCases } from './routeMatrix.ts';

// Route payloads the editor can produce, replayed through the Go validator by
// internal/panel/route_ui_contract_test.go. Regenerate after changing the
// route model: UPDATE_UI_FIXTURES=1 npm test
const fixture = new URL('../../internal/panel/testdata/ui_route_payloads.json.gz', import.meta.url);

test('UI route payload fixture matches the current route model', () => {
  const current = JSON.stringify(uiPayloadCases());
  if (process.env.UPDATE_UI_FIXTURES === '1') {
    writeFileSync(fixture, gzipSync(current, { level: 9 }));
    return;
  }
  assert.ok(existsSync(fixture), 'fixture missing: run UPDATE_UI_FIXTURES=1 npm test');
  const stored = gunzipSync(readFileSync(fixture)).toString('utf8');
  assert.ok(stored === current, 'internal/panel/testdata/ui_route_payloads.json.gz is stale: run UPDATE_UI_FIXTURES=1 npm test');
});
