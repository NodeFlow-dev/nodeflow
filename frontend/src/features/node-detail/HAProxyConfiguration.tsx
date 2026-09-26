import { Alert, Button, Group, NumberInput, Select, SimpleGrid, Stack, Tabs, Text, Textarea, TextInput } from '@mantine/core';
import { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api, APIError } from '../../lib/api';
import type { NodeRecord } from '../../lib/contracts';

type Settings = { max_connections?: number; threads?: number; timeout_connect?: string; timeout_client?: string; timeout_server?: string };
type Revision = { revision: number; config: string; note?: string; metadata: Record<string, unknown> };
type ConfigState = { desired_revision: number | null; actual_revision: number | null; state: string; last_error?: string };

export function HAProxyConfiguration({ node, demo, onSaved, onEditorState }: { node: NodeRecord; demo: boolean; onSaved: (node: NodeRecord) => void; onEditorState: (dirty: boolean, busy: boolean) => void }) {
  const base = `/api/v1/nodes/${node.id}`;
  const [settings, setSettings] = useState<Settings>((node.metadata?.haproxy_settings as Settings) ?? {});
  const [config, setConfig] = useState('');
  const [savedConfig, setSavedConfig] = useState('');
  const [revision, setRevision] = useState<string | null>(null);
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [message, setMessage] = useState('');
  const dirty = config !== savedConfig;
  const settingsDirty = JSON.stringify(settings) !== JSON.stringify((node.metadata?.haproxy_settings as Settings) ?? {});
  useEffect(() => { onEditorState(dirty || settingsDirty, busy); }, [dirty, settingsDirty, busy, onEditorState]);
  const query = useQuery({
    queryKey: ['haproxy-config', node.id], enabled: !demo,
    queryFn: async () => {
      const revisions = await api<Revision[]>(`${base}/config-revisions`);
      const state = revisions.length ? await api<ConfigState>(`${base}/config-state`).catch((error: unknown) => {
        if (error instanceof APIError && error.status === 404) return null;
        throw error;
      }) : null;
      const active = revisions.find((r) => r.revision === state?.desired_revision)
        ?? (state?.desired_revision ? await api<Revision>(`${base}/config-revisions/${state.desired_revision}`) : null);
      return { revisions, state, active };
    }, refetchInterval: 5000,
  });
  const state = query.data?.state;
  const manual = query.data?.active?.metadata.source === 'advanced_editor';
  useEffect(() => {
    if (!dirty && !settingsDirty) return;
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); };
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [dirty, settingsDirty]);
  const run = async (action: () => Promise<void>) => {
    setBusy(true); setError(''); setMessage('');
    try { await action(); } catch (error) { setError(error instanceof Error ? error.message : 'Операция не выполнена'); }
    finally { setBusy(false); }
  };
  const replace = async (action: () => Promise<void>) => {
    if (dirty && !window.confirm('Заменить несохранённый текст конфигурации?')) return;
    await run(action);
  };
  const loadRevision = async (value: string) => {
    const loaded = await api<Revision>(`${base}/config-revisions/${value}`);
    setConfig(loaded.config); setSavedConfig(loaded.config); setRevision(value); setNote(loaded.note ?? '');
  };
  const saveSettings = () => run(async () => {
    // Read current metadata so opening this editor does not overwrite newer node settings.
    const current = await api<NodeRecord>(base);
    const updated = await api<NodeRecord>(base, { method: 'PUT', body: JSON.stringify({ name: current.name, address: current.address, metadata: { ...current.metadata, haproxy_settings: settings } }) });
    onSaved(updated); setMessage(manual ? 'Параметры сохранены для следующей сборки из маршрутов. Ручной конфиг не изменён.' : (state?.desired_revision ? 'Параметры сохранены. Активная конфигурация из маршрутов будет обновлена агентом.' : 'Параметры сохранены для следующей сборки из маршрутов.'));
    await query.refetch();
  });
  const generate = () => {
    if (settingsDirty) { setError('Сначала сохраните параметры Global / defaults, чтобы они вошли в сборку.'); return; }
    return replace(async () => {
    const rendered = await api<{config: string}>(`${base}/render-config`, { method: 'POST' });
    setConfig(rendered.config); setSavedConfig(''); setRevision(null); setNote('');
    setMessage('Загружена сборка из сохранённых маршрутов и параметров. Текст можно редактировать и сохранить как ручную версию.');
    });
  };
  const saveDraft = () => run(async () => {
    const created = await api<Revision>(`${base}/config-revisions`, { method: 'POST', body: JSON.stringify({config, note, metadata: {source: 'advanced_editor'}}) });
    setRevision(String(created.revision)); setSavedConfig(config); setMessage(`Версия ${created.revision} сохранена. Для отправки на ноду нажмите «Применить версию».`); await query.refetch();
  });
  const assign = () => run(async () => {
    await api(`${base}/desired-revision`, {method: 'PUT', body: JSON.stringify({revision: Number(revision)})});
    setMessage('Версия назначена. Агент проверит конфиг перед применением; результат появится ниже.'); await query.refetch();
  });
  const resume = async () => {
    if (settingsDirty) { setError('Сначала сохраните параметры Global / defaults.'); return; }
    if (!window.confirm('Вернуться к конфигурации из сохранённых маршрутов? Ручная версия останется в истории.')) return;
    await replace(async () => {
      const created = await api<Revision>(`${base}/generated-config`, {method: 'POST'});
      setConfig(created.config); setSavedConfig(created.config); setRevision(String(created.revision)); setNote(created.note ?? '');
      setMessage('Назначена конфигурация из маршрутов. Ожидается применение агентом.'); await query.refetch();
    });
  };
  return <Stack>
    {demo && <Alert>Демонстрация интерфейса. Сохранение и применение доступны при подключении к Panel API.</Alert>}
    <Text size="sm">Режим: {manual ? 'Advanced — ручная конфигурация' : 'Сборка из маршрутов'}. Назначена: {state?.desired_revision ?? '—'} · Применена: {state?.actual_revision ?? '—'} · Статус: {state?.state ?? 'нет данных'}</Text>
    {state?.last_error && <Alert color="red" title="Ошибка применения">{state.last_error}</Alert>}
    {(error || query.error) && <Alert color="red">{error || query.error?.message}</Alert>}
    {message && <Alert color="teal">{message}</Alert>}
    <Tabs defaultValue="settings">
      <Tabs.List><Tabs.Tab value="settings">Global / defaults</Tabs.Tab><Tabs.Tab value="advanced">Advanced</Tabs.Tab></Tabs.List>
      <Tabs.Panel value="settings" pt="md"><Stack>
        <Text size="sm" c="dimmed">Параметры HAProxy для всех маршрутов этой ноды. Пустые поля используют значения по умолчанию. Сохранение обновляет активную сборку из маршрутов.</Text>
        <SimpleGrid cols={{base: 1, sm: 2}}>
        <NumberInput label="Максимум соединений · maxconn" placeholder="Автоматически" min={1} max={10000000} allowDecimal={false} value={settings.max_connections || ''} onChange={(v) => setSettings({...settings, max_connections: typeof v === 'number' ? v : undefined})} />
        <NumberInput label="Потоки · nbthread" placeholder="Автоматически" min={1} max={256} allowDecimal={false} value={settings.threads || ''} onChange={(v) => setSettings({...settings, threads: typeof v === 'number' ? v : undefined})} />
        </SimpleGrid>
        <SimpleGrid cols={{base: 1, sm: 3}}>
        {(['connect', 'client', 'server'] as const).map((key) => <TextInput key={key} label={`timeout ${key}`} placeholder={key === 'connect' ? '5s' : '15m'} description="Положительное значение с единицей ms, s, m или h" value={settings[`timeout_${key}`] ?? ''} onChange={(e) => setSettings({...settings, [`timeout_${key}`]: e.currentTarget.value})} />)}
        </SimpleGrid>
        <Button disabled={demo || busy || query.isLoading || query.isError} loading={busy} onClick={saveSettings}>Сохранить параметры</Button>
      </Stack></Tabs.Panel>
      <Tabs.Panel value="advanced" pt="md"><Stack>
        <Alert color="yellow">При применении ручной версии автоматическая публикация маршрутов блокируется до возврата к сборке из UI. Произвольный конфиг не синхронизируется обратно в формы маршрутов. Runtime-метрики, квоты и управление firewall зависят от структуры конфигурации.</Alert>
        <Group>
          <Button variant="default" disabled={demo || busy} onClick={generate}>Загрузить из UI</Button>
          <Button variant="default" disabled={demo || busy || !state?.desired_revision} onClick={() => replace(() => loadRevision(String(state?.desired_revision)))}>Загрузить назначенную</Button>
          <Button variant="default" disabled={demo || busy} onClick={resume}>Вернуться к сборке из UI</Button>
        </Group>
        <Select label="История конфигураций" placeholder="Загрузить версию" value={revision} data={(query.data?.revisions ?? []).map((r) => ({value: String(r.revision), label: `v${r.revision}${r.note ? ` · ${r.note}` : ''}`}))} disabled={demo || busy} onChange={(v) => { if (v) void replace(() => loadRevision(v)); }} />
        <Textarea label="haproxy.cfg" disabled={busy} description={dirty ? 'Есть несохранённые изменения' : 'Сохранённый текст'} value={config} onChange={(e) => {setConfig(e.currentTarget.value);}} autosize minRows={18} maxRows={30} spellCheck={false} styles={{input: {fontFamily: 'monospace', fontSize: 13, tabSize: 4}}} />
        <TextInput label="Комментарий к версии" maxLength={500} value={note} onChange={(e) => setNote(e.currentTarget.value)} />
        <Group justify="space-between"><Text size="xs" c="dimmed">Лимит: 512 КиБ. Проверка HAProxy выполняется агентом перед применением.</Text><Group>
          <Button variant="default" disabled={demo || busy || !config.trim() || new TextEncoder().encode(config).length > 524288} onClick={saveDraft}>Сохранить версию</Button>
          <Button disabled={demo || busy || dirty || !revision} onClick={assign}>Применить версию</Button>
        </Group></Group>
      </Stack></Tabs.Panel>
    </Tabs>
  </Stack>;
}
