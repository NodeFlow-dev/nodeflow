// Local Monaco bundle for the haproxy.cfg editor. Loaded only by the lazy
// HAProxy config page: no CDN, one editor worker, no bundled languages.
// Curated subset of monaco-editor/esm/vs/editor/editor.all.js: only the
// contributions this editor uses, to keep the lazy chunk small.
import 'monaco-editor/esm/vs/editor/browser/coreCommands.js';
import 'monaco-editor/esm/vs/editor/browser/widget/codeEditor/codeEditorWidget.js';
import 'monaco-editor/esm/vs/editor/browser/widget/diffEditor/diffEditor.contribution.js';
import 'monaco-editor/esm/vs/editor/contrib/bracketMatching/browser/bracketMatching.js';
import 'monaco-editor/esm/vs/editor/contrib/clipboard/browser/clipboard.js';
import 'monaco-editor/esm/vs/editor/contrib/comment/browser/comment.js';
import 'monaco-editor/esm/vs/editor/contrib/contextmenu/browser/contextmenu.js';
import 'monaco-editor/esm/vs/editor/contrib/cursorUndo/browser/cursorUndo.js';
import 'monaco-editor/esm/vs/editor/contrib/find/browser/findController.js';
import 'monaco-editor/esm/vs/editor/contrib/folding/browser/folding.js';
import 'monaco-editor/esm/vs/editor/contrib/format/browser/formatActions.js';
import 'monaco-editor/esm/vs/editor/contrib/gotoError/browser/gotoError.js';
import 'monaco-editor/esm/vs/editor/contrib/hover/browser/hoverContribution.js';
import 'monaco-editor/esm/vs/editor/contrib/indentation/browser/indentation.js';
import 'monaco-editor/esm/vs/editor/contrib/linesOperations/browser/linesOperations.js';
import 'monaco-editor/esm/vs/editor/contrib/multicursor/browser/multicursor.js';
import 'monaco-editor/esm/vs/editor/contrib/suggest/browser/suggestController.js';
import 'monaco-editor/esm/vs/editor/contrib/tokenization/browser/tokenization.js';
import 'monaco-editor/esm/vs/editor/contrib/wordHighlighter/browser/wordHighlighter.js';
import 'monaco-editor/esm/vs/editor/contrib/wordOperations/browser/wordOperations.js';
import 'monaco-editor/esm/vs/editor/contrib/readOnlyMessage/browser/contribution.js';
import 'monaco-editor/esm/vs/editor/common/standaloneStrings.js';
import 'monaco-editor/esm/vs/base/browser/ui/codicons/codiconStyles.js';
import 'monaco-editor/esm/vs/editor/standalone/browser/quickAccess/standaloneGotoLineQuickAccess.js';
import 'monaco-editor/esm/vs/editor/standalone/browser/quickAccess/standaloneCommandsQuickAccess.js';
import * as monaco from 'monaco-editor/esm/vs/editor/editor.api.js';
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker.js?worker';
import { loader } from '@monaco-editor/react';
import { argDocs, directiveDocs, keywordDocs, SECTION_KEYWORDS, sectionDocs, type HAProxyDoc } from './haproxyDocs.ts';
import { formatConfig, hoverMarkdown, resolveHover, sectionKindAt, tokenizeLine } from './haproxyModel.ts';

export const HAPROXY_LANGUAGE = 'haproxy';
export const HAPROXY_THEME = 'nodeflow-dark';

declare global {
  interface Window { MonacoEnvironment?: monaco.Environment }
}

self.MonacoEnvironment = { getWorker: () => new EditorWorker() };
loader.config({ monaco });

const optionWords = [...new Set(Object.values(argDocs).flatMap((group) => Object.keys(group)))].filter((word) => /^[a-z][\w.-]*$/.test(word));

function docMarkdown(title: string, doc: HAProxyDoc): string {
  return [`**\`${title}\`**`, doc.summary, doc.syntax ? '```haproxy\n' + doc.syntax + '\n```' : '', doc.warning ?? ''].filter(Boolean).join('\n\n');
}

function registerLanguage() {
  monaco.languages.register({ id: HAPROXY_LANGUAGE, extensions: ['.cfg'], aliases: ['HAProxy', 'haproxy'] });
  monaco.languages.setLanguageConfiguration(HAPROXY_LANGUAGE, {
    comments: { lineComment: '#' },
    brackets: [['{', '}'], ['(', ')']],
    autoClosingPairs: [{ open: '{', close: '}' }, { open: '(', close: ')' }, { open: '"', close: '"' }],
    wordPattern: /[^\s,()#]+/,
    indentationRules: { increaseIndentPattern: new RegExp(`^(${SECTION_KEYWORDS.join('|')})\\b.*$`), decreaseIndentPattern: /^\s*\.endif\b/ },
  });
  const sectionRe = SECTION_KEYWORDS.map((s) => s.replace('-', '\\-')).join('|');
  monaco.languages.setMonarchTokensProvider(HAPROXY_LANGUAGE, {
    ignoreCase: true,
    optionWords,
    tokenizer: {
      root: [
        [/^\s*#.*$/, 'comment'],
        [new RegExp(`^(${sectionRe})(\\s+)([^\\s#]+)`), ['keyword.section', 'white', 'type.section']],
        [new RegExp(`^(${sectionRe})(?=\\s|$)`), 'keyword.section'],
        [/^(\s*)(\.(?:if|elif|else|endif|notice|warning|alert|diag))\b/, ['white', 'keyword.conditional']],
        [/^(\s+)(timeout|option|no\s+option|tcp-request|tcp-response|http-request|http-response|stats|stick|tcp-check|http-check|filter|hold|rate-limit)(\s+)([a-z][\w.-]*)/, ['white', 'keyword.directive', 'white', 'keyword.directive']],
        [/^(\s+)([a-z_][\w.-]*)/, ['white', 'keyword.directive']],
        [/#.*$/, 'comment'],
        [/"([^"\\]|\\.)*"?/, 'string'],
        [/'[^']*'?/, 'string'],
        [/\b(if|unless|or)\b/, 'keyword.condition'],
        [/\|\||!/, 'operator'],
        [/[{}]/, 'delimiter.bracket'],
        [/[(),]/, 'delimiter'],
        [/nf_\w+/, 'variable.generated'],
        [/\[[0-9a-f:.]+\](:\d+(-\d+)?)?/, 'number.address'],
        [/(ipv4@|ipv6@)?\d{1,3}(\.\d{1,3}){3}(\/\d+)?(:\d+(-\d+)?)?/, 'number.address'],
        [/(ipv6@)?[0-9a-f]*::?[0-9a-f:]*:\d+\b/, 'number.address'],
        [/:\d+(-\d+)?\b/, 'number.address'],
        [/(unix@|abns@)?\/[^\s,#]*/, 'string.path'],
        [/(req|res|ssl_fc|ssl_c|ssl_s|sc\d|sc_|src|dst|fc|bc)[._][\w.]+/, 'predefined'],
        [/\b(src|dst|dst_port|src_port|always_true|always_false)\b/, 'predefined'],
        [/-[a-z]\b/, 'attribute.flag'],
        [/\d+(us|ms|s|m|h|d|k|g)?\b/, 'number'],
        [/[a-z][\w.-]*/, { cases: { '@optionWords': 'attribute', '@default': 'identifier' } }],
        [/\s+/, 'white'],
      ],
    },
  } as monaco.languages.IMonarchLanguage);

  monaco.languages.registerHoverProvider(HAPROXY_LANGUAGE, {
    provideHover(model, position) {
      const lines = model.getLinesContent();
      const hover = resolveHover(lines, position.lineNumber - 1, position.column - 1);
      if (!hover) return null;
      return {
        range: new monaco.Range(position.lineNumber, hover.start + 1, position.lineNumber, hover.end + 1),
        contents: [{ value: hoverMarkdown(hover) }],
      };
    },
  });

  monaco.languages.registerCompletionItemProvider(HAPROXY_LANGUAGE, {
    triggerCharacters: [' '],
    provideCompletionItems(model, position) {
      const line = model.getLineContent(position.lineNumber);
      const before = line.slice(0, position.column - 1);
      const word = model.getWordUntilPosition(position);
      const range = new monaco.Range(position.lineNumber, word.startColumn, position.lineNumber, word.endColumn);
      const kinds = monaco.languages.CompletionItemKind;
      const item = (label: string, doc: HAProxyDoc, kind: monaco.languages.CompletionItemKind) => ({
        label, kind, insertText: label, range, detail: doc.syntax, documentation: { value: docMarkdown(label, doc) },
      });
      if (/^\S*$/.test(before)) return { suggestions: Object.entries(sectionDocs).map(([key, doc]) => item(key, doc, kinds.Module)) };
      const tokens = tokenizeLine(before);
      const atFirstWord = /^\s+\S*$/.test(before);
      if (atFirstWord) {
        const section = sectionKindAt(model.getLinesContent(), position.lineNumber - 1);
        return {
          suggestions: Object.entries(directiveDocs)
            .filter(([, doc]) => !section || !doc.sections || doc.sections.includes(section as never))
            .map(([key, doc]) => item(key, doc, kinds.Keyword)),
        };
      }
      const owner = tokens[0]?.text.toLowerCase() ?? '';
      const group = argDocs[owner] ?? (owner === 'tcp-request' ? argDocs['tcp-request'] : undefined);
      const suggestions = group ? Object.entries(group).map(([key, doc]) => item(key, doc, kinds.Property)) : [];
      suggestions.push(...['if', 'unless', 'src', 'dst_port', 'req.ssl_sni'].map((key) => item(key, keywordDocs[key], kinds.Function)));
      return { suggestions };
    },
  });

  monaco.languages.registerDocumentFormattingEditProvider(HAPROXY_LANGUAGE, {
    provideDocumentFormattingEdits(model) {
      return [{ range: model.getFullModelRange(), text: formatConfig(model.getValue()) }];
    },
  });
}

const themeRules: monaco.editor.ITokenThemeRule[] = [
  { token: 'keyword.section', foreground: '5fd38d', fontStyle: 'bold' },
  { token: 'type.section', foreground: 'e5c07b', fontStyle: 'bold' },
  { token: 'keyword.conditional', foreground: 'c678dd', fontStyle: 'bold' },
  { token: 'keyword.directive', foreground: '61afef' },
  { token: 'keyword.condition', foreground: 'c678dd' },
  { token: 'operator', foreground: 'c678dd' },
  { token: 'attribute', foreground: '56b6c2' },
  { token: 'attribute.flag', foreground: 'd19a66' },
  { token: 'predefined', foreground: 'e06c75' },
  { token: 'number', foreground: 'd19a66' },
  { token: 'number.address', foreground: 'b5cea8' },
  { token: 'string', foreground: '98c379' },
  { token: 'string.path', foreground: '98c379' },
  { token: 'variable.generated', foreground: 'e5c07b' },
  { token: 'comment', foreground: '6b7f78', fontStyle: 'italic' },
  { token: 'delimiter.bracket', foreground: 'abb2bf' },
  { token: 'identifier', foreground: 'd7dfdb' },
];

const themeColors: monaco.editor.IColors = {
  'editor.background': '#07110f',
  'editor.foreground': '#d7dfdb',
  'editorLineNumber.foreground': '#4b5f58',
  'editorLineNumber.activeForeground': '#9fb3ab',
  'editor.lineHighlightBackground': '#0f1d18',
  'editor.selectionBackground': '#1f4a33',
  'editorGutter.background': '#07110f',
  'minimap.background': '#081411',
  'editorHoverWidget.background': '#0d1a16',
  'editorHoverWidget.border': '#2c4a3d',
  'editorHoverWidget.foreground': '#d7dfdb',
  'editorSuggestWidget.background': '#0d1a16',
  'editorSuggestWidget.border': '#2c4a3d',
  'editorSuggestWidget.selectedBackground': '#13271d',
  'editorWidget.background': '#0d1a16',
  'editorWidget.border': '#2c4a3d',
  'textCodeBlock.background': '#07110f',
  'textLink.foreground': '#5fd38d',
  'scrollbarSlider.background': '#5fd38d22',
  'scrollbarSlider.hoverBackground': '#5fd38d44',
  'editorError.foreground': '#ff6b6b',
  'editorWarning.foreground': '#e5c07b',
  'diffEditor.insertedTextBackground': '#2ea04333',
  'diffEditor.removedTextBackground': '#f8514933',
};

/** Resolves a CSS color (any syntax, incl. var()/oklch) to #rrggbb[aa] via a canvas pixel. */
function cssColor(expression: string, fallback: string): string {
  try {
    const probe = document.createElement('span');
    probe.style.color = expression;
    document.body.appendChild(probe);
    const computed = getComputedStyle(probe).color;
    probe.remove();
    const canvas = document.createElement('canvas');
    canvas.width = canvas.height = 1;
    const context = canvas.getContext('2d', { willReadFrequently: true });
    if (!context || !computed) return fallback;
    context.fillStyle = computed;
    context.fillRect(0, 0, 1, 1);
    const [r, g, b, a] = context.getImageData(0, 0, 1, 1).data;
    const hex = (value: number) => value.toString(16).padStart(2, '0');
    return `#${hex(r)}${hex(g)}${hex(b)}${a < 255 ? hex(a) : ''}`;
  } catch {
    return fallback;
  }
}

/** Re-tints the editor chrome with the active NodeFlow appearance theme. */
function applyAppearance() {
  const field = cssColor('var(--nf-field)', '#07110f');
  const surface = cssColor('var(--nf-surface-soft)', '#0d1a16');
  const raised = cssColor('var(--nf-surface-raised)', '#0f1d18');
  const border = cssColor('var(--nf-border-strong)', '#2c4a3d');
  const accent = cssColor('var(--nf-accent)', '#5fd38d');
  const accentSoft = cssColor('var(--nf-accent-soft)', '#1f4a33');
  const text = cssColor('var(--nf-text)', '#d7dfdb');
  const muted = cssColor('var(--nf-text-tertiary)', '#4b5f58');
  monaco.editor.defineTheme(HAPROXY_THEME, {
    base: 'vs-dark',
    inherit: true,
    rules: [...themeRules, { token: 'keyword.section', foreground: accent.slice(1, 7), fontStyle: 'bold' }],
    colors: {
      ...themeColors,
      'editor.background': field,
      'editorGutter.background': field,
      'editor.foreground': text,
      'editorLineNumber.foreground': muted,
      'editor.lineHighlightBackground': raised,
      'editor.selectionBackground': accentSoft.length === 9 ? accentSoft.slice(0, 7) + '66' : accentSoft,
      'minimap.background': field,
      'editorHoverWidget.background': surface,
      'editorHoverWidget.border': border,
      'editorHoverWidget.foreground': text,
      'editorSuggestWidget.background': surface,
      'editorSuggestWidget.border': border,
      'editorSuggestWidget.selectedBackground': raised,
      'editorWidget.background': surface,
      'editorWidget.border': border,
      'textCodeBlock.background': field,
      'textLink.foreground': accent,
    },
  });
}

let registered = false;
export function setupHAProxyMonaco(): typeof monaco {
  if (!registered) { registerLanguage(); registered = true; }
  applyAppearance();
  return monaco;
}

export { monaco };
