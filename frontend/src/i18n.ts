/**
 * Lightweight UI localization: typed flat dictionaries (locales/ru.ts is the
 * source of truth for keys, locales/en.ts must have exactly the same keys) and
 * a module-level `t()` usable from components and plain modules alike.
 *
 * The language is resolved once per page load (localStorage → browser
 * languages → ru). Switching the language persists the choice and reloads the
 * page, so module-level label tables built with `t()` are always consistent.
 * Missing English entries fall back to Russian.
 */
import { en } from './locales/en';
import { ru, type MessageKey, type Message } from './locales/ru';

export type Lang = 'ru' | 'en';
export type { MessageKey };
export const LANGS: readonly Lang[] = ['ru', 'en'];
export const LANG_STORAGE_KEY = 'nodeflow.lang';

const dictionaries: Record<Lang, Partial<Record<MessageKey, Message>>> = { ru, en };

function storage(): Storage | null {
  try { return typeof localStorage === 'undefined' ? null : localStorage; } catch { return null; }
}

export function detectLang(stored: string | null | undefined, browser: readonly string[] = []): Lang {
  if (stored === 'ru' || stored === 'en') return stored;
  for (const tag of browser) {
    const base = tag.toLowerCase().split('-')[0];
    if (base === 'ru' || base === 'en') return base;
  }
  return 'ru';
}

function initialLang(): Lang {
  // Only a real browser contributes navigator languages (Node test runs stay ru).
  const nav = typeof window === 'undefined' || typeof navigator === 'undefined' ? [] : navigator.languages?.length ? navigator.languages : [navigator.language];
  return detectLang(storage()?.getItem(LANG_STORAGE_KEY), nav.filter(Boolean));
}

let current: Lang = initialLang();
if (typeof document !== 'undefined') document.documentElement.lang = current;

export const getLang = (): Lang => current;
/** BCP 47 locale for Intl number/date formatting. */
export const locale = (): string => (current === 'en' ? 'en-US' : 'ru-RU');

/** Persists the language and reloads so every label table is rebuilt. */
export function setLang(lang: Lang, reload = true): void {
  storage()?.setItem(LANG_STORAGE_KEY, lang);
  if (lang === current) return;
  current = lang;
  if (typeof document !== 'undefined') document.documentElement.lang = lang;
  if (reload && typeof window !== 'undefined') window.location.reload();
}

export type Params = Record<string, string | number | null | undefined>;

const pluralRules: Partial<Record<Lang, Intl.PluralRules>> = {};
function pick(message: Message, lang: Lang, params?: Params): string {
  if (typeof message === 'string') return message;
  const count = Number(params?.count ?? 0);
  const rules = (pluralRules[lang] ??= new Intl.PluralRules(lang === 'en' ? 'en-US' : 'ru-RU'));
  return message[rules.select(count)] ?? message.other ?? message.many ?? Object.values(message)[0] ?? '';
}

export function translate(lang: Lang, key: MessageKey, params?: Params): string {
  const message = dictionaries[lang][key] ?? ru[key];
  if (message === undefined) return key;
  const text = pick(message, dictionaries[lang][key] === undefined ? 'ru' : lang, params);
  return params ? text.replace(/\{(\w+)\}/g, (match, name: string) => (params[name] == null ? match : String(params[name]))) : text;
}

/** Translate a key in the current UI language; `{name}` placeholders take params, `count` selects plural forms. */
export const t = (key: MessageKey, params?: Params): string => translate(current, key, params);

/** True when `key` exists in the dictionaries (dynamic keys such as API error codes). */
export const hasKey = (key: string): key is MessageKey => Object.prototype.hasOwnProperty.call(ru, key);

/**
 * Localized text for a backend error code; unknown codes keep the server's
 * message (the backend stays the source of detail for validation errors).
 */
export function apiErrorText(code: string | undefined, serverMessage: string): string {
  const key = `apiError.${code ?? ''}`;
  return code && hasKey(key) ? t(key) : serverMessage;
}

/** Hook form for components; the language is fixed per page load, so this is just `t`. */
export const useT = (): typeof t => t;
