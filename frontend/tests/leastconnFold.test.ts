import { test } from 'node:test';
import assert from 'node:assert/strict';
import { foldLeastConnCosts } from '../src/features/routes/leastconnFold.ts';

const ip = (addr: string, weight: number | '', cost: number | '') => ({ ip: addr, weight, cost });

test('no cost: nothing to fold', () => {
  assert.equal(foldLeastConnCosts([{ weight: 2, cost: '', ipWeights: [ip('192.0.2.1', 3, '')] }]), null);
});

test('per-IP cost 2 becomes half the weight of the other addresses', () => {
  // Production shape: DNS pool, one IP with cost 2, the rest inherit 1/1.
  const out = foldLeastConnCosts([{ weight: '', cost: '', ipWeights: [ip('31.77.147.33', '', 2)] }])!;
  assert.deepEqual(out[0], { weight: 2, cost: '', ipWeights: [ip('31.77.147.33', '', '')].map((w) => ({ ...w, weight: 1 })) });
});

test('ratios weight/cost are preserved with small integers', () => {
  const out = foldLeastConnCosts([
    { weight: '', cost: 2, ipWeights: [] }, // 0.5
    { weight: '', cost: '', ipWeights: [] }, // 1
    { weight: 2, cost: 0.5, ipWeights: [] }, // 4
  ])!;
  assert.deepEqual(out.map((s) => s.weight), ['', 2, 8]);
  assert.ok(out.every((s) => s.cost === ''));
});

test('non-integer ratios fall back to the render scale (max 256)', () => {
  const out = foldLeastConnCosts([{ weight: '', cost: 3, ipWeights: [] }, { weight: '', cost: 1.37, ipWeights: [] }])!;
  const [a, b] = out.map((s) => (s.weight === '' ? 1 : s.weight));
  assert.equal(b, 256);
  assert.ok(Math.abs(a / b - 1.37 / 3) < 0.01);
});
