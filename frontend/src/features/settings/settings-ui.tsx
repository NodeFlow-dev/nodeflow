/**
 * Settings page layout primitives (settings-local; the route editor has its own).
 *
 *   <CfgFields><CfgField id="panel-theme" label="Тема" hint="…">control</CfgField>…</CfgFields>
 *     Label above a short control; related fields share a row and wrap.
 *
 *   <CfgNumber unit="мин" … />
 *     Short numeric field (fits 4 digits) with the unit inside the field.
 *
 *   <StatusPill tone="accent|ok|warn|muted|info">Установлено</StatusPill>
 *     One badge shape for every status on the page (sentence case, 22 px).
 */
import { NumberInput, type NumberInputProps } from '@mantine/core';
import type { ReactNode } from 'react';

/**
 * Compact field: label above the control, hint under the label. Fields sit
 * side by side inside <CfgFields> and wrap when the card gets narrow, so a
 * short control never floats far away from its label.
 */
export function CfgField({ id, label, hint, children, group = false }: {
  id: string; label: ReactNode; hint?: ReactNode; children: ReactNode; group?: boolean;
}) {
  return (
    <div className="nf-cfg-field">
      <div className="nf-cfg-field__label">
        {group ? <span id={`${id}-label`}>{label}</span> : <label htmlFor={id} id={`${id}-label`}>{label}</label>}
        {hint && <small id={`${id}-hint`}>{hint}</small>}
      </div>
      {children}
    </div>
  );
}

export function CfgFields({ children }: { children: ReactNode }) {
  return <div className="nf-cfg-fields">{children}</div>;
}

export function CfgNumber({ unit, ...props }: NumberInputProps & { unit: string }) {
  return (
    <NumberInput
      {...props}
      className={`nf-cfg-number ${props.className ?? ''}`.trim()}
      hideControls
      allowDecimal={false}
      allowNegative={false}
      rightSection={<span className="nf-cfg-number__unit">{unit}</span>}
      rightSectionPointerEvents="none"
      rightSectionWidth={unit.length > 3 ? 64 : 48}
    />
  );
}

export type PillTone = 'accent' | 'ok' | 'warn' | 'danger' | 'info' | 'muted';

export function StatusPill({ tone = 'muted', children, title }: { tone?: PillTone; children: ReactNode; title?: string }) {
  return <span className={`nf-cfg-pill is-${tone}`} title={title}>{children}</span>;
}

/** Read-only value: monospace text, never a disabled input. */
export function CfgValue({ children, mono = true }: { children: ReactNode; mono?: boolean }) {
  return <span className={`nf-cfg-value${mono ? ' is-mono' : ''}`}>{children}</span>;
}

const dateFormat = new Intl.DateTimeFormat('ru-RU', { day: 'numeric', month: 'short', year: 'numeric' });
const dateTimeFormat = new Intl.DateTimeFormat('ru-RU', { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' });

export function formatDay(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? '—' : dateFormat.format(date);
}

export function formatDayTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? '—' : dateTimeFormat.format(date);
}
