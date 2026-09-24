/**
 * «Меньше соединений» splits new clients by weight ÷ cost, so a cost there is
 * just an inverse weight. The editor shows only «Вес» in that mode and folds
 * any stored cost into integer weights with the same ratios.
 */

export interface FoldServer {
  weight: number | '';
  cost: number | '';
  ipWeights: { ip: string; weight: number | ''; cost: number | '' }[];
}

const num = (v: number | '', fallback: number) => (v === '' || !(Number(v) > 0) ? fallback : Number(v));

/**
 * Returns servers with cost folded into weight (costs cleared), or null when
 * nothing sets a cost. Ratios are kept exactly when small integers allow it
 * (up to 256), else rounded like the HAProxy render (max weight 256).
 */
export function foldLeastConnCosts<T extends FoldServer>(servers: T[]): T[] | null {
  const hasCost = servers.some((s) => num(s.cost, 0) > 0 || s.ipWeights.some((w) => num(w.cost, 0) > 0));
  if (!hasCost) return null;
  const serverRatio = (s: FoldServer) => num(s.weight, 1) / num(s.cost, 1);
  const ipRatio = (s: FoldServer, w: FoldServer['ipWeights'][number]) =>
    num(w.weight === '' ? s.weight : w.weight, 1) / num(w.cost === '' ? s.cost : w.cost, 1);
  const ratios: number[] = [];
  servers.forEach((s) => {
    ratios.push(serverRatio(s));
    s.ipWeights.forEach((w) => ratios.push(ipRatio(s, w)));
  });
  const min = Math.min(...ratios);
  const max = Math.max(...ratios);
  let scale = 256 / max;
  for (let m = 1; m <= 256; m++) {
    const k = m / min;
    if (max * k > 256 + 1e-9) break;
    if (ratios.every((r) => Math.abs(r * k - Math.round(r * k)) < 1e-6 * Math.max(1, r * k))) { scale = k; break; }
  }
  const toWeight = (r: number) => Math.min(256, Math.max(1, Math.round(r * scale)));
  return servers.map((s) => {
    const weight = toWeight(serverRatio(s));
    return {
      ...s,
      weight: weight === 1 ? '' : weight,
      cost: '',
      ipWeights: s.ipWeights.map((w) => {
        const ipWeight = toWeight(ipRatio(s, w));
        return { ...w, weight: ipWeight === weight ? '' : ipWeight, cost: '' };
      }),
    };
  });
}
