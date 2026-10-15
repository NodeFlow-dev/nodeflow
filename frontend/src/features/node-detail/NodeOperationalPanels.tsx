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
import { t, locale } from '../../i18n';

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
    'route.created': t('node.routeCreated'),
    'route.updated': t('node.routeChanged'),
    'route.enabled': t('node.routeEnabled'),
    'route.disabled': t('node.routeDisabled'),
    'route.deleted': t('node.routeDeleted'),
    'route.change': t('node.routeChanged'),
    'agent.update.assigned': t('node.nodeAgentUpdateScheduled'),
    'agent.update.assign': t('node.nodeAgentUpdateScheduled'),
    'agent.update.rollback': t('node.nodeAgentRollbackScheduled'),
    'firewall.policy.updated': t('node.ufwPolicyChanged'),
    'firewall.policy': t('node.ufwPolicyChanged'),
    'node.update': t('node.nodeSettingsChanged'),
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
    installed: t('node.installed'), idle: t('node.ready'), assigned: t('node.scheduled'), downloading: t('node.downloading'), verifying: t('node.verifying'), activating: t('node.installing'), failed: t('node.error'), rolled_back: t('node.autoRollback'),
  };
  return values[state ?? ''] ?? state ?? t('node.noData');
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
      setRollbackError(t('node.nodePlatformIsUnknownOr'));
      return;
    }
    setRollbackBusy(true);
    setRollbackError('');
    try {
      await onRollback(rollbackRelease, update.actual_sequence, update.desired_release?.sequence ?? 0);
      setRollbackRelease(null);
    } catch (reason) {
      setRollbackError(reason instanceof Error ? reason.message : t('node.rollbackNotScheduled'));
    } finally {
      setRollbackBusy(false);
    }
  };

  return (
    <div className="nf-detail-bottom-grid">
      <Surface id="operations" className="nf-detail-operations" title={t('node.updatesAndFirewall')}>
        <div className="nf-operation-row">
          <div className="nf-operation-label"><span>Node Agent</span><strong>{agentVersion || '—'}</strong></div>
          <div className="nf-operation-state"><span className={`nf-state-chip is-${update?.state ?? 'unknown'}`}>{updateStateCopy(update?.state)}</span><small>{platform ? latest ? t('node.platformlabelVersionAvailable', { platformLabel: platformLabel(platform), version: latest.version }) : t('node.platformlabelUpToDate', { platformLabel: platformLabel(platform) }) : t('node.platformNotReceived')}</small></div>
          <div className="nf-operation-actions">
            {newerReleases.length > 1 ? (
              <Menu position="bottom-end" withinPortal shadow="md">
                <Menu.Target>
                  <Button variant="default" size="xs" leftSection={<IconCloudUpload size={15} />} rightSection={<IconChevronDown size={14} />} disabled={!update || !platform || busyAction === 'update'} loading={busyAction === 'update'}>{t('nodes.refresh')}</Button>
                </Menu.Target>
                <Menu.Dropdown>
                  <Menu.Label>{t('node.selectVersion')}</Menu.Label>
                  {newerReleases.map((release) => (
                    <Menu.Item key={release.id} leftSection={<IconCloudUpload size={15} />} onClick={() => onAssignRelease(release)}>{releaseChoiceLabel(release)}</Menu.Item>
                  ))}
                </Menu.Dropdown>
              </Menu>
            ) : (
              <Button variant="default" size="xs" leftSection={<IconCloudUpload size={15} />} disabled={!update || !platform || !latest} loading={busyAction === 'update'} onClick={() => update && latest && platform && releaseMatchesPlatform(latest, platform) && onAssignRelease(latest)}>{t('nodes.refresh')}</Button>
            )}
            {olderReleases.length > 1 ? (
              <Menu position="bottom-end" withinPortal shadow="md">
                <Menu.Target>
                  <Button variant="subtle" color="gray" size="xs" leftSection={<IconArrowBackUp size={15} />} rightSection={<IconChevronDown size={14} />} disabled={!platform || busyAction === 'rollback'}>{t('node.rollBack')}</Button>
                </Menu.Target>
                <Menu.Dropdown>
                  <Menu.Label>{t('node.selectVersion')}</Menu.Label>
                  {olderReleases.map((release) => (
                    <Menu.Item key={release.id} leftSection={<IconArrowBackUp size={15} />} onClick={() => { setRollbackError(''); setRollbackRelease(release); }}>{releaseChoiceLabel(release)}</Menu.Item>
                  ))}
                </Menu.Dropdown>
              </Menu>
            ) : (
              <Button variant="subtle" color="gray" size="xs" leftSection={<IconArrowBackUp size={15} />} disabled={!platform || olderReleases.length === 0 || busyAction === 'rollback'} onClick={() => { setRollbackError(''); const release = olderReleases[0]; if (release && platform && releaseMatchesPlatform(release, platform)) setRollbackRelease(release); }}>{t('node.rollBack')}</Button>
            )}
          </div>
        </div>
        {!platform && <p className="nf-operation-error" role="status">{t('node.releaseSchedulingIsBlockedThe')}</p>}
        {(partialErrors.update || partialErrors.releases || update?.last_error) && <p className="nf-operation-error" role="alert">{update?.last_error || partialErrors.update || partialErrors.releases}</p>}
        <div className="nf-operation-row nf-operation-row--firewall">
          <div className="nf-operation-label"><span>{t('node.ufwPolicy')}</span><strong>{firewall?.mode === 'apply' ? t('node.autoOpenAllowed') : firewall?.mode === 'observe' ? t('node.checkOnly') : firewall?.mode === 'off' ? t('node.notManaged') : '—'}</strong></div>
          <div className="nf-listener-ports"><span>{t('node.plannedPorts')}</span><strong>{listenerPorts.length ? listenerPorts.join(', ') : t('node.noneActive')}</strong></div>
          <SegmentedControl
            size="xs"
            value={firewall?.mode ?? 'off'}
            onChange={(value) => onFirewallMode(value as FirewallMode)}
            data={[{ label: t('node.off'), value: 'off' }, { label: t('node.verifying'), value: 'observe' }, { label: t('node.auto2'), value: 'apply' }]}
            disabled={!firewall || busyAction === 'firewall'}
            aria-label={t('node.ufwManagementPolicy')}
          />
        </div>
        {partialErrors.firewall && <p className="nf-operation-error" role="alert">UFW: {partialErrors.firewall}</p>}
        <button className="nf-operation-disclosure" type="button" onClick={onOpenNodeSettings}>
          {t('node.advancedNodeSettings')} <IconSettings size={15} />
        </button>
      </Surface>

      <Surface className="nf-detail-audit" title={t('node.recentActions')}>
        {partialErrors.audit ? <div className="nf-detail-panel-state" role="alert">{partialErrors.audit}</div> : audit.length ? (
          <div className="nf-audit-scroll" role="list" aria-label={t('node.recentActionsScrollableList')}>
            {[...audit].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at)).map((entry) => (
              <div className="nf-audit-row" role="listitem" key={entry.id}>
                <i><AuditIcon action={entry.action} /></i>
                <strong title={auditCopy(entry)}>{auditCopy(entry)}</strong>
                <time dateTime={entry.created_at}>{new Intl.DateTimeFormat(locale(), { hour: '2-digit', minute: '2-digit' }).format(new Date(entry.created_at))}</time>
                <span>{entry.actor_type === 'agent' ? 'Agent' : t('app.operator')}</span>
                <em>{t('node.succeeded')}</em>
              </div>
            ))}
          </div>
        ) : <div className="nf-detail-panel-state">{t('node.noActionsYet')}</div>}
      </Surface>

      <Surface className="nf-detail-versions" title={t('node.versionsAndConnection')}>
        <dl>
          <div><dt>HAProxy</dt><Tooltip label={packageVersion || t('node.fullVersionNotReceived')} openDelay={250}><dd tabIndex={0}>{shortHAProxy(packageVersion)}</dd></Tooltip></div>
          <div><dt>Node Agent</dt><dd>{agentVersion || '—'}</dd></div>
          <div><dt>{t('node.mtlsConnection')}</dt><dd className={mtlsReady ? 'is-ready' : ''}><IconShieldCheck size={17} /> {mtlsReady ? t('node.connected') : t('node.noFreshHeartbeat')}</dd></div>
          <div><dt>{t('node.panelConnection')}</dt><dd>{t('node.outboundMtls')}{agentPort}</dd></div>
          <div><dt>{t('node.lastAuthorization')}</dt><Tooltip label={formatDateTime(credentialLastUsed)} openDelay={250}><dd tabIndex={0}>{credentialLastUsed ? timeAgo(credentialLastUsed) : '—'}</dd></Tooltip></div>
          <div><dt>{t('node.updatePolicy')}</dt><dd><IconSparkles size={16} /> {t('node.ed25519AutoRollback')}</dd></div>
        </dl>
      </Surface>

      <Modal opened={Boolean(rollbackRelease)} onClose={() => !rollbackBusy && setRollbackRelease(null)} title={t('node.rollBackNodeAgent')} classNames={{ content: 'nf-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}>
        <div className="nf-confirm-dialog">
          <p>{t('node.theSignedRelease')} <strong>{rollbackRelease ? releaseChoiceLabel(rollbackRelease) : '—'}</strong> {t('node.for')} <strong>{platformLabel(platform)}</strong>{t('node.willBeScheduledTheUpdater')}</p>
          {rollbackError && <div className="nf-inline-error" role="alert">{rollbackError}</div>}
          <div><Button variant="default" disabled={rollbackBusy} onClick={() => setRollbackRelease(null)}>{t('node.cancel')}</Button><Button leftSection={<IconArrowBackUp size={16} />} loading={rollbackBusy} onClick={doRollback}>{t('node.rollBack')}</Button></div>
        </div>
      </Modal>
    </div>
  );
}
