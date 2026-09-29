// Pure haproxy.cfg helpers shared by the Monaco editor and node --test:
// tokenizer, hover resolution, client-side formatter and fallback linter.
// Only type-free runtime imports with explicit .ts extensions are allowed here.
import {
  argDocs, directiveDocs, DURATION_UNITS, GENERATED_NAME_DOC, keywordDocs, SECTION_KEYWORDS, sectionDocs,
  type HAProxyDoc,
} from './haproxyDocs.ts';
import {
  checkBalance, checkBindKeywords, checkHashType, checkLog, checkMode, checkOption, checkServerKeywords, checkTimeoutKind,
  isProxySection, knownKeyword, LINT_SECTIONS, negatable, proxyKeywordAllowed, punctWord, strayTail, trailingPunct, unknownDirective,
  type RuleEmit, type RuleWord,
} from './haproxyLintRules.ts';

export interface LineToken { text: string; start: number; end: number }

export type LintSeverity = 'error' | 'warning';
export interface LintIssue { line: number; column: number; end_column: number; severity: LintSeverity; code: string; message: string }
export interface LintSection { type: string; name: string; line: number }
export interface LintResult { valid: boolean; issues: LintIssue[]; listener_tcp_ports: number[]; sections: LintSection[] }

export const MAX_CONFIG_BYTES = 512 * 1024;
const SECTION_SET = new Set(SECTION_KEYWORDS);
const NAMED_SECTIONS = new Set(['frontend', 'backend', 'listen', 'resolvers', 'peers', 'userlist', 'cache', 'program', 'ring', 'http-errors', 'mailers', 'log-forward']);
const PROXY_SECTIONS = new Set(['frontend', 'backend', 'listen', 'defaults']);
const CONDITIONALS = new Set(['.if', '.elif', '.else', '.endif', '.notice', '.warning', '.alert', '.diag']);
const DURATION_RE = /^\d+(us|ms|s|m|h|d)?$/;
const GENERATED_RE = /^nf_[a-z0-9_]+$/i;

/** Splits one line into whitespace-separated words; stops at an unquoted #. */
export function tokenizeLine(line: string): LineToken[] {
  const tokens: LineToken[] = [];
  let i = 0;
  while (i < line.length) {
    while (i < line.length && /\s/.test(line[i])) i++;
    if (i >= line.length || line[i] === '#') break;
    const start = i;
    let quote = '';
    while (i < line.length) {
      const ch = line[i];
      if (quote) {
        if (ch === '\\' && quote === '"') { i += 2; continue; }
        if (ch === quote) quote = '';
        i++; continue;
      }
      if (ch === '"' || ch === "'") { quote = ch; i++; continue; }
      if (ch === '\\') { i += 2; continue; }
      if (/\s/.test(ch)) break;
      i++;
    }
    tokens.push({ text: line.slice(start, Math.min(i, line.length)), start, end: Math.min(i, line.length) });
  }
  return tokens;
}

function sectionAt(lines: string[], index: number): { type: string; line: number } | null {
  for (let i = index; i >= 0; i--) {
    const first = tokenizeLine(lines[i])[0]?.text.toLowerCase();
    if (first && SECTION_SET.has(first)) return { type: first, line: i };
  }
  return null;
}

/** Longest known directive phrase at the start of a line («timeout connect», «option tcplog», «maxconn»). */
export function directiveKey(words: string[]): { key: string; length: number } | null {
  const lower = words.map((w) => w.toLowerCase());
  for (let length = Math.min(3, lower.length); length >= 1; length--) {
    const key = lower.slice(0, length).join(' ');
    if (directiveDocs[key]) return { key, length };
  }
  if (lower[0] === 'no' && lower[1] === 'option') return { key: 'no option', length: 2 };
  return null;
}

export type HoverKind = 'section' | 'section-name' | 'directive' | 'argument' | 'keyword' | 'value' | 'generated' | 'fallback';
export interface HoverResult {
  title: string;
  kind: HoverKind;
  doc: HAProxyDoc;
  /** Extra first line, e.g. «Значение `15m` = 15 минут». */
  value?: string;
  /** Section the hovered line belongs to. */
  section?: string;
  start: number;
  end: number;
}

function describeValue(word: string): string | null {
  const duration = /^(\d+)(us|ms|s|m|h|d)$/i.exec(word);
  if (duration) return `Значение \`${word}\` = ${duration[1]} ${DURATION_UNITS[duration[2].toLowerCase()]}`;
  if (/^\d+$/.test(word)) return `Значение \`${word}\``;
  const size = /^(\d+)([kmg])$/i.exec(word);
  if (size) return `Значение \`${word}\` = ${size[1]} ${{ k: 'тысяч', m: 'миллионов', g: 'миллиардов' }[size[2].toLowerCase() as 'k' | 'm' | 'g']}`;
  if (/^(unix@|abns@)?\//.test(word)) return `Путь \`${word}\``;
  const address = parseAddress(word);
  if (address) return address.port ? `Адрес \`${address.host || '*'}\`, порт ${address.port}` : `Адрес \`${address.host}\``;
  return null;
}

function parseAddress(word: string): { host: string; port: string } | null {
  const plain = word.replace(/^(ipv4|ipv6|tcp|tcp4|tcp6)@/i, '');
  const bracket = /^\[([0-9a-f:.]+)\](?::([\d-]+))?$/i.exec(plain);
  if (bracket) return { host: bracket[1], port: bracket[2] ?? '' };
  const v4 = /^(\d{1,3}(?:\.\d{1,3}){3}|\*)?(?:\/\d+)?(?::([\d-]+))?$/.exec(plain);
  if (v4 && (v4[1] || v4[2])) return { host: v4[1] ?? '', port: v4[2] ?? '' };
  if (/^[0-9a-f]*:[0-9a-f:]*:[0-9a-f.:]*$/i.test(plain)) {
    const idx = plain.lastIndexOf(':');
    return { host: plain.slice(0, idx), port: plain.slice(idx + 1) };
  }
  return null;
}

/** Splits `src,ipmask(32,64)` into its function/argument parts (addresses are kept whole). */
function subParts(token: LineToken): LineToken[] {
  if (!/[,()]/.test(token.text) || /^["']/.test(token.text)) return [token];
  const parts: LineToken[] = [];
  const re = /[^,()]+/g;
  let match: RegExpExecArray | null;
  while ((match = re.exec(token.text))) parts.push({ text: match[0], start: token.start + match.index, end: token.start + match.index + match[0].length });
  return parts.length ? parts : [token];
}

/** Resolves the documentation shown for the character at (lineIndex, column), both 0-based. */
export function resolveHover(lines: string[], lineIndex: number, column: number): HoverResult | null {
  const line = lines[lineIndex] ?? '';
  const tokens = tokenizeLine(line);
  const index = tokens.findIndex((t) => column >= t.start && column < t.end);
  if (index < 0) return null;
  const words = tokens.map((t) => t.text);
  const lower = words.map((w) => w.toLowerCase());
  const token = tokens[index];
  const section = sectionAt(lines, lineIndex)?.type;

  if (SECTION_SET.has(lower[0])) {
    if (index === 0) return { title: lower[0], kind: 'section', doc: sectionDocs[lower[0]], start: token.start, end: token.end };
    if (index === 1) {
      if (GENERATED_RE.test(words[1])) return { title: words[1], kind: 'generated', doc: GENERATED_NAME_DOC, section: lower[0], start: token.start, end: token.end };
      return { title: words[1], kind: 'section-name', doc: { summary: `Имя секции ${lower[0]}. На него ссылаются use_backend, default_backend и статистика.`, syntax: sectionDocs[lower[0]].syntax }, section: lower[0], start: token.start, end: token.end };
    }
  }

  const directive = directiveKey(words);
  if (directive && index < directive.length) {
    const t0 = tokens[0];
    const tl = tokens[directive.length - 1];
    return { title: directive.key, kind: 'directive', doc: directiveDocs[directive.key], section, start: t0.start, end: tl.end };
  }

  const parts = subParts(token);
  const part = parts.find((p) => column >= p.start && column < p.end) ?? token;
  const word = part.text.toLowerCase();
  const prev = index > 0 ? lower[index - 1] : '';
  const partIndex = parts.indexOf(part);
  const range = { start: part.start, end: part.end };

  if (GENERATED_RE.test(part.text)) return { title: part.text, kind: 'generated', doc: GENERATED_NAME_DOC, section, ...range };
  if (keywordDocs[word] && !(directive && argDocs[directive.key]?.[word]) && !argDocs[lower[0]]?.[word]) {
    return { title: word, kind: 'keyword', doc: keywordDocs[word], section, ...range };
  }
  // Value of an option that owns its own vocabulary: init-addr last,none / hash-key addr.
  if (argDocs[prev]?.[word]) return { title: word, kind: 'argument', doc: argDocs[prev][word], section, value: `Значение опции \`${prev}\``, ...range };
  const ownerDocs = (directive && argDocs[directive.key]) || argDocs[lower[0]];
  if (ownerDocs?.[word] && (partIndex === 0 || lower[0] === 'balance' || lower[0] === 'stick-table')) {
    return { title: word, kind: 'argument', doc: ownerDocs[word], section, ...range };
  }
  if (keywordDocs[word]) return { title: word, kind: 'keyword', doc: keywordDocs[word], section, ...range };

  const valueLine = describeValue(part.text) ?? describeValue(token.text);
  const ownerKey = directive?.key ?? lower[0];
  if (valueLine) {
    const optionDoc = ownerDocs?.[prev];
    if (optionDoc) return { title: prev, kind: 'value', doc: optionDoc, value: valueLine, section, ...range };
    const doc = directiveDocs[ownerKey];
    if (doc) return { title: ownerKey, kind: 'value', doc, value: valueLine, section, ...range };
    return null;
  }
  const doc = directive ? directiveDocs[directive.key] : undefined;
  if (doc) return { title: directive!.key, kind: 'fallback', doc, value: `Аргумент \`${part.text}\``, section, ...range };
  return null;
}

const KIND_LABEL: Record<HoverKind, string> = {
  section: 'секция', 'section-name': 'имя секции', directive: 'директива', argument: 'опция', keyword: 'выражение', value: 'значение', generated: 'имя NodeFlow', fallback: 'аргумент',
};

export function hoverMarkdown(result: HoverResult): string {
  const context = result.section && result.kind !== 'section' ? ` · ${result.section}` : '';
  const out = [`**\`${result.title}\`** — ${KIND_LABEL[result.kind]}${context}`];
  if (result.value) out.push(result.value);
  out.push(result.doc.summary);
  if (result.doc.syntax) out.push('```haproxy\n' + result.doc.syntax + '\n```');
  if (result.doc.warning) out.push(result.doc.warning);
  return out.join('\n\n');
}

/** Section keywords at column 0, everything else 4 spaces (+4 per .if level), no trailing spaces, max one blank line. */
export function formatConfig(text: string): string {
  const out: string[] = [];
  let inSection = false;
  let depth = 0;
  let blank = false;
  for (const raw of text.replace(/\r\n?/g, '\n').split('\n')) {
    const body = raw.trim();
    if (!body) { blank = out.length > 0; continue; }
    if (blank) { out.push(''); blank = false; }
    const first = tokenizeLine(body)[0]?.text.toLowerCase() ?? '';
    const base = inSection ? 1 : 0;
    if (SECTION_SET.has(first)) {
      inSection = true; depth = 0;
      out.push(body);
      continue;
    }
    if (first === '.endif' || first === '.else' || first === '.elif') {
      if (first === '.endif') depth = Math.max(0, depth - 1);
      out.push(' '.repeat((base + Math.max(0, depth - (first === '.endif' ? 0 : 1))) * 4) + body);
      continue;
    }
    out.push(' '.repeat((base + depth) * 4) + body);
    if (first === '.if') depth++;
  }
  return out.length ? out.join('\n') + '\n' : '';
}

function issue(list: LintIssue[], line: number, token: LineToken | undefined, lineText: string, severity: LintSeverity, code: string, message: string) {
  const start = token ? token.start : lineText.length - lineText.trimStart().length;
  const end = token ? token.end : Math.max(start + 1, lineText.trimEnd().length);
  list.push({ line, column: start + 1, end_column: end + 1, severity, code, message });
}

function validPort(value: string): boolean {
  const range = /^(\d+)(?:-(\d+))?$/.exec(value);
  if (!range) return false;
  const low = Number(range[1]); const high = Number(range[2] ?? range[1]);
  return low >= 1 && high <= 65535 && low <= high;
}

/** Token with HAProxy quoting removed (the raw range stays for markers). */
function ruleWord(t: LineToken): RuleWord {
  const quoted = /["'\\]/.test(t.text);
  const text = quoted ? t.text.replace(/\\(.)|["']/g, (_m, ch: string | undefined) => ch ?? '') : t.text;
  return { text, start: t.start, end: t.end, quoted };
}

/** Offline linter used when POST /config-lint is unavailable. */
export function lintConfig(text: string): LintResult {
  const issues: LintIssue[] = [];
  const sections: LintSection[] = [];
  const ports = new Set<number>();
  const lines = text.replace(/\r\n?/g, '\n').split('\n');
  const proxyNames = new Map<string, number>();
  const backendRefs: { name: string; line: number; token: LineToken; text: string }[] = [];
  const ifStack: number[] = [];
  let current: { type: string; name: string; line: number; binds: number } | null = null;
  let namedDefaults = false;
  const finishSection = () => {
    if (current && (current.type === 'frontend' || current.type === 'listen') && current.binds === 0) {
      issues.push({ line: current.line, column: 1, end_column: current.type.length + 1, severity: 'warning', code: 'no_bind', message: `В ${current.type} «${current.name}» нет ни одного bind — секция не принимает соединения.` });
    }
  };
  if (new TextEncoder().encode(text).length > MAX_CONFIG_BYTES) {
    issues.push({ line: 1, column: 1, end_column: 2, severity: 'error', code: 'too_large', message: 'Конфиг больше 512 КиБ.' });
  }
  if (!text.trim()) issues.push({ line: 1, column: 1, end_column: 1, severity: 'error', code: 'empty', message: 'Конфиг пуст.' });

  lines.forEach((lineText, i) => {
    const lineNo = i + 1;
    const tokens = tokenizeLine(lineText);
    if (!tokens.length) return;
    const lower = tokens.map((t) => t.text.toLowerCase());
    const first = lower[0];
    const indented = /^\s/.test(lineText);
    const opens = lower.filter((w) => w === '{').length;
    const closes = lower.filter((w) => w === '}').length;
    if (opens !== closes) issue(issues, lineNo, undefined, lineText, 'error', 'unbalanced_braces', 'Непарные фигурные скобки { } в условии.');
    for (const t of tokens) {
      if (/^["']/.test(t.text) && (t.text.length < 2 || t.text[t.text.length - 1] !== t.text[0])) issue(issues, lineNo, t, lineText, 'error', 'unterminated_quote', 'Незакрытая кавычка.');
    }

    if (CONDITIONALS.has(first)) {
      if (first === '.if') ifStack.push(lineNo);
      else if (first === '.elif' || first === '.else') { if (!ifStack.length) issue(issues, lineNo, tokens[0], lineText, 'error', 'conditional_unbalanced', `${first} без открывающего .if.`); }
      else if (first === '.endif') { if (!ifStack.pop()) issue(issues, lineNo, tokens[0], lineText, 'error', 'conditional_unbalanced', '.endif без открывающего .if.'); }
      if ((first === '.if' || first === '.elif') && tokens.length < 2) issue(issues, lineNo, tokens[0], lineText, 'error', 'conditional_missing', `После ${first} нужно условие.`);
      return;
    }

    if (LINT_SECTIONS.has(tokens[0].text)) {
      finishSection();
      const name = tokens[1]?.text ?? '';
      if (indented) issue(issues, lineNo, tokens[0], lineText, 'warning', 'section_indent', `Секция ${first} должна начинаться с первой колонки.`);
      if (NAMED_SECTIONS.has(first) && !name) issue(issues, lineNo, tokens[0], lineText, 'error', 'section_name_missing', `У секции ${first} должно быть имя.`);
      if (first === 'global' && tokens.length > 1) issue(issues, lineNo, tokens[1], lineText, 'error', 'unexpected_argument', 'У секции global нет имени.');
      if (name && PROXY_SECTIONS.has(first) && first !== 'defaults') {
        const key = `${first === 'frontend' ? 'fe' : 'be'}:${name}`;
        const listenKey = `${first === 'frontend' ? 'be' : 'fe'}:${name}`;
        if (proxyNames.has(key) || (first === 'listen' && proxyNames.has(listenKey)) || proxyNames.has(`listen:${name}`)) {
          issue(issues, lineNo, tokens[1], lineText, 'error', 'duplicate_proxy', `Имя «${name}» уже используется в строке ${proxyNames.get(key) ?? proxyNames.get(listenKey) ?? proxyNames.get(`listen:${name}`)}.`);
        }
        if (first === 'listen') { proxyNames.set(`listen:${name}`, lineNo); proxyNames.set(`fe:${name}`, lineNo); proxyNames.set(`be:${name}`, lineNo); }
        else proxyNames.set(key, lineNo);
      }
      if (first === 'defaults') namedDefaults = Boolean(name) && !(name === 'from' && tokens.length === 3);
      current = { type: first, name, line: lineNo, binds: 0 };
      sections.push({ type: first, name, line: lineNo });
      return;
    }

    if (!current) {
      issue(issues, lineNo, tokens[0], lineText, 'error', 'outside_section', `«${tokens[0].text}» вне секции: сначала объявите global, defaults, frontend или backend.`);
      return;
    }
    const section = current.type;
    let words = tokens.map(ruleWord);
    const emit: RuleEmit = (w, code, message, severity = 'error') => issue(issues, lineNo, { text: w.text, start: w.start, end: w.end }, lineText, severity, code, message);
    // «no»/«default» modifiers shift the arguments like cfgparse.c does.
    let modifier = '';
    if ((words[0].text === 'no' || words[0].text === 'default') && words.length > 1) { modifier = words[0].text; words = words.slice(1); }
    const kw = words[0];
    const known = knownKeyword(section, kw.text);
    if (!indented && !known && !modifier) {
      const [base, n] = trailingPunct(kw.text);
      if (n > 0 && knownKeyword(section, base)) emit(punctWord(kw, n), 'stray_punctuation', `Лишний символ «${kw.text.slice(base.length)}» после «${base}»`);
      else issue(issues, lineNo, tokens[0], lineText, 'error', 'unknown_section', `Неизвестная секция «${tokens[0].text}».`);
      return;
    }
    if (!indented) issue(issues, lineNo, tokens[0], lineText, 'warning', 'indentation', 'Директива внутри секции обычно пишется с отступом.');
    if (!known) { unknownDirective(section, kw, emit); return; }
    if (modifier && !negatable(section, kw.text)) { emit(kw, 'negation_not_supported', `Префикс «${modifier}» не поддерживается для «${kw.text}»`); return; }
    if (isProxySection(section) && !proxyKeywordAllowed(section, namedDefaults, words, emit)) return;
    const before = issues.length;
    const proxy = isProxySection(section);
    switch (kw.text) {
      case 'option': if (proxy) checkOption(section, words, modifier, emit); break;
      case 'mode': if (proxy) checkMode(section, words, emit); break;
      case 'balance': if (proxy) checkBalance(words, emit); break;
      case 'hash-type': if (proxy) checkHashType(words, emit); break;
      case 'log': if ((proxy || section === 'global') && !modifier) checkLog(section, words, emit); break;
      case 'stats': if (section === 'global' && words.length > 2 && words[1].text === 'socket') checkBindKeywords(words, 3, emit); break;
      case 'default-server': checkServerKeywords(words, 1, emit); break;
      case 'bind': if (words.length > 1) checkBindKeywords(words, 2, emit); break;
      case 'server': if (['backend', 'listen', 'peers', 'ring'].includes(section) && words.length > 2) checkServerKeywords(words, 3, emit); break;
      case 'server-template': if (['backend', 'listen'].includes(section) && words.length > 3) checkServerKeywords(words, 4, emit); break;
    }
    if (kw.text === 'timeout' && !checkTimeoutKind(section, words, emit)) return;
    const tail = strayTail(words);
    if (tail && !issues.slice(before).some((i) => i.line === lineNo && i.column <= tail.word.start + 1 && tail.word.start + 1 < i.end_column)) {
      emit(tail.word, 'stray_punctuation', `Лишний символ «${tail.punct}» в конце строки`);
    }

    if (first === 'timeout' || first === 'hard-stop-after' || (first === 'tcp-request' && lower[1] === 'inspect-delay') || (first === 'hold' && current.type === 'resolvers') || (first === 'stats' && lower[1] === 'timeout')) {
      const valueIndex = first === 'hard-stop-after' ? 1 : 2;
      const value = tokens[valueIndex];
      if (!value) issue(issues, lineNo, tokens[valueIndex - 1] ?? tokens[0], lineText, 'error', 'timeout_missing', 'Не указано значение времени, например 5s или 15m.');
      else if (!DURATION_RE.test(value.text)) issue(issues, lineNo, value, lineText, 'error', 'timeout_format', `«${value.text}» — неверное время. Формат: число и единица us, ms, s, m, h или d (например 5s).`);
    }

    if (first === 'bind') {
      current.binds++;
      const addr = tokens[1];
      if (!addr) issue(issues, lineNo, tokens[0], lineText, 'error', 'bind_address_missing', 'У bind не указан адрес и порт.');
      else {
        for (const piece of addr.text.split(',')) {
          if (/^(unix@|abns@|fd@|sockpair@|\/|quic)/i.test(piece)) continue;
          const parsed = parseAddress(piece);
          if (!parsed || !parsed.port) { issue(issues, lineNo, addr, lineText, 'error', 'bind_port_missing', `В «${piece}» не указан порт (пример :443).`); continue; }
          if (!validPort(parsed.port)) { issue(issues, lineNo, addr, lineText, 'error', 'port_range', `Порт «${parsed.port}» вне диапазона 1–65535.`); continue; }
          if (current.type === 'frontend' || current.type === 'listen') {
            const [low, high = low] = parsed.port.split('-').map(Number);
            for (let p = low; p <= high && p - low < 1024; p++) ports.add(p);
          }
        }
      }
    }

    if (first === 'server' || first === 'server-template') {
      const addr = tokens[first === 'server' ? 2 : 3];
      if (!addr && !(current.type === 'peers')) issue(issues, lineNo, tokens[1] ?? tokens[0], lineText, 'error', 'server_address_missing', 'У server не указан адрес.');
      else if (addr && !/^(unix@|abns@|\/|\$)/i.test(addr.text)) {
        const parsed = parseAddress(addr.text) ?? (/:([^:\]]+)$/.exec(addr.text) ? { host: '', port: /:([^:\]]+)$/.exec(addr.text)![1] } : null);
        if (parsed?.port && !/^[+-]/.test(parsed.port) && !validPort(parsed.port)) issue(issues, lineNo, addr, lineText, 'error', 'port_range', `Порт «${parsed.port}» вне диапазона 1–65535.`);
      }
    }

    if ((first === 'use_backend' || first === 'default_backend') && tokens[1] && !tokens[1].text.includes('%[')) {
      backendRefs.push({ name: tokens[1].text, line: lineNo, token: tokens[1], text: lineText });
    }
    if ((first === 'use_backend' || first === 'default_backend') && !tokens[1]) issue(issues, lineNo, tokens[0], lineText, 'error', 'backend_missing', `У ${first} не указан backend.`);
    if (first === 'use_backend' && tokens[1] && tokens.length > 2 && !['if', 'unless'].includes(lower[2])) issue(issues, lineNo, tokens[2], lineText, 'error', 'condition_keyword', 'После имени backend ожидается if или unless.');
  });
  finishSection();
  for (const line of ifStack) issues.push({ line, column: 1, end_column: 1 + (lines[line - 1]?.length ?? 1), severity: 'error', code: 'conditional_unbalanced', message: '.if без закрывающего .endif.' });
  for (const ref of backendRefs) {
    if (!proxyNames.has(`be:${ref.name}`)) issue(issues, ref.line, ref.token, ref.text, 'error', 'unknown_backend', `Backend «${ref.name}» не объявлен.`);
  }
  issues.sort((a, b) => a.line - b.line || a.column - b.column);
  return { valid: !issues.some((i) => i.severity === 'error'), issues, listener_tcp_ports: [...ports].sort((a, b) => a - b), sections };
}

/** Section kind for completion at a line (0-based). */
export function sectionKindAt(lines: string[], lineIndex: number): string | null {
  return sectionAt(lines, lineIndex)?.type ?? null;
}
