import {
  Alert, Button, Checkbox, Group, Loader, Modal, NumberInput, PasswordInput, SegmentedControl, Select, Stack,
  Switch, Textarea, TextInput,
} from '@mantine/core';
import { IconAlertCircle, IconCheck, IconKey, IconRefresh, IconServer, IconShieldCheck } from '@tabler/icons-react';
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../../lib/api';
import type {
  AgentRelease, BootstrapAuthMode, BootstrapJobResponse, BootstrapNodeRequest, BootstrapSudoMode, HostKeyResult,
} from '../../lib/contracts';
import { locale, t } from '../../i18n';

export interface ReinstallNodeTarget {
  id: string;
  name: string;
  address: string;
  sshPort?: number;
  agentPort?: number;
  allowFirewallApply?: boolean;
  os?: string;
  arch?: string;
}

interface AddNodeDialogProps {
  opened: boolean;
  onClose: () => void;
  onInstalled?: () => void;
  reinstallTarget?: ReinstallNodeTarget;
  demo?: boolean;
}
type WizardStep = 1 | 2 | 3;

const defaultAlgorithm: HostKeyResult['algorithm'] = 'ssh-ed25519';
const automaticReleaseValue = '__automatic__';
const demoReleases: AgentRelease[] = [
  { id: '00000000-0000-4000-8000-000000000005', version: '0.4.5-dev', os: 'linux', arch: 'amd64', sha256: 'demo', size_bytes: 7_340_032, sequence: 5, signature: 'demo', created_at: new Date().toISOString() },
  { id: '00000000-0000-4000-8000-000000000006', version: '0.4.5-dev', os: 'linux', arch: 'arm64', sha256: 'demo', size_bytes: 6_980_608, sequence: 6, signature: 'demo', created_at: new Date().toISOString() },
];
const bootstrapStageLabels: Record<string, string> = {
  queued: t('nodes.jobIsWaitingForA'), installing: t('nodes.preparingInstallation'), configuration: t('nodes.checkingConfiguration'),
  binary: t('nodes.preparingNodeAgent'), updater_binary: t('nodes.preparingSafeUpdate'), identity: t('nodes.creatingMtlsIdentity'),
  authentication: t('nodes.checkingSshAuthentication'), connect: t('nodes.connectingOverSsh'), prepare: t('nodes.preparingServer'), upload: t('nodes.uploadingNodeAgent'),
  updater_upload: t('nodes.uploadingUpdateComponent'), privilege: t('nodes.checkingRootSudoPrivileges'), install: t('nodes.installingAndStartingNodeAgent'),
  create_node: t('nodes.registeringNodeInThePanel'), generate_token: t('nodes.creatingAgentCredentials'), store_token: t('nodes.savingAgentCredentials'),
  lookup_node: t('nodes.checkingNode'), verify_credential: t('nodes.checkingNewMtlsChannel'),
  revoke_credentials: t('nodes.revokingPreviousCredentials'), credential_cleanup: t('nodes.cleaningUpCredentials'),
  rollback: t('nodes.restoringPreviousCredentials'), finalize: t('nodes.finishingReinstall'),
  firewall_policy: t('nodes.applyingUfwPolicy'),
  timeout: t('nodes.installationTimedOut'), installed: t('nodes.nodeAgentInstalled'),
};
const bootstrapStatusLabels: Record<BootstrapJobResponse['status'], string> = {
  queued: t('nodes.pending'), running: t('nodes.running'), installed: t('nodes.done'), failed: t('node.error'),
};

function formatJournalTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleTimeString(locale(), { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

export function AddNodeDialog({ opened, onClose, onInstalled, reinstallTarget, demo = false }: AddNodeDialogProps) {
  const reinstall = Boolean(reinstallTarget);
  const [step, setStep] = useState<WizardStep>(1);
  const [name, setName] = useState('');
  const [authMode, setAuthMode] = useState<BootstrapAuthMode>('password');
  const [sudoMode, setSudoMode] = useState<BootstrapSudoMode>('auto');
  const [address, setAddress] = useState('');
  const [port, setPort] = useState<number | string>(22);
  const [agentPort, setAgentPort] = useState<number | string>(4200);
  const [username, setUsername] = useState('root');
  const [password, setPassword] = useState('');
  const [privateKey, setPrivateKey] = useState('');
  const [privateKeyPassphrase, setPrivateKeyPassphrase] = useState('');
  const [sudoPassword, setSudoPassword] = useState('');
  const [hostKeyAlgorithm, setHostKeyAlgorithm] = useState<HostKeyResult['algorithm']>(defaultAlgorithm);
  const [hostKey, setHostKey] = useState<HostKeyResult | null>(null);
  const [hostKeyAccepted, setHostKeyAccepted] = useState(false);
  const [allowFirewallApply, setAllowFirewallApply] = useState(true);
  const [haproxyLogs, setHAProxyLogs] = useState(true);
  const [detectedOS, setDetectedOS] = useState('');
  const [detectedArch, setDetectedArch] = useState('');
  const [releases, setReleases] = useState<AgentRelease[]>([]);
  const [selectedReleaseID, setSelectedReleaseID] = useState(automaticReleaseValue);
  const [releasesLoading, setReleasesLoading] = useState(false);
  const [releasesError, setReleasesError] = useState('');
  const [job, setJob] = useState<BootstrapJobResponse | null>(null);
  const [confirmCloseOpened, setConfirmCloseOpened] = useState(false);
  const [pending, setPending] = useState(false);
  const [polling, setPolling] = useState(false);
  const [pollError, setPollError] = useState('');
  const [error, setError] = useState('');
  const [journalOpen, setJournalOpen] = useState(false);
  const onInstalledRef = useRef(onInstalled);
  const announcedInstalledJob = useRef('');
  const submitInFlight = useRef(false);

  useEffect(() => { onInstalledRef.current = onInstalled; }, [onInstalled]);
  useEffect(() => {
    if (!opened) return;
    setName(reinstallTarget?.name ?? '');
    setAddress(reinstallTarget?.address ?? '');
    setPort(reinstallTarget?.sshPort ?? 22);
    setAgentPort(reinstallTarget?.agentPort ?? 4200);
    setAllowFirewallApply(reinstallTarget?.allowFirewallApply ?? true);
    setHAProxyLogs(true);
    setDetectedOS(reinstallTarget?.os?.trim().toLowerCase() || '');
    setDetectedArch(reinstallTarget?.arch?.trim().toLowerCase() || '');
  }, [opened, reinstallTarget?.id, reinstallTarget?.allowFirewallApply, reinstallTarget?.agentPort, reinstallTarget?.sshPort, reinstallTarget?.os, reinstallTarget?.arch]);

  useEffect(() => {
    if (!opened) return;
    let cancelled = false;
    setReleasesLoading(true);
    setReleasesError('');
    void (demo ? Promise.resolve(demoReleases) : api<AgentRelease[]>('/api/v1/agent-releases'))
      .then((value) => { if (!cancelled) setReleases([...value].sort((a, b) => b.sequence - a.sequence)); })
      .catch((reason) => { if (!cancelled) { setReleases([]); setReleasesError(reason instanceof Error ? reason.message : t('nodes.failedToLoadAgentReleases')); } })
      .finally(() => { if (!cancelled) setReleasesLoading(false); });
    return () => { cancelled = true; };
  }, [opened, demo]);

  const selectedRelease = releases.find((release) => release.id === selectedReleaseID);
  const automaticRelease = selectedReleaseID === automaticReleaseValue;
  const manualReleaseMismatch = Boolean(selectedRelease && detectedOS && detectedArch
    && (selectedRelease.os.toLowerCase() !== detectedOS || selectedRelease.arch.toLowerCase() !== detectedArch));
  const releaseOptions = [
    { value: automaticReleaseValue, label: t('nodes.latestCompatibleVersionAutomatic') },
    ...releases.map((release) => ({
      value: release.id,
      label: `${release.version} · ${release.os}/${release.arch} · sequence #${release.sequence}`,
    })),
  ];

  const clearSecrets = () => {
    setPassword('');
    setPrivateKey('');
    setPrivateKeyPassphrase('');
    setSudoPassword('');
  };
  const reset = () => {
    clearSecrets();
    setStep(1); setName(''); setAuthMode('password'); setSudoMode('auto'); setAddress(''); setPort(22);
    setAgentPort(4200); setUsername('root'); setHostKeyAlgorithm(defaultAlgorithm); setHostKey(null);
    setHostKeyAccepted(false); setAllowFirewallApply(true); setJob(null); setConfirmCloseOpened(false); setPending(false);
    setDetectedOS(''); setDetectedArch(''); setReleases([]); setSelectedReleaseID(automaticReleaseValue); setReleasesLoading(false); setReleasesError('');
    setPolling(false); setPollError(''); setError(''); setJournalOpen(false); announcedInstalledJob.current = ''; submitInFlight.current = false;
  };
  const resetAndClose = () => {
    reset();
    onClose();
  };
  const dirty = Boolean(name.trim() !== (reinstallTarget?.name ?? '') || address.trim() !== (reinstallTarget?.address ?? '')
    || password || privateKey || privateKeyPassphrase || sudoPassword
    || username !== 'root' || Number(port) !== (reinstallTarget?.sshPort ?? 22) || Number(agentPort) !== (reinstallTarget?.agentPort ?? 4200) || authMode !== 'password'
    || sudoMode !== 'auto' || allowFirewallApply !== (reinstallTarget?.allowFirewallApply ?? true) || !haproxyLogs
    || selectedReleaseID !== automaticReleaseValue || hostKey);
  const jobActive = job?.status === 'queued' || job?.status === 'running';
  const requestClose = () => {
    if (pending) return;
    if (jobActive) { setConfirmCloseOpened(true); return; }
    if (step === 3 || !dirty) resetAndClose(); else setConfirmCloseOpened(true);
  };

  const pollJob = useCallback(async (jobID: string) => {
    setPolling(true); setPollError('');
    try {
      const next = await api<BootstrapJobResponse>(`/api/v1/bootstrap/${encodeURIComponent(jobID)}`);
      setJob(next);
      if (next.status === 'installed' && announcedInstalledJob.current !== next.job_id) {
        announcedInstalledJob.current = next.job_id;
        onInstalledRef.current?.();
      }
      return next;
    } catch (reason) {
      setPollError(reason instanceof Error ? reason.message : t('nodes.failedToGetInstallationStatus'));
      return null;
    } finally { setPolling(false); }
  }, []);

  useEffect(() => {
    if (!job || (job.status !== 'queued' && job.status !== 'running')) return undefined;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const tick = async () => {
      const next = await pollJob(job.job_id);
      if (!stopped && next && (next.status === 'queued' || next.status === 'running')) timer = setTimeout(tick, 1_000);
      if (!stopped && !next) timer = setTimeout(tick, 2_000);
    };
    timer = setTimeout(tick, 350);
    return () => { stopped = true; if (timer) clearTimeout(timer); };
  }, [job?.job_id, job?.status, pollJob]);

  useEffect(() => {
    if (job?.status === 'failed') setJournalOpen(true);
  }, [job?.job_id, job?.status]);

  const scanHostKey = async (event: FormEvent) => {
    event.preventDefault();
    setPending(true); setError(''); setHostKey(null); setHostKeyAccepted(false);
    try {
      if (demo) {
        setHostKey({ algorithm: hostKeyAlgorithm, fingerprint: 'SHA256:NodeFlowDemoHostKeyFingerprint00000000000' });
        setStep(2);
        return;
      }
      const result = await api<HostKeyResult>('/api/v1/bootstrap/host-key', {
        method: 'POST',
        body: JSON.stringify({ address: address.trim(), ssh_port: Number(port), algorithm: hostKeyAlgorithm }),
      });
      if (result.os) setDetectedOS(result.os.trim().toLowerCase());
      if (result.arch) setDetectedArch(result.arch.trim().toLowerCase());
      setHostKey(result);
      setStep(2);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t('nodes.sshCheckFailed'));
    } finally { setPending(false); }
  };

  const install = async () => {
    if (!hostKey || !hostKeyAccepted || manualReleaseMismatch || submitInFlight.current) return;
    submitInFlight.current = true;
    const request: BootstrapNodeRequest = {
      name: name.trim(), address: address.trim(), ssh_port: Number(port), username: username.trim(), auth_mode: authMode,
      sudo_mode: sudoMode, agent_port: Number(agentPort), host_key_sha256: hostKey.fingerprint,
      host_key_algorithm: hostKey.algorithm, allow_firewall_apply: allowFirewallApply,
      ...(!reinstall && !haproxyLogs ? { haproxy_logs: false } : {}),
      ...(!automaticRelease ? { release_id: selectedReleaseID } : {}),
      ...(authMode === 'password' ? { password } : { private_key: privateKey, private_key_passphrase: privateKeyPassphrase }),
      ...(sudoPassword ? { sudo_password: sudoPassword } : {}),
    };
    const body = JSON.stringify(request);
    clearSecrets();
    setPending(true); setError('');
    try {
      const created = demo
        ? {
          job_id: `demo-${Date.now()}`, status: 'installed' as const, stage: 'installed',
          node_id: reinstallTarget?.id ?? '00000000-0000-4000-8000-000000000001',
          created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
        }
        : await api<BootstrapJobResponse>(reinstall
          ? `/api/v1/nodes/${encodeURIComponent(reinstallTarget!.id)}/reinstall`
          : '/api/v1/bootstrap', { method: 'POST', body });
      setJob(created); setStep(3);
      if (created.status === 'installed') onInstalledRef.current?.();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : reinstall ? t('nodes.failedToReinstallNodeAgent') : t('nodes.failedToInstallNodeAgent'));
      setStep(1);
    } finally { submitInFlight.current = false; setPending(false); }
  };

  const connectionValid = name.trim() && address.trim() && username.trim() && Number(port) > 0 && Number(agentPort) > 0
    && (authMode === 'password' ? password : privateKey)
    && (sudoMode !== 'password' || authMode === 'password' || sudoPassword);
  const selectedAgentPort = Number.isInteger(Number(agentPort)) && Number(agentPort) > 0 ? Number(agentPort) : '—';

  return (<>
    <Modal opened={opened} onClose={requestClose} title={reinstall ? t('node.reinstallNodeAgent') : t('nodes.addNode')} size="lg" closeOnClickOutside={!pending} closeOnEscape={!pending} classNames={{ content: 'nf-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}>
      <form onSubmit={scanHostKey}>
        <Stack gap="md">
          <p className="nf-dialog__intro">{reinstall
            ? t('nodes.thePanelWillReVerify', { selectedAgentPort })
            : t('nodes.thePanelConnectsOverSsh', { selectedAgentPort })}</p>
          <div className="nf-stepper">
            <span className={step >= 1 ? 'is-active' : ''}><i>1</i> {t('editor.connection')}</span>
            <span className={step >= 2 ? 'is-active' : ''}><i>2</i> {t('nodes.hostKey')}</span>
            <span className={step >= 3 ? 'is-active' : ''}><i>3</i> {t('node.installing')}</span>
          </div>
          {error && <Alert color="red" icon={<IconAlertCircle size={18} />}>{error}</Alert>}
          {step === 1 && <>
            <div className="nf-field-grid nf-field-grid--2-even">
              <TextInput label={t('node.nodeName')} placeholder="edge-msk-01" value={name} onChange={(event) => setName(event.currentTarget.value)} readOnly={reinstall} maxLength={200} required />
              <TextInput label={t('node.nodeIpAddress')} placeholder="203.0.113.10" value={address} onChange={(event) => setAddress(event.currentTarget.value)} leftSection={<IconServer size={16} />} readOnly={reinstall} required />
            </div>
            <div className="nf-field-grid nf-field-grid--3">
              <NumberInput label={t('nodes.sshPort')} value={port} onChange={setPort} min={1} max={65535} allowDecimal={false} required />
              <TextInput label={t('nodes.user')} value={username} onChange={(event) => setUsername(event.currentTarget.value)} required />
              <NumberInput label={t('nodes.mtlsChannelPort')} description={t('nodes.panelAgentPortSelectedagentportSelected', { selectedAgentPort })} value={agentPort} onChange={setAgentPort} min={1} max={65535} allowDecimal={false} required />
            </div>
            <Select
              label={t('nodes.nodeAgentVersion')}
              description={t('nodes.thePanelDetectsTheServer')}
              value={selectedReleaseID}
              onChange={(value) => setSelectedReleaseID(value ?? automaticReleaseValue)}
              allowDeselect={false}
              data={releaseOptions}
              disabled={releasesLoading}
            />
            {releasesError && <Alert color="yellow" icon={<IconAlertCircle size={18} />}>
              {t('nodes.uploadedReleasesListIsUnavailable')} {releasesError}. {t('addNode.autoSelectStillWorks')}
            </Alert>}
            <div>
              <label className="nf-field-label">{t('nodes.signInMethod')}</label>
              <SegmentedControl fullWidth value={authMode} onChange={(value) => { setAuthMode(value as BootstrapAuthMode); clearSecrets(); }} data={[{ label: t('nodes.password'), value: 'password' }, { label: t('nodes.privateSshKey'), value: 'private_key' }]} aria-label={t('nodes.sshSignInMethod')} />
            </div>
            {authMode === 'password' ? (
              <PasswordInput label={t('nodes.sshPassword')} value={password} onChange={(event) => setPassword(event.currentTarget.value)} required autoComplete="new-password" />
            ) : <>
              <Textarea label={t('nodes.privateSshKey')} description={t('nodes.pasteTheOpensshPemContents')} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" minRows={5} autosize maxRows={9} value={privateKey} onChange={(event) => setPrivateKey(event.currentTarget.value)} leftSection={<IconKey size={16} />} required />
              <PasswordInput label={t('nodes.keyPassphrase')} description={t('nodes.leaveEmptyIfTheKey')} value={privateKeyPassphrase} onChange={(event) => setPrivateKeyPassphrase(event.currentTarget.value)} autoComplete="new-password" />
            </>}
            <div className="nf-field-grid nf-field-grid--2-even">
              <Select label={t('nodes.serverPrivileges')} value={sudoMode} onChange={(value) => { setSudoMode((value ?? 'auto') as BootstrapSudoMode); setSudoPassword(''); }} allowDeselect={false} data={[
                { value: 'auto', label: t('nodes.detectAutomatically') }, { value: 'root', label: t('nodes.signInAsRoot') },
                { value: 'passwordless', label: t('nodes.passwordlessSudo') }, { value: 'password', label: t('nodes.sudoWithPassword') },
              ]} />
              <Select label={t('nodes.sshHostKeyAlgorithm')} value={hostKeyAlgorithm} onChange={(value) => setHostKeyAlgorithm((value ?? defaultAlgorithm) as HostKeyResult['algorithm'])} allowDeselect={false} data={[
                { value: 'ssh-ed25519', label: t('nodes.ed25519Recommended') }, { value: 'ecdsa-sha2-nistp256', label: 'ECDSA P-256' }, { value: 'rsa-sha2-256', label: 'RSA SHA-256' },
              ]} />
            </div>
            {sudoMode === 'password' && <PasswordInput label={t('nodes.sudoPassword')} description={authMode === 'password' ? t('nodes.optionalIfEmptyTheSsh') : t('nodes.requiredToElevatePrivilegesAfter')} value={sudoPassword} onChange={(event) => setSudoPassword(event.currentTarget.value)} required={authMode === 'private_key'} autoComplete="new-password" />}
            <div className="nf-dialog__firewall">
              <Stack gap="sm">
              <Switch checked={allowFirewallApply} onChange={(event) => setAllowFirewallApply(event.currentTarget.checked)} label={t('nodes.allowTheAgentToOpen')} description={reinstall ? t('nodes.theSelectedPolicyAppliesAfter', { selectedAgentPort }) : t('nodes.thisOnlyPermitsRulesLabeled', { selectedAgentPort })} />
              {!reinstall && <Switch checked={haproxyLogs} onChange={(event) => setHAProxyLogs(event.currentTarget.checked)} label={t('node.haproxyConnectionLogs')} description={t('nodes.everyConnectionIsWrittenTo')} />}
              </Stack>
            </div>
            <Group justify="space-between" mt="sm"><Button variant="default" onClick={requestClose}>{t('node.cancel')}</Button><Button type="submit" loading={pending} disabled={!connectionValid}>{t('nodes.fetchHostKey')}</Button></Group>
          </>}
          {step === 2 && hostKey && <>
            <Alert color="nodeflow" icon={<IconShieldCheck size={18} />} title={t('nodes.verifyTheSshHostKey')}>
              <code className="nf-fingerprint">{hostKey.fingerprint}</code>
              <span className="nf-host-key-algorithm">{hostKey.algorithm}</span>
            </Alert>
            <div className="nf-bootstrap-summary">
              <div><span>{t('nodes.node')}</span><strong>{name}</strong><small>{address}:{Number(port)}</small></div>
              <div><span>{t('nodes.signIn')}</span><strong>{authMode === 'password' ? t('nodes.password') : t('nodes.sshKey')}</strong><small>{username} · {sudoMode}</small></div>
              <div><span>Agent</span><strong>{automaticRelease ? t('nodes.latestCompatibleVersion') : selectedRelease?.version ?? t('nodes.releaseUnavailable')}</strong><small>{detectedOS || t('nodes.detectingOs')} / {detectedArch || t('nodes.detectingArchitecture')} · {t('addNode.portN', { port: selectedAgentPort })}</small></div>
            </div>
            {automaticRelease && <Alert color="gray">
              {t('nodes.afterConfirmationThePanelInstalls')} {detectedOS || t('nodes.theDetectedOs')} / {detectedArch || t('nodes.theDetectedArchitecture')}.
            </Alert>}
            {manualReleaseMismatch && selectedRelease && <Alert color="yellow" icon={<IconAlertCircle size={18} />} title={t('nodes.theSelectedReleaseIsNot')}>
              {t('nodes.server')} {detectedOS} / {detectedArch}. {t('addNode.releasePlatform', { os: selectedRelease.os, arch: selectedRelease.arch })} <Link to="/settings#node-agent" onClick={requestClose}>{t('nodes.settingsNodeAgent')}</Link>.
            </Alert>}
            <Checkbox checked={hostKeyAccepted} onChange={(event) => setHostKeyAccepted(event.currentTarget.checked)} label={t('nodes.iVerifiedTheFingerprintWith')} />
            <Group justify="space-between" mt="sm"><Button variant="default" onClick={() => { setStep(1); setHostKeyAccepted(false); }}>{t('nodes.back')}</Button><Button onClick={install} loading={pending} disabled={!hostKeyAccepted || manualReleaseMismatch}>{reinstall ? t('node.reinstallNodeAgent') : t('nodes.installNodeAgent')}</Button></Group>
          </>}
          {step === 3 && job && <div className={`nf-bootstrap-progress is-${job.status}`}>
            <div className="nf-bootstrap-progress__status">
              {job.status === 'installed' ? <IconCheck size={30} /> : job.status === 'failed' ? <IconAlertCircle size={30} /> : <Loader size={28} color="nodeflow" />}
              <div>
                <strong>{job.status === 'installed' ? (reinstall ? t('nodes.nodeAgentReinstalled') : t('nodes.nodeInstalled')) : job.status === 'failed' ? t('nodes.installationNotFinished') : (reinstall ? t('nodes.reinstallingNodeAgent') : t('nodes.installingNodeAgent'))}</strong>
                <span>{bootstrapStageLabels[job.stage] ?? t('nodes.secureInstallationInProgress')}</span>
              </div>
            </div>
            {(job.status === 'queued' || job.status === 'running') && <>
              <div className="nf-bootstrap-progress__bar" role="progressbar" aria-label={t('nodes.nodeAgentInstallationProgress')} aria-valuetext={bootstrapStageLabels[job.stage] ?? t('nodes.installationInProgress')}><span /></div>
              <small>{t('nodes.youCanCloseThisWindow')}</small>
            </>}
            {pollError && <Alert color="yellow" icon={<IconAlertCircle size={17} />}>{t('nodes.connectionToThePanelTemporarily')} {pollError}</Alert>}
            {job.journal && job.journal.length > 0 && <details className="nf-bootstrap-journal" open={journalOpen} onToggle={(event) => setJournalOpen(event.currentTarget.open)}>
              <summary>
                <span>{t('nodes.installationLog')}</span>
                <small>{job.journal.length} {job.journal.length === 1 ? t('nodes.step') : t('nodes.steps')}</small>
              </summary>
              <ol>
                {job.journal.map((entry, index) => <li key={`${entry.at}-${entry.stage}-${index}`} className={`is-${entry.status}`}>
                  <time dateTime={entry.at}>{formatJournalTime(entry.at)}</time>
                  <span>{bootstrapStageLabels[entry.stage] ?? t('nodes.secureInstallationStep')}</span>
                  <small>{bootstrapStatusLabels[entry.status]}</small>
                </li>)}
              </ol>
              <p>{t('nodes.theLogContainsOnlySteps')}</p>
            </details>}
            {job.status === 'installed' && <>
              <span>{reinstall ? t('nodes.nodeAgentReconnectedToThe') : t('nodes.nodeAgentConnectedTheNode')}</span>
              {job.node_id && <code>{job.node_id}</code>}
              <Button onClick={resetAndClose}>{t('nodes.done')}</Button>
            </>}
            {job.status === 'failed' && <>
              {job.failure_summary && <Alert color="red" icon={<IconAlertCircle size={17} />} title={t('nodes.stopReason')}>
                {job.failure_summary}
                {job.failure_code && <code className="nf-bootstrap-diagnostic">{job.failure_code}{job.exit_code ? ` · exit ${job.exit_code}` : ''}</code>}
              </Alert>}
              <Alert color="red" icon={<IconAlertCircle size={17} />}>{t('nodes.accessParametersHaveBeenWiped')}</Alert>
              <Group justify="space-between">
                <Button variant="default" leftSection={<IconRefresh size={16} />} loading={polling} onClick={() => void pollJob(job.job_id)}>{t('nodes.checkStatusAgain')}</Button>
                <Button onClick={() => { setJob(null); setHostKey(null); setHostKeyAccepted(false); setPollError(''); setError(''); setStep(1); }}>{t('nodes.editDetails')}</Button>
              </Group>
              <small>{t('nodes.checkStatusAgainOnlySends')}</small>
            </>}
          </div>}
        </Stack>
      </form>
    </Modal>
    <Modal opened={confirmCloseOpened} onClose={() => setConfirmCloseOpened(false)} title={jobActive ? t('nodes.hideInstallation') : t('nodes.closeInstallation')} size="sm" centered classNames={{ content: 'nf-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}>
      <Stack gap="md">
        <p className="nf-dialog__intro">{jobActive ? t('nodes.theServerSideInstallationIs') : t('nodes.enteredDataWillNotBe')}</p>
        <Group justify="flex-end"><Button variant="default" onClick={() => setConfirmCloseOpened(false)}>{jobActive ? t('nodes.stay') : t('nodes.continueSetup')}</Button><Button color={jobActive ? 'nodeflow' : 'red'} onClick={jobActive ? () => { setConfirmCloseOpened(false); onClose(); } : resetAndClose}>{jobActive ? t('nodes.hide') : t('nodes.close')}</Button></Group>
      </Stack>
    </Modal>
  </>);
}
