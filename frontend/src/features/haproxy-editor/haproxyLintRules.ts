// Keyword-level checks of the offline linter. Mirrors
// internal/panel/haproxy_lint_checks.go: both read haproxyKeywords.json (the Go
// side embeds a byte-identical copy) and must report the same codes.
import KEYWORDS from './haproxyKeywords.json' with { type: 'json' };

export interface RuleWord { text: string; start: number; end: number; quoted: boolean }
export type RuleEmit = (word: RuleWord, code: string, message: string, severity?: 'error' | 'warning') => void;

interface KeywordData {
  global: { keywords: string[]; prefixes: string[]; negatable: string[] };
  proxy: Record<string, string>;
  proxy_sub: Record<string, string>;
  proxy_strict_sub: string[];
  proxy_negatable: string[];
  options: Record<string, string>;
  options_no_negation: string[];
  option_args: Record<string, string>;
  server: Record<string, number>;
  bind: Record<string, number>;
  timeouts: Record<string, string[]>;
  modes: Record<string, string>;
  balance: string[];
  hash_type: { methods: string[]; functions: string[]; modifiers: string[] };
  log: { facilities: string[]; levels: string[]; options: string[] };
  other_sections: Record<string, string[]>;
}

const K = KEYWORDS as unknown as KeywordData;
const set = (words: string[]) => new Set(words);
const GLOBAL = set(K.global.keywords);
const GLOBAL_NEGATABLE = set(K.global.negatable);
const STRICT_SUB = set(K.proxy_strict_sub);
const OPTIONS_NO_NEG = set(K.options_no_negation);
const TIMEOUTS = Object.fromEntries(Object.entries(K.timeouts).map(([k, v]) => [k, set(v)]));
const OTHER = Object.fromEntries(Object.entries(K.other_sections).map(([k, v]) => [k, set(v)]));
const BALANCE = set(K.balance);
const HASH = [set(K.hash_type.methods), set(K.hash_type.functions), set(K.hash_type.modifiers)];
const LOG_FACILITIES = set(K.log.facilities);
const LOG_LEVELS = set(K.log.levels);
const LOG_OPTIONS = set(K.log.options);
const DURATION_RE = /^\d+(us|ms|s|m|h|d)?$/;
const SERVER_DURATION = set(['inter', 'fastinter', 'downinter', 'slowstart', 'agent-inter', 'pool-purge-delay']);
const FREE_TEXT = set([
  'acl', 'bind', 'capture', 'description', 'email-alert', 'error-log-format', 'errorfile', 'errorfiles',
  'errorloc', 'errorloc302', 'errorloc303', 'filter', 'http-after-response', 'http-check', 'http-error',
  'http-request', 'http-response', 'log-format', 'log-format-sd', 'log-tag', 'monitor-uri', 'node',
  'presetenv', 'redirect', 'set-var', 'set-var-fmt', 'setenv', 'stats', 'tcp-check', 'tcp-request',
  'tcp-response', 'unique-id-format', 'unique-id-header', 'use_backend', 'default_backend', 'use-server',
  'stick', 'balance', 'command', 'user', 'group', 'lua-load', 'lua-load-per-thread', 'lua-prepend-path',
  'hash-type', 'server', 'server-template', 'default-server', 'option', 'timeout', 'mode', 'log',
]);

/** Sections the linter knows; hover docs only cover SECTION_KEYWORDS. */
export const LINT_SECTIONS = new Set(['global', 'defaults', 'frontend', 'backend', 'listen', 'resolvers', 'peers', 'userlist', 'cache', 'program', 'http-errors', 'ring', 'log-forward', 'mailers', 'crt-store', 'traces', 'acme']);

const has = (o: Record<string, unknown>, k: string) => Object.prototype.hasOwnProperty.call(o, k);

export const isProxySection = (section: string) => section === 'defaults' || section === 'frontend' || section === 'backend' || section === 'listen';

export function knownKeyword(section: string, kw: string): boolean {
  if (section === 'global') return GLOBAL.has(kw) || K.global.prefixes.some((p) => kw.startsWith(p));
  if (isProxySection(section)) return has(K.proxy, kw);
  return OTHER[section]?.has(kw) ?? false;
}

/** Section letters: d=defaults, n=named defaults only, f=frontend, l=listen, b=backend. */
function sectionAllowed(flags: string, section: string, namedDefaults: boolean): { allowed: boolean; anonymous: boolean } {
  switch (section) {
    case 'defaults':
      if (flags.includes('d')) return { allowed: true, anonymous: false };
      if (flags.includes('n')) return { allowed: namedDefaults, anonymous: !namedDefaults };
      return { allowed: false, anonymous: false };
    case 'frontend': return { allowed: flags.includes('f'), anonymous: false };
    case 'listen': return { allowed: flags.includes('l'), anonymous: false };
    case 'backend': return { allowed: flags.includes('b'), anonymous: false };
  }
  return { allowed: true, anonymous: false };
}

export function trailingPunct(text: string): [string, number] {
  const base = text.replace(/[,;:.]+$/, '');
  return [base, text.length - base.length];
}

/** Narrows a word to its trailing punctuation (marker range). */
export function punctWord(w: RuleWord, n: number): RuleWord {
  if (w.quoted || n === 0) return w;
  return { ...w, start: w.end - n };
}

const stray = (emit: RuleEmit, w: RuleWord, base: string, n: number) => emit(punctWord(w, n), 'stray_punctuation', `Лишний символ «${w.text.slice(base.length)}» после «${base}»`);

export function negatable(section: string, kw: string): boolean {
  return section === 'global' ? GLOBAL_NEGATABLE.has(kw) : kw === 'option' || kw === 'log';
}

export function unknownDirective(section: string, kw: RuleWord, emit: RuleEmit): void {
  const [base, n] = trailingPunct(kw.text);
  if (n > 0 && knownKeyword(section, base)) { stray(emit, kw, base, n); return; }
  emit(kw, 'unknown_directive', `Неизвестная директива «${kw.text}» в секции ${section}`, section === 'global' || isProxySection(section) ? 'error' : 'warning');
}

function sectionAllows(section: string, namedDefaults: boolean, w: RuleWord, name: string, flags: string, emit: RuleEmit): boolean {
  const { allowed, anonymous } = sectionAllowed(flags, section, namedDefaults);
  if (allowed) return true;
  emit(w, 'keyword_not_allowed', anonymous ? `«${name}» недопустим в безымянной секции defaults (задайте ей имя)` : `«${name}» недопустим в секции ${section}`);
  return false;
}

/** HAProxy 4.1 keyword matrix; false when the keyword is refused here. */
export function proxyKeywordAllowed(section: string, namedDefaults: boolean, words: RuleWord[], emit: RuleEmit): boolean {
  const kw = words[0];
  if (!sectionAllows(section, namedDefaults, kw, kw.text, K.proxy[kw.text] ?? '', emit)) return false;
  if (kw.text === 'timeout' || kw.text === 'option' || words.length < 2) return true;
  const sub = words[1];
  const key = `${kw.text} ${sub.text}`;
  if (has(K.proxy_sub, key)) return sectionAllows(section, namedDefaults, sub, key, K.proxy_sub[key], emit);
  if (!STRICT_SUB.has(kw.text)) return true;
  const [base, n] = trailingPunct(sub.text);
  if (n > 0 && has(K.proxy_sub, `${kw.text} ${base}`)) { stray(emit, sub, base, n); return false; }
  emit(sub, 'unknown_keyword', `Неизвестный параметр «${sub.text}» у ${kw.text}`);
  return false;
}

export function checkTimeoutKind(section: string, words: RuleWord[], emit: RuleEmit): boolean {
  if (words.length < 2) return true;
  const cls = isProxySection(section) ? 'proxy' : section;
  const kinds = TIMEOUTS[cls];
  const kind = words[1];
  if (kinds && !kinds.has(kind.text)) {
    const [base, n] = trailingPunct(kind.text);
    if (n > 0 && kinds.has(base)) stray(emit, kind, base, n);
    else emit(kind, 'unknown_timeout', `Неизвестный тип timeout «${kind.text}»`);
    return false;
  }
  if (cls === 'proxy') {
    const flags = K.proxy_sub[`timeout ${kind.text}`];
    if (flags && !sectionAllowed(flags, section, true).allowed) emit(kind, 'timeout_ignored', `timeout ${kind.text} игнорируется в секции ${section}`, 'warning');
  }
  return true;
}

export function checkOption(section: string, words: RuleWord[], modifier: string, emit: RuleEmit): void {
  if (words.length < 2) { emit(words[0], 'option_missing_name', 'У option не указано имя'); return; }
  const name = words[1];
  if (!has(K.options, name.text)) {
    const [base, n] = trailingPunct(name.text);
    if (n > 0 && has(K.options, base)) emit(punctWord(name, n), 'unknown_option', `Неизвестная опция «${name.text}» — лишний символ «${name.text.slice(base.length)}»`);
    else emit(name, 'unknown_option', `Неизвестная опция «${name.text}»`);
    return;
  }
  if (modifier && OPTIONS_NO_NEG.has(name.text)) { emit(name, 'negation_not_supported', `Префикс «${modifier}» не поддерживается для option ${name.text}`); return; }
  if (!sectionAllowed(K.options[name.text], section, true).allowed) emit(name, 'option_ignored', `option ${name.text} игнорируется в секции ${section}`, 'warning');
  const args = K.option_args[name.text];
  if (args === 'none' && words.length > 2) emit(words[2], 'too_many_args', `option ${name.text} не принимает аргументов`);
  if (args === 'clf') {
    if (words.length > 2 && words[2].text !== 'clf') emit(words[2], 'invalid_option_arg', `option ${name.text} принимает только «clf»`);
    else if (words.length > 3) emit(words[3], 'too_many_args', `Лишний аргумент option ${name.text}`);
  }
}

export function checkMode(section: string, words: RuleWord[], emit: RuleEmit): void {
  if (words.length < 2) { emit(words[0], 'invalid_mode', 'У mode не указан режим (tcp или http)'); return; }
  const value = words[1];
  if (value.text === 'health') { emit(value, 'invalid_mode', 'mode health больше не поддерживается'); return; }
  if (!has(K.modes, value.text)) {
    const [base, n] = trailingPunct(value.text);
    if (n > 0 && has(K.modes, base)) stray(emit, value, base, n);
    else emit(value, 'invalid_mode', `Неизвестный режим «${value.text}» (tcp, http, log, spop)`);
    return;
  }
  if (!sectionAllowed(K.modes[value.text], section, true).allowed) emit(value, 'invalid_mode', `mode ${value.text} недопустим в секции ${section}`);
  else if (words.length > 2) emit(words[2], 'too_many_args', 'Лишний аргумент mode');
}

export function checkBalance(words: RuleWord[], emit: RuleEmit): void {
  if (words.length < 2) { emit(words[0], 'invalid_balance', 'У balance не указан алгоритм'); return; }
  const algo = words[1];
  if (BALANCE.has(algo.text.split('(')[0])) return;
  const [base, n] = trailingPunct(algo.text);
  if (n > 0 && BALANCE.has(base)) stray(emit, algo, base, n);
  else emit(algo, 'invalid_balance', `Неизвестный алгоритм balance «${algo.text}»`);
}

export function checkHashType(words: RuleWord[], emit: RuleEmit): void {
  if (words.length < 2) { emit(words[0], 'invalid_hash_type', 'hash-type: укажите consistent или map-based'); return; }
  for (let i = 1; i < words.length; i++) {
    const w = words[i];
    const allowed = HASH[i - 1];
    if (!allowed) { emit(w, 'too_many_args', 'Лишний аргумент hash-type'); return; }
    if (allowed.has(w.text)) continue;
    const [base, n] = trailingPunct(w.text);
    if (n > 0 && allowed.has(base)) stray(emit, w, base, n);
    else emit(w, 'invalid_hash_type', `Неверный аргумент hash-type «${w.text}»`);
    return;
  }
}

export function checkLog(section: string, words: RuleWord[], emit: RuleEmit): void {
  if (words.length < 2) { emit(words[0], 'log_missing_target', 'У log не указан адрес'); return; }
  if (words[1].text === 'global' && section !== 'global') {
    if (words.length > 2) emit(words[2], 'too_many_args', 'Лишний аргумент log global');
    return;
  }
  let i = 2;
  while (i < words.length && LOG_OPTIONS.has(words[i].text)) i += 2;
  if (i >= words.length) { emit(words[words.length - 1], 'log_missing_facility', 'У log не указан facility (например local0)'); return; }
  for (let n = 0; i + n < words.length; n++) {
    const w = words[i + n];
    if (n > 2) { emit(w, 'too_many_args', `Лишний аргумент log «${w.text}»`); return; }
    const allowed = n === 0 ? LOG_FACILITIES : LOG_LEVELS;
    if (w.text.includes('$') || allowed.has(w.text)) continue;
    const [base, cut] = trailingPunct(w.text);
    if (cut > 0 && allowed.has(base)) stray(emit, w, base, cut);
    else emit(w, 'invalid_log', `Неизвестный ${n === 0 ? 'facility' : 'уровень'} log «${w.text}»`);
    return;
  }
}

function keywordArgs(words: RuleWord[], start: number, table: Record<string, number>, code: string, what: string, emit: RuleEmit, check?: (kw: string, value: RuleWord) => void): void {
  for (let i = start; i < words.length;) {
    const w = words[i];
    const name = w.text.split('(')[0];
    if (!has(table, name)) {
      const [base, cut] = trailingPunct(w.text);
      if (cut > 0 && has(table, base)) stray(emit, w, base, cut);
      else emit(w, code, `Неизвестный параметр ${what} «${w.text}»`);
      i++;
      continue;
    }
    const n = table[name];
    if (n < 0) {
      i += 2;
      while (i + 1 < words.length && (words[i].text === 'usesrc' || words[i].text === 'interface')) i += 2;
      continue;
    }
    if (n > 0 && i + n >= words.length) { emit(w, 'missing_value', `У параметра «${w.text}» нет значения`); return; }
    if (n > 0 && check) check(name, words[i + 1]);
    i += 1 + n;
  }
}

export function checkServerKeywords(words: RuleWord[], start: number, emit: RuleEmit): void {
  keywordArgs(words, start, K.server, 'unknown_server_keyword', 'server', emit, (kw, value) => {
    if (value.text.includes('$')) return;
    if (SERVER_DURATION.has(kw)) {
      if (!DURATION_RE.test(value.text)) emit(value, 'invalid_duration', `«${value.text}» — неверное время для ${kw} (пример: 5s, 500ms)`);
    } else if (kw === 'fall' || kw === 'rise') {
      if (!/^[1-9]\d*$/.test(value.text)) emit(value, 'invalid_number', `${kw} ожидает целое число ≥ 1`);
    } else if (kw === 'weight') {
      if (!/^\d+$/.test(value.text) || Number(value.text) > 256) emit(value, 'invalid_weight', 'weight — целое число 0–256');
    }
  });
}

export function checkBindKeywords(words: RuleWord[], start: number, emit: RuleEmit): void {
  keywordArgs(words, start, K.bind, 'unknown_bind_keyword', 'bind', emit);
}

/** Bare trailing ',' or ';' after a plain-value directive (maxconn 100;). Returns the marker word or null. */
export function strayTail(words: RuleWord[]): { word: RuleWord; punct: string } | null {
  if (FREE_TEXT.has(words[0].text) || words.length < 2) return null;
  const last = words[words.length - 1];
  if (last.quoted || last.text.includes('%[') || /[{}]/.test(last.text)) return null;
  const base = last.text.replace(/[,;]+$/, '');
  if (base === last.text) return null;
  return { word: punctWord(last, last.text.length - base.length), punct: last.text.slice(base.length) };
}
