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
import './settings.css';

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
  catch (error) { errors.push(`${label}: ${error instanceof Error ? error.message : 'данные недоступны'}`); return fallback; }
}

async function loadSettings(): Promise<SettingsData> {
  if (explicitDemo) return demoSettings();
  const partialErrors: string[] = [];
  const [panel, nodes, releases, signingKey] = await Promise.all([
    api<PanelSettings>('/api/v1/settings'),
    api<NodeRecord[]>('/api/v1/nodes'),
    optional(api<AgentRelease[]>('/api/v1/agent-releases'), partialErrors, 'Релизы', []),
    optional(api<SigningKeyInfo>('/api/v1/agent-releases/signing-key'), partialErrors, 'Ключ подписи', null),
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
  if (!accentPattern.test(settings.accent)) errors.push('Акцент: цвет в формате #RRGGBB.');
  if (!Number.isInteger(settings.session_timeout_minutes) || settings.session_timeout_minutes < 5 || settings.session_timeout_minutes > 1440) errors.push('Таймаут сессии: от 5 до 1440 минут.');
  if (!Number.isInteger(settings.max_sessions) || settings.max_sessions < 1 || settings.max_sessions > 100) errors.push('Количество сессий: от 1 до 100.');
  if (!Number.isInteger(settings.audit_retention_days) || settings.audit_retention_days < 7 || settings.audit_retention_days > 3650) errors.push('Хранение аудита: от 7 до 3650 дней.');
  return errors;
}

function releaseStatus(release: AgentRelease, actualSequence: number | null, platform: AgentPlatform | null) {
  if (!platform) return 'Платформа неизвестна';
  if (actualSequence === null) return 'Состояние неизвестно';
  if (!releaseMatchesPlatform(release, platform)) return 'Другая платформа';
  if (release.sequence === actualSequence) return 'Установлено';
  if (release.sequence > actualSequence) return 'Доступно';
  return 'Для отката';
}

function updaterStateLabel(state?: string) {
  return ({
    idle: 'Ожидание', pending: 'Назначено', downloading: 'Загрузка', verified: 'Проверено',
    activating: 'Установка', installed: 'Установлено', failed: 'Ошибка', rolled_back: 'Выполнен откат',
  } as Record<string, string>)[state ?? ''] ?? state ?? 'Нет отчёта';
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
      optional(api<NodeOperational>(`/api/v1/nodes/${selectedNodeID}/operational`, { signal: controller.signal }), errors, 'Телеметрия', null),
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
      setFeedback({ tone: 'ok', text: explicitDemo ? 'Demo-настройки сохранены в текущем интерфейсе. Live Panel не изменён.' : 'Настройки панели сохранены.' });
    } catch (reason) {
      setPanelError(reason instanceof APIError && reason.status === 409
        ? 'Конфликт: настройки уже изменены в другой сессии. Обновите состояние перед повторным сохранением.'
        : reason instanceof Error ? reason.message : 'Настройки не сохранены.');
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
      updateNodeState(value); setFeedback({ tone: 'ok', text: `${available.version} назначен ноде ${selected.node.name}. Updater проверит подпись и выполнит авт rollback при ошибке.` });
    } catch (reason) { setFeedback({ tone: 'error', text: reason instanceof Error ? reason.message : 'Обновление не назначено' }); }
    finally { setBusy(''); }
  };

  const rollbackNode = async () => {
    if (!rollbackIntent) return;
    const target = data?.nodes.find(({ node }) => node.id === rollbackIntent.nodeID);
    const currentPlatform = heartbeatPlatform(target?.operational?.latest_heartbeat?.metrics);
    if (!currentPlatform || !releaseMatchesPlatform(rollbackIntent.release, currentPlatform)
      || currentPlatform.os !== rollbackIntent.platform.os || currentPlatform.arch !== rollbackIntent.platform.arch) {
      setRollbackError('Платформа ноды неизвестна или изменилась. Обновите состояние и выберите совместимый релиз заново.');
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
      setFeedback({ tone: 'ok', text: `Безопасный откат ${rollbackIntent.nodeName} на ${rollbackIntent.release.version} назначен.` });
      setRollbackIntent(null);
    } catch (reason) { setRollbackError(reason instanceof Error ? reason.message : 'Откат не назначен'); }
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
      setUploadError(`Файл больше 64 MiB: ${formatBytes(file.size)}.`);
      return;
    }
    setUploadFile(file);
    setUploadError('');
  };

  const upload = async () => {
    if (!uploadFile || !uploadVersion.trim()) return;
    if (uploadFile.size > maxReleaseBytes) {
      setUploadError(`Файл больше 64 MiB: ${formatBytes(uploadFile.size)}.`);
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
      setFeedback({ tone: 'ok', text: `Релиз ${value.version} загружен, SHA-256 проверен и подписан Ed25519.` });
      resetUpload();
    } catch (reason) { setUploadError(reason instanceof Error ? reason.message : 'Релиз не загружен'); }
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
      setFeedback({ tone: 'ok', text: `Релиз ${releaseToDelete.version} · ${releaseToDelete.os}/${releaseToDelete.arch} удалён.` });
      setReleaseToDelete(null);
    } catch (reason) {
      const releaseInUse = reason instanceof APIError && reason.status === 409 && reason.code === 'release_in_use';
      setDeleteReleaseBlocked(releaseInUse);
      setDeleteReleaseError(reason instanceof APIError && reason.status === 409
        ? 'Релиз установлен или назначен хотя бы одной ноде. Сначала переведите эти ноды на другую версию.'
        : reason instanceof Error ? reason.message : 'Релиз не удалён.');
    } finally { setBusy(''); }
  };

  if (loading) return <main className="nf-page nf-settings-page"><div className="nf-settings-loading"><LoadingOverlay visible /><span>Загружаем параметры панели и подписанные релизы…</span></div></main>;
  if (error && isUnauthorized(error)) return <LoginPanel onSuccess={load} />;
  if (error || !data) return <main className="nf-page"><StateView title="Настройки не загрузились" description={error instanceof Error ? error.message : 'Panel API недоступен'} tone="error" action={<Button onClick={load}>Повторить</Button>} /></main>;

  const releaseRowsLimit = 5;
  const visibleReleases = releasesExpanded ? releases : releases.slice(0, releaseRowsLimit);
  const hiddenInstalled = !releasesExpanded && currentRelease && !visibleReleases.includes(currentRelease) ? currentRelease : null;
  const updateBlocked = !nodePlatform || !selected?.update || detailsLoading || Boolean(busy);
  const panelEnabledMetrics = Boolean(data.panel.metrics_enabled);
  const metricsCidrs = data.panel.metrics_allow_cidrs ?? 0;
  const scrapeURL = `${data.panel.public_url || 'http://panel:8080'}/metrics`;
  const pillTone = (status: string): PillTone => status === 'Установлено' ? 'accent' : status === 'Доступно' ? 'ok' : status === 'Для отката' || status === 'Готов к установке' ? 'muted' : 'warn';
  const renderReleaseRow = (release: AgentRelease) => {
    const status = data.nodes.length ? releaseStatus(release, actualSequence, nodePlatform) : 'Готов к установке';
    const twin = releaseTwins.get(release.id);
    const installed = status === 'Установлено';
    const assigned = selected?.update?.desired_release?.id === release.id && !installed;
    return <div className={`nf-release-row${installed ? ' is-installed' : ''}${twin ? ' is-twin' : ''}`} role="row" key={release.id}>
      <div className="nf-release-row__version" role="cell"><strong>{release.version}</strong></div>
      <div className="nf-release-row__seq" role="cell"><code>#{release.sequence}</code></div>
      <div className="nf-release-row__platform" role="cell">{release.os}/{release.arch}<small>{formatBytes(release.size_bytes)}</small></div>
      <div className="nf-release-row__date" role="cell"><Tooltip label={formatDayTime(release.created_at)} withArrow><span>{formatDay(release.created_at)}</span></Tooltip></div>
      <div className="nf-release-row__sha" role="cell"><Tooltip label={release.sha256} withArrow multiline maw={320}><code>{release.sha256.slice(0, 10)}</code></Tooltip></div>
      <div className="nf-release-row__status" role="cell">
        <StatusPill tone={assigned ? 'info' : pillTone(status)}>{assigned ? 'Назначено' : status}</StatusPill>
        {twin && <span className="nf-release-row__twin" title={twin.kind === 'same' ? `SHA-256 совпадает с #${twin.sequence}` : `Версия ${release.version} перевыпущена в #${twin.sequence}`}>{twin.kind === 'same' ? `тот же бинарник, что #${twin.sequence}` : `пересобрана в #${twin.sequence}`}</span>}
      </div>
      <div className="nf-release-row__delete" role="cell">
        <Tooltip label={installed ? 'Установленный релиз удалить нельзя' : `Удалить ${release.version} · #${release.sequence}`} withArrow>
          <ActionIcon variant="subtle" color="red" size={32} aria-label={`Удалить релиз ${release.version} #${release.sequence} для ${release.os}/${release.arch}`} onClick={() => { setDeleteReleaseError(''); setDeleteReleaseBlocked(false); setReleaseToDelete(release); }} disabled={Boolean(busy) || installed}><IconTrash size={16} /></ActionIcon>
        </Tooltip>
      </div>
    </div>;
  };

  const agentStatusIcon = !nodePlatform || !selected?.update ? <IconAlertCircle size={15} /> : <IconCircleCheck size={15} />;
  const agentStatusText = detailsLoading ? 'Проверяем…' : !nodePlatform ? 'Платформа неизвестна' : !selected?.update ? 'Нет отчёта updater' : 'Актуальная версия';
  const updaterState = selected?.update?.state;

  const cards: Record<SettingsCardKey, ReactElement> = {
    agent: <Surface
      key="agent"
      id="node-agent"
      className="nf-settings-card nf-agent-settings"
      data-card="agent"
      title={<span className="nf-settings-card-title"><IconRocket size={18} />Node Agent</span>}
      description={<span className="nf-agent-trust"><IconShieldCheck size={14} />Подписанные релизы · {data.signingKey?.algorithm || 'Ed25519'} + SHA-256</span>}
      actions={<>
        {/* Fixed-width slot: switching nodes swaps «Обновить до …» and the status line without moving the other buttons. */}
        {data.nodes.length > 0 && <span className="nf-agent-primary">{available && nodePlatform && selected?.update
          ? <Button leftSection={<IconRocket size={16} />} onClick={assign} disabled={updateBlocked} loading={busy === 'update'} title={`Обновить до ${available.version}`}>Обновить до {available.version}</Button>
          : <span className="nf-agent-uptodate">{agentStatusIcon}{agentStatusText}</span>}</span>}
        {data.nodes.length > 0 && <Menu position="bottom-end" withinPortal shadow="md" width={300}>
          <Menu.Target>
            <Button variant="default" leftSection={<IconArrowBackUp size={16} />} rightSection={<IconChevronDown size={14} />} disabled={updateBlocked || rollbackReleases.length === 0}>Откатить</Button>
          </Menu.Target>
          <Menu.Dropdown className="nf-rollback-menu">
            <Menu.Label>Откатить {selected?.node.name} до версии</Menu.Label>
            {rollbackReleases.map((release) => {
              const sameBinary = currentRelease && release.sha256 === currentRelease.sha256;
              return <Menu.Item key={release.id} leftSection={<IconArrowBackUp size={15} />} rightSection={<code>#{release.sequence}</code>} onClick={() => requestRollback(release)}>
                {release.version}{sameBinary && <small> · тот же бинарник</small>}
              </Menu.Item>;
            })}
          </Menu.Dropdown>
        </Menu>}
        <Button variant="default" leftSection={<IconCloudUpload size={16} />} onClick={() => setUploadOpen(true)}>Загрузить релиз</Button>
      </>}
    >
      {data.nodes.length ? <div className="nf-agent-node">
        <dl className="nf-agent-facts">
          <div className="nf-agent-facts__node">
            <dt><label htmlFor="nf-agent-node-select">Нода</label></dt>
            <dd><Select id="nf-agent-node-select" className="nf-agent-node-select" aria-label="Нода для обновления" value={selected?.node.id} onChange={(value) => setSelectedNodeID(value ?? '')} allowDeselect={false} data={data.nodes.map(({ node }) => ({ value: node.id, label: `${node.name} · ${node.address}` }))} disabled={Boolean(busy)} leftSection={<IconServer size={15} />} /></dd>
          </div>
          <div><dt>Установлено</dt><dd><strong>{detailsLoading ? 'Загрузка…' : installedVersion}</strong>{selected?.update && <code>#{selected.update.actual_sequence}</code>}</dd></div>
          <div><dt>Отчёт updater</dt><dd>{selected?.update?.last_report_at ? <><span>{timeAgo(selected.update.last_report_at)}</span>{updaterState !== 'installed' && <StatusPill tone={updaterState === 'failed' ? 'danger' : 'muted'}>{updaterStateLabel(updaterState)}</StatusPill>}</> : <span className="is-muted">{detailsLoading ? 'Получаем состояние' : 'Ещё не получен'}</span>}</dd></div>
          <div><dt>Доступно</dt><dd>{available ? <><strong>{available.version}</strong><code>#{available.sequence}</code></> : <span className="is-muted">{!nodePlatform || actualSequence === null ? '—' : 'Новее нет'}</span>}</dd></div>
          <div><dt>Платформа</dt><dd><span>{platformLabel(nodePlatform)}</span></dd></div>
        </dl>
        {detailsError && <Alert color="yellow" icon={<IconAlertCircle size={17} />} role="alert">{detailsError}</Alert>}
        {!detailsLoading && !nodePlatform && <Alert color="yellow" icon={<IconAlertCircle size={17} />} role="status" title="Назначение релиза заблокировано">Дождитесь сигнала с полями os и arch. NodeFlow не будет предполагать платформу ноды.</Alert>}
        {!detailsLoading && nodePlatform && !selected?.update && <Alert color="yellow" icon={<IconAlertCircle size={17} />} role="status" title="Состояние updater неизвестно">Назначение и откат заблокированы, пока Panel не получит фактический sequence ноды.</Alert>}
        {selected?.update?.last_error && <Alert color="red" icon={<IconAlertCircle size={17} />}>{selected.update.last_error}</Alert>}
        <p className="nf-agent-note">HAProxy не перезапускается. Если новый Agent не пройдёт проверку запуска, updater атомарно вернёт предыдущий бинарник.</p>
      </div> : <StateView title="Нод пока нет" description="Релизы можно подготовить заранее; назначение обновлений станет доступно после установки ноды." action={<Button component={Link} to={`/nodes${demoQuery}`}>Добавить ноду</Button>} />}
    </Surface>,

    versions: <Surface
      key="versions"
      id="nf-agent-history"
      className="nf-settings-card nf-release-history"
      data-card="versions"
      title={<span className="nf-settings-card-title">Версии Agent <span className="nf-cfg-count">{releases.length}</span></span>}
      description="Новая нода получает новейший релиз для своей платформы. Sequence уникален и растёт с каждой загрузкой."
    >
      {releases.length ? <>
        <div className="nf-release-table" role="table" aria-label="Подписанные версии Agent">
          <div className="nf-release-row is-head" role="row">
            <span role="columnheader" className="nf-release-row__version">Версия</span><span role="columnheader" className="nf-release-row__seq">Seq</span><span role="columnheader" className="nf-release-row__platform">Платформа</span><span role="columnheader" className="nf-release-row__date">Загружен</span><span role="columnheader" className="nf-release-row__sha">SHA-256</span><span role="columnheader" className="nf-release-row__status">Статус</span><span role="columnheader" className="nf-release-row__delete"><span className="nf-visually-hidden">Удалить</span></span>
          </div>
          {visibleReleases.map(renderReleaseRow)}
          {hiddenInstalled && <>
            <div className="nf-release-gap" role="row"><span role="cell">…</span></div>
            {renderReleaseRow(hiddenInstalled)}
          </>}
        </div>
        {releases.length > releaseRowsLimit && <button type="button" className="nf-release-more" onClick={() => setReleasesExpanded((value) => !value)} aria-expanded={releasesExpanded}>
          <IconChevronDown size={15} aria-hidden />{releasesExpanded ? 'Свернуть' : `Показать все (${releases.length})`}
        </button>}
      </> : <StateView title="Релизов пока нет" description="Без совместимого релиза установка ноды будет заблокирована. Загрузите бинарник Linux для нужной архитектуры." action={<Button leftSection={<IconCloudUpload size={16} />} onClick={() => setUploadOpen(true)}>Загрузить релиз</Button>} />}
    </Surface>,

    appearance: <Surface
      key="appearance"
      className="nf-settings-card nf-settings-card--compact"
      data-card="appearance"
      title={<span className="nf-settings-card-title"><IconPalette size={18} />Внешний вид</span>}
      description="Применяется сразу, сохраняется кнопкой «Сохранить»"
    >
      <div className="nf-cfg-body">
        <CfgFields>
          <CfgField id="panel-theme" label="Тема">
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
          <CfgField id="panel-accent" label="Цветовой акцент">
            <Popover opened={accentPickerOpen} onChange={setAccentPickerOpen} position="bottom-start" width={280} shadow="md" withinPortal>
              <Popover.Target>
                <button id="panel-accent" className="nf-accent-trigger" type="button" onClick={() => setAccentPickerOpen((current) => !current)} disabled={panelSaving} aria-label={`Выбрать цветовой акцент, сейчас ${panelDraft.accent}`} aria-expanded={accentPickerOpen}>
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
                <div className="nf-accent-presets" aria-label="Готовые акценты">
                  {accentPresets.map((color) => <button key={color} type="button" style={{ background: color }} onClick={() => setCustomAccent(color)} aria-label={`Акцент ${color}`} />)}
                </div>
                <TextInput
                  label="HEX"
                  value={panelDraft.accent}
                  onChange={(event) => setCustomAccent(event.currentTarget.value.toUpperCase())}
                  error={panelDraft.accent && !accentPattern.test(panelDraft.accent) ? 'Формат #RRGGBB' : undefined}
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
      title={<span className="nf-settings-card-title"><IconShieldLock size={18} />Сессии и безопасность</span>}
    >
      <div className="nf-cfg-body">
        <CfgFields>
          <CfgField id="panel-session-timeout" label="Таймаут неактивности" hint="5–1440 минут">
            <CfgNumber id="panel-session-timeout" unit="мин" value={panelDraft.session_timeout_minutes} onChange={(value) => setPanelField('session_timeout_minutes', Number(value))} min={5} max={1440} disabled={panelSaving} aria-describedby="panel-session-timeout-hint" onKeyDown={submitOnEnter} />
          </CfgField>
          <CfgField id="panel-max-sessions" label="Макс. активных сессий" hint="на панель · 1–100">
            <CfgNumber id="panel-max-sessions" unit="шт" value={panelDraft.max_sessions} onChange={(value) => setPanelField('max_sessions', Number(value))} min={1} max={100} disabled={panelSaving} aria-describedby="panel-max-sessions-hint" onKeyDown={submitOnEnter} />
          </CfgField>
        </CfgFields>
      </div>
    </Surface>

    <Surface
      className="nf-settings-card nf-settings-card--compact nf-settings-card--logs"
      title={<span className="nf-settings-card-title"><IconHistory size={18} />Журналы</span>}
    >
      <div className="nf-cfg-body">
        <CfgFields>
          <CfgField id="panel-audit-retention" label="Хранить события аудита" hint="7–3650 дней">
            <CfgNumber id="panel-audit-retention" unit="дней" value={panelDraft.audit_retention_days} onChange={(value) => setPanelField('audit_retention_days', Number(value))} min={7} max={3650} disabled={panelSaving} aria-describedby="panel-audit-retention-hint" onKeyDown={submitOnEnter} />
          </CfgField>
        </CfgFields>
      </div>
    </Surface></div>,

    addresses: <Surface
      key="addresses"
      className="nf-settings-card nf-settings-card--compact"
      data-card="addresses"
      title={<span className="nf-settings-card-title"><IconWorld size={18} />Адреса и канал нод</span>}
      actions={<span className="nf-cfg-title-note"><IconLock size={13} />только чтение</span>}
    >
      <dl className="nf-cfg-kv" title="Берутся из runtime-конфигурации сервера и не меняются из браузера">
        <div className="is-wide"><dt>URL панели</dt><dd><CfgValue>{panelDraft.public_url || '—'}</CfgValue></dd></div>
        <div><dt>Web/API-порт</dt><dd><CfgValue>{panelDraft.web_port}</CfgValue></dd></div>
        <div><dt>Порт канала Agent</dt><dd><CfgValue>{panelDraft.agent_port}</CfgValue></dd></div>
        <div><dt>mTLS-канал нод</dt><dd><StatusPill tone={connectedNodes === 0 ? 'muted' : connectedNodes < nodeCount ? 'warn' : 'ok'} title={nodeCount ? `Agent подключается к панели по mTLS, TCP ${panelDraft.agent_port}` : 'Добавьте ноду, чтобы проверить защищённый канал'}><IconShieldCheck size={12} />{nodeCount ? `${connectedNodes} из ${nodeCount} на связи` : 'Нод нет'}</StatusPill></dd></div>
      </dl>
    </Surface>,

    metrics: <Surface key="metrics" className="nf-settings-card nf-metrics-card" data-card="metrics" title={<span className="nf-settings-card-title"><IconChartDots3 size={18} />Метрики Prometheus</span>} actions={<StatusPill tone={panelEnabledMetrics ? 'ok' : 'muted'}>{panelEnabledMetrics ? 'Включён' : 'Выключен'}</StatusPill>}>
      <div className="nf-metrics-body"><div className="nf-metrics-main">
        <p className="nf-metrics-state">{panelEnabledMetrics ? `Эндпоинт /metrics · Bearer-токен обязателен${metricsCidrs ? ` · allowlist: ${metricsCidrs} CIDR` : ' · без IP-allowlist'}.` : 'Эндпоинт /metrics выключен. Включается переменными окружения панели.'}</p>
        <div className="nf-metrics-url"><code>{scrapeURL}</code><CopyButton value={scrapeURL}>{({ copied, copy }) => <Tooltip label={copied ? 'Скопировано' : 'Копировать'} withArrow><ActionIcon size={28} variant="subtle" onClick={copy} aria-label="Копировать URL">{copied ? <IconCheck size={14} /> : <IconCopy size={14} />}</ActionIcon></Tooltip>}</CopyButton></div>
        <pre className="nf-metrics-snippet">{`- job_name: nodeflow
  metrics_path: /metrics
  authorization:
    credentials: <PANEL_METRICS_TOKEN>
  static_configs:
    - targets: ['${scrapeURL.replace(/^https?:\/\//, '').replace(/\/metrics$/, '')}']`}</pre>
      </div><div className="nf-metrics-ref">
        <details className="nf-cfg-details" open={!panelEnabledMetrics}>
          <summary><h3>Настройка в .env панели</h3><IconChevronDown size={15} aria-hidden /></summary>
          <dl className="nf-metrics-vars">
            <dt><Code>PANEL_METRICS_ENABLED</Code></dt><dd><Code>true</Code> — включить эндпоинт</dd>
            <dt><Code>PANEL_METRICS_TOKEN</Code></dt><dd>Bearer-токен, от 32 символов</dd>
            <dt><Code>PANEL_METRICS_ALLOW_CIDRS</Code></dt><dd>необязательно, CIDR через запятую</dd>
          </dl>
          <p className="nf-cfg-help">После изменения: <Code>docker compose up -d</Code></p>
        </details>
        {/* Own column at 3 columns: show the list, it fills the column instead of empty space. */}
        <details className="nf-cfg-details" open={columnCount === 3 || undefined}>
          <summary><h3>Список метрик <small>7 · префикс <code>nodeflow_node_</code></small></h3><IconChevronDown size={15} aria-hidden /></summary>
          <ul className="nf-metrics-list" aria-label="Метрики nodeflow_node_*">
            <li title="nodeflow_node_up"><code>up</code><span>нода на связи</span></li>
            <li title="nodeflow_node_last_seen_seconds"><code>last_seen_seconds</code><span>последний heartbeat</span></li>
            <li title="nodeflow_node_config_in_sync"><code>config_in_sync</code><span>ревизия применена</span></li>
            <li title="nodeflow_node_connections_current"><code>connections_current</code><span>текущие соединения</span></li>
            <li title="nodeflow_node_backends_healthy, _degraded, _unavailable"><code>backends_healthy</code><span>+ degraded, unavailable</span></li>
            <li title="nodeflow_node_routes_total, nodeflow_node_routes_enabled"><code>routes_total</code><span>+ routes_enabled</span></li>
            <li title="nodeflow_node_traffic_bytes_in_month, nodeflow_node_traffic_bytes_out_month"><code>traffic_bytes_in_month</code><span>+ out_month</span></li>
          </ul>
          <p className="nf-cfg-help">Метки: <code>node</code>, <code>node_name</code></p>
        </details>
      </div></div>
    </Surface>,
  };

  return (
    <main className="nf-page nf-settings-page">
      <PageHeader
        className="nf-settings-header"
        icon={<IconSettings size={21} />}
        title="Настройки"
        description={<>Обновления Node Agent, параметры панели и метрики Prometheus{savedPanel.updated_at ? ` · панель сохранена ${formatDayTime(savedPanel.updated_at)}` : ''}{explicitDemo ? ' · демо, без записи в Panel API' : ''}</>}
        badge={explicitDemo ? <span className="nf-demo-badge">Демо-данные</span> : undefined}
        actions={<Button variant="default" leftSection={<IconRefresh size={16} />} onClick={load}>Обновить состояние</Button>}
      />

      {data.partialErrors.length > 0 && <Alert className="nf-settings-partial" color="yellow" icon={<IconAlertCircle size={18} />} title="Часть данных недоступна">{data.partialErrors.slice(0, 3).join(' · ')}</Alert>}
      {feedback && <Alert className="nf-settings-feedback" color={feedback.tone === 'ok' ? 'nodeflow' : 'red'} icon={feedback.tone === 'ok' ? <IconCheck size={18} /> : <IconAlertCircle size={18} />} withCloseButton onClose={() => setFeedback(null)} role="status">{feedback.text}</Alert>}

      <div ref={gridRef} className={`nf-settings-grid is-${columnCount}col`} data-columns={columnCount}>
        {layout.columns.map((column, index) => <div key={index} className="nf-settings-col">{column.map((key) => cards[key])}</div>)}
        {layout.below.length > 0 && <div className="nf-settings-col nf-settings-col--below">{layout.below.map((key) => cards[key])}</div>}
      </div>

      {/* Zero-height sticky anchor: the bar floats over the bottom padding reserved by the page and never pushes content. */}
      <div className="nf-cfg-savebar-anchor">
        {(panelDirty || panelError) && <div className={`nf-cfg-savebar${panelDirty ? ' is-dirty' : ''}`} role="region" aria-label="Сохранение настроек панели">
          <span>{panelDirty && <i aria-hidden />}{panelError || (validationErrors.length ? validationErrors.join(' ') : 'Есть несохранённые изменения панели')}</span>
          {panelDirty && <Group gap={8} wrap="nowrap">
            <Button type="button" variant="default" onClick={resetPanelSettings} disabled={panelSaving}>Сбросить</Button>
            <Button type="button" leftSection={<IconCheck size={16} />} onClick={() => void savePanelSettings()} loading={panelSaving} disabled={validationErrors.length > 0}>Сохранить</Button>
          </Group>}
        </div>}
      </div>

      <Modal opened={Boolean(rollbackIntent)} onClose={() => busy !== 'rollback' && setRollbackIntent(null)} title="Подтвердить откат Node Agent" size="sm" closeOnClickOutside={busy !== 'rollback'} closeOnEscape={busy !== 'rollback'} classNames={{ content: 'nf-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}>
        <div className="nf-confirm-dialog">
          <p>Нода <strong>{rollbackIntent?.nodeName}</strong> · <strong>{platformLabel(rollbackIntent?.platform ?? null)}</strong> перейдёт с sequence <strong>#{rollbackIntent?.expectedActualSequence}</strong> на подписанный релиз <strong>{rollbackIntent?.release.version} · #{rollbackIntent?.release.sequence}</strong>. Updater проверит Ed25519 и SHA-256 перед активацией.</p>
          {rollbackError && <div className="nf-inline-error" role="alert">{rollbackError}</div>}
          <div><Button variant="default" onClick={() => setRollbackIntent(null)} disabled={busy === 'rollback'}>Отмена</Button><Button leftSection={<IconArrowBackUp size={16} />} onClick={rollbackNode} loading={busy === 'rollback'}>Назначить откат</Button></div>
        </div>
      </Modal>

      <Modal opened={Boolean(releaseToDelete)} onClose={() => { if (busy !== 'delete-release') { setReleaseToDelete(null); setDeleteReleaseBlocked(false); } }} title="Удалить версию Agent?" size="sm" closeOnClickOutside={busy !== 'delete-release'} closeOnEscape={busy !== 'delete-release'} classNames={{ content: 'nf-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}>
        <div className="nf-confirm-dialog">
          <p>Релиз <strong>{releaseToDelete?.version}</strong> · <strong>{releaseToDelete?.os}/{releaseToDelete?.arch}</strong> · sequence <strong>#{releaseToDelete?.sequence}</strong> будет удалён вместе с бинарником. Номер sequence повторно не используется.</p>
          <Alert color="yellow" icon={<IconAlertCircle size={17} />}>Если релиз установлен или назначен хотя бы одной ноде, Panel отклонит удаление и сохранит артефакт.</Alert>
          {deleteReleaseError && <div className="nf-inline-error" role="alert">{deleteReleaseError}</div>}
          <div><Button variant="default" onClick={() => { setReleaseToDelete(null); setDeleteReleaseBlocked(false); }} disabled={busy === 'delete-release'}>Отмена</Button><Button color="red" leftSection={<IconTrash size={16} />} onClick={deleteRelease} loading={busy === 'delete-release'} disabled={deleteReleaseBlocked}>{deleteReleaseBlocked ? 'Удаление недоступно' : 'Удалить версию'}</Button></div>
        </div>
      </Modal>

      <Modal opened={uploadOpen} onClose={() => busy !== 'upload' && resetUpload()} title="Загрузить подписанный релиз" size="md" closeOnClickOutside={busy !== 'upload'} closeOnEscape={busy !== 'upload'} classNames={{ content: 'nf-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}>
        <div className="nf-release-upload">
          <Alert color="nodeflow" icon={<IconKey size={18} />}>Panel подпишет бинарник серверным Ed25519-ключом. Приватный ключ никогда не передаётся в браузер.</Alert>
          <div className="nf-field-grid nf-field-grid--3">
            <TextInput label="Версия" placeholder="0.4.6-dev" value={uploadVersion} onChange={(event) => setUploadVersion(event.currentTarget.value)} required />
            <Select label="ОС" value={uploadOS} onChange={(value) => setUploadOS(value ?? 'linux')} allowDeselect={false} data={['linux']} />
            <Select label="Архитектура" value={uploadArch} onChange={(value) => setUploadArch(value ?? 'amd64')} allowDeselect={false} data={['amd64', 'arm64']} />
          </div>
          <FileButton onChange={selectUploadFile} accept="application/octet-stream" resetRef={resetUploadInput}>
            {(props) => <Button {...props} variant="default" leftSection={<IconCloudUpload size={17} />}>{uploadFile ? `${uploadFile.name} · ${formatBytes(uploadFile.size)}` : 'Выбрать бинарник до 64 MiB'}</Button>}
          </FileButton>
          {uploadError && <Alert color="red" icon={<IconAlertCircle size={17} />} role="alert">{uploadError}</Alert>}
          <Group justify="flex-end"><Button variant="default" onClick={resetUpload} disabled={busy === 'upload'}>Отмена</Button><Button onClick={upload} loading={busy === 'upload'} disabled={!uploadFile || !uploadVersion.trim() || Boolean(uploadError)}>Загрузить и подписать</Button></Group>
        </div>
      </Modal>
    </main>
  );
}
