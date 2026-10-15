import { randomUUID } from '../lib/uuid';
import {
  ActionIcon, Alert, Button, Code, ColorPicker, CopyButton, FileButton, Group, LoadingOverlay, Menu, Modal, Popover, Select, TextInput, Tooltip,
} from '@mantine/core';
import {
  IconAlertCircle, IconArrowBackUp, IconCheck, IconChevronDown, IconCircleCheck, IconCloudUpload, IconKey,
  IconHistory, IconLock, IconPalette, IconRefresh, IconRocket, IconServer, IconSettings, IconShieldCheck, IconShieldLock, IconTrash,
  IconChartDots3, IconCopy, IconWorld,
} from '@tabler/icons-react';
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactElement } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { agentReleasesQueryKey } from '../lib/polling';
import { Link, useLocation } from 'react-router-dom';
import { StateView } from '../components/StateView';
import { PageHeader } from '../components/PageHeader';
import { Surface } from '../components/Surface';
import { api, APIError, isUnauthorized } from '../lib/api';
import type {
  AgentRelease, NodeAgentUpdateState, NodeOperational, NodeRecord, PanelSettings, PanelSettingsUpdate,
} from '../lib/contracts';
import {
  accentPattern, applyNodeFlowAccent, applyNodeFlowAppearance, applyNodeFlowTheme, defaultAccent, normaliseAccent,
  themeAccents,
} from '../lib/appearance';
import { formatBytes, timeAgo } from '../lib/format';
import { LoginPanel } from '../components/LoginPanel';
import { heartbeatPlatform, platformLabel, releaseMatchesPlatform, type AgentPlatform } from '../features/releases/platform';
import {
  CfgField, CfgFields, CfgNumber, CfgValue, StatusPill, formatDay, formatDayTime, type PillTone,
} from '../features/settings/settings-ui';
import { releaseTwinMap } from '../features/settings/release-twins';
import { isValidReleaseVersion } from '../lib/agentVersion';
import './settings.css';
import { t } from '../i18n';

interface SigningKeyInfo { algorithm?: string; sha256?: string; fingerprint?: string }
interface ManagedNode {
  node: NodeRecord;
  operational: NodeOperational | null;
  update: NodeAgentUpdateState | null;
}
interface SettingsData {
  panel: PanelSettings;
  nodes: ManagedNode[];
  releases: AgentRelease[];
  signingKey: SigningKeyInfo | null;
  partialErrors: string[];
}

interface RollbackIntent {
  nodeID: string;
  nodeName: string;
  release: AgentRelease;
  expectedActualSequence: number;
  expectedDesiredSequence: number;
  platform: AgentPlatform;
}

type SettingsCardKey = 'agent' | 'versions' | 'appearance' | 'security' | 'addresses' | 'metrics';

/**
 * Dashboard layout: independent columns, every card at its natural height.
 * The column count follows the width of the grid itself (sidebar and page
 * padding already excluded):
 *   ≥ 1960 px  Node Agent + versions | panel cards | Prometheus
 *   ≥ 1000 px  Node Agent + versions | panel cards, Prometheus under the shorter column
 *   narrower   one column; compact panel cards pair up via CSS auto-fill
 * «Сессии» and «Журналы» are one short row of numbers each, so they share a
 * row (security) instead of leaving a lone 120 px field in a wide card.
 */
function settingsColumns(width: number): 1 | 2 | 3 {
  if (width >= 1960) return 3;
  if (width >= 1000) return 2;
  return 1;
}

type MetricsPlacement = 'left' | 'right' | 'below';

/** Columns plus an optional full-width row under them. */
function settingsLayout(columns: 1 | 2 | 3, metrics: MetricsPlacement): { columns: SettingsCardKey[][]; below: SettingsCardKey[] } {
  const panel: SettingsCardKey[] = ['appearance', 'security', 'addresses'];
  if (columns === 3) return { columns: [['agent', 'versions'], panel, ['metrics']], below: [] };
  if (columns === 2) {
    if (metrics === 'below') return { columns: [['agent', 'versions'], panel], below: ['metrics'] };
    return { columns: metrics === 'left' ? [['agent', 'versions', 'metrics'], panel] : [['agent', 'versions'], [...panel, 'metrics']], below: [] };
  }
  return { columns: [['agent', 'versions', 'appearance', 'addresses', 'security', 'metrics']], below: [] };
}

const maxReleaseBytes = 64 * 1024 * 1024;
const panelDefaults: PanelSettings = {
  public_url: 'https://panel.example.com', web_port: 8080, agent_port: 4200,
  theme: 'rose', accent: defaultAccent, session_timeout_minutes: 30, max_sessions: 5,
  audit_retention_days: 90,
};
const accentPresets = ['#22C55E', '#C27087', '#45C7D8', '#E7B84B'];

function visiblePanelTheme(settings: Pick<PanelSettings, 'theme' | 'accent'>): PanelSettings['theme'] {
  if (['green', 'rose', 'cyan', 'amber'].includes(settings.theme)) return settings.theme;
  return normaliseAccent(settings.accent) === '#22C55E' ? 'green' : 'rose';
}

function normalisePanelAppearance(settings: PanelSettings): PanelSettings {
  return { ...settings, theme: visiblePanelTheme(settings) };
}

const explicitDemo = new URLSearchParams(window.location.search).get('demo') === '1'
  || import.meta.env.VITE_NODEFLOW_DEMO === 'true';

async function demoSettings(): Promise<SettingsData> {
  const { demoNodeBundles } = await import('../fixtures/demo');
  const now = Date.now();
  const day = 86_400_000;
  // Mirrors a real install: 12 signed releases, #21 is a re-upload of the
  // #20 binary (same SHA-256), #17 and #18 are two builds of 1.1.0.
  const releaseSpecs: Array<[number, string, number, number, string]> = [
    [22, '2.0.0', 8_470_528, 0.1, '9c3e71a4f0b25d68e1c7a93f4b0d2e86c5f17a39b2e4d60c8f1a7b35e92d04c6'],
    [21, '1.1.2', 8_462_336, 0.2, '25f449ad85c1be0d9a7f1c3e0b54a2d7e61f9c08a3b7d42e95f06c1d8a2b3e47'],
    [20, '1.1.2', 8_462_336, 0.25, '25f449ad85c1be0d9a7f1c3e0b54a2d7e61f9c08a3b7d42e95f06c1d8a2b3e47'],
    [19, '1.1.1', 8_451_840, 0.4, '47c24b0b96e3a1f7d05c2b8e9f4a6d13c7b25e80f9a1d4c6b3e72a05f8d91c2b'],
    [18, '1.1.0', 8_441_344, 1.2, 'a68a075355d2e8b1c47f09a3e6b5d21c8f7a4e90b3c6d15f2a8e7b40c9d31f6a'],
    [17, '1.1.0', 8_441_344, 1.3, '6e8654c4c4f1a0b9d73e25c8a6f4b1e09d7c3a52f8e6b14d0c9a7e35b2f81d4c'],
    [16, '1.0.5', 7_749_632, 56, 'a8d400cff41e7b2c9d05a6f3e8b1c47d2a9f60e3b5c8d71f4a2e9b06c3d85f1a'],
    [15, '1.0.4', 7_749_632, 63, '84e063ebb7a2f5c1d98e40b6a3f7c2e15d9b08a4f6e3c71b2d5a8e94f0c6b3d2'],
    [14, '1.0.2', 7_749_632, 64, 'be71d6c2219f3a8e5b0c7d4f1a6e2b98c3d05f7a4e1b6c92d8f3a50e7b4c1d6f'],
    [13, '0.5.0', 7_738_880, 67, 'e5a975f060b8c3d1e7f4a2b95c0d6e83f1a7b4c29d5e0f6a3b8c71d4e2f95a0b'],
    [12, '0.4.9-dev', 7_738_880, 68, 'a76ab9c9a9d2e5f1b8c4a07e3d6f92b5c1e8a4d73f0b6c29e5a1d8f4b7c03e6d'],
    [11, '0.4.8-dev', 7_738_880, 68.2, 'fd19989d4e7a3c1b6f02d8e5a9c4b71f3e6d0a28b5c9f14e7d3a6b80c2f5e19a'],
  ];
  const releases: AgentRelease[] = releaseSpecs.map(([sequence, version, size_bytes, ageDays, sha256]) => ({
    id: `00000000-0000-4000-8000-${String(sequence).padStart(12, '0')}`,
    version, os: 'linux', arch: 'amd64', sha256, size_bytes, sequence, signature: 'verified',
    created_at: new Date(now - ageDays * day).toISOString(),
  }));
  // dev-node-01 runs the newest build; the rest lag on 1.0.5 (#16) and can update.
  const installed = (index: number) => releases.find((release) => release.sequence === (index === 0 ? 22 : 16))!;
  const nodes = demoNodeBundles.map((bundle, index) => ({
    node: bundle.node,
    operational: bundle.operational && bundle.operational.latest_heartbeat ? {
      ...bundle.operational,
      latest_heartbeat: { ...bundle.operational.latest_heartbeat, agent_version: installed(index).version },
    } : bundle.operational,
    update: {
      node_id: bundle.node.id,
      actual_sequence: installed(index).sequence,
      state: 'installed',
      last_report_at: new Date(now - 13_000 - index * 1_000).toISOString(),
      updated_at: new Date(now - 18_000).toISOString(),
    },
  }));
  return {
    panel: { ...panelDefaults, public_url: 'https://panel.example.com', metrics_enabled: true, updated_at: new Date(now - 63 * day).toISOString() },
    nodes, releases,
    signingKey: { algorithm: 'Ed25519', sha256: '703105fbac33f7388d7cd83e9cf02ad8130fa77d93b62bc111808d4cd5f3d387' },
    partialErrors: [],
  };
}

async function optional<T>(promise: Promise<T>, errors: string[], label: string, fallback: T): Promise<T> {
  try { return await promise; }
  catch (error) { errors.push(t('settings.labelP1', { label, p1: error instanceof Error ? error.message : t('misc.dataUnavailable') })); return fallback; }
}

async function loadSettings(): Promise<SettingsData> {
  if (explicitDemo) return demoSettings();
  const partialErrors: string[] = [];
  const [panel, nodes, releases, signingKey] = await Promise.all([
    api<PanelSettings>('/api/v1/settings'),
    api<NodeRecord[]>('/api/v1/nodes'),
    optional(api<AgentRelease[]>('/api/v1/agent-releases'), partialErrors, t('settings.releases'), []),
    optional(api<SigningKeyInfo>('/api/v1/agent-releases/signing-key'), partialErrors, t('settings.signingKey'), null),
  ]);
  const managed = nodes.map((node) => ({ node, operational: null, update: null }));
  return { panel: normalisePanelAppearance(panel), nodes: managed, releases, signingKey, partialErrors };
}

function panelUpdate(settings: PanelSettings): PanelSettingsUpdate {
  return {
    theme: settings.theme,
    accent: settings.accent,
    session_timeout_minutes: settings.session_timeout_minutes,
    max_sessions: settings.max_sessions,
    audit_retention_days: settings.audit_retention_days,
  };
}

function samePanelUpdate(left: PanelSettings, right: PanelSettings) {
  return JSON.stringify(panelUpdate(left)) === JSON.stringify(panelUpdate(right));
}

function panelValidation(settings: PanelSettings): string[] {
  const errors: string[] = [];
  if (!accentPattern.test(settings.accent)) errors.push(t('settings.accentColorInRrggbbFormat'));
  if (!Number.isInteger(settings.session_timeout_minutes) || settings.session_timeout_minutes < 5 || settings.session_timeout_minutes > 1440) errors.push(t('settings.sessionTimeout5To1440'));
  if (!Number.isInteger(settings.max_sessions) || settings.max_sessions < 1 || settings.max_sessions > 100) errors.push(t('settings.sessionCount1To100'));
  if (!Number.isInteger(settings.audit_retention_days) || settings.audit_retention_days < 7 || settings.audit_retention_days > 3650) errors.push(t('settings.auditRetention7To3650'));
  return errors;
}

function releaseStatus(release: AgentRelease, actualSequence: number | null, platform: AgentPlatform | null) {
  if (!platform) return t('settings.unknownPlatform');
  if (actualSequence === null) return t('settings.stateUnknown');
  if (!releaseMatchesPlatform(release, platform)) return t('settings.otherPlatform');
  if (release.sequence === actualSequence) return t('node.installed');
  if (release.sequence > actualSequence) return t('settings.available');
  return t('settings.forRollback');
}

function updaterStateLabel(state?: string) {
  return ({
    idle: t('settings.waiting'), pending: t('node.scheduled'), downloading: t('node.downloading'), verified: t('settings.verified'),
    activating: t('node.installing'), installed: t('node.installed'), failed: t('node.error'), rolled_back: t('settings.rolledBack'),
  } as Record<string, string>)[state ?? ''] ?? state ?? t('settings.noReport');
}

export function SettingsPage() {
  const location = useLocation();
  const queryClient = useQueryClient();
  const [data, setData] = useState<SettingsData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const [selectedNodeID, setSelectedNodeID] = useState('');
  const [detailsRefresh, setDetailsRefresh] = useState(0);
  const [detailsLoading, setDetailsLoading] = useState(false);
  const [detailsError, setDetailsError] = useState('');
  const [busy, setBusy] = useState('');
  const [feedback, setFeedback] = useState<{ tone: 'ok' | 'error'; text: string } | null>(null);
  const [rollbackIntent, setRollbackIntent] = useState<RollbackIntent | null>(null);
  const [rollbackError, setRollbackError] = useState('');
  const [uploadOpen, setUploadOpen] = useState(false);
  const [uploadVersion, setUploadVersion] = useState('');
  const [uploadOS, setUploadOS] = useState('linux');
  const [uploadArch, setUploadArch] = useState('amd64');
  const [uploadFile, setUploadFile] = useState<File | null>(null);
  const [uploadError, setUploadError] = useState('');
  const [releaseToDelete, setReleaseToDelete] = useState<AgentRelease | null>(null);
  const [deleteReleaseError, setDeleteReleaseError] = useState('');
  const [deleteReleaseBlocked, setDeleteReleaseBlocked] = useState(false);
  const [panelDraft, setPanelDraft] = useState<PanelSettings>(panelDefaults);
  const [savedPanel, setSavedPanel] = useState<PanelSettings>(panelDefaults);
  const [panelSaving, setPanelSaving] = useState(false);
  const [panelError, setPanelError] = useState('');
  const [accentPickerOpen, setAccentPickerOpen] = useState(false);
  const [releasesExpanded, setReleasesExpanded] = useState(false);
  const resetUploadInput = useRef<() => void>(null);
  const [gridWidth, setGridWidth] = useState(0);
  const [metricsPlacement, setMetricsPlacement] = useState<MetricsPlacement>('below');
  const gridObserver = useRef<ResizeObserver | null>(null);
  const gridElement = useRef<HTMLDivElement | null>(null);
  const gridRef = useCallback((element: HTMLDivElement | null) => {
    gridObserver.current?.disconnect();
    gridElement.current = element;
    if (!element) return;
    setGridWidth(element.clientWidth);
    gridObserver.current = new ResizeObserver(([entry]) => setGridWidth(Math.round(entry.contentRect.width)));
    gridObserver.current.observe(element);
  }, []);
  const columnCount = settingsColumns(gridWidth);
  const layout = settingsLayout(columnCount, metricsPlacement);
  // Two columns: when both columns end at about the same height Prometheus
  // spans the full width under them; otherwise it fills the shorter column.
  // Measured from the other cards only (their widths never depend on where
  // Prometheus goes), so the choice cannot oscillate.
  useLayoutEffect(() => {
    const grid = gridElement.current;
    if (!grid || columnCount !== 2) return;
    const height = (keys: SettingsCardKey[]) => keys.reduce((sum, key) => sum + (grid.querySelector<HTMLElement>(`[data-card="${key}"]`)?.offsetHeight ?? 0), 0);
    const gap = parseFloat(getComputedStyle(grid).rowGap) || 0;
    const left = height(['agent', 'versions']) + gap;
    const right = height(['appearance', 'security', 'addresses']) + 2 * gap;
    const next: MetricsPlacement = Math.abs(left - right) < 160 ? 'below' : left < right ? 'left' : 'right';
    if (next !== metricsPlacement) setMetricsPlacement(next);
  });
  useEffect(() => () => gridObserver.current?.disconnect(), []);
  const demoQuery = explicitDemo ? '?demo=1' : location.search;

  const load = async () => {
    setLoading(true); setError(null); setFeedback(null);
    try {
      const value = await loadSettings();
      setData(value);
      setPanelDraft(value.panel);
      setSavedPanel(value.panel);
      applyNodeFlowAppearance(value.panel, true);
      setPanelError('');
      setSelectedNodeID((current) => current && value.nodes.some(({ node }) => node.id === current) ? current : value.nodes[0]?.node.id ?? '');
      setDetailsRefresh((current) => current + 1);
    } catch (reason) { setError(reason); }
    finally { setLoading(false); }
  };
  useEffect(() => { void load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (explicitDemo || !selectedNodeID) {
      setDetailsLoading(false);
      setDetailsError('');
      return undefined;
    }
    const controller = new AbortController();
    const errors: string[] = [];
    setDetailsLoading(true);
    setDetailsError('');
    void Promise.all([
      optional(api<NodeOperational>(`/api/v1/nodes/${selectedNodeID}/operational`, { signal: controller.signal }), errors, t('settings.telemetry'), null),
      optional(api<NodeAgentUpdateState>(`/api/v1/nodes/${selectedNodeID}/agent-update`, { signal: controller.signal }), errors, 'Updater', null),
    ]).then(([operational, update]) => {
      if (controller.signal.aborted) return;
      setData((current) => current ? ({
        ...current,
        nodes: current.nodes.map((item) => item.node.id === selectedNodeID ? { ...item, operational, update } : item),
      }) : current);
      setDetailsError(errors.join(' · '));
    }).finally(() => {
      if (!controller.signal.aborted) setDetailsLoading(false);
    });
    return () => controller.abort();
  }, [selectedNodeID, detailsRefresh]);

  const selected = data?.nodes.find(({ node }) => node.id === selectedNodeID) ?? data?.nodes[0];
  const releases = useMemo(() => [...(data?.releases ?? [])].sort((a, b) => b.sequence - a.sequence), [data?.releases]);
  const releaseTwins = useMemo(() => releaseTwinMap(releases), [releases]);
  const actualSequence = selected?.update ? selected.update.actual_sequence : null;
  const nodePlatform = heartbeatPlatform(selected?.operational?.latest_heartbeat?.metrics);
  const compatibleReleases = releases.filter((release) => releaseMatchesPlatform(release, nodePlatform));
  const currentRelease = actualSequence === null ? undefined : compatibleReleases.find((release) => release.sequence === actualSequence);
  const available = actualSequence === null ? undefined : compatibleReleases.find((release) => release.sequence > actualSequence);
  const rollbackReleases = actualSequence === null ? [] : compatibleReleases.filter((release) => release.sequence < actualSequence);
  const installedVersion = selected?.operational?.latest_heartbeat?.agent_version ?? currentRelease?.version ?? '—';
  // The Panel already applies the 45 s freshness window when it serialises
  // node.status (store.go scanNode: stale "online" -> "offline"). Comparing
  // server timestamps with the browser clock double-applied that window and
  // broke on any clock skew (a fresh heartbeat "from the future" failed
  // age >= 0). Count exactly what Prometheus nodeflow_node_up counts.
  const connectedNodes = data?.nodes.filter(({ node, operational }) => {
    const hasSignal = Boolean(operational?.latest_heartbeat?.received_at ?? node.last_seen_at);
    return hasSignal && (node.status === 'online' || node.status === 'degraded');
  }).length ?? 0;
  const nodeCount = data?.nodes.length ?? 0;
  const validationErrors = panelValidation(panelDraft);
  const panelDirty = !samePanelUpdate(panelDraft, savedPanel);
  const setPanelField = <K extends keyof PanelSettings>(field: K, value: PanelSettings[K]) => {
    const normalised = field === 'accent' ? normaliseAccent(String(value)) : null;
    const nextValue = field === 'accent' && normalised ? normalised : value;
    setPanelDraft((current) => ({ ...current, [field]: nextValue }));
    if (field === 'accent') applyNodeFlowAccent(String(nextValue));
    if (field === 'theme') applyNodeFlowTheme(nextValue as PanelSettings['theme']);
    setFeedback(null);
    setPanelError('');
  };
  const setPanelTheme = (theme: PanelSettings['theme']) => {
    const accent = themeAccents[theme] ?? panelDraft.accent;
    setPanelDraft((current) => ({ ...current, theme, accent }));
    applyNodeFlowAppearance({ theme, accent });
    setFeedback(null);
    setPanelError('');
  };
  const setCustomAccent = (value: string) => {
    const accent = normaliseAccent(value) ?? value;
    const theme = visiblePanelTheme(panelDraft);
    setPanelDraft((current) => ({ ...current, theme, accent }));
    if (normaliseAccent(accent)) applyNodeFlowAppearance({ theme, accent });
    setFeedback(null);
    setPanelError('');
  };
  const resetPanelSettings = () => {
    setPanelDraft(savedPanel);
    applyNodeFlowAppearance(savedPanel, true);
    setPanelError('');
  };
  const savePanelSettings = async () => {
    if (validationErrors.length || panelSaving) return;
    setPanelSaving(true);
    setPanelError('');
    setFeedback(null);
    try {
      const value = explicitDemo
        ? { ...panelDraft, updated_at: new Date().toISOString() }
        : await api<PanelSettings>('/api/v1/settings', { method: 'PUT', body: JSON.stringify(panelUpdate(panelDraft)) });
      setPanelDraft(value);
      setSavedPanel(value);
      setData((current) => current ? { ...current, panel: value } : current);
      applyNodeFlowAppearance(value, true);
      setFeedback({ tone: 'ok', text: explicitDemo ? t('settings.demoSettingsSavedInThe') : t('settings.panelSettingsSaved') });
    } catch (reason) {
      setPanelError(reason instanceof APIError && reason.status === 409
        ? t('settings.conflictSettingsWereChangedIn')
        : reason instanceof Error ? reason.message : t('settings.settingsNotSaved'));
    } finally {
      setPanelSaving(false);
    }
  };

  const submitOnEnter = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key !== 'Enter') return;
    event.preventDefault();
    if (panelDirty) void savePanelSettings();
  };

  const updateNodeState = (value: NodeAgentUpdateState) => setData((current) => current ? ({
    ...current,
    nodes: current.nodes.map((item) => item.node.id === value.node_id ? { ...item, update: value } : item),
  }) : current);

  const assign = async () => {
    if (!selected || !selected.update || !available || !nodePlatform || !releaseMatchesPlatform(available, nodePlatform)) return;
    setBusy('update'); setFeedback(null);
    try {
      const value = explicitDemo
        ? { ...selected.update!, desired_release: available, state: 'assigned', updated_at: new Date().toISOString() }
        : await api<NodeAgentUpdateState>(`/api/v1/nodes/${selected.node.id}/agent-update`, {
          method: 'PUT',
          body: JSON.stringify({
            release_id: available.id,
            expected_actual_sequence: selected.update.actual_sequence,
            expected_desired_sequence: selected.update.desired_release?.sequence ?? 0,
          }),
        });
      updateNodeState(value); setFeedback({ tone: 'ok', text: t('settings.versionAssignedToNodeName', { version: available.version, name: selected.node.name }) });
    } catch (reason) { setFeedback({ tone: 'error', text: reason instanceof Error ? reason.message : t('settings.updateNotAssigned') }); }
    finally { setBusy(''); }
  };

  const rollbackNode = async () => {
    if (!rollbackIntent) return;
    const target = data?.nodes.find(({ node }) => node.id === rollbackIntent.nodeID);
    const currentPlatform = heartbeatPlatform(target?.operational?.latest_heartbeat?.metrics);
    if (!currentPlatform || !releaseMatchesPlatform(rollbackIntent.release, currentPlatform)
      || currentPlatform.os !== rollbackIntent.platform.os || currentPlatform.arch !== rollbackIntent.platform.arch) {
      setRollbackError(t('settings.theNodePlatformIsUnknown'));
      return;
    }
    setBusy('rollback'); setFeedback(null);
    setRollbackError('');
    try {
      const selectedState = data?.nodes.find(({ node }) => node.id === rollbackIntent.nodeID)?.update;
      const value = explicitDemo
        ? { ...selectedState!, desired_release: rollbackIntent.release, state: 'assigned', updated_at: new Date().toISOString() }
        : await api<NodeAgentUpdateState>(`/api/v1/nodes/${rollbackIntent.nodeID}/agent-update/rollback`, {
          method: 'POST',
          body: JSON.stringify({
            target_release_id: rollbackIntent.release.id,
            expected_actual_sequence: rollbackIntent.expectedActualSequence,
            expected_desired_sequence: rollbackIntent.expectedDesiredSequence,
          }),
        });
      updateNodeState(value);
      setFeedback({ tone: 'ok', text: t('settings.safeRollbackOfNodenameTo', { nodeName: rollbackIntent.nodeName, version: rollbackIntent.release.version }) });
      setRollbackIntent(null);
    } catch (reason) { setRollbackError(reason instanceof Error ? reason.message : t('node.rollbackNotScheduled')); }
    finally { setBusy(''); }
  };

  const requestRollback = (release: AgentRelease) => {
    if (!selected || !selected.update || !nodePlatform || !releaseMatchesPlatform(release, nodePlatform) || release.sequence >= selected.update.actual_sequence) return;
    setRollbackError('');
    setRollbackIntent({
      nodeID: selected.node.id,
      nodeName: selected.node.name,
      release,
      expectedActualSequence: selected.update.actual_sequence,
      expectedDesiredSequence: selected.update.desired_release?.sequence ?? 0,
      platform: nodePlatform,
    });
  };

  const resetUpload = () => {
    resetUploadInput.current?.();
    setUploadOpen(false);
    setUploadVersion('');
    setUploadOS('linux');
    setUploadArch('amd64');
    setUploadFile(null);
    setUploadError('');
  };

  const selectUploadFile = (file: File | null) => {
    if (file && file.size > maxReleaseBytes) {
      resetUploadInput.current?.();
      setUploadFile(null);
      setUploadError(t('settings.fileIsLargerThan64', { size: formatBytes(file.size) }));
      return;
    }
    setUploadFile(file);
    setUploadError('');
  };

  const upload = async () => {
    if (!uploadFile || !uploadVersion.trim()) return;
    if (!isValidReleaseVersion(uploadVersion)) {
      setUploadError(t('settings.versionLatinLettersDigitsAnd'));
      return;
    }
    if (uploadFile.size > maxReleaseBytes) {
      setUploadError(t('settings.fileIsLargerThan64', { size: formatBytes(uploadFile.size) }));
      return;
    }
    setBusy('upload'); setFeedback(null);
    setUploadError('');
    try {
      const value = explicitDemo ? {
        id: randomUUID(), version: uploadVersion.trim(), os: uploadOS, arch: uploadArch,
        sha256: 'demo-artifact-sha256', size_bytes: uploadFile.size, sequence: (releases[0]?.sequence ?? 0) + 1,
        signature: 'demo-signed', created_at: new Date().toISOString(),
      } satisfies AgentRelease : await api<AgentRelease>(`/api/v1/agent-releases?version=${encodeURIComponent(uploadVersion.trim())}&os=${encodeURIComponent(uploadOS)}&arch=${encodeURIComponent(uploadArch)}`, {
        method: 'POST', headers: { 'Content-Type': 'application/octet-stream' }, body: uploadFile,
      });
      setData((current) => current ? { ...current, releases: [value, ...current.releases] } : current);
      queryClient.removeQueries({ queryKey: agentReleasesQueryKey });
      setFeedback({ tone: 'ok', text: t('settings.releaseVersionUploadedSha256', { version: value.version }) });
      resetUpload();
    } catch (reason) { setUploadError(reason instanceof Error ? reason.message : t('settings.releaseNotUploaded')); }
    finally { setBusy(''); }
  };

  const deleteRelease = async () => {
    if (!releaseToDelete || deleteReleaseBlocked) return;
    setBusy('delete-release');
    setDeleteReleaseError('');
    setFeedback(null);
    try {
      if (!explicitDemo) await api<void>(`/api/v1/agent-releases/${encodeURIComponent(releaseToDelete.id)}`, { method: 'DELETE' });
      setData((current) => current ? { ...current, releases: current.releases.filter((release) => release.id !== releaseToDelete.id) } : current);
      queryClient.removeQueries({ queryKey: agentReleasesQueryKey });
      setFeedback({ tone: 'ok', text: t('settings.releaseVersionOsArchDeleted', { version: releaseToDelete.version, os: releaseToDelete.os, arch: releaseToDelete.arch }) });
      setReleaseToDelete(null);
    } catch (reason) {
      const releaseInUse = reason instanceof APIError && reason.status === 409 && reason.code === 'release_in_use';
      setDeleteReleaseBlocked(releaseInUse);
      setDeleteReleaseError(reason instanceof APIError && reason.status === 409
        ? t('settings.theReleaseIsInstalledOn')
        : reason instanceof Error ? reason.message : t('settings.releaseNotDeleted'));
    } finally { setBusy(''); }
  };

  if (loading) return <main className="nf-page nf-settings-page"><div className="nf-settings-loading"><LoadingOverlay visible /><span>{t('settings.loadingPanelSettingsAndSigned')}</span></div></main>;
  if (error && isUnauthorized(error)) return <LoginPanel onSuccess={load} />;
  if (error || !data) return <main className="nf-page"><StateView title={t('settings.settingsFailedToLoad')} description={error instanceof Error ? error.message : t('nodes.panelApiIsUnreachable')} tone="error" action={<Button onClick={load}>{t('app.retry')}</Button>} /></main>;

  const releaseRowsLimit = 5;
  const visibleReleases = releasesExpanded ? releases : releases.slice(0, releaseRowsLimit);
  const hiddenInstalled = !releasesExpanded && currentRelease && !visibleReleases.includes(currentRelease) ? currentRelease : null;
  const updateBlocked = !nodePlatform || !selected?.update || detailsLoading || Boolean(busy);
  const panelEnabledMetrics = Boolean(data.panel.metrics_enabled);
  const metricsCidrs = data.panel.metrics_allow_cidrs ?? 0;
  const scrapeURL = `${data.panel.public_url || 'http://panel:8080'}/metrics`;
  const pillTone = (status: string): PillTone => status === t('node.installed') ? 'accent' : status === t('settings.available') ? 'ok' : status === t('settings.forRollback') || status === t('settings.readyToInstall') ? 'muted' : 'warn';
  const renderReleaseRow = (release: AgentRelease) => {
    const status = data.nodes.length ? releaseStatus(release, actualSequence, nodePlatform) : t('settings.readyToInstall');
    const twin = releaseTwins.get(release.id);
    const installed = status === t('node.installed');
    const assigned = selected?.update?.desired_release?.id === release.id && !installed;
    return <div className={`nf-release-row${installed ? ' is-installed' : ''}${twin ? ' is-twin' : ''}`} role="row" key={release.id}>
      <div className="nf-release-row__version" role="cell"><strong>{release.version}</strong></div>
      <div className="nf-release-row__seq" role="cell"><code>#{release.sequence}</code></div>
      <div className="nf-release-row__platform" role="cell">{release.os}/{release.arch}<small>{formatBytes(release.size_bytes)}</small></div>
      <div className="nf-release-row__date" role="cell"><Tooltip label={formatDayTime(release.created_at)} withArrow><span>{formatDay(release.created_at)}</span></Tooltip></div>
      <div className="nf-release-row__sha" role="cell"><Tooltip label={release.sha256} withArrow multiline maw={320}><code>{release.sha256.slice(0, 10)}</code></Tooltip></div>
      <div className="nf-release-row__status" role="cell">
        <StatusPill tone={assigned ? 'info' : pillTone(status)}>{assigned ? t('node.scheduled') : status}</StatusPill>
        {twin && <span className="nf-release-row__twin" title={twin.kind === 'same' ? t('settings.sha256MatchesSequence', { sequence: twin.sequence }) : t('settings.versionVersionReReleasedAs', { version: release.version, sequence: twin.sequence })}>{twin.kind === 'same' ? t('settings.sameBinaryAsSequence', { sequence: twin.sequence }) : t('settings.rebuiltAsSequence', { sequence: twin.sequence })}</span>}
      </div>
      <div className="nf-release-row__delete" role="cell">
        <Tooltip label={installed ? t('settings.anInstalledReleaseCannotBe') : t('settings.deleteVersionSequence', { version: release.version, sequence: release.sequence })} withArrow>
          <ActionIcon variant="subtle" color="red" size={32} aria-label={t('settings.deleteReleaseVersionSequenceFor', { version: release.version, sequence: release.sequence, os: release.os, arch: release.arch })} onClick={() => { setDeleteReleaseError(''); setDeleteReleaseBlocked(false); setReleaseToDelete(release); }} disabled={Boolean(busy) || installed}><IconTrash size={16} /></ActionIcon>
        </Tooltip>
      </div>
    </div>;
  };

  const agentStatusIcon = !nodePlatform || !selected?.update ? <IconAlertCircle size={15} /> : <IconCircleCheck size={15} />;
  const agentStatusText = detailsLoading ? t('settings.checking') : !nodePlatform ? t('settings.unknownPlatform') : !selected?.update ? t('settings.noUpdaterReport') : t('settings.upToDate');
  const updaterState = selected?.update?.state;

  const cards: Record<SettingsCardKey, ReactElement> = {
    agent: <Surface
      key="agent"
      id="node-agent"
      className="nf-settings-card nf-agent-settings"
      data-card="agent"
      title={<span className="nf-settings-card-title"><IconRocket size={18} />Node Agent</span>}
      description={<span className="nf-agent-trust"><IconShieldCheck size={14} />{t('settings.signedReleases')} {data.signingKey?.algorithm || 'Ed25519'} + SHA-256</span>}
      actions={<>
        {/* Fixed-width slot: switching nodes swaps «Обновить до …» and the status line without moving the other buttons. */}
        {data.nodes.length > 0 && <span className="nf-agent-primary">{available && nodePlatform && selected?.update
          ? <Button leftSection={<IconRocket size={16} />} onClick={assign} disabled={updateBlocked} loading={busy === 'update'} title={t('settings.updateToVersion', { version: available.version })}>{t('settings.updateTo')} {available.version}</Button>
          : <span className="nf-agent-uptodate">{agentStatusIcon}{agentStatusText}</span>}</span>}
        {data.nodes.length > 0 && <Menu position="bottom-end" withinPortal shadow="md" width={300}>
          <Menu.Target>
            <Button variant="default" leftSection={<IconArrowBackUp size={16} />} rightSection={<IconChevronDown size={14} />} disabled={updateBlocked || rollbackReleases.length === 0}>{t('node.rollBack')}</Button>
          </Menu.Target>
          <Menu.Dropdown className="nf-rollback-menu">
            <Menu.Label>{t('settings.rollBackTo', { name: selected?.node.name })}</Menu.Label>
            {rollbackReleases.map((release) => {
              const sameBinary = currentRelease && release.sha256 === currentRelease.sha256;
              return <Menu.Item key={release.id} leftSection={<IconArrowBackUp size={15} />} rightSection={<code>#{release.sequence}</code>} onClick={() => requestRollback(release)}>
                {release.version}{sameBinary && <small> {t('settings.sameBinary')}</small>}
              </Menu.Item>;
            })}
          </Menu.Dropdown>
        </Menu>}
        <Button variant="default" leftSection={<IconCloudUpload size={16} />} onClick={() => setUploadOpen(true)}>{t('settings.uploadRelease')}</Button>
      </>}
    >
      {data.nodes.length ? <div className="nf-agent-node">
        <dl className="nf-agent-facts">
          <div className="nf-agent-facts__node">
            <dt><label htmlFor="nf-agent-node-select">{t('nodes.node')}</label></dt>
            <dd><Select id="nf-agent-node-select" className="nf-agent-node-select" aria-label={t('settings.nodeToUpdate')} value={selected?.node.id} onChange={(value) => setSelectedNodeID(value ?? '')} allowDeselect={false} data={data.nodes.map(({ node }) => ({ value: node.id, label: `${node.name} · ${node.address}` }))} disabled={Boolean(busy)} leftSection={<IconServer size={15} />} /></dd>
          </div>
          <div><dt>{t('node.installed')}</dt><dd><strong>{detailsLoading ? t('templates.loading') : installedVersion}</strong>{selected?.update && <code>#{selected.update.actual_sequence}</code>}</dd></div>
          <div><dt>{t('settings.updaterReport')}</dt><dd>{selected?.update?.last_report_at ? <><span>{timeAgo(selected.update.last_report_at)}</span>{updaterState !== 'installed' && <StatusPill tone={updaterState === 'failed' ? 'danger' : 'muted'}>{updaterStateLabel(updaterState)}</StatusPill>}</> : <span className="is-muted">{detailsLoading ? t('settings.fetchingState') : t('settings.notReceivedYet')}</span>}</dd></div>
          <div><dt>{t('settings.available')}</dt><dd>{available ? <><strong>{available.version}</strong><code>#{available.sequence}</code></> : <span className="is-muted">{!nodePlatform || actualSequence === null ? '—' : t('settings.nothingNewer')}</span>}</dd></div>
          <div><dt>{t('settings.platform')}</dt><dd><span>{platformLabel(nodePlatform)}</span></dd></div>
        </dl>
        {detailsError && <Alert color="yellow" icon={<IconAlertCircle size={17} />} role="alert">{detailsError}</Alert>}
        {!detailsLoading && !nodePlatform && <Alert color="yellow" icon={<IconAlertCircle size={17} />} role="status" title={t('settings.releaseAssignmentBlocked')}>{t('settings.waitForAHeartbeatWith')}</Alert>}
        {!detailsLoading && nodePlatform && !selected?.update && <Alert color="yellow" icon={<IconAlertCircle size={17} />} role="status" title={t('settings.updaterStateUnknown')}>{t('settings.assignmentAndRollbackAreBlocked')}</Alert>}
        {selected?.update?.last_error && <Alert color="red" icon={<IconAlertCircle size={17} />}>{selected.update.last_error}</Alert>}
        <p className="nf-agent-note">{t('settings.haproxyIsNotRestartedIf')}</p>
      </div> : <StateView title={t('nodes.noNodesYet')} description={t('settings.releasesCanBePreparedIn')} action={<Button component={Link} to={`/nodes${demoQuery}`}>{t('nodes.addNode')}</Button>} />}
    </Surface>,

    versions: <Surface
      key="versions"
      id="nf-agent-history"
      className="nf-settings-card nf-release-history"
      data-card="versions"
      title={<span className="nf-settings-card-title">{t('settings.agentVersions')} <span className="nf-cfg-count">{releases.length}</span></span>}
      description={t('settings.aNewNodeGetsThe')}
    >
      {releases.length ? <>
        <div className="nf-release-table" role="table" aria-label={t('settings.signedAgentVersions')}>
          <div className="nf-release-row is-head" role="row">
            <span role="columnheader" className="nf-release-row__version">{t('settings.version')}</span><span role="columnheader" className="nf-release-row__seq">Seq</span><span role="columnheader" className="nf-release-row__platform">{t('settings.platform')}</span><span role="columnheader" className="nf-release-row__date">{t('settings.uploaded')}</span><span role="columnheader" className="nf-release-row__sha">SHA-256</span><span role="columnheader" className="nf-release-row__status">{t('nodes.status')}</span><span role="columnheader" className="nf-release-row__delete"><span className="nf-visually-hidden">{t('node.delete')}</span></span>
          </div>
          {visibleReleases.map(renderReleaseRow)}
          {hiddenInstalled && <>
            <div className="nf-release-gap" role="row"><span role="cell">…</span></div>
            {renderReleaseRow(hiddenInstalled)}
          </>}
        </div>
        {releases.length > releaseRowsLimit && <button type="button" className="nf-release-more" onClick={() => setReleasesExpanded((value) => !value)} aria-expanded={releasesExpanded}>
          <IconChevronDown size={15} aria-hidden />{releasesExpanded ? t('settings.collapse') : t('settings.showAllLength', { length: releases.length })}
        </button>}
      </> : <StateView title={t('settings.noReleasesYet')} description={t('settings.withoutACompatibleReleaseNode')} action={<Button leftSection={<IconCloudUpload size={16} />} onClick={() => setUploadOpen(true)}>{t('settings.uploadRelease')}</Button>} />}
    </Surface>,

    appearance: <Surface
      key="appearance"
      className="nf-settings-card nf-settings-card--compact"
      data-card="appearance"
      title={<span className="nf-settings-card-title"><IconPalette size={18} />{t('settings.appearance')}</span>}
      description={t('settings.appliesImmediatelySavedWithSave')}
    >
      <div className="nf-cfg-body">
        <CfgFields>
          <CfgField id="panel-theme" label={t('settings.theme')}>
            <Select
              id="panel-theme"
              className="nf-cfg-select"
              value={panelDraft.theme}
              onChange={(value) => setPanelTheme((value ?? 'rose') as PanelSettings['theme'])}
              allowDeselect={false}
              disabled={panelSaving}
              data={[
                { value: 'rose', label: 'Rose' },
                { value: 'green', label: 'Green' },
                { value: 'cyan', label: 'Arctic Cyan' },
                { value: 'amber', label: 'Warm Amber' },
              ]}
            />
          </CfgField>
          <CfgField id="panel-accent" label={t('settings.accentColor')}>
            <Popover opened={accentPickerOpen} onChange={setAccentPickerOpen} position="bottom-start" width={280} shadow="md" withinPortal>
              <Popover.Target>
                <button id="panel-accent" className="nf-accent-trigger" type="button" onClick={() => setAccentPickerOpen((current) => !current)} disabled={panelSaving} aria-label={t('settings.chooseAccentColorCurrentlyAccent', { accent: panelDraft.accent })} aria-expanded={accentPickerOpen}>
                  <i style={{ background: normaliseAccent(panelDraft.accent) ?? defaultAccent }} />
                  <span>{panelDraft.accent || '#RRGGBB'}</span>
                  <IconChevronDown size={14} aria-hidden />
                </button>
              </Popover.Target>
              <Popover.Dropdown className="nf-accent-popover">
                <ColorPicker
                  format="hex"
                  value={normaliseAccent(panelDraft.accent) ?? defaultAccent}
                  onChange={(value) => setCustomAccent(value.toUpperCase())}
                  fullWidth
                />
                <div className="nf-accent-presets" aria-label={t('settings.presetAccents')}>
                  {accentPresets.map((color) => <button key={color} type="button" style={{ background: color }} onClick={() => setCustomAccent(color)} aria-label={t('settings.accentColor2', { color })} />)}
                </div>
                <TextInput
                  label="HEX"
                  value={panelDraft.accent}
                  onChange={(event) => setCustomAccent(event.currentTarget.value.toUpperCase())}
                  error={panelDraft.accent && !accentPattern.test(panelDraft.accent) ? t('settings.formatRrggbb') : undefined}
                  placeholder="#C27087"
                  maxLength={7}
                />
              </Popover.Dropdown>
            </Popover>
          </CfgField>
        </CfgFields>
      </div>
    </Surface>,

    security: <div key="security" className="nf-settings-pair" data-card="security"><Surface
      className="nf-settings-card nf-settings-card--compact nf-settings-card--sessions"
      title={<span className="nf-settings-card-title"><IconShieldLock size={18} />{t('settings.sessionsAndSecurity')}</span>}
    >
      <div className="nf-cfg-body">
        <CfgFields>
          <CfgField id="panel-session-timeout" label={t('settings.inactivityTimeout')} hint={t('settings.n51440Minutes')}>
            <CfgNumber id="panel-session-timeout" unit={t('routeModel.min')} value={panelDraft.session_timeout_minutes} onChange={(value) => setPanelField('session_timeout_minutes', Number(value))} min={5} max={1440} disabled={panelSaving} aria-describedby="panel-session-timeout-hint" onKeyDown={submitOnEnter} />
          </CfgField>
          <CfgField id="panel-max-sessions" label={t('settings.maxActiveSessions')} hint={t('settings.perPanel1100')}>
            <CfgNumber id="panel-max-sessions" unit={t('settings.pcs')} value={panelDraft.max_sessions} onChange={(value) => setPanelField('max_sessions', Number(value))} min={1} max={100} disabled={panelSaving} aria-describedby="panel-max-sessions-hint" onKeyDown={submitOnEnter} />
          </CfgField>
        </CfgFields>
      </div>
    </Surface>

    <Surface
      className="nf-settings-card nf-settings-card--compact nf-settings-card--logs"
      title={<span className="nf-settings-card-title"><IconHistory size={18} />{t('settings.logs')}</span>}
    >
      <div className="nf-cfg-body">
        <CfgFields>
          <CfgField id="panel-audit-retention" label={t('settings.keepAuditEvents')} hint={t('settings.n73650Days')}>
            <CfgNumber id="panel-audit-retention" unit={t('settings.days')} value={panelDraft.audit_retention_days} onChange={(value) => setPanelField('audit_retention_days', Number(value))} min={7} max={3650} disabled={panelSaving} aria-describedby="panel-audit-retention-hint" onKeyDown={submitOnEnter} />
          </CfgField>
        </CfgFields>
      </div>
    </Surface></div>,

    addresses: <Surface
      key="addresses"
      className="nf-settings-card nf-settings-card--compact"
      data-card="addresses"
      title={<span className="nf-settings-card-title"><IconWorld size={18} />{t('settings.addressesAndNodeChannel')}</span>}
      actions={<span className="nf-cfg-title-note"><IconLock size={13} />{t('editor.readOnly')}</span>}
    >
      <dl className="nf-cfg-kv" title={t('settings.takenFromTheServerRuntime')}>
        <div className="is-wide"><dt>{t('settings.panelUrl')}</dt><dd><CfgValue>{panelDraft.public_url || '—'}</CfgValue></dd></div>
        <div><dt>{t('settings.webApiPort')}</dt><dd><CfgValue>{panelDraft.web_port}</CfgValue></dd></div>
        <div><dt>{t('settings.agentChannelPort')}</dt><dd><CfgValue>{panelDraft.agent_port}</CfgValue></dd></div>
        <div><dt>{t('settings.nodeMtlsChannel')}</dt><dd><StatusPill tone={connectedNodes === 0 ? 'muted' : connectedNodes < nodeCount ? 'warn' : 'ok'} title={nodeCount ? t('settings.theAgentConnectsToThe', { agent_port: panelDraft.agent_port }) : t('settings.addANodeToCheck')}><IconShieldCheck size={12} />{nodeCount ? t('settings.connectednodesOfNodecountOnline', { connectedNodes, nodeCount }) : t('settings.noNodes')}</StatusPill></dd></div>
      </dl>
    </Surface>,

    metrics: <Surface key="metrics" className="nf-settings-card nf-metrics-card" data-card="metrics" title={<span className="nf-settings-card-title"><IconChartDots3 size={18} />{t('settings.prometheusMetrics')}</span>} actions={<StatusPill tone={panelEnabledMetrics ? 'ok' : 'muted'}>{panelEnabledMetrics ? t('editor.enabled') : t('node.disabled')}</StatusPill>}>
      <div className="nf-metrics-body"><div className="nf-metrics-main">
        <p className="nf-metrics-state">{panelEnabledMetrics ? t('settings.metricsEndpointBearerTokenRequired', { p0: metricsCidrs ? ` · allowlist: ${metricsCidrs} CIDR` : t('misc.withoutIpAllowlist') }) : t('settings.metricsEndpointIsDisabledEnable')}</p>
        <div className="nf-metrics-url"><code>{scrapeURL}</code><CopyButton value={scrapeURL}>{({ copied, copy }) => <Tooltip label={copied ? t('settings.copied') : t('templates.copy')} withArrow><ActionIcon size={28} variant="subtle" onClick={copy} aria-label={t('settings.copyUrl')}>{copied ? <IconCheck size={14} /> : <IconCopy size={14} />}</ActionIcon></Tooltip>}</CopyButton></div>
        <pre className="nf-metrics-snippet">{`- job_name: nodeflow
  metrics_path: /metrics
  authorization:
    credentials: <PANEL_METRICS_TOKEN>
  static_configs:
    - targets: ['${scrapeURL.replace(/^https?:\/\//, '').replace(/\/metrics$/, '')}']`}</pre>
      </div><div className="nf-metrics-ref">
        <details className="nf-cfg-details" open={!panelEnabledMetrics}>
          <summary><h3>{t('settings.configureInThePanelEnv')}</h3><IconChevronDown size={15} aria-hidden /></summary>
          <dl className="nf-metrics-vars">
            <dt><Code>PANEL_METRICS_ENABLED</Code></dt><dd><Code>true</Code> {t('settings.enableTheEndpoint')}</dd>
            <dt><Code>PANEL_METRICS_TOKEN</Code></dt><dd>{t('settings.bearerTokenAtLeast32')}</dd>
            <dt><Code>PANEL_METRICS_ALLOW_CIDRS</Code></dt><dd>{t('settings.optionalCommaSeparatedCidrs')}</dd>
          </dl>
          <p className="nf-cfg-help">{t('settings.afterChanging')} <Code>docker compose up -d</Code></p>
        </details>
        {/* Own column at 3 columns: show the list, it fills the column instead of empty space. */}
        <details className="nf-cfg-details" open={columnCount === 3 || undefined}>
          <summary><h3>{t('settings.metricsList')} <small>{t('settings.n7Prefix')} <code>nodeflow_node_</code></small></h3><IconChevronDown size={15} aria-hidden /></summary>
          <ul className="nf-metrics-list" aria-label={t('settings.nodeflowNodeMetrics')}>
            <li title="nodeflow_node_up"><code>up</code><span>{t('settings.nodeOnline')}</span></li>
            <li title="nodeflow_node_last_seen_seconds"><code>last_seen_seconds</code><span>{t('settings.lastHeartbeat')}</span></li>
            <li title="nodeflow_node_config_in_sync"><code>config_in_sync</code><span>{t('settings.revisionApplied')}</span></li>
            <li title="nodeflow_node_connections_current"><code>connections_current</code><span>{t('settings.currentConnections')}</span></li>
            <li title="nodeflow_node_backends_healthy, _degraded, _unavailable"><code>backends_healthy</code><span>+ degraded, unavailable</span></li>
            <li title="nodeflow_node_routes_total, nodeflow_node_routes_enabled"><code>routes_total</code><span>+ routes_enabled</span></li>
            <li title="nodeflow_node_traffic_bytes_in_month, nodeflow_node_traffic_bytes_out_month"><code>traffic_bytes_in_month</code><span>+ out_month</span></li>
          </ul>
          <p className="nf-cfg-help">{t('settings.labels')} <code>node</code>, <code>node_name</code></p>
        </details>
      </div></div>
    </Surface>,
  };

  return (
    <main className="nf-page nf-settings-page">
      <PageHeader
        className="nf-settings-header"
        icon={<IconSettings size={21} />}
        title={t('app.settings')}
        description={<>{t('settings.nodeAgentUpdatesPanelSettings')}{savedPanel.updated_at ? t('settings.panelSavedUpdatedAt', { updated_at: formatDayTime(savedPanel.updated_at) }) : ''}{explicitDemo ? t('settings.demoNothingIsWrittenTo') : ''}</>}
        badge={explicitDemo ? <span className="nf-demo-badge">{t('settings.demoData')}</span> : undefined}
        actions={<Button variant="default" leftSection={<IconRefresh size={16} />} onClick={load}>{t('settings.refreshState')}</Button>}
      />

      {data.partialErrors.length > 0 && <Alert className="nf-settings-partial" color="yellow" icon={<IconAlertCircle size={18} />} title={t('settings.someDataIsUnavailable')}>{data.partialErrors.slice(0, 3).join(' · ')}</Alert>}
      {feedback && <Alert className="nf-settings-feedback" color={feedback.tone === 'ok' ? 'nodeflow' : 'red'} icon={feedback.tone === 'ok' ? <IconCheck size={18} /> : <IconAlertCircle size={18} />} withCloseButton onClose={() => setFeedback(null)} role="status">{feedback.text}</Alert>}

      <div ref={gridRef} className={`nf-settings-grid is-${columnCount}col`} data-columns={columnCount}>
        {layout.columns.map((column, index) => <div key={index} className="nf-settings-col">{column.map((key) => cards[key])}</div>)}
        {layout.below.length > 0 && <div className="nf-settings-col nf-settings-col--below">{layout.below.map((key) => cards[key])}</div>}
      </div>

      {/* Zero-height sticky anchor: the bar floats over the bottom padding reserved by the page and never pushes content. */}
      <div className="nf-cfg-savebar-anchor">
        {(panelDirty || panelError) && <div className={`nf-cfg-savebar${panelDirty ? ' is-dirty' : ''}`} role="region" aria-label={t('settings.savingPanelSettings')}>
          <span>{panelDirty && <i aria-hidden />}{panelError || (validationErrors.length ? validationErrors.join(' ') : t('settings.unsavedPanelChanges'))}</span>
          {panelDirty && <Group gap={8} wrap="nowrap">
            <Button type="button" variant="default" onClick={resetPanelSettings} disabled={panelSaving}>{t('settings.reset')}</Button>
            <Button type="button" leftSection={<IconCheck size={16} />} onClick={() => void savePanelSettings()} loading={panelSaving} disabled={validationErrors.length > 0}>{t('node.save')}</Button>
          </Group>}
        </div>}
      </div>

      <Modal opened={Boolean(rollbackIntent)} onClose={() => busy !== 'rollback' && setRollbackIntent(null)} title={t('settings.confirmNodeAgentRollback')} size="sm" closeOnClickOutside={busy !== 'rollback'} closeOnEscape={busy !== 'rollback'} classNames={{ content: 'nf-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}>
        <div className="nf-confirm-dialog">
          <p>{t('nodes.node')} <strong>{rollbackIntent?.nodeName}</strong> · <strong>{platformLabel(rollbackIntent?.platform ?? null)}</strong> {t('settings.willMoveFromSequence')} <strong>#{rollbackIntent?.expectedActualSequence}</strong> {t('settings.toSignedRelease')} <strong>{rollbackIntent?.release.version} · #{rollbackIntent?.release.sequence}</strong>{t('settings.theUpdaterVerifiesEd25519And')}</p>
          {rollbackError && <div className="nf-inline-error" role="alert">{rollbackError}</div>}
          <div><Button variant="default" onClick={() => setRollbackIntent(null)} disabled={busy === 'rollback'}>{t('node.cancel')}</Button><Button leftSection={<IconArrowBackUp size={16} />} onClick={rollbackNode} loading={busy === 'rollback'}>{t('settings.assignRollback')}</Button></div>
        </div>
      </Modal>

      <Modal opened={Boolean(releaseToDelete)} onClose={() => { if (busy !== 'delete-release') { setReleaseToDelete(null); setDeleteReleaseBlocked(false); } }} title={t('settings.deleteAgentVersion')} size="sm" closeOnClickOutside={busy !== 'delete-release'} closeOnEscape={busy !== 'delete-release'} classNames={{ content: 'nf-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}>
        <div className="nf-confirm-dialog">
          <p>{t('settings.release')} <strong>{releaseToDelete?.version}</strong> · <strong>{releaseToDelete?.os}/{releaseToDelete?.arch}</strong> · sequence <strong>#{releaseToDelete?.sequence}</strong> {t('settings.willBeDeletedTogetherWith')}</p>
          <Alert color="yellow" icon={<IconAlertCircle size={17} />}>{t('settings.ifTheReleaseIsInstalled')}</Alert>
          {deleteReleaseError && <div className="nf-inline-error" role="alert">{deleteReleaseError}</div>}
          <div><Button variant="default" onClick={() => { setReleaseToDelete(null); setDeleteReleaseBlocked(false); }} disabled={busy === 'delete-release'}>{t('node.cancel')}</Button><Button color="red" leftSection={<IconTrash size={16} />} onClick={deleteRelease} loading={busy === 'delete-release'} disabled={deleteReleaseBlocked}>{deleteReleaseBlocked ? t('settings.deletionUnavailable') : t('settings.deleteVersion')}</Button></div>
        </div>
      </Modal>

      <Modal opened={uploadOpen} onClose={() => busy !== 'upload' && resetUpload()} title={t('settings.uploadSignedRelease')} size="md" closeOnClickOutside={busy !== 'upload'} closeOnEscape={busy !== 'upload'} classNames={{ content: 'nf-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}>
        <div className="nf-release-upload">
          <Alert color="nodeflow" icon={<IconKey size={18} />}>{t('settings.thePanelSignsTheBinary')}</Alert>
          <div className="nf-field-grid nf-field-grid--3">
            <TextInput label={t('settings.version')} placeholder="0.4.6-dev" value={uploadVersion} onChange={(event) => { setUploadVersion(event.currentTarget.value); if (uploadFile && uploadFile.size <= maxReleaseBytes) setUploadError(''); }} required />
            <Select label={t('settings.os')} value={uploadOS} onChange={(value) => setUploadOS(value ?? 'linux')} allowDeselect={false} data={['linux']} />
            <Select label={t('settings.architecture')} value={uploadArch} onChange={(value) => setUploadArch(value ?? 'amd64')} allowDeselect={false} data={['amd64', 'arm64']} />
          </div>
          <FileButton onChange={selectUploadFile} accept="application/octet-stream" resetRef={resetUploadInput}>
            {(props) => <Button {...props} variant="default" leftSection={<IconCloudUpload size={17} />}>{uploadFile ? `${uploadFile.name} · ${formatBytes(uploadFile.size)}` : t('settings.chooseABinaryUpTo')}</Button>}
          </FileButton>
          {uploadError && <Alert color="red" icon={<IconAlertCircle size={17} />} role="alert">{uploadError}</Alert>}
          <Group justify="flex-end"><Button variant="default" onClick={resetUpload} disabled={busy === 'upload'}>{t('node.cancel')}</Button><Button onClick={upload} loading={busy === 'upload'} disabled={!uploadFile || !uploadVersion.trim() || Boolean(uploadError)}>{t('settings.uploadAndSign')}</Button></Group>
        </div>
      </Modal>
    </main>
  );
}
