import { Alert, Button, Group, Modal, MultiSelect, Select, Stack, Text, Textarea, TextInput } from '@mantine/core';
import { useEffect, useState } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import type { NodeRecord, RouteRecord } from '../../lib/contracts';
import { copyRoutes, deleteTemplate, listTemplates, saveRouteTemplate, templateNodes, type CopyResult, type RouteTemplate } from './templates';

export function RouteLibraryDialog({ opened, onClose, nodeID, source }: {
  opened: boolean; onClose: () => void; nodeID: string; source?: RouteRecord;
}) {
  const navigate = useNavigate();
  const location = useLocation();
  const cache = useQueryClient();
  const [nodes, setNodes] = useState<NodeRecord[]>([]);
  const [templates, setTemplates] = useState<RouteTemplate[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [targets, setTargets] = useState<string[]>([]);
  const [name, setName] = useState('');
  const [templateName, setTemplateName] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [message, setMessage] = useState('');
  const [results, setResults] = useState<CopyResult[]>([]);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const template = templates.find(t => t.id === selected);
  const current = source || template;
  const query = new URLSearchParams(location.search).get('demo') === '1' ? '&demo=1' : '';
  useEffect(() => {
    if (!opened) return;
    let active = true;
    setError(''); setMessage(''); setResults([]); setSelected(null); setConfirmDelete(false);
    setTargets([nodeID]); setName(source?.name || ''); setTemplateName(source?.name || ''); setDescription(''); setBusy(true);
    Promise.all([templateNodes(), listTemplates()]).then(([n, t]) => {
      if (active) { setNodes(n); setTemplates(t); }
    }).catch(e => { if (active) setError(e.message); }).finally(() => { if (active) setBusy(false); });
    return () => { active = false; };
  }, [opened, nodeID, source?.id]);
  const perform = async (action: () => Promise<void>) => {
    setBusy(true); setError(''); setMessage('');
    try { await action(); } catch (e) { setError(e instanceof Error ? e.message : 'Операция не выполнена'); }
    finally { setBusy(false); }
  };
  const apply = () => perform(async () => {
    if (!current) return;
    const copied = await copyRoutes(current, targets, name);
    setResults(copied);
    // Keep failed targets selected, so retry cannot duplicate successful copies.
    setTargets(copied.filter(r => r.error).map(r => r.node_id));
    await cache.invalidateQueries({ queryKey: ['node-detail'] });
    setMessage(`Создано черновиков: ${copied.filter(r => r.route).length}. Включите их после проверки настроек на целевых нодах.`);
  });
  return <Modal opened={opened} onClose={busy ? () => {} : onClose} title={source ? `Копирование маршрута «${source.name}»` : 'Шаблоны маршрутов'} size="lg">
    <Stack gap="md">
      <Text size="sm" c="dimmed">Копии создаются выключенными. Настройки сохраняются, но состояние соединений, счётчики и история не переносятся.</Text>
      {error && <Alert color="red">{error}</Alert>}
      {message && <Alert color="green">{message}</Alert>}
      {!source && <>
        <Select label="Шаблон" placeholder={templates.length ? 'Выберите шаблон' : 'Нет шаблонов — сохраните любой маршрут как шаблон'} searchable
          data={templates.map(t => ({ value: t.id, label: t.name }))} value={selected} disabled={busy}
          onChange={id => { setSelected(id); setName(String(templates.find(t => t.id === id)?.config.name || '')); setConfirmDelete(false); setResults([]); }} />
        {template && <>
          <Text size="sm">{template.description || 'Без описания'}</Text>
          <Text size="sm" c="dimmed">{String(template.config.listener_ip || '*')}:{String(template.config.listener_port || 443)} · {Array.isArray(template.config.snis) ? template.config.snis.join(', ') : 'Фолбэк'} · версия {template.version}</Text>
          <Group>
            <Button variant="default" disabled={busy} onClick={() => { onClose(); navigate(`/nodes/${nodeID}/routes/new?template=${template.id}${query}`); }}>Настроить для этой ноды</Button>
            <Button variant="default" disabled={busy} onClick={() => { onClose(); navigate(`/nodes/${nodeID}/routes/new?template=${template.id}&edit_template=1${query}`); }}>Редактировать настройки</Button>
            <Button color="red" variant="subtle" disabled={busy} onClick={() => {
              if (!confirmDelete) { setConfirmDelete(true); return; }
              void perform(async () => { await deleteTemplate(template); setTemplates(await listTemplates()); setSelected(null); setConfirmDelete(false); });
            }}>{confirmDelete ? 'Подтвердить удаление шаблона' : 'Удалить шаблон'}</Button>
          </Group>
        </>}
      </>}
      {current && <>
        <MultiSelect label="Целевые ноды" maxValues={100} placeholder="Выберите одну или несколько нод" searchable
          data={nodes.map(n => ({ value: n.id, label: n.name }))} value={targets} onChange={setTargets} disabled={busy} />
        <TextInput label="Имя новых маршрутов" value={name} onChange={e => setName(e.currentTarget.value)} maxLength={80} disabled={busy} />
        <Text size="sm" c="dimmed">Проверьте listener IP, локальные Unix-сокеты, доступность бэкендов и версию агента на целевых нодах. При включении штатная проверка обнаружит конфликты SNI и портов.</Text>
        <Button loading={busy} disabled={!targets.length || !name.trim()} onClick={() => void apply()}>Создать копии на выбранных нодах</Button>
      </>}
      {results.map(r => <Group key={r.node_id} justify="space-between">
        <Text size="sm">{nodes.find(n => n.id === r.node_id)?.name || r.node_id}: {r.error || 'Черновик создан'}</Text>
        {r.route && <Button size="xs" variant="subtle" onClick={() => { onClose(); navigate(`/nodes/${r.node_id}/routes/${r.route!.id}/edit${query ? '?demo=1' : ''}`); }}>Открыть</Button>}
      </Group>)}
      {source && <>
        <Text fw={600}>Сохранить настройки в общую библиотеку</Text>
        <TextInput label="Название шаблона" value={templateName} onChange={e => setTemplateName(e.currentTarget.value)} maxLength={80} disabled={busy} />
        <Textarea label="Описание шаблона" value={description} onChange={e => setDescription(e.currentTarget.value)} maxLength={1000} disabled={busy} />
        <Button variant="default" loading={busy} disabled={!templateName.trim()} onClick={() => void perform(async () => {
          const value = await saveRouteTemplate(source, templateName, description); setTemplates(await listTemplates()); setMessage(`Шаблон «${value.name}» сохранён. Изменения шаблона не меняют существующие маршруты.`);
        })}>Сохранить как шаблон</Button>
      </>}
    </Stack>
  </Modal>;
}
