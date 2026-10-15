import { ActionIcon, Button, Group, Menu, Modal, MultiSelect, Stack, Text, Textarea, TextInput } from '@mantine/core';
import { IconCopy, IconDotsVertical, IconEdit, IconPlus, IconTemplate, IconTrash } from '@tabler/icons-react';
import { useEffect, useState } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import type { NodeRecord, RouteRecord } from '../../lib/contracts';
import { copyRoutes, deleteTemplate, listTemplates, saveRouteTemplate, templateNodes, type CopyResult, type RouteTemplate } from './templates';
import { apiErrorText, t } from '../../i18n';

// Same Modal chrome as every other panel dialog (node settings, delete route).
const dialogClassNames = { content: 'nf-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' };
const COPY_NOTE = t('templates.copiesAreCreatedDisabledCounters');
const LISTENER_HINT = t('templates.onAnotherNodeCheckThe');

function errorText(e: unknown, fallback: string) {
  return e instanceof Error ? e.message : fallback;
}

function useDemoQuery() {
  const location = useLocation();
  return new URLSearchParams(location.search).get('demo') === '1';
}

/** Target nodes, name and per-node results shared by «Копировать на ноды» and template apply. */
function CopyTargets({ nodes, nodeID, targets, setTargets, name, setName, namePlaceholder, results, busy, onOpen }: {
  nodes: NodeRecord[]; nodeID: string; targets: string[]; setTargets: (v: string[]) => void;
  name: string; setName: (v: string) => void; namePlaceholder: string;
  results: CopyResult[]; busy: boolean; onOpen: (result: CopyResult) => void;
}) {
  const otherNode = targets.some((id) => id !== nodeID);
  const nodeName = (id: string) => nodes.find((n) => n.id === id)?.name || id;
  return <>
    <MultiSelect
      label={t('templates.targetNodes')} placeholder={targets.length ? '' : t('templates.selectNodes')} searchable maxValues={100}
      data={nodes.map((n) => ({ value: n.id, label: n.id === nodeID ? t('templates.nameThisNode', { name: n.name }) : n.name }))}
      value={targets} onChange={setTargets} disabled={busy}
      description={otherNode ? LISTENER_HINT : undefined}
    />
    <TextInput label={t('editor.routeName')} placeholder={namePlaceholder} value={name} onChange={(e) => setName(e.currentTarget.value)} maxLength={80} disabled={busy} />
    {results.length > 0 && <ul className="nf-copy-results" aria-label={t('templates.resultPerNode')}>
      {results.map((r) => <li key={r.node_id} className={r.error ? 'is-error' : 'is-ok'}>
        <strong>{nodeName(r.node_id)}</strong>
        <span>{r.error ? apiErrorText(r.error_code, r.error) : t('templates.draftCreated')}</span>
        {r.route && <Button size="xs" variant="subtle" onClick={() => onOpen(r)}>{t('templates.open')}</Button>}
      </li>)}
    </ul>}
  </>;
}

/** «Копировать на ноды»: disabled copies of one route on selected nodes. */
export function RouteCopyDialog({ opened, onClose, nodeID, source }: {
  opened: boolean; onClose: () => void; nodeID: string; source: RouteRecord | null;
}) {
  const navigate = useNavigate();
  const cache = useQueryClient();
  const demo = useDemoQuery();
  const [nodes, setNodes] = useState<NodeRecord[]>([]);
  const [targets, setTargets] = useState<string[]>([]);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [results, setResults] = useState<CopyResult[]>([]);
  useEffect(() => {
    if (!opened) return;
    let active = true;
    setError(''); setResults([]); setTargets([]); setName('');
    templateNodes().then((n) => { if (active) setNodes(n); }).catch((e) => { if (active) setError(errorText(e, t('templates.nodeListUnavailable'))); });
    return () => { active = false; };
  }, [opened, source?.id]);
  const copy = async () => {
    if (!source) return;
    setBusy(true); setError('');
    try {
      // An empty name keeps the source name; the copy on this node gets « (копия)».
      const copied = await copyRoutes(source, targets, name.trim());
      setResults(copied);
      // Keep failed targets selected, so a retry cannot duplicate successful copies.
      setTargets(copied.filter((r) => r.error).map((r) => r.node_id));
      await cache.invalidateQueries({ queryKey: ['node-detail'] });
    } catch (e) { setError(errorText(e, t('templates.copiesNotCreated'))); } finally { setBusy(false); }
  };
  const done = results.length > 0 && targets.length === 0;
  return <Modal opened={opened} onClose={busy ? () => {} : onClose} title={t('node.copyToNodes')} size="md" closeOnClickOutside={!busy} closeOnEscape={!busy} classNames={dialogClassNames}>
    <Stack gap="md">
      <Text size="sm" c="dimmed">{COPY_NOTE}</Text>
      <CopyTargets
        nodes={nodes} nodeID={nodeID} targets={targets} setTargets={setTargets}
        name={name} setName={setName} namePlaceholder={source?.name || ''} results={results} busy={busy}
        onOpen={(r) => { onClose(); navigate(`/nodes/${r.node_id}/routes/${r.route!.id}/edit${demo ? '?demo=1' : ''}`); }}
      />
      {error && <div className="nf-inline-error" role="alert">{error}</div>}
      <Group justify="flex-end">
        <Button variant="default" disabled={busy} onClick={onClose}>{done ? t('nodes.close') : t('node.cancel')}</Button>
        {!done && <Button leftSection={<IconCopy size={16} />} loading={busy} disabled={!targets.length} onClick={() => void copy()}>{t('templates.copy')}</Button>}
      </Group>
    </Stack>
  </Modal>;
}

/** «Сохранить как шаблон»: name and an optional short description. */
export function RouteTemplateSaveDialog({ opened, onClose, title = t('node.saveAsTemplate'), initialName, initialDescription = '', onSave }: {
  opened: boolean; onClose: () => void; title?: string; initialName: string; initialDescription?: string;
  onSave: (name: string, description: string) => Promise<void>;
}) {
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => {
    if (!opened) return;
    setName(initialName); setDescription(initialDescription); setError('');
  }, [opened]); // eslint-disable-line react-hooks/exhaustive-deps
  const save = async () => {
    setBusy(true); setError('');
    try { await onSave(name.trim(), description.trim()); } catch (e) { setError(errorText(e, t('templates.templateNotSaved'))); } finally { setBusy(false); }
  };
  return <Modal opened={opened} onClose={busy ? () => {} : onClose} title={title} size="md" closeOnClickOutside={!busy} closeOnEscape={!busy} classNames={dialogClassNames}>
    <Stack gap="md">
      <Text size="sm" c="dimmed">{t('templates.changingATemplateDoesNot')}</Text>
      <TextInput label={t('templates.name')} value={name} onChange={(e) => setName(e.currentTarget.value)} maxLength={80} disabled={busy} data-autofocus required />
      <Textarea label={t('templates.description')} placeholder={t('templates.optional')} rows={2} value={description} onChange={(e) => setDescription(e.currentTarget.value)} maxLength={1000} disabled={busy} />
      {error && <div className="nf-inline-error" role="alert">{error}</div>}
      <Group justify="flex-end">
        <Button variant="default" disabled={busy} onClick={onClose}>{t('node.cancel')}</Button>
        <Button leftSection={<IconTemplate size={16} />} loading={busy} disabled={!name.trim()} onClick={() => void save()}>{t('node.save')}</Button>
      </Group>
    </Stack>
  </Modal>;
}

/** Route menu entry point: saves a stored route as a template. */
export function RouteSaveTemplateDialog({ opened, onClose, source }: { opened: boolean; onClose: () => void; source: RouteRecord | null }) {
  return <RouteTemplateSaveDialog
    opened={opened} onClose={onClose} initialName={source?.name || ''}
    onSave={async (name, description) => { if (source) { await saveRouteTemplate(source, name, description); onClose(); } }}
  />;
}

function templateSummary(tpl: RouteTemplate): string {
  const c = tpl.config;
  const listener = `${String(c.listener_ip || '*')}:${String(c.listener_port || 443)}`;
  const snis = Array.isArray(c.snis) && c.snis.length ? c.snis.join(', ') : t('templates.fallback');
  return `${listener} · ${snis}`;
}

/** «Шаблоны маршрутов» on the node page: the library list and apply to nodes. */
export function RouteLibraryDialog({ opened, onClose, nodeID }: { opened: boolean; onClose: () => void; nodeID: string }) {
  const navigate = useNavigate();
  const cache = useQueryClient();
  const demo = useDemoQuery();
  const query = demo ? '&demo=1' : '';
  const [nodes, setNodes] = useState<NodeRecord[]>([]);
  const [templates, setTemplates] = useState<RouteTemplate[] | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [targets, setTargets] = useState<string[]>([]);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [results, setResults] = useState<CopyResult[]>([]);
  const [deleting, setDeleting] = useState<RouteTemplate | null>(null);
  const template = templates?.find((tpl) => tpl.id === selected);
  useEffect(() => {
    if (!opened) return;
    let active = true;
    setError(''); setResults([]); setSelected(null); setTargets([nodeID]); setName(''); setTemplates(null);
    Promise.all([templateNodes(), listTemplates()])
      .then(([n, list]) => { if (active) { setNodes(n); setTemplates(list); } })
      .catch((e) => { if (active) { setError(errorText(e, t('templates.templatesUnavailable'))); setTemplates([]); } });
    return () => { active = false; };
  }, [opened, nodeID]);
  const go = (path: string) => { onClose(); navigate(path); };
  const apply = async () => {
    if (!template) return;
    setBusy(true); setError('');
    try {
      const copied = await copyRoutes(template, targets, name.trim() || String(template.config.name || ''));
      setResults(copied);
      setTargets(copied.filter((r) => r.error).map((r) => r.node_id));
      await cache.invalidateQueries({ queryKey: ['node-detail'] });
    } catch (e) { setError(errorText(e, t('templates.routesNotCreated'))); } finally { setBusy(false); }
  };
  const remove = async () => {
    if (!deleting) return;
    setBusy(true); setError('');
    try {
      await deleteTemplate(deleting);
      if (selected === deleting.id) setSelected(null);
      setTemplates(await listTemplates());
      setDeleting(null);
    } catch (e) { setError(errorText(e, t('templates.templateNotDeleted'))); setDeleting(null); } finally { setBusy(false); }
  };
  const done = results.length > 0 && targets.length === 0;
  return <>
    <Modal opened={opened && !deleting} onClose={busy ? () => {} : onClose} title={t('node.routeTemplates')} size="lg" closeOnClickOutside={!busy} closeOnEscape={!busy} classNames={dialogClassNames}>
      <Stack gap="md">
        <Text size="sm" c="dimmed">{COPY_NOTE}</Text>
        {templates === null ? <Text size="sm" c="dimmed">{t('templates.loading')}</Text> : templates.length === 0
          ? <div className="nf-template-empty">{t('templates.noTemplatesSaveARoute')}</div>
          : <ul className="nf-template-list" role="listbox" aria-label={t('templates.templates')}>
            {templates.map((tpl) => <li key={tpl.id} role="option" aria-selected={tpl.id === selected} className={tpl.id === selected ? 'is-selected' : ''}
              onClick={() => { if (tpl.id !== selected) { setSelected(tpl.id); setResults([]); setName(''); } }}>
              <div className="nf-template-list__main">
                <strong>{tpl.name}</strong>
                <span>{templateSummary(tpl)}{tpl.description ? ` · ${tpl.description}` : ''}</span>
              </div>
              <Menu position="bottom-end" withinPortal>
                <Menu.Target><ActionIcon variant="subtle" color="gray" aria-label={t('nodes.actionsName', { name: tpl.name })} onClick={(e) => e.stopPropagation()}><IconDotsVertical size={16} /></ActionIcon></Menu.Target>
                <Menu.Dropdown>
                  <Menu.Item leftSection={<IconPlus size={16} />} onClick={() => go(`/nodes/${nodeID}/routes/new?template=${tpl.id}${query}`)}>{t('templates.configureForThisNode')}</Menu.Item>
                  <Menu.Item leftSection={<IconEdit size={16} />} onClick={() => go(`/nodes/${nodeID}/routes/new?template=${tpl.id}&edit_template=1${query}`)}>{t('templates.editTemplate')}</Menu.Item>
                  <Menu.Divider />
                  <Menu.Item color="red" leftSection={<IconTrash size={16} />} onClick={() => setDeleting(tpl)}>{t('node.delete')}</Menu.Item>
                </Menu.Dropdown>
              </Menu>
            </li>)}
          </ul>}
        {template && <CopyTargets
          nodes={nodes} nodeID={nodeID} targets={targets} setTargets={setTargets}
          name={name} setName={setName} namePlaceholder={String(template.config.name || '')} results={results} busy={busy}
          onOpen={(r) => go(`/nodes/${r.node_id}/routes/${r.route!.id}/edit${demo ? '?demo=1' : ''}`)}
        />}
        {error && <div className="nf-inline-error" role="alert">{error}</div>}
        <Group justify="flex-end">
          <Button variant="default" disabled={busy} onClick={onClose}>{done || !template ? t('nodes.close') : t('node.cancel')}</Button>
          {template && !done && <Button leftSection={<IconCopy size={16} />} loading={busy} disabled={!targets.length} onClick={() => void apply()}>{t('templates.createRoutes')}</Button>}
        </Group>
      </Stack>
    </Modal>
    <Modal opened={Boolean(deleting)} onClose={() => !busy && setDeleting(null)} title={t('templates.deleteTemplate')} classNames={dialogClassNames}>
      <div className="nf-confirm-dialog">
        <p>{t('templates.template')} <strong>{deleting?.name}</strong> {t('templates.willBeDeletedRoutesCreated')}</p>
        <div><Button variant="default" onClick={() => setDeleting(null)} disabled={busy}>{t('node.cancel')}</Button><Button color="red" leftSection={<IconTrash size={16} />} onClick={() => void remove()} loading={busy}>{t('node.delete')}</Button></div>
      </div>
    </Modal>
  </>;
}
