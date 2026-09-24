import { Button, Menu, Modal, SegmentedControl, Tooltip } from '@mantine/core';
import {
  IconArrowBackUp, IconCheck, IconChevronDown, IconCloudUpload, IconFirewallCheck, IconRoute, IconSettings, IconShieldCheck, IconSparkles,
} from '@tabler/icons-react';
import { useMemo, useState } from 'react';
import type {
  AgentRelease, AuditEntry, FirewallMode, HeartbeatMetrics, NodeAgentUpdateState, NodeFirewallPolicy, RouteRecord,
} from '../../lib/contracts';
import { formatDateTime, shortHAProxy, timeAgo } from '../../lib/format';
import { Surface } from '../../components/Surface';
import { heartbeatPlatform, platformLabel, releaseMatchesPlatform } from '../releases/platform';

interface NodeOperationalPanelsProps {
  metrics?: HeartbeatMetrics;
  agentVersion?: string;
  agentPort: number;
  mtlsReady: boolean;
  credentialLastUsed?: string;
  routes: RouteRecord[];
  firewall: NodeFirewallPolicy | null;
  update: NodeAgentUpdateState | null;
  releases: AgentRelease[];
  audit: AuditEntry[];
  partialErrors: Partial<Record<'firewall' | 'update' | 'releases' | 'audit', string>>;
  busyAction?: string;
  onFirewallMode: (mode: FirewallMode) => Promise<void>;
  onAssignRelease: (release: AgentRelease) => Promise<void>;
  onRollback: (release: AgentRelease, actualSequence: number, desiredSequence: number) => Promise<void>;
  onOpenNodeSettings: () => void;
}

function auditCopy(entry: AuditEntry): string {
  const summary = entry.details && typeof entry.details.summary === 'string' ? entry.details.summary : '';
  if (summary) return summary;
  const labels: Record<string, string> = {
    'route.created': 'Создан маршрут',
    'route.updated': 'Изменён маршрут',
    'route.enabled': 'Маршрут включён',
    'route.disabled': 'Маршрут выключен',
    'route.deleted': 'Удалён маршрут',
    'route.change': 'Изменён маршрут',
    'agent.update.assigned': 'Назначено обновление Node Agent',
    'agent.update.assign': 'Назначено обновление Node Agent',
    'agent.update.rollback': 'Назначен откат Node Agent',
    'firewall.policy.updated': 'Изменена политика UFW',
    'firewall.policy': 'Изменена политика UFW',
    'node.update': 'Изменены параметры ноды',
  };
  return labels[entry.action] ?? entry.action.replaceAll('.', ' · ');
}

function AuditIcon({ action }: { action: string }) {
  if (action.startsWith('route')) return <IconRoute size={16} />;
  if (action.startsWith('firewall')) return <IconFirewallCheck size={16} />;
  if (action.includes('update')) return <IconCloudUpload size={16} />;
  return <IconCheck size={16} />;
}

function updateStateCopy(state?: string) {
  const values: Record<string, string> = {
    installed: 'Установлено', idle: 'Готов', assigned: 'Назначено', downloading: 'Загрузка', verifying: 'Проверка', activating: 'Установка', failed: 'Ошибка', rolled_back: 'Автооткат',
  };
  return values[state ?? ''] ?? state ?? 'Нет данных';
}

function releaseChoiceLabel(release: AgentRelease) {
  return `${release.version} · sequence #${release.sequence}`;
}

export function NodeOperationalPanels({
  metrics, agentVersion, agentPort, mtlsReady, credentialLastUsed, routes, firewall, update, releases, audit, partialErrors,
  busyAction, onFirewallMode, onAssignRelease, onRollback, onOpenNodeSettings,
}: NodeOperationalPanelsProps) {
  const [rollbackRelease, setRollbackRelease] = useState<AgentRelease | null>(null);
  const [rollbackBusy, setRollbackBusy] = useState(false);
  const [rollbackError, setRollbackError] = useState('');
  const platform = heartbeatPlatform(metrics);
  const compatible = useMemo(() => releases.filter((release) => releaseMatchesPlatform(release, platform)).sort((a, b) => b.sequence - a.sequence), [releases, platform?.os, platform?.arch]);
  const actualSequence = update?.actual_sequence ?? 0;
  const newerReleases = compatible.filter((release) => release.sequence > actualSequence);
  const olderReleases = compatible.filter((release) => release.sequence < actualSequence);
  const latest = newerReleases[0];
  const listenerPorts = [...new Set(routes.filter((route) => route.enabled).map((route) => route.listener_port))].sort((a, b) => a - b);
  const packageVersion = String(metrics?.haproxy_version ?? '');
  const doRollback = async () => {
    if (!rollbackRelease || !update) return;
    if (!platform || !releaseMatchesPlatform(rollbackRelease, platform)) {
      setRollbackError('Платформа ноды неизвестна или изменилась. Обновите состояние перед откатом.');
      return;
    }
    setRollbackBusy(true);
    setRollbackError('');
    try {
      await onRollback(rollbackRelease, update.actual_sequence, update.desired_release?.sequence ?? 0);
      setRollbackRelease(null);
    } catch (reason) {
      setRollbackError(reason instanceof Error ? reason.message : 'Откат не назначен');
    } finally {
      setRollbackBusy(false);
    }
  };

  return (
    <div className="nf-detail-bottom-grid">
      <Surface id="operations" className="nf-detail-operations" title="Обновление и firewall">
        <div className="nf-operation-row">
          <div className="nf-operation-label"><span>Node Agent</span><strong>{agentVersion || '—'}</strong></div>
          <div className="nf-operation-state"><span className={`nf-state-chip is-${update?.state ?? 'unknown'}`}>{updateStateCopy(update?.state)}</span><small>{platform ? latest ? `${platformLabel(platform)} · доступна ${latest.version}` : `${platformLabel(platform)} · обновлений нет` : 'платформа не получена'}</small></div>
          <div className="nf-operation-actions">
            {newerReleases.length > 1 ? (
              <Menu position="bottom-end" withinPortal shadow="md">
                <Menu.Target>
                  <Button variant="default" size="xs" leftSection={<IconCloudUpload size={15} />} rightSection={<IconChevronDown size={14} />} disabled={!update || !platform || busyAction === 'update'} loading={busyAction === 'update'}>Обновить</Button>
                </Menu.Target>
                <Menu.Dropdown>
                  <Menu.Label>Выберите версию</Menu.Label>
                  {newerReleases.map((release) => (
                    <Menu.Item key={release.id} leftSection={<IconCloudUpload size={15} />} onClick={() => onAssignRelease(release)}>{releaseChoiceLabel(release)}</Menu.Item>
                  ))}
                </Menu.Dropdown>
              </Menu>
            ) : (
              <Button variant="default" size="xs" leftSection={<IconCloudUpload size={15} />} disabled={!update || !platform || !latest} loading={busyAction === 'update'} onClick={() => update && latest && platform && releaseMatchesPlatform(latest, platform) && onAssignRelease(latest)}>Обновить</Button>
            )}
            {olderReleases.length > 1 ? (
              <Menu position="bottom-end" withinPortal shadow="md">
                <Menu.Target>
                  <Button variant="subtle" color="gray" size="xs" leftSection={<IconArrowBackUp size={15} />} rightSection={<IconChevronDown size={14} />} disabled={!platform || busyAction === 'rollback'}>Откатить</Button>
                </Menu.Target>
                <Menu.Dropdown>
                  <Menu.Label>Выберите версию</Menu.Label>
                  {olderReleases.map((release) => (
                    <Menu.Item key={release.id} leftSection={<IconArrowBackUp size={15} />} onClick={() => { setRollbackError(''); setRollbackRelease(release); }}>{releaseChoiceLabel(release)}</Menu.Item>
                  ))}
                </Menu.Dropdown>
              </Menu>
            ) : (
              <Button variant="subtle" color="gray" size="xs" leftSection={<IconArrowBackUp size={15} />} disabled={!platform || olderReleases.length === 0 || busyAction === 'rollback'} onClick={() => { setRollbackError(''); const release = olderReleases[0]; if (release && platform && releaseMatchesPlatform(release, platform)) setRollbackRelease(release); }}>Откатить</Button>
            )}
          </div>
        </div>
        {!platform && <p className="nf-operation-error" role="status">Назначение релиза заблокировано: Agent ещё не передал os и arch.</p>}
        {(partialErrors.update || partialErrors.releases || update?.last_error) && <p className="nf-operation-error" role="alert">{update?.last_error || partialErrors.update || partialErrors.releases}</p>}
        <div className="nf-operation-row nf-operation-row--firewall">
          <div className="nf-operation-label"><span>Политика UFW</span><strong>{firewall?.mode === 'apply' ? 'Автооткрытие разрешено' : firewall?.mode === 'observe' ? 'Только проверка' : firewall?.mode === 'off' ? 'Не управляется' : '—'}</strong></div>
          <div className="nf-listener-ports"><span>Порты по плану</span><strong>{listenerPorts.length ? listenerPorts.join(', ') : 'нет активных'}</strong></div>
          <SegmentedControl
            size="xs"
            value={firewall?.mode ?? 'off'}
            onChange={(value) => onFirewallMode(value as FirewallMode)}
            data={[{ label: 'Выкл.', value: 'off' }, { label: 'Проверка', value: 'observe' }, { label: 'Авто', value: 'apply' }]}
            disabled={!firewall || busyAction === 'firewall'}
            aria-label="Политика управления UFW"
          />
        </div>
        {partialErrors.firewall && <p className="nf-operation-error" role="alert">UFW: {partialErrors.firewall}</p>}
        <button className="nf-operation-disclosure" type="button" onClick={onOpenNodeSettings}>
          Дополнительные настройки ноды <IconSettings size={15} />
        </button>
      </Surface>

      <Surface className="nf-detail-audit" title="Последние действия">
        {partialErrors.audit ? <div className="nf-detail-panel-state" role="alert">{partialErrors.audit}</div> : audit.length ? (
          <div className="nf-audit-scroll" role="list" aria-label="Последние действия, прокручиваемый список">
            {[...audit].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at)).map((entry) => (
              <div className="nf-audit-row" role="listitem" key={entry.id}>
                <i><AuditIcon action={entry.action} /></i>
                <strong title={auditCopy(entry)}>{auditCopy(entry)}</strong>
                <time dateTime={entry.created_at}>{new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit' }).format(new Date(entry.created_at))}</time>
                <span>{entry.actor_type === 'agent' ? 'Agent' : 'Оператор'}</span>
                <em>Успешно</em>
              </div>
            ))}
          </div>
        ) : <div className="nf-detail-panel-state">Действий пока нет.</div>}
      </Surface>

      <Surface className="nf-detail-versions" title="Версии и соединение">
        <dl>
          <div><dt>HAProxy</dt><Tooltip label={packageVersion || 'Полная версия не получена'} openDelay={250}><dd tabIndex={0}>{shortHAProxy(packageVersion)}</dd></Tooltip></div>
          <div><dt>Node Agent</dt><dd>{agentVersion || '—'}</dd></div>
          <div><dt>mTLS соединение</dt><dd className={mtlsReady ? 'is-ready' : ''}><IconShieldCheck size={17} /> {mtlsReady ? 'На связи' : 'Нет свежего сигнала'}</dd></div>
          <div><dt>Связь с Panel</dt><dd>Исходящий mTLS · :{agentPort}</dd></div>
          <div><dt>Последняя авторизация</dt><Tooltip label={formatDateTime(credentialLastUsed)} openDelay={250}><dd tabIndex={0}>{credentialLastUsed ? timeAgo(credentialLastUsed) : '—'}</dd></Tooltip></div>
          <div><dt>Политика обновления</dt><dd><IconSparkles size={16} /> Ed25519 · автооткат</dd></div>
        </dl>
      </Surface>

      <Modal opened={Boolean(rollbackRelease)} onClose={() => !rollbackBusy && setRollbackRelease(null)} title="Откатить Node Agent?" classNames={{ content: 'nf-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}>
        <div className="nf-confirm-dialog">
          <p>Будет назначен подписанный релиз <strong>{rollbackRelease ? releaseChoiceLabel(rollbackRelease) : '—'}</strong> для <strong>{platformLabel(platform)}</strong>. Updater проверит Ed25519, SHA-256 и вернёт текущий бинарник при ошибке запуска.</p>
          {rollbackError && <div className="nf-inline-error" role="alert">{rollbackError}</div>}
          <div><Button variant="default" disabled={rollbackBusy} onClick={() => setRollbackRelease(null)}>Отмена</Button><Button leftSection={<IconArrowBackUp size={16} />} loading={rollbackBusy} onClick={doRollback}>Откатить</Button></div>
        </div>
      </Modal>
    </div>
  );
}
