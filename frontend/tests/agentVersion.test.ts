// Run: npm test (node --test with built-in TypeScript type stripping).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { agentVersionAtLeast } from '../src/lib/agentVersion.ts';

const gates = ['1.1.0', '1.1.1', '1.1.3'];

test('2.x Agents pass every capability gate (numeric, not string compare)', () => {
  for (const version of ['2.0.0', 'v2.0.0', '2.0.0-rc1', '2.0.0+build.7', '2.1.0', '10.0.0', '1.10.0']) {
    for (const gate of gates) assert.equal(agentVersionAtLeast(version, gate), true, `${version} >= ${gate}`);
  }
});

test('older Agents are gated', () => {
  assert.equal(agentVersionAtLeast('1.0.5', '1.1.0'), false);
  assert.equal(agentVersionAtLeast('1.1.0', '1.1.1'), false);
  assert.equal(agentVersionAtLeast('1.1.2', '1.1.3'), false);
  assert.equal(agentVersionAtLeast('1.1.3', '1.1.3'), true);
  assert.equal(agentVersionAtLeast('0.99.99', '1.1.0'), false);
});

test('unknown versions defer to the Panel API', () => {
  for (const version of [undefined, null, '', 'dev', '1.1']) assert.equal(agentVersionAtLeast(version, '1.1.3'), true);
});
