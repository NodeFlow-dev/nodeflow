/**
 * Route editor layout primitives.
 *
 * One spacing scale (4 / 8 / 12 / 16 / 24 px, the --nf-space-* tokens) and one
 * pattern per job:
 *
 *   <EditorSection index="2" title="Назначение" summary="2 сервера · пул">…</EditorSection>
 *     Numbered form section: header (index · title · one-line state summary ·
 *     optional aside) and a body with a 16 px rhythm between blocks.
 *
 *   <SettingsGrid>
 *     <SettingRow id="dist-tolerance" label="Толерантность" hint="Кто медленнее…" aside="≈ 12 мс">
 *       <UnitNumberInput id="dist-tolerance" unit="%" … />
 *     </SettingRow>
 *     <SettingsDivider />
 *     <SettingsNote>Задержку меряет проверка здоровья…</SettingsNote>
 *   </SettingsGrid>
 *     Flowing field grid: every row is a compact cell (label above, control,
 *     one-line muted hint under the control); cells sit side by side and wrap
 *     on narrow widths. Short controls keep their own width; `grow` cells
 *     (domains, address lists) take the rest of the line; `inline` puts the
 *     control on the label line (switches). `aside` is a muted live hint to the
 *     right of the control (derived values: memory, effective defaults).
 *     `id` must equal the control's id: the row wires <label htmlFor>,
 *     `${id}-label` and `${id}-hint` (use them in aria-labelledby/-describedby).
 *     `error` renders a message under the control.
 *
 *   <OptionList><OptionRow title hint control open>{expanded}</OptionRow></OptionList>
 *     Toggle/option card: title + hint with the switch right next to the
 *     title (never at the far edge); expanded content underneath.
 *
 *   <UnitNumberInput unit="мс" value onValue … />
 *     Numeric input with the unit inside the field (right), fixed width that
 *     fits 6 digits; clearing while typing is allowed (see DraftNumberInput).
 */
import { Collapse, NumberInput, type NumberInputProps } from '@mantine/core';
import { IconInfoCircle } from '@tabler/icons-react';
import { useState, type ReactNode } from 'react';

// ─── Plural helper (ru) ─────────────────────────────────────────────────────────
/** plural(3, ['сервер', 'сервера', 'серверов']) → 'сервера' */
export function plural(count: number, forms: readonly [string, string, string]): string {
  const n = Math.abs(count) % 100;
  const last = n % 10;
  if (n > 10 && n < 20) return forms[2];
  if (last > 1 && last < 5) return forms[1];
  if (last === 1) return forms[0];
  return forms[2];
}

// ─── Section ────────────────────────────────────────────────────────────────────
interface EditorSectionProps {
  id: string;
  index?: string;
  title: string;
  summary?: ReactNode;
  aside?: ReactNode;
  children: ReactNode;
  className?: string;
}

export function EditorSection({ id, index, title, summary, aside, children, className = '' }: EditorSectionProps) {
  return (
    <section className={`nf-re-section ${className}`.trim()} aria-labelledby={`${id}-title`}>
      <header className="nf-re-section__head">
        {index && <span className="nf-re-section__index" aria-hidden="true">{index}</span>}
        <h2 id={`${id}-title`}>{title}</h2>
        {summary && <span className="nf-re-section__summary">{summary}</span>}
        {aside && <span className="nf-re-section__aside">{aside}</span>}
      </header>
      <div className="nf-re-section__body">{children}</div>
    </section>
  );
}

/** Sub-heading inside a section body: title + one-line hint on the same baseline. `stableHint` reserves two lines for a hint whose text varies. */
export function BlockHead({ title, hint, id, stableHint = false }: { title: string; hint?: ReactNode; id?: string; stableHint?: boolean }) {
  return (
    <div className={`nf-re-block-head${stableHint ? ' has-stable-hint' : ''}`}>
      <h3 id={id}>{title}</h3>
      {hint && <p title={stableHint && typeof hint === 'string' ? hint : undefined}>{hint}</p>}
    </div>
  );
}

// ─── Settings grid ──────────────────────────────────────────────────────────────
/**
 * Layout stability: every grid uses fixed tracks (never content-sized flow), so
 * a control that appears, disappears or changes its text never moves its
 * neighbours sideways. `layout="fixed"` — equal auto-fill columns whose count
 * depends only on the available width; `layout="<name>"` — a named
 * grid-template-areas layout from route-editor.css (cells set `area`).
 */
export function SettingsGrid({ children, className = '', layout = 'fixed', ...rest }: { children: ReactNode; className?: string; layout?: 'fixed' | 'connection' } & Record<`data-${string}`, string>) {
  return <div className={`nf-setgrid nf-setgrid--${layout} ${className}`.trim()} {...rest}>{children}</div>;
}

interface SettingRowProps {
  id: string;
  label: ReactNode;
  hint?: ReactNode;
  /** Muted live hint to the right of the control (derived value, effective default). */
  aside?: ReactNode;
  error?: ReactNode;
  children: ReactNode;
  /** Label is not a <label> (control is a group: segmented, chips). */
  group?: boolean;
  field?: string;
  /** Takes the rest of the line (domains, address lists). */
  grow?: boolean;
  /** Control on the label line (switches). */
  inline?: boolean;
  /** Named grid area (layouts with grid-template-areas). */
  area?: string;
  /** Spans two columns of a fixed grid. */
  wide?: boolean;
  /** Hint text varies with the value: reserve two lines so the row height never changes. */
  stableHint?: boolean;
  /** Placeholder for a setting that does not apply now: dimmed, keeps its slot. */
  inactive?: boolean;
}

export function SettingRow({ id, label, hint, aside, error, children, group = false, field, grow = false, inline = false, area, wide = false, stableHint = false, inactive = false }: SettingRowProps) {
  const classes = ['nf-setting-row', grow && 'is-grow', inline && 'is-inline', wide && 'is-wide', stableHint && 'has-stable-hint', inactive && 'is-inactive'].filter(Boolean).join(' ');
  return (
    <div className={classes} data-field={field} style={area ? { gridArea: area } : undefined}>
      <div className="nf-setting-row__label">
        {group
          ? <span id={`${id}-label`}>{label}</span>
          : <label htmlFor={id} id={`${id}-label`}>{label}</label>}
      </div>
      <div className="nf-setting-row__control">
        <div className="nf-setting-row__line">
          {children}
          {aside && <span className="nf-setting-row__aside" id={`${id}-aside`} title={typeof aside === 'string' ? aside : undefined}>{aside}</span>}
        </div>
      </div>
      {/* One slot for hint and error: an appearing error replaces the hint instead of adding a line. */}
      <small
        className={`nf-setting-row__hint${error ? ' is-error' : ''}`}
        id={`${id}-hint`}
        role={error ? 'alert' : undefined}
      >
        {error ?? hint}
      </small>
    </div>
  );
}

export function SettingsDivider() {
  return <div className="nf-setgrid__divider" aria-hidden="true" />;
}

export function SettingsNote({ children, tone = 'info' }: { children: ReactNode; tone?: 'info' | 'warning' }) {
  return (
    <p className={`nf-re-note${tone === 'warning' ? ' is-warning' : ''} nf-setgrid__note`}>
      <IconInfoCircle size={14} aria-hidden="true" />
      <span>{children}</span>
    </p>
  );
}

/** Standalone muted note (outside a grid). */
export function Note({ children, tone = 'info', icon }: { children: ReactNode; tone?: 'info' | 'warning'; icon?: ReactNode }) {
  return (
    <p className={`nf-re-note${tone === 'warning' ? ' is-warning' : ''}`}>
      {icon ?? <IconInfoCircle size={14} aria-hidden="true" />}
      <span>{children}</span>
    </p>
  );
}

// ─── Option rows ────────────────────────────────────────────────────────────────
export function OptionList({ children, cards = false }: { children: ReactNode; cards?: boolean }) {
  return <div className={`nf-re-options${cards ? ' is-cards' : ''}`}>{children}</div>;
}

interface OptionRowProps {
  title: string;
  hint?: ReactNode;
  control: ReactNode;
  children?: ReactNode;
  open?: boolean;
  disabled?: boolean;
  field?: string;
}

/** Title + one-line hint in the label column, control in the control column, expanded details underneath. */
export function OptionRow({ title, hint, control, children, open = false, disabled = false, field }: OptionRowProps) {
  return (
    <div className={`nf-re-option${open ? ' is-open' : ''}${disabled ? ' is-disabled' : ''}`} data-field={field}>
      <div className="nf-re-option__main">
        <div className="nf-re-option__text">
          <strong>{title}</strong>
          {hint && <p>{hint}</p>}
        </div>
        <div className="nf-re-option__control">{control}</div>
      </div>
      {children && (
        <Collapse in={open}>
          <div className="nf-re-option__body">{children}</div>
        </Collapse>
      )}
    </div>
  );
}

// ─── Numeric inputs ─────────────────────────────────────────────────────────────
/**
 * NumberInput that lets the operator clear the field while typing even when
 * the parent maps '' back to a default: the local text wins while focused and
 * resyncs to the stored value on blur.
 */
export function DraftNumberInput({ value, onValue, ...rest }: Omit<NumberInputProps, 'value' | 'onChange'> & {
  value: number | '';
  onValue: (value: number | '') => void;
}) {
  const [text, setText] = useState<number | string>(value);
  const [focused, setFocused] = useState(false);
  return (
    <NumberInput
      {...rest}
      value={focused ? text : value}
      onFocus={(event) => { setFocused(true); setText(value); rest.onFocus?.(event); }}
      onBlur={(event) => { setFocused(false); rest.onBlur?.(event); }}
      onChange={(next) => {
        setText(next);
        if (next === '') onValue('');
        else if (typeof next === 'number' && Number.isFinite(next)) onValue(next);
      }}
    />
  );
}

type UnitNumberInputProps = Omit<NumberInputProps, 'value' | 'onChange'> & {
  value: number | '';
  onValue: (value: number | '') => void;
  /** Unit shown inside the field on the right: %, мс, с, клиентов, Мбит/с. */
  unit?: string;
  /** Wider field for 7+ digit values. */
  wide?: boolean;
};

export function UnitNumberInput({ unit, wide = false, className = '', ...rest }: UnitNumberInputProps) {
  const unitWidth = unit ? Math.max(28, unit.length * 7 + 16) : undefined;
  return (
    <DraftNumberInput
      hideControls
      size="sm"
      {...rest}
      className={`nf-num${wide ? ' nf-num--wide' : ''} ${className}`.trim()}
      rightSection={unit ? <span className="nf-num__unit" aria-hidden="true">{unit}</span> : undefined}
      rightSectionWidth={unitWidth}
      rightSectionPointerEvents="none"
    />
  );
}
