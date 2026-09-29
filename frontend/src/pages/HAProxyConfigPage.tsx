import { Alert, Badge, Button, Group, Modal, Select, Stack, Text, TextInput, Tooltip } from '@mantine/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { IconAlertTriangle, IconArrowBackUp, IconArrowLeft, IconCircleCheck, IconCircleX, IconDeviceFloppy, IconDownload, IconSend, IconWand } from '@tabler/icons-react';
import Editor, { DiffEditor, type OnMount } from '@monaco-editor/react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useBeforeUnload, useBlocker, useLocation, useNavigate, useParams } from 'react-router-dom';
import { LoginPanel } from '../components/LoginPanel';
import { PageHeader } from '../components/PageHeader';
import { StateView } from '../components/StateView';
import { DEMO_HAPROXY_CONFIG } from '../features/haproxy-editor/demoConfig';
import { formatConfig, lintConfig, MAX_CONFIG_BYTES, type LintIssue, type LintResult } from '../features/haproxy-editor/haproxyModel';
import { HAPROXY_LANGUAGE, HAPROXY_THEME, monaco, setupHAProxyMonaco } from '../features/haproxy-editor/monacoSetup';
import { isNodeDetailDemoMode } from '../features/node-detail/useNodeDetail';
import { api, APIError, isUnauthorized } from '../lib/api';
import type { ConfigRevision, NodeConfigState, NodeRecord } from '../lib/contracts';
import { demoSuffix } from '../lib/navigation';
import { HAProxyApplyErrorDetail } from './HAProxyApplyErrorDetail';
import './haproxy-config.css';

type Baseline = { text: string; revision: number | null };
type LintView = LintResult & { checkedAt: Date; local: boolean };
type ServerLint = { text: string; result: LintResult; checkedAt: Date };
type Notice = { tone: 'teal' | 'red' | 'yellow'; text: string } | null;

const MARKER_OWNER = 'haproxy-lint';
const stateCopy: Record<string, string> = {
  in_sync: 'применена', pending: 'ожидает агента', applying: 'применяется', applied: 'применена', failed: 'ошибка применения', drifted: 'расхождение', rolled_back: 'откат',
};
const stateColor: Record<string, string> = { in_sync: 'teal', applied: 'teal', pending: 'yellow', applying: 'yellow', failed: 'red', drifted: 'orange', rolled_back: 'orange' };

function plural(count: number, one: string, few: string, many: string): string {
  const mod10 = count % 10; const mod100 = count % 100;
  if (mod10 === 1 && mod100 !== 11) return one;
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return few;
  return many;
}

function errorText(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback;
}

function isManual(revision: ConfigRevision | undefined): boolean {
  return revision?.metadata?.source === 'advanced_editor';
}

function lintIssuesFromPayload(payload: unknown): LintIssue[] | null {
  const issues = (payload as { issues?: unknown } | null)?.issues;
  return Array.isArray(issues) ? issues as LintIssue[] : null;
}

/**
 * Server result for the same text wins; local errors the server did not
 * report are kept, so the status never says «valid» while the browser linter
 * sees an error.
 */
function mergeLint(local: LintResult, server: ServerLint | null, text: string, localAt: Date): LintView {
  if (!server || server.text !== text) return { ...local, checkedAt: localAt, local: true };
  const key = (issue: LintIssue) => `${issue.line}:${issue.column}:${issue.code}`;
  const seen = new Set(server.result.issues.map(key));
  const extra = local.issues.filter((issue) => issue.severity === 'error' && !seen.has(key(issue))
    && !server.result.issues.some((other) => other.line === issue.line && other.severity === 'error'));
  const issues = extra.length ? [...server.result.issues, ...extra].sort((a, b) => a.line - b.line || a.column - b.column) : server.result.issues;
  return { ...server.result, issues, valid: !issues.some((issue) => issue.severity === 'error'), checkedAt: server.checkedAt, local: false };
}

export function HAProxyConfigPage() {
  const { nodeId = '' } = useParams();
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const demo = isNodeDetailDemoMode();
  const suffix = demo ? '?demo=1' : demoSuffix(location.search);
  const base = `/api/v1/nodes/${encodeURIComponent(nodeId)}`;
  const nodeURL = `/nodes/${encodeURIComponent(nodeId)}${suffix}`;

  const editorRef = useRef<monaco.editor.IStandaloneCodeEditor | null>(null);
  const [text, setText] = useState('');
  const [baseline, setBaseline] = useState<Baseline | null>(null);
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState('');
  const [notice, setNotice] = useState<Notice>(null);
  const [serverResult, setServerResult] = useState<ServerLint | null>(null);
  const [serverLint, setServerLint] = useState(!demo);
  const [applyOpened, setApplyOpened] = useState(false);
  const [appliedText, setAppliedText] = useState<string | null>(null);
  const [initError, setInitError] = useState('');
  const bypassBlocker = useRef(false);

  const nodeQuery = useQuery({
    queryKey: ['haproxy-editor-node', nodeId, demo],
    queryFn: async (): Promise<NodeRecord> => {
      if (!demo) return api<NodeRecord>(base);
      const { demoNodeBundles } = await import('../fixtures/demo');
      return (demoNodeBundles.find((bundle) => bundle.node.id === nodeId) ?? demoNodeBundles[0]).node;
    },
  });
  const stateQuery = useQuery({
    queryKey: ['haproxy-config-state', nodeId],
    enabled: !demo,
    refetchInterval: 5000,
    queryFn: () => api<NodeConfigState>(`${base}/config-state`).catch((error: unknown) => {
      if (error instanceof APIError && error.status === 404) return null;
      throw error;
    }),
  });
  const revisionsQuery = useQuery({
    queryKey: ['haproxy-config-revisions', nodeId],
    enabled: !demo,
    queryFn: () => api<ConfigRevision[]>(`${base}/config-revisions`),
  });
  const configState = stateQuery.data ?? null;
  const revisions = useMemo(() => [...(revisionsQuery.data ?? [])].sort((a, b) => b.revision - a.revision), [revisionsQuery.data]);
  const desiredRevision = revisions.find((revision) => revision.revision === configState?.desired_revision);
  const manual = isManual(desiredRevision);

  // Instant local pass on every keystroke; the server result replaces it when
  // it arrives for the same text.
  const localLint = useMemo(() => (baseline === null ? null : { result: lintConfig(text), at: new Date() }), [baseline, text]);
  const lint = useMemo<LintView | null>(() => (localLint ? mergeLint(localLint.result, serverResult, text, localLint.at) : null), [localLint, serverResult, text]);

  const dirty = baseline !== null && text !== baseline.text;
  const needsSave = baseline !== null && (dirty || baseline.revision === null);
  const errors = lint?.issues.filter((issue) => issue.severity === 'error').length ?? 0;
  const warnings = (lint?.issues.length ?? 0) - errors;
  const tooLarge = new TextEncoder().encode(text).length > MAX_CONFIG_BYTES;
  const canEdit = !demo && baseline !== null && !busy;
  const canSave = canEdit && needsSave && text.trim() !== '' && !tooLarge;
  const alreadyDesired = !needsSave && baseline?.revision != null && baseline.revision === configState?.desired_revision;

  // Replaces editor text while keeping the undo stack. Model edits work even
  // while the editor is read-only (executeEdits would be ignored then).
  const replaceText = useCallback((value: string) => {
    const model = editorRef.current?.getModel();
    if (model && model.getValue() !== value) {
      model.pushStackElement();
      model.pushEditOperations([], [{ range: model.getFullModelRange(), text: value }], () => null);
      model.pushStackElement();
    }
    setText(value);
  }, []);

  const loadBaseline = useCallback((value: string, revision: number | null) => {
    replaceText(value);
    setBaseline({ text: value, revision });
  }, [replaceText]);

  // Initial content: desired revision, or a fresh render from routes.
  const initialised = useRef(false);
  useEffect(() => {
    if (initialised.current) return;
    if (demo) { initialised.current = true; loadBaseline(DEMO_HAPROXY_CONFIG, null); return; }
    if (!stateQuery.isSuccess || !revisionsQuery.isSuccess) return;
    initialised.current = true;
    const desired = stateQuery.data?.desired_revision;
    (async () => {
      try {
        if (desired) {
          const revision = await api<ConfigRevision>(`${base}/config-revisions/${desired}`);
          loadBaseline(revision.config, revision.revision);
          setNote('');
        } else {
          const rendered = await api<{ config: string }>(`${base}/render-config`, { method: 'POST' });
          loadBaseline(rendered.config, null);
          setNotice({ tone: 'yellow', text: 'Назначенной версии нет. Загружена сборка из маршрутов — сохраните её, чтобы применить.' });
        }
      } catch (error) {
        setInitError(errorText(error, 'Конфигурация не загрузилась'));
      }
    })();
  }, [base, demo, loadBaseline, revisionsQuery.isSuccess, stateQuery.data, stateQuery.isSuccess]);

  // Debounced server linter (full check incl. runtime names); stale responses
  // are dropped, and a response only applies to the text it was computed for.
  const lintSeq = useRef(0);
  useEffect(() => {
    if (baseline === null || !serverLint || demo) return;
    const seq = ++lintSeq.current;
    const config = text;
    const timer = window.setTimeout(async () => {
      try {
        const result = await api<LintResult>(`${base}/config-lint`, { method: 'POST', body: JSON.stringify({ config }) });
        if (seq !== lintSeq.current) return;
        setServerResult({ text: config, checkedAt: new Date(), result: { ...result, issues: result.issues ?? [], listener_tcp_ports: result.listener_tcp_ports ?? [], sections: result.sections ?? [] } });
      } catch (error) {
        if (error instanceof APIError && (error.status === 404 || error.status === 405)) setServerLint(false);
      }
    }, 250);
    return () => window.clearTimeout(timer);
  }, [base, baseline, demo, serverLint, text]);

  // Lint issues → Monaco markers (shown squiggly and at the top of the hover).
  useEffect(() => {
    const model = editorRef.current?.getModel();
    if (!model) return;
    const lines = model.getLineCount();
    monaco.editor.setModelMarkers(model, MARKER_OWNER, (lint?.issues ?? []).map((issue) => {
      const line = Math.min(Math.max(1, issue.line), lines);
      const maxColumn = model.getLineMaxColumn(line);
      const start = Math.min(Math.max(1, issue.column || 1), maxColumn);
      const end = Math.max(start + 1, Math.min(issue.end_column || maxColumn, maxColumn));
      return {
        startLineNumber: line, startColumn: start, endLineNumber: line, endColumn: end === start ? maxColumn : end,
        severity: issue.severity === 'error' ? monaco.MarkerSeverity.Error : monaco.MarkerSeverity.Warning,
        message: issue.message, code: issue.code, source: lint?.local ? 'NodeFlow (локально)' : 'NodeFlow',
      };
    }));
  }, [lint]);

  const refreshAll = useCallback(async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['haproxy-config-state', nodeId] }),
      queryClient.invalidateQueries({ queryKey: ['haproxy-config-revisions', nodeId] }),
      queryClient.invalidateQueries({ queryKey: ['node-detail', nodeId] }),
    ]);
  }, [nodeId, queryClient]);

  const run = async (label: string, action: () => Promise<void>) => {
    setBusy(label); setNotice(null);
    try { await action(); } catch (error) { setNotice({ tone: 'red', text: errorText(error, 'Операция не выполнена') }); } finally { setBusy(''); }
  };

  /** Creates a revision from the editor text; null when the server rejected it by lint. */
  const saveRevision = async (force: boolean): Promise<ConfigRevision | null> => {
    const config = text;
    try {
      const created = await api<ConfigRevision>(`${base}/config-revisions`, {
        method: 'POST',
        body: JSON.stringify({ config, note: note.trim(), metadata: { source: 'advanced_editor' }, ...(force ? { force: true } : {}) }),
      });
      // The Panel may rewrite the generated header; keep the stored text as the baseline.
      loadBaseline(created.config ?? config, created.revision);
      setNote('');
      await queryClient.invalidateQueries({ queryKey: ['haproxy-config-revisions', nodeId] });
      return created;
    } catch (error) {
      if (error instanceof APIError && error.status === 422 && error.code === 'config_lint_failed') {
        const issues = lintIssuesFromPayload(error.payload);
        if (issues) setServerResult({ text: config, checkedAt: new Date(), result: { valid: false, issues, listener_tcp_ports: lint?.listener_tcp_ports ?? [], sections: lint?.sections ?? [] } });
        const count = issues?.filter((issue) => issue.severity === 'error').length ?? 0;
        setNotice({ tone: 'red', text: `Не сохранено: ${count || 'есть'} ${plural(count || 5, 'ошибка', 'ошибки', 'ошибок')} проверки. Исправьте их или нажмите «Всё равно сохранить».` });
        return null;
      }
      throw error;
    }
  };

  const save = (force = false) => {
    if (!canSave) return;
    void run(force ? 'force' : 'save', async () => {
      const created = await saveRevision(force);
      if (created) setNotice({ tone: 'teal', text: `Версия v${created.revision} сохранена. Нажмите «Применить на ноду», чтобы отправить её агенту.` });
    });
  };
  const saveRef = useRef(save);
  saveRef.current = save;

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === 's') {
        event.preventDefault();
        saveRef.current(false);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  const openApply = () => {
    if (!canEdit || !text.trim()) return;
    if (errors > 0 && !window.confirm(`В конфиге ${errors} ${plural(errors, 'ошибка', 'ошибки', 'ошибок')}, агент скорее всего отклонит его (haproxy -c). Всё равно применить?`)) return;
    setAppliedText(null);
    setApplyOpened(true);
    const actual = configState?.actual_revision;
    if (!actual) { setAppliedText(''); return; }
    api<ConfigRevision>(`${base}/config-revisions/${actual}`)
      .then((revision) => setAppliedText(revision.config))
      .catch(() => setAppliedText(''));
  };

  const apply = () => run('apply', async () => {
    let revision = baseline?.revision ?? null;
    if (needsSave) {
      // Errors were explicitly confirmed in openApply.
      const created = await saveRevision(errors > 0);
      if (!created) { setApplyOpened(false); return; }
      revision = created.revision;
    }
    if (!revision) return;
    await api(`${base}/desired-revision`, { method: 'PUT', body: JSON.stringify({ revision }) });
    setApplyOpened(false);
    setNotice({ tone: 'teal', text: `Версия v${revision} назначена. Агент проверит её через haproxy -c и применит мягким reload — статус обновится выше.` });
    await refreshAll();
  });

  const confirmReplace = (question: string) => !dirty || window.confirm(question);

  const loadFromRoutes = () => {
    if (!confirmReplace('Заменить несохранённый текст сборкой из маршрутов?')) return;
    void run('render', async () => {
      const rendered = await api<{ config: string }>(`${base}/render-config`, { method: 'POST' });
      loadBaseline(rendered.config, null);
      setNotice({ tone: 'teal', text: 'Загружена сборка из текущих маршрутов и настроек ноды. Её можно отредактировать и сохранить как ручную версию.' });
    });
  };

  const resumeGenerated = () => {
    if (!window.confirm('Вернуться к сборке из маршрутов UI? Будет назначена конфигурация из текущих маршрутов, ручные версии останутся в истории.')) return;
    void run('resume', async () => {
      const created = await api<ConfigRevision>(`${base}/generated-config`, { method: 'POST' });
      loadBaseline(created.config, created.revision);
      setNotice({ tone: 'teal', text: `Назначена сборка из маршрутов (v${created.revision}). Изменения маршрутов в UI снова доступны.` });
      await refreshAll();
    });
  };

  const loadRevision = (value: string | null) => {
    if (!value || Number(value) === baseline?.revision) return;
    if (!confirmReplace(`Загрузить v${value}? Несохранённые изменения будут потеряны.`)) return;
    void run('history', async () => {
      const revision = await api<ConfigRevision>(`${base}/config-revisions/${value}`);
      loadBaseline(revision.config, revision.revision);
      setNotice({ tone: 'teal', text: `Загружена версия v${revision.revision}${revision.note ? ` · ${revision.note}` : ''}.` });
    });
  };

  const format = () => {
    const formatted = formatConfig(text);
    if (formatted !== text) replaceText(formatted);
    editorRef.current?.focus();
  };

  const jumpToFirstIssue = () => {
    const first = lint?.issues.find((issue) => issue.severity === 'error') ?? lint?.issues[0];
    const editor = editorRef.current;
    if (!first || !editor) return;
    editor.revealLineInCenter(first.line);
    editor.setPosition({ lineNumber: first.line, column: Math.max(1, first.column) });
    editor.focus();
  };

  // Leaving with unsaved text: router blocker + browser unload prompt.
  const blocker = useBlocker(({ currentLocation, nextLocation }) => !bypassBlocker.current && dirty && currentLocation.pathname !== nextLocation.pathname);
  useBeforeUnload((event) => {
    if (!dirty) return;
    event.preventDefault();
    event.returnValue = '';
  });

  const textRef = useRef(text);
  textRef.current = text;
  const onMount: OnMount = (editor) => {
    editorRef.current = editor;
    // Text loaded before Monaco finished mounting.
    if (textRef.current) editor.setValue(textRef.current);
  };

  if (nodeQuery.isError && isUnauthorized(nodeQuery.error)) return <LoginPanel onSuccess={() => { void nodeQuery.refetch(); }} />;
  const node = nodeQuery.data;
  const loadError = initError || (stateQuery.error ? errorText(stateQuery.error, '') : '') || (revisionsQuery.error ? errorText(revisionsQuery.error, '') : '');
  if (loadError && baseline === null) {
    return <main className="nf-page"><StateView title="Конфигурация не загрузилась" description={loadError} tone="error" action={<Button variant="default" onClick={() => navigate(nodeURL)}>К ноде</Button>} /></main>;
  }

  const statusText = !lint
    ? 'Проверка…'
    : errors || warnings
      ? `${lint.checkedAt.toLocaleTimeString('ru-RU')} | ${[errors ? `${errors} ${plural(errors, 'ошибка', 'ошибки', 'ошибок')}` : '', warnings ? `${warnings} ${plural(warnings, 'предупреждение', 'предупреждения', 'предупреждений')}` : ''].filter(Boolean).join(', ')}`
      : `${lint.checkedAt.toLocaleTimeString('ru-RU')} | Конфиг валиден`;
  const statusTone = !lint ? 'is-pending' : errors ? 'is-error' : warnings ? 'is-warning' : 'is-valid';
  const history = revisions.map((revision) => ({
    value: String(revision.revision),
    label: [
      `v${revision.revision}`,
      isManual(revision) ? 'ручной' : 'из маршрутов',
      revision.revision === configState?.desired_revision ? 'назначена' : '',
      revision.revision === configState?.actual_revision ? 'применена' : '',
      revision.note ?? '',
    ].filter(Boolean).join(' · '),
  }));

  return (
    <main className="nf-page nf-haproxy-editor-page">
      <PageHeader
        breadcrumb={<><Link to={`/nodes${suffix}`}>Ноды</Link><span>/</span><Link to={nodeURL}>{node?.name ?? nodeId}</Link><span>/</span><span aria-current="page">HAProxy</span></>}
        backAction={<Button variant="subtle" color="gray" px={6} onClick={() => navigate(nodeURL)} aria-label="Назад к ноде"><IconArrowLeft size={20} /></Button>}
        title={`Конфигурация HAProxy${node ? ` · ${node.name}` : ''}`}
        badge={<Badge variant="light" color={manual ? 'orange' : 'teal'}>{manual ? 'Ручной конфиг' : 'Сборка из маршрутов'}</Badge>}
        meta={<>
          {node && <code>{node.address}</code>}
          {!demo && <span className="nf-haproxy-editor__revisions">
            Назначена {configState?.desired_revision ? `v${configState.desired_revision}` : '—'} · Применена {configState?.actual_revision ? `v${configState.actual_revision}` : '—'} · <Text span inherit c={stateColor[configState?.state ?? ''] ?? 'dimmed'}>{configState ? stateCopy[configState.state] ?? configState.state : 'нет данных'}</Text>
          </span>}
        </>}
      />

      {demo && <Alert color="yellow" mb="sm">Демонстрация: показан пример конфигурации только для чтения. Сохранение и применение доступны при подключении к Panel API.</Alert>}
      {configState?.last_error && <Alert color="red" mb="sm" icon={<IconAlertTriangle size={18} />} title="Ошибка применения на ноде">{configState.last_error}<HAProxyApplyErrorDetail detail={configState.last_error_detail} onRevealLine={(line) => { const editor = editorRef.current; if (!editor) return; editor.revealLineInCenter(line); editor.setPosition({ lineNumber: line, column: 1 }); editor.focus(); }} /></Alert>}
      {notice && <Alert color={notice.tone} mb="sm" withCloseButton onClose={() => setNotice(null)}>{notice.text}</Alert>}

      <section className="nf-haproxy-editor">
        <div className={`nf-haproxy-editor__status ${statusTone}`}>
          <button type="button" onClick={jumpToFirstIssue} disabled={!lint?.issues.length} title={lint?.issues.length ? 'Перейти к первой проблеме' : undefined}>
            {errors ? <IconCircleX size={16} /> : warnings ? <IconAlertTriangle size={16} /> : <IconCircleCheck size={16} />}
            <span>{statusText}</span>
          </button>
          <Group gap={8} wrap="nowrap">
            {lint?.local && !demo && <Tooltip label={serverLint ? 'Показана мгновенная проверка в браузере, проверка на сервере выполняется' : 'Проверка на сервере недоступна — используется проверка в браузере'}><Badge variant="outline" color="gray">локальная проверка</Badge></Tooltip>}
            {!!lint?.listener_tcp_ports.length && <Badge variant="light" color="gray" className="nf-haproxy-editor__ports">Порты: {lint.listener_tcp_ports.join(', ')}</Badge>}
            {dirty && <Badge variant="light" color="yellow">Есть несохранённые изменения</Badge>}
          </Group>
        </div>
        <div className="nf-haproxy-editor__monaco">
          <Editor
            language={HAPROXY_LANGUAGE}
            theme={HAPROXY_THEME}
            defaultValue=""
            beforeMount={() => { setupHAProxyMonaco(); }}
            onMount={onMount}
            onChange={(value) => setText(value ?? '')}
            loading={<div className="nf-route-loader" aria-label="Загрузка редактора" />}
            options={{
              readOnly: demo || baseline === null,
              fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
              fontSize: 13,
              tabSize: 4,
              insertSpaces: true,
              minimap: { enabled: true },
              wordWrap: 'off',
              automaticLayout: true,
              scrollBeyondLastLine: false,
              renderWhitespace: 'selection',
              fixedOverflowWidgets: true,
              hover: { enabled: true, delay: 250, sticky: true },
              quickSuggestions: { other: true, comments: false, strings: false },
              padding: { top: 8 },
            }}
          />
        </div>
        <div className="nf-haproxy-editor__toolbar">
          <Group gap={8} wrap="wrap">
            <Button leftSection={<IconDeviceFloppy size={16} />} disabled={!canSave} loading={busy === 'save'} onClick={() => save(false)}>Сохранить</Button>
            {errors > 0 && <Button color="red" variant="light" disabled={!canSave} loading={busy === 'force'} onClick={() => save(true)}>Всё равно сохранить</Button>}
            <Button variant="default" leftSection={<IconWand size={16} />} disabled={demo || baseline === null || !!busy} onClick={format}>Форматировать</Button>
            <TextInput className="nf-haproxy-editor__note" placeholder="Комментарий" aria-label="Комментарий к версии" maxLength={500} value={note} disabled={!canEdit} onChange={(event) => setNote(event.currentTarget.value)} />
            <span className="nf-haproxy-editor__divider" />
            <Tooltip label={alreadyDesired ? 'Эта версия уже назначена ноде' : needsSave ? 'Сохранит текст новой версией и назначит её ноде' : 'Назначить версию ноде'}>
              <Button color="teal" variant="filled" leftSection={<IconSend size={16} />} disabled={!canEdit || !text.trim() || tooLarge || alreadyDesired} loading={busy === 'apply'} onClick={openApply}>Применить на ноду</Button>
            </Tooltip>
          </Group>
          <Group gap={8} wrap="wrap" className="nf-haproxy-editor__toolbar-right">
            <Select className="nf-haproxy-editor__history" placeholder="История" aria-label="История версий" data={history} value={baseline?.revision != null ? String(baseline.revision) : null} onChange={loadRevision} disabled={!canEdit || !history.length} searchable nothingFoundMessage="Нет версий" comboboxProps={{ width: 360, position: 'top-end' }} />
            <Button variant="default" leftSection={<IconDownload size={16} />} disabled={!canEdit} loading={busy === 'render'} onClick={loadFromRoutes}>Загрузить из маршрутов</Button>
            <Tooltip label={manual ? 'Назначить конфиг из текущих маршрутов' : 'Уже используется сборка из маршрутов'}>
              <Button variant="default" leftSection={<IconArrowBackUp size={16} />} disabled={!canEdit || !manual} loading={busy === 'resume'} onClick={resumeGenerated}>Вернуться к сборке из UI</Button>
            </Tooltip>
          </Group>
        </div>
        {tooLarge && <Text size="xs" c="red" mt={6}>Конфиг больше 512 КиБ — сохранение невозможно.</Text>}
      </section>

      <Modal opened={applyOpened} onClose={() => !busy && setApplyOpened(false)} title="Применить конфигурацию на ноду?" size="90%" closeOnClickOutside={!busy} classNames={{ content: 'nf-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}>
        <Stack gap="sm">
          <Text size="sm" c="dimmed">
            Слева — {configState?.actual_revision ? `применённая v${configState.actual_revision}` : 'на ноде ещё нет применённой версии'}, справа — {needsSave ? 'текст из редактора (будет сохранён новой версией)' : `v${baseline?.revision}`}.
          </Text>
          <div className="nf-haproxy-editor__diff">
            {appliedText === null
              ? <div className="nf-route-loader" aria-label="Загрузка применённой версии" />
              : <DiffEditor original={appliedText} modified={text} language={HAPROXY_LANGUAGE} theme={HAPROXY_THEME} originalModelPath={`inmemory://nodeflow/${nodeId}/applied.cfg`} modifiedModelPath={`inmemory://nodeflow/${nodeId}/pending.cfg`} keepCurrentOriginalModel keepCurrentModifiedModel beforeMount={() => { setupHAProxyMonaco(); }} options={{ readOnly: true, originalEditable: false, renderSideBySide: true, minimap: { enabled: false }, fontSize: 13, automaticLayout: true, scrollBeyondLastLine: false }} />}
          </div>
          <Alert color="yellow" icon={<IconAlertTriangle size={18} />}>Пока активен ручной конфиг, изменения маршрутов в UI блокируются (409). Агент проверит конфиг через haproxy -c перед применением.</Alert>
          {errors > 0 && <Alert color="red">В конфиге {errors} {plural(errors, 'ошибка', 'ошибки', 'ошибок')} проверки — агент скорее всего отклонит его. Версия будет сохранена принудительно.</Alert>}
          <Group justify="flex-end">
            <Button variant="default" disabled={!!busy} onClick={() => setApplyOpened(false)}>Отмена</Button>
            <Button color="teal" leftSection={<IconSend size={16} />} loading={busy === 'apply'} onClick={apply}>{needsSave ? 'Сохранить и применить' : 'Применить'}</Button>
          </Group>
        </Stack>
      </Modal>

      <Modal opened={blocker.state === 'blocked'} onClose={() => blocker.state === 'blocked' && blocker.reset()} title="Выйти без сохранения?" size="sm" classNames={{ content: 'nf-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}>
        <div className="nf-confirm-dialog"><p>Несохранённые изменения конфигурации будут потеряны. Нода не изменится.</p><div>
          <Button variant="default" onClick={() => blocker.state === 'blocked' && blocker.reset()}>Остаться</Button>
          <Button color="red" onClick={() => { bypassBlocker.current = true; if (blocker.state === 'blocked') blocker.proceed(); }}>Выйти</Button>
        </div></div>
      </Modal>
    </main>
  );
}
