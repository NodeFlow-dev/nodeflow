import { Anchor, Code } from '@mantine/core';
import type { ReactNode } from 'react';

// Matches haproxy -c line references: "[haproxy.cfg:12]" or "line 12".
const LINE_REF = /(\[haproxy\.cfg:(\d+)\]|\bline (\d+)\b)/g;

/** Renders the haproxy -c excerpt reported by the Agent; line references reveal that line in the editor. */
export function HAProxyApplyErrorDetail({ detail, onRevealLine }: { detail?: string; onRevealLine: (line: number) => void }) {
  if (!detail) return null;
  const parts: ReactNode[] = [];
  let last = 0;
  for (const match of detail.matchAll(LINE_REF)) {
    const index = match.index ?? 0;
    const line = Number(match[2] ?? match[3]);
    parts.push(detail.slice(last, index));
    parts.push(
      <Anchor key={index} component="button" type="button" fz="inherit" ff="inherit" onClick={() => onRevealLine(line)}>
        {match[0]}
      </Anchor>,
    );
    last = index + match[0].length;
  }
  parts.push(detail.slice(last));
  return (
    <Code block mt="xs" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
      {parts}
    </Code>
  );
}
