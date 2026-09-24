import type { ReactNode } from 'react';

export interface SegmentTab<T extends string> {
  value: T;
  label: ReactNode;
  count?: number;
}

interface SegmentTabsProps<T extends string> {
  value: T;
  items: readonly SegmentTab<T>[];
  onChange: (value: T) => void;
  label: string;
  className?: string;
}

export function SegmentTabs<T extends string>({ value, items, onChange, label, className = '' }: SegmentTabsProps<T>) {
  return (
    <div className={`nf-segment-tabs ${className}`.trim()} role="group" aria-label={label}>
      {items.map((item) => (
        <button
          type="button"
          key={item.value}
          className={value === item.value ? 'is-active' : ''}
          onClick={() => onChange(item.value)}
          aria-pressed={value === item.value}
        >
          {item.label}{item.count !== undefined && <span>{item.count}</span>}
        </button>
      ))}
    </div>
  );
}
