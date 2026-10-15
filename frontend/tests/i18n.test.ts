import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ru } from '../src/locales/ru.ts';
import { en } from '../src/locales/en.ts';
import { detectLang, hasKey, translate } from '../src/i18n.ts';

const placeholders = (value: unknown): string[] =>
  [...new Set(JSON.stringify(value).match(/\{\w+\}/g) ?? [])].sort();

test('en and ru dictionaries have identical keys', () => {
  assert.deepEqual(Object.keys(en).sort(), Object.keys(ru).sort());
});

test('every English message is non-empty and has no Cyrillic', () => {
  for (const [key, value] of Object.entries(en)) {
    const text = JSON.stringify(value);
    assert.ok(text.replace(/"/g, '').trim().length > 0, key);
    if (key !== 'app.languageRu') assert.doesNotMatch(text, /[А-Яа-яЁё]/, key);
  }
});

test('en and ru messages use the same placeholders', () => {
  for (const key of Object.keys(ru) as (keyof typeof ru)[]) {
    assert.deepEqual(placeholders(en[key]), placeholders(ru[key]), key);
  }
});

test('plural forms and interpolation', () => {
  assert.equal(translate('ru', 'editor.serversCount', { count: 1 }), '1 сервер');
  assert.equal(translate('ru', 'editor.serversCount', { count: 3 }), '3 сервера');
  assert.equal(translate('ru', 'editor.serversCount', { count: 5 }), '5 серверов');
  assert.equal(translate('en', 'editor.serversCount', { count: 1 }), '1 server');
  assert.equal(translate('en', 'editor.serversCount', { count: 2 }), '2 servers');
  assert.equal(translate('en', 'nodes.openNodeName', { name: 'edge-1' }), 'Open node edge-1');
});

test('language detection: stored choice, then browser, then ru', () => {
  assert.equal(detectLang('en', ['ru-RU']), 'en');
  assert.equal(detectLang(null, ['de-DE', 'en-GB']), 'en');
  assert.equal(detectLang(null, ['ru']), 'ru');
  assert.equal(detectLang('xx', ['de']), 'ru');
});

test('API error codes: known codes localized, unknown keep the server message', async () => {
  const { apiErrorText } = await import('../src/i18n.ts');
  assert.equal(apiErrorText('node_not_found', 'x'), 'Нода не найдена');
  assert.equal(apiErrorText('some_new_code', 'server text'), 'server text');
  assert.equal(apiErrorText(undefined, 'server text'), 'server text');
  assert.equal(translate('en', 'apiError.route_copy_failed'), 'Failed to create a draft on the node');
});

test('HAProxy docs: every Russian doc text has an English translation without Cyrillic', async () => {
  const { allDocs, DURATION_UNITS } = await import('../src/features/haproxy-editor/haproxyDocs.ts');
  const { DOCS_EN } = await import('../src/features/haproxy-editor/haproxyDocs.en.ts');
  const cyr = /[А-Яа-яЁё]/;
  const texts = [...allDocs().flatMap((doc) => [doc.summary, doc.syntax, doc.warning]), ...Object.values(DURATION_UNITS)]
    .filter((text): text is string => Boolean(text) && cyr.test(text as string));
  assert.deepEqual(texts.filter((text) => !(text in DOCS_EN)), []);
  assert.deepEqual(Object.values(DOCS_EN).filter((text) => cyr.test(text)), []);
});

test('HAProxy lint: every backend message key has ru and en translations', async () => {
  const { readFileSync } = await import('node:fs');
  const go = readFileSync(new URL('../../internal/panel/haproxy_lint_messages.go', import.meta.url), 'utf8');
  const keys = [...go.matchAll(/"[^"\n]*":\s+"([a-z0-9_]+)",/g)].map((m) => `lint.${m[1]}`);
  assert.ok(keys.length > 50);
  const missing = keys.filter((key) => !hasKey(key));
  assert.deepEqual(missing, []);
});
