import { randomUUID } from '../lib/uuid';
import {
  Alert, Button, Collapse, LoadingOverlay, Menu, Modal, NumberInput, SegmentedControl, Select,
  Switch, TagsInput, Textarea, TextInput, Tooltip, ActionIcon,
} from '@mantine/core';
import {
  IconAlertCircle, IconAlertTriangle, IconArrowDown, IconArrowLeft, IconArrowUp, IconCheck, IconChevronDown,
  IconCode, IconDots, IconInfoCircle, IconPlus, IconShieldCheck, IconSwitchHorizontal, IconTrash, IconWorldSearch,
} from '@tabler/icons-react';
import { useQueryClient } from '@tanstack/react-query';
import { useBeforeUnload } from 'react-router-dom';
import { useEffect, useMemo, useRef, useState, type CSSProperties, type FormEvent } from 'react';
import { Link, useBlocker, useLocation, useNavigate, useParams } from 'react-router-dom';
import { LoginPanel } from '../components/LoginPanel';
import { PageHeader } from '../components/PageHeader';
import { StateView } from '../components/StateView';
import { Surface } from '../components/Surface';
import {
  balanceModeApplicable, effectiveBalanceMode, effectiveHealthCheck, emptyRouteDraft, emptyServerDraft, isIPAddress, nextServerName,
  preferredIPApplies, quotaPeriodOptions, renderRoutePreview, routePayload, routeToDraft, stickyApplicable,
  serverIsBackup, withCanonicalFailoverRoles, SERVER_NAME_MAX, kernelShaperSupported, KERNEL_SHAPER_MIN_AGENT,
  effectiveStickyMode, balanceAlgorithmOptions, balanceAlgorithmFamily, effectiveBalanceAlgorithm,
  hasDNSPoolServer, validateRouteDraft, DEFAULT_STICKY_TTL, DEFAULT_LEASTPING_TOLERANCE_PCT,
  LEASTCONN_TOLERANCE_MIN_AGENT, leastconnToleranceSupported,
  distributionChoice, distributionChoiceOptions, stickyModeForChoice, stickyTTLQuickPicks, ttlParts, ttlUnitLabels,
  ttlQuickPickLabel, tableEntriesCount, tableEntriesToken, tableMemoryEstimate, TABLE_MAX_CLIENTS,
  autoTableHint, draftAutoTableEntries, routeUsesAutoTable,
  slowstartSeconds,
  type BalanceAlgorithm, type BalanceMode, type DistributionChoice, type IPWeightDraft, type ProxyProtocol, type QuotaAction,
  type QuotaPeriod, type RouteDraft, type RouteDraftError, type RouteMatchMode, type ServerDraft, type ShaperMode,
} from '../features/routes/model';
import {
  BlockHead, EditorSection, Note, OptionList, OptionRow, SettingRow, SettingsDivider, SettingsGrid, SettingsNote,
  UnitNumberInput, plural,
} from '../features/routes/editor-ui';
import { APIError, api, isUnauthorized, resolveDNS } from '../lib/api';
import type { NodeOperational, NodeRecord, RouteRecord, DNSResolveResult } from '../lib/contracts';
import './route-editor.css';

interface EditorData { node: NodeRecord; routes: RouteRecord[]; route?: RouteRecord; agentVersion?: string; memoryTotalBytes?: number }

const KERNEL_SHAPER_AGENT_CODE = 'kernel_shaper_requires_agent_1_1';
const kernelShaperAgentHint = `Ядерный шейпер требует Node Agent ${KERNEL_SHAPER_MIN_AGENT} на этой ноде`;
type SaveIntent = 'draft' | 'enable' | 'apply';

const explicitDemo = new URLSearchParams(window.location.search).get('demo') === '1'
  || import.meta.env.VITE_NODEFLOW_DEMO === 'true';

function errorText(errors: RouteDraftError[], fields: RouteDraftError['field'][]): string | undefined {
  return errors.find((error) => fields.includes(error.field))?.message;
}

const MISSING_FIELD_LABELS: Partial<Record<RouteDraftError['field'], string>> = {
  name: 'имя', snis: 'SNI', listenerPort: 'порт', listenerIP: 'IP ноды', listener: 'порт',
  quota: 'лимит трафика', clientUploadMbps: 'скорость', clientDownloadMbps: 'скорость', bandwidth: 'скорость',
};

/** Neutral «what is left» line for an untouched form: «Заполните имя, SNI и адрес сервера». */
function missingFieldsHint(errors: RouteDraftError[]): string {
  const parts: string[] = [];
  for (const error of errors) {
    const label = error.field === 'servers'
      ? (/адрес|путь/i.test(error.message) ? 'адрес сервера' : 'серверы')
      : MISSING_FIELD_LABELS[error.field];
    if (label && !parts.includes(label)) parts.push(label);
  }
  if (!parts.length) return 'Проверьте поля формы';
  const list = parts.length > 1 ? `${parts.slice(0, -1).join(', ')} и ${parts.at(-1)}` : parts[0];
  return `Заполните ${list}`;
}

function statusCopy(intent: SaveIntent, editing: boolean) {
  if (intent === 'draft') return editing ? 'Черновик сохранён' : 'Маршрут создан как выключенный черновик';
  if (intent === 'apply') return 'Изменения отправлены на ноду';
  return 'Маршрут создан и отправлен на включение';
}

async function loadEditor(nodeID: string, routeID?: string): Promise<EditorData> {
  if (explicitDemo) {
    const { demoNodeBundles, mergeDemoRoutes } = await import('../fixtures/demo');
    const bundle = structuredClone(demoNodeBundles.find(({ node }) => node.id === nodeID) ?? demoNodeBundles[0]);
    bundle.node.id = nodeID;
    bundle.routes = mergeDemoRoutes(nodeID, bundle.routes.map((route) => ({ ...route, node_id: nodeID })));
    const route = routeID ? bundle.routes.find((item) => item.id === routeID) : undefined;
    const heartbeat = bundle.operational?.latest_heartbeat;
    return { node: bundle.node, routes: bundle.routes, route, agentVersion: heartbeat?.agent_version, memoryTotalBytes: heartbeat?.metrics?.memory_total_bytes };
  }
  const [node, routes, operational] = await Promise.all([
    api<NodeRecord>(`/api/v1/nodes/${nodeID}`),
    api<RouteRecord[]>(`/api/v1/nodes/${nodeID}/routes`),
    // Gates the kernel shaper option and sizes «Авто» stick-tables (node RAM); the editor works without it.
    api<NodeOperational>(`/api/v1/nodes/${nodeID}/operational`).catch(() => null),
  ]);
  // The list row carries servers[] too, but the single-route GET is the
  // authoritative full shape the editor round-trips.
  const route = routeID ? await api<RouteRecord>(`/api/v1/nodes/${nodeID}/routes/${routeID}`) : undefined;
  const heartbeat = operational?.latest_heartbeat;
  return { node, routes, route, agentVersion: heartbeat?.agent_version, memoryTotalBytes: heartbeat?.metrics?.memory_total_bytes };
}

/** Full stored shape of a route after a save (POST/PUT responses may omit servers[]). */
function savedDraft(submitted: RouteDraft, saved: RouteRecord): RouteDraft {
  const sentServers = routePayload(submitted, saved.enabled).servers.length > 0;
  // A response/record without servers[] is not proof that servers were removed:
  // keep what was submitted instead of collapsing it to the legacy target.
  if (sentServers && !(saved.servers ?? []).length) return submitted;
  return routeToDraft({ ...saved, name: submitted.name });
}

async function fetchSavedRoute(nodeID: string, saved: RouteRecord): Promise<RouteRecord> {
  try {
    return await api<RouteRecord>(`/api/v1/nodes/${nodeID}/routes/${saved.id}`);
  } catch (error) {
    if (isUnauthorized(error)) throw error;
    return saved;
  }
}


// ─── DNS lookups (Основной IP / отдельные IP) ─────────────────────────────────
interface ResolveState {
  host: string;
  loading: boolean;
  addresses: string[];
  error: string;
  done: boolean;
}

const idleResolve: ResolveState = { host: '', loading: false, addresses: [], error: '', done: false };

async function resolveHost(host: string) {
  if (explicitDemo) {
    const { demoResolveDNS } = await import('../fixtures/demo');
    return demoResolveDNS(host);
  }
  return resolveDNS(host);
}

/** One resolver per sub-row; results belong to one host, editing the address hides stale chips. */
function useResolver(hostValue: string) {
  const [state, setState] = useState<ResolveState>(idleResolve);
  const host = hostValue.trim();
  const canResolve = host !== '' && !isIPAddress(host);
  const current = state.host === host ? state : idleResolve;
  const run = async () => {
    if (!canResolve) return;
    setState({ ...idleResolve, host, loading: true });
    try {
      const result: DNSResolveResult = await resolveHost(host);
      setState({
        host, loading: false, done: true, addresses: result.addresses ?? [],
        error: result.addresses?.length ? '' : (result.error || 'Домен не вернул ни одного адреса.'),
      });
    } catch (error) {
      const message = error instanceof APIError && error.code === 'invalid_host'
        ? 'Некорректное имя домена.'
        : error instanceof Error ? error.message : 'Не удалось получить адреса.';
      setState({ ...idleResolve, host, done: true, error: message });
    }
  };
  return { host, canResolve, current, run };
}

function ResolveButton({ canResolve, loading, disabled = false, onClick }: { canResolve: boolean; loading: boolean; disabled?: boolean; onClick: () => void }) {
  return (
    <Tooltip label="Сначала укажите домен в поле «Адрес»" disabled={canResolve} openDelay={200}>
      <span className="nf-inline-flex">
        <Button variant="default" size="sm" leftSection={<IconWorldSearch size={14} />} onClick={onClick} loading={loading} disabled={!canResolve || disabled}>
          IP домена
        </Button>
      </span>
    </Tooltip>
  );
}

// ─── Server table: sub-rows ─────────────────────────────────────────────────────
function PreferredIPLine({ server, hasOtherServers, error, onChange }: {
  server: ServerDraft; hasOtherServers: boolean; error?: string; onChange: (preferredIP: string) => void;
}) {
  const { host, canResolve, current, run } = useResolver(server.host);
  const value = server.preferredIP.trim();
  const invalid = value !== '' && !isIPAddress(value);
  const id = `preferred-${server._key}`;

  return (
    <div className="nf-srv-sub">
      <div className="nf-srv-sub__head">
        <label className="nf-srv-sub__title" htmlFor={id}>Основной IP</label>
        <span className="nf-srv-sub__hint" id={`${id}-hint`}>
          {hasOtherServers
            ? 'Весь трафик на этот IP; если он недоступен — на все IP домена и резервные серверы.'
            : 'Весь трафик на этот IP; если он недоступен — на все IP домена.'}
        </span>
      </div>
      <div className="nf-srv-sub__controls">
        <TextInput
          id={id}
          size="sm"
          className="nf-srv-sub__ip"
          placeholder="не задан"
          value={server.preferredIP}
          onChange={(e) => onChange(e.currentTarget.value)}
          error={invalid || error ? true : undefined}
          aria-describedby={`${id}-hint`}
        />
        <ResolveButton canResolve={canResolve} loading={current.loading} onClick={() => void run()} />
      </div>
      {(invalid || error) && <p className="nf-cell-error">{invalid ? 'Некорректный IP-адрес.' : error}</p>}
      {current.done && (
        current.error
          ? <p className="nf-srv-sub__warning"><IconAlertCircle size={13} aria-hidden="true" />{current.error}</p>
          : (
            <>
              <div className="nf-ip-chips" role="group" aria-label={`IP-адреса ${host}`}>
                {current.addresses.map((address) => {
                  const selected = address === value;
                  return (
                    <button type="button" key={address} className={`nf-ip-chip${selected ? ' is-selected' : ''}`} aria-pressed={selected} onClick={() => onChange(selected ? '' : address)}>
                      {selected && <IconCheck size={12} aria-hidden="true" />}
                      {address}
                    </button>
                  );
                })}
              </div>
              <p className="nf-srv-sub__note">Адреса получены с хоста панели — нода может видеть другие.</p>
            </>
          )
      )}
    </div>
  );
}

const IP_WEIGHT_HELP = 'Доля новых клиентов для этого IP: 2 — вдвое больше, чем у IP с весом 1. Пусто — вес сервера (по умолчанию 1).';
const IP_COST_CONN_HELP = 'Сколько весит одно соединение на этом IP: 2 — каждое соединение считается за два, и IP получает вдвое меньше клиентов. Пусто — стоимость сервера (по умолчанию 1).';
const IP_COST_PING_HELP = 'Множитель задержки этого IP: 2 — IP кажется вдвое медленнее и выбирается реже. Пусто — стоимость сервера (по умолчанию 1).';
const COST_OFF_HELP = 'Стоимость учитывают алгоритмы «Меньше соединений» и «Меньше задержка».';
const IP_SHARE_CONN_NOTE = 'Доля IP = вес ÷ стоимость. Например, 4 ядра против 2: вес 2 у 4-ядерного (или стоимость 2 у 2-ядерного) — он получит вдвое больше клиентов. При «Запоминании» уже закреплённые клиенты остаются на своём IP.';
const IP_SHARE_PING_NOTE = 'Чем меньше задержка × стоимость, тем больше клиентов получает IP; вес задаёт долю при равной задержке. Под мощность сервера: 4 ядра против 2 — вес 2 у 4-ядерного.';
const IP_SHARE_NOTE = 'Доля IP пропорциональна весу. Например, 4 ядра против 2: вес 2 у 4-ядерного — он получит вдвое больше клиентов.';

interface IPWeightsEditorProps {
  server: ServerDraft;
  agentAllowed: boolean;
  /** Per-IP cost column (same condition as the server cost column). */
  showCost: boolean;
  /** leastconn: cost multiplies connections (else it multiplies latency). */
  connections: boolean;
  /** static-rr / «Ровное» hash: runtime weights cannot change. */
  weightsFixed: boolean;
  error?: string;
  onChange: (ipWeights: IPWeightDraft[]) => void;
}

function IPWeightsEditor({ server, agentAllowed, showCost, connections, weightsFixed, error, onChange }: IPWeightsEditorProps) {
  const { host, canResolve, current, run } = useResolver(server.host);
  const [manual, setManual] = useState('');
  const known = new Set(server.ipWeights.map((w) => w.ip));
  const full = server.ipWeights.length >= 32;
  const manualValue = manual.trim();
  const manualInvalid = manualValue !== '' && !isIPAddress(manualValue);
  const manualDuplicate = manualValue !== '' && known.has(manualValue);
  const label = server.name.trim() || host;

  const addIP = (ip: string) => {
    if (known.has(ip) || full) return;
    onChange([...server.ipWeights, { ip, weight: '', cost: '' }]);
  };
  const addManual = () => {
    if (!manualValue || manualInvalid || manualDuplicate) return;
    addIP(manualValue);
    setManual('');
  };
  const patch = (ip: string, value: Partial<IPWeightDraft>) => {
    onChange(server.ipWeights.map((w) => (w.ip === ip ? { ...w, ...value } : w)));
  };
  const removeIP = (ip: string) => onChange(server.ipWeights.filter((w) => w.ip !== ip));

  return (
    <div className="nf-srv-sub">
      <div className="nf-srv-sub__head">
        <span className="nf-srv-sub__title">Вес и стоимость отдельных IP</span>
        <span className="nf-srv-sub__hint">Пусто — как у сервера.{weightsFixed ? ' Сейчас веса фиксированы и не применятся.' : ''}</span>
      </div>
      {!agentAllowed && <p className="nf-srv-sub__warning is-error"><IconAlertTriangle size={13} aria-hidden="true" />Веса отдельных IP применяет агент ноды — обновите Node Agent до {KERNEL_SHAPER_MIN_AGENT} или новее.</p>}
      <div className="nf-srv-sub__body">
      {server.ipWeights.length > 0 && !weightsFixed && (
        <p className="nf-srv-sub__note">{connections ? IP_SHARE_CONN_NOTE : showCost ? IP_SHARE_PING_NOTE : IP_SHARE_NOTE}</p>
      )}
      {server.ipWeights.length > 0 && (
        // Cost column is always present (disabled when the algorithm ignores it) so the table never reflows.
        <div className="nf-ipw has-cost" role="table" aria-label={`Отдельные IP ${label}`}>
          <div className="nf-ipw__row is-head" role="row">
            <span role="columnheader">IP</span>
            <span role="columnheader">
              <Tooltip label={IP_WEIGHT_HELP} multiline w={260} openDelay={150}>
                <span className="nf-help-term">Вес</span>
              </Tooltip>
            </span>
            <span role="columnheader" className={showCost ? undefined : 'is-off'}>
              <Tooltip label={!showCost ? COST_OFF_HELP : connections ? IP_COST_CONN_HELP : IP_COST_PING_HELP} multiline w={260} openDelay={150}>
                <span className="nf-help-term">Стоимость</span>
              </Tooltip>
            </span>
            {/* Stays in grid flow: a hidden (absolute) cell would shift every row by one column. */}
            <span role="columnheader" aria-label="Удалить" />
          </div>
          {server.ipWeights.map((w) => (
            <div className="nf-ipw__row" role="row" key={w.ip}>
              <span className="nf-ipw__ip" role="cell" title={w.ip}>{w.ip}</span>
              <span role="cell">
                <NumberInput size="sm" value={w.weight} min={1} max={256} allowDecimal={false} hideControls inputMode="numeric" placeholder={server.weight === '' ? '1' : String(server.weight)} disabled={weightsFixed && w.weight === ''} error={weightsFixed && w.weight !== '' ? true : undefined} onChange={(v) => patch(w.ip, { weight: v === '' ? '' : Number(v) })} aria-label={`Вес ${w.ip}`} />
              </span>
              <span role="cell">
                <NumberInput size="sm" value={showCost ? w.cost : ''} min={0.01} max={100} decimalScale={2} hideControls inputMode="decimal" placeholder={!showCost ? '—' : server.cost === '' ? '1' : String(server.cost)} disabled={!showCost} onChange={(v) => patch(w.ip, { cost: v === '' ? '' : Number(v) })} aria-label={`Стоимость ${w.ip}`} />
              </span>
              <span role="cell" className="nf-ipw__action">
                <ActionIcon variant="subtle" color="gray" size={36} onClick={() => removeIP(w.ip)} aria-label={`Убрать ${w.ip}`}><IconTrash size={14} /></ActionIcon>
              </span>
            </div>
          ))}
        </div>
      )}
      <div className="nf-srv-sub__controls">
        <TextInput
          size="sm"
          className="nf-srv-sub__ip"
          placeholder="IP-адрес"
          value={manual}
          onChange={(e) => setManual(e.currentTarget.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addManual(); } }}
          error={manualInvalid || manualDuplicate ? true : undefined}
          aria-label={`Добавить IP для ${label}`}
          disabled={full}
        />
        <Button variant="default" size="sm" leftSection={<IconPlus size={14} />} onClick={addManual} disabled={!manualValue || manualInvalid || manualDuplicate || full}>
          Добавить
        </Button>
        <ResolveButton canResolve={canResolve} loading={current.loading} disabled={full} onClick={() => void run()} />
      </div>
      </div>
      {(manualInvalid || manualDuplicate) && <p className="nf-cell-error">{manualInvalid ? 'Некорректный IP-адрес.' : 'Этот IP уже в списке.'}</p>}
      {error && <p className="nf-cell-error">{error}</p>}
      {current.done && (
        current.error
          ? <p className="nf-srv-sub__warning"><IconAlertCircle size={13} aria-hidden="true" />{current.error}</p>
          : (
            <div className="nf-ip-chips" role="group" aria-label={`IP-адреса ${host}`}>
              {current.addresses.map((address) => (
                <button type="button" key={address} className={`nf-ip-chip${known.has(address) ? ' is-selected' : ''}`} disabled={known.has(address) || full} onClick={() => addIP(address)}>
                  {known.has(address) ? <IconCheck size={12} aria-hidden="true" /> : <IconPlus size={12} aria-hidden="true" />}
                  {address}
                </button>
              ))}
            </div>
          )
      )}
    </div>
  );
}

// ─── Server table ───────────────────────────────────────────────────────────────
type ServerCell = 'name' | 'host' | 'port' | 'unix' | 'dns' | 'weight' | 'cost' | 'preferred' | 'ipweights' | 'row';
type ServerColumn = 'role' | 'name' | 'type' | 'host' | 'port' | 'dns' | 'weight' | 'cost' | 'actions';

/** Fixed column tracks; header and every row share one template, so cells align to the pixel. */
const SERVER_COLUMN_TRACKS: Record<ServerColumn, string> = {
  // Bounded tracks reach their max before the address (1fr) takes the rest.
  // Every column is always present (unused ones render disabled).
  role: '0px',
  name: 'minmax(88px, 180px)',
  type: 'minmax(108px, 140px)',
  host: 'minmax(120px, 1fr)',
  port: 'minmax(72px, 96px)',
  dns: 'minmax(56px, 80px)',
  weight: 'minmax(64px, 72px)',
  cost: 'minmax(72px, 88px)',
  actions: '36px',
};

/**
 * Routes «Сервер N: …» validation messages to the offending cell. UI-only mapping
 * over the model's messages; anything unrecognised stays a row-level message.
 */
function cellForMessage(text: string): ServerCell {
  if (/^имя/i.test(text)) return 'name';
  if (text.includes('основной IP')) return 'preferred';
  if (/(вес|стоимость) IP|отдельных IP|IP не должны|IP в списке|вес и стоимость IP/.test(text)) return 'ipweights';
  if (text.includes('DNS-пул')) return 'dns';
  if (/адрес|IPv4/.test(text)) return 'host';
  if (text.includes('Unix')) return 'unix';
  if (text.includes('порт')) return 'port';
  if (text.startsWith('вес')) return 'weight';
  if (text.startsWith('стоимость')) return 'cost';
  return 'row';
}

function splitServerErrors(messages: string[]) {
  const perServer = new Map<number, { cell: ServerCell; message: string }[]>();
  const general: string[] = [];
  for (const message of messages) {
    const match = /^Сервер (\d+): (.+)$/.exec(message);
    if (!match) { general.push(message); continue; }
    const index = Number(match[1]) - 1;
    const text = match[2];
    const list = perServer.get(index) ?? [];
    list.push({ cell: cellForMessage(text), message: text.charAt(0).toUpperCase() + text.slice(1) });
    perServer.set(index, list);
  }
  return { perServer, general };
}

interface ServersEditorProps {
  servers: ServerDraft[];
  balanceMode: BalanceMode;
  /** Set when several candidates share the load (pool mode); null otherwise. */
  poolSettings: { showCost: boolean; connections: boolean; weightsFixed: boolean; agentAllowed: boolean } | null;
  onChange: (servers: ServerDraft[]) => void;
  /** Validation messages for field 'servers' (already gated by submit). */
  errors: string[];
}

function ServersEditor({ servers, balanceMode: rawBalanceMode, poolSettings, onChange, errors }: ServersEditorProps) {
  const maxReached = servers.length >= 16;
  const balanceMode = effectiveBalanceMode({ servers, balanceMode: rawBalanceMode });
  const draftView = { servers, balanceMode };
  const failover = balanceMode === 'failover' && servers.length > 1;
  const canRemove = servers.length > 1;
  const { perServer, general } = splitServerErrors(errors);

  // One template for every mode, so switching DNS-пул / algorithm / failover never
  // resizes «Адрес»: Вес and Стоимость (pool only) stay as disabled cells, and in
  // «Основной + резервные» the role badge takes those same two tracks.
  const columns: ServerColumn[] = ['name', 'type', 'host', 'port', 'dns', 'weight', 'cost', 'actions'];
  const col = (key: ServerColumn) => (key === 'role' ? columns.indexOf('weight') : columns.indexOf(key)) + 1;
  const tableStyle = {
    '--srv-cols': columns.map((key) => SERVER_COLUMN_TRACKS[key]).join(' '),
    '--srv-sub-start': String(col('name')),
  } as CSSProperties;
  const weightOn = Boolean(poolSettings);
  const costOn = Boolean(poolSettings?.showCost);
  const offHint = 'Нужно два сервера или DNS-пул в режиме «Пул»';

  const update = (index: number, patch: Partial<ServerDraft>) => {
    onChange(servers.map((s, i) => i === index ? { ...s, ...patch } : s));
  };
  const remove = (index: number) => {
    const next = servers.filter((_, i) => i !== index);
    // Never leave a failover route with reserves only: promote the first server.
    if (next.length > 0 && next.every((s) => s.backup)) next[0] = { ...next[0], backup: false };
    onChange(next);
  };
  const setRole = (index: number, backup: boolean) => {
    update(index, backup ? { backup, preferredIP: '' } : { backup });
  };
  const move = (index: number, delta: -1 | 1) => {
    const target = index + delta;
    if (target < 0 || target >= servers.length) return;
    const next = [...servers];
    [next[index], next[target]] = [next[target], next[index]];
    onChange(next);
  };

  return (
    <div className="nf-servers">
      <div className={`nf-srv-table${failover ? ' is-failover' : ''}`} style={tableStyle}>
        <div className="nf-srv-table__head" aria-hidden="true">
          <span className="is-field">Имя</span>
          <span className="is-field">Тип</span>
          <span className="is-field">Адрес</span>
          <span className="is-field">Порт</span>
          <span className="is-center">
            <Tooltip label="HAProxy резолвит домен и распределяет трафик по всем его IP. Только для домена." multiline w={240} openDelay={150}>
              <span className="nf-help-term">DNS-пул</span>
            </Tooltip>
          </span>
          {failover ? <span className="nf-srv-table__role-head">Роль</span> : <>
          <span className={`is-field${weightOn ? '' : ' is-off'}`}>
            <Tooltip label={weightOn ? 'Доля новых клиентов относительно других серверов: 2 — вдвое больше, чем у сервера с весом 1. Пусто — 1.' : `Вес: ${offHint.toLowerCase()}.`} multiline w={220} openDelay={150}>
              <span className="nf-help-term">Вес</span>
            </Tooltip>
          </span>
          <span className={`is-field${costOn ? '' : ' is-off'}`}>
            <Tooltip label={!costOn
              ? 'Стоимость учитывают алгоритмы «Меньше соединений» и «Меньше задержка».'
              : poolSettings?.connections
                ? 'Каждое соединение считается за столько: 2 — сервер получает вдвое меньше клиентов. Пусто — 1.'
                : 'Множитель задержки: 2 — сервер кажется вдвое медленнее. Пусто — 1.'} multiline w={220} openDelay={150}>
              <span className="nf-help-term">Стоимость</span>
            </Tooltip>
          </span>
          </>}
          <span />
        </div>
        {servers.map((server, index) => {
          const hostIsIP = server.host.trim() !== '' && isIPAddress(server.host);
          const tcp = server.targetType === 'tcp';
          const label = server.name.trim() || `сервер ${index + 1}`;
          const backup = serverIsBackup(draftView, index);
          const cellErrors = perServer.get(index) ?? [];
          const errorFor = (cell: ServerCell) => cellErrors.find((e) => e.cell === cell)?.message;
          const inlineErrors = cellErrors.filter((e) => e.cell !== 'preferred' && e.cell !== 'ipweights');
          const showPreferred = tcp && preferredIPApplies({ servers, balanceMode }, index);
          const showIPWeights = tcp && server.dnsPool && poolSettings;
          return (
            <div className={`nf-srv-row${backup ? ' is-backup' : ''}${cellErrors.length ? ' has-error' : ''}`} key={server._key}>
              <TextInput
                className="nf-srv-row__name"
                label="Имя"
                placeholder="srv1"
                maxLength={SERVER_NAME_MAX}
                value={server.name}
                onChange={(e) => update(index, { name: e.currentTarget.value })}
                error={errorFor('name') ? true : undefined}
              />
              <Select
                className="nf-srv-row__type"
                label="Тип"
                value={server.targetType}
                onChange={(v) => update(index, { targetType: (v ?? 'tcp') as 'tcp' | 'unix', dnsPool: false, preferredIP: '' })}
                allowDeselect={false}
                data={[{ value: 'tcp', label: 'IP / домен' }, { value: 'unix', label: 'Unix socket' }]}
              />
              {tcp ? (
                <>
                  <TextInput
                    className="nf-srv-row__host"
                    label="Адрес"
                    placeholder="IP или домен"
                    value={server.host}
                    onChange={(e) => update(index, { host: e.currentTarget.value })}
                    error={errorFor('host') ? true : undefined}
                  />
                  <NumberInput
                    className="nf-srv-row__port"
                    label="Порт"
                    value={server.port}
                    onChange={(v) => update(index, { port: v === '' ? '' : Number(v) })}
                    min={1} max={65535} allowDecimal={false} hideControls inputMode="numeric"
                    error={errorFor('port') ? true : undefined}
                  />
                  <Tooltip label="DNS-пул доступен только для домена" disabled={!hostIsIP} openDelay={150}>
                    <div className="nf-srv-row__dns">
                      <Switch
                        size="sm"
                        aria-label={`DNS-пул для ${label}: все IP домена`}
                        checked={server.dnsPool}
                        disabled={hostIsIP && !server.dnsPool}
                        error={errorFor('dns') ? true : undefined}
                        onChange={(e) => update(index, { dnsPool: e.currentTarget.checked, preferredIP: e.currentTarget.checked ? server.preferredIP : '' })}
                      />
                      <span className="nf-srv-row__dns-label" aria-hidden="true">DNS-пул</span>
                    </div>
                  </Tooltip>
                </>
              ) : (
                <TextInput
                  className="nf-srv-row__unix"
                  label="Путь socket"
                  placeholder="/run/service.sock"
                  value={server.unixSocketPath}
                  onChange={(e) => update(index, { unixSocketPath: e.currentTarget.value })}
                  error={errorFor('unix') ? true : undefined}
                />
              )}
              {failover ? (
                <div className="nf-srv-row__role">
                  <span className={`nf-role-badge${backup ? ' is-backup' : ''}`}>
                    <b>{index + 1}</b>{backup ? 'Резерв' : 'Основной'}
                  </span>
                </div>
              ) : <>
              <Tooltip label={offHint} disabled={weightOn} openDelay={200}>
                <div className="nf-srv-row__weight">
                  <NumberInput
                    label="Вес"
                    placeholder={weightOn ? '1' : '—'}
                    value={weightOn ? server.weight : ''}
                    onChange={(v) => update(index, { weight: v === '' ? '' : Number(v) })}
                    min={1} max={256} allowDecimal={false} hideControls inputMode="numeric"
                    disabled={!weightOn}
                    error={weightOn && errorFor('weight') ? true : undefined}
                  />
                </div>
              </Tooltip>
              <Tooltip label="Стоимость учитывают алгоритмы «Меньше соединений» и «Меньше задержка»" disabled={costOn} openDelay={200}>
                <div className="nf-srv-row__cost">
                  <NumberInput
                    label="Стоимость"
                    placeholder={costOn ? '1' : '—'}
                    value={costOn ? server.cost : ''}
                    onChange={(v) => update(index, { cost: v === '' ? '' : Number(v) })}
                    min={0.01} max={100} decimalScale={2} hideControls inputMode="decimal"
                    disabled={!costOn}
                    error={costOn && errorFor('cost') ? true : undefined}
                  />
                </div>
              </Tooltip>
              </>}
              <div className="nf-srv-row__actions">
                <Menu position="bottom-end" withinPortal shadow="md" width={210}>
                  <Menu.Target>
                    <ActionIcon variant="subtle" color="gray" size={36} aria-label={`Действия: ${label}`}>
                      <IconDots size={16} />
                    </ActionIcon>
                  </Menu.Target>
                  <Menu.Dropdown>
                    {failover && (
                      <>
                        <Menu.Item leftSection={<IconArrowUp size={14} />} disabled={index === 0} onClick={() => move(index, -1)}>Выше</Menu.Item>
                        <Menu.Item leftSection={<IconArrowDown size={14} />} disabled={index === servers.length - 1} onClick={() => move(index, 1)}>Ниже</Menu.Item>
                        <Menu.Item leftSection={<IconSwitchHorizontal size={14} />} onClick={() => setRole(index, !server.backup)}>
                          {server.backup ? 'Сделать основным' : 'Сделать резервным'}
                        </Menu.Item>
                        <Menu.Divider />
                      </>
                    )}
                    <Menu.Item color="red" leftSection={<IconTrash size={14} />} disabled={!canRemove} onClick={() => remove(index)}>
                      Удалить сервер
                    </Menu.Item>
                  </Menu.Dropdown>
                </Menu>
              </div>
              {inlineErrors.map((e) => {
                const cell = e.cell === 'row' ? 'name' : e.cell === 'unix' ? 'host' : e.cell;
                return (
                  <p key={`${e.cell}-${e.message}`} className="nf-cell-error nf-srv-row__error" style={{ gridColumn: `${Math.max(1, col(cell as ServerColumn))} / -1` }}>
                    {e.message}
                  </p>
                );
              })}
              {showPreferred && (
                <PreferredIPLine
                  server={server}
                  hasOtherServers={servers.length > 1}
                  error={errorFor('preferred')}
                  onChange={(preferredIP) => update(index, { preferredIP })}
                />
              )}
              {showIPWeights && (
                <IPWeightsEditor
                  server={server}
                  agentAllowed={poolSettings.agentAllowed}
                  showCost={poolSettings.showCost}
                  connections={poolSettings.connections}
                  weightsFixed={poolSettings.weightsFixed}
                  error={errorFor('ipweights')}
                  onChange={(ipWeights) => update(index, { ipWeights })}
                />
              )}
            </div>
          );
        })}
      </div>
      {general.length > 0 && (
        <div className="nf-servers__errors" role="alert" data-invalid="true" tabIndex={-1}>
          {general.map((message) => <p key={message} className="nf-cell-error"><IconAlertCircle size={13} aria-hidden="true" />{message}</p>)}
        </div>
      )}
      <div className="nf-servers__footer">
        <Button
          variant="default"
          size="sm"
          leftSection={<IconPlus size={14} />}
          disabled={maxReached}
          // New servers join as reserves; in pool mode the flag is ignored.
          onClick={() => onChange([...servers, { ...emptyServerDraft(nextServerName(servers)), backup: servers.length > 0 }])}
        >
          Добавить сервер
        </Button>
        {maxReached && <span className="nf-muted">Максимум — 16 серверов.</span>}
      </div>
    </div>
  );
}

/** One plain-language line explaining what the selected distribution mode does. */
function balanceExplanation(draft: RouteDraft): string {
  const servers = draft.servers;
  const single = servers.length === 1;
  if (effectiveBalanceMode(draft) === 'pool') {
    return single
      ? 'Нагрузка делится между всеми IP домена.'
      : 'Нагрузка делится между всеми серверами и всеми IP доменов с DNS-пулом.';
  }
  const preferred = preferredIPApplies(draft, 0) && servers[0].preferredIP.trim() !== '';
  if (single) {
    return preferred
      ? 'Трафик идёт на основной IP; если он упал — делится между всеми IP домена.'
      : 'Задайте основной IP — он получит весь трафик, остальные IP домена станут резервом.';
  }
  // Renderer emits `option allbackups` when a DNS pool is a backup or the primary has a preferred IP.
  const allBackups = preferred || servers.some((s, i) => serverIsBackup(draft, i) && s.targetType === 'tcp' && s.dnsPool);
  const primaries = servers.filter((_, i) => !serverIsBackup(draft, i)).length;
  const head = primaries > 1 ? 'Трафик делится между основными серверами' : 'Трафик идёт на основной сервер';
  const fallen = primaries > 1 ? 'если все основные упали' : 'если он упал';
  return allBackups
    ? `${head}; ${fallen} — делится между всеми резервными сразу.`
    : `${head}; резервные включаются по порядку, ${fallen}.`;
}

// ─── «Распределение клиентов» ───────────────────────────────────────────────────
interface DistributionBlockProps {
  draft: RouteDraft;
  update: <K extends keyof RouteDraft>(key: K, value: RouteDraft[K]) => void;
  agent110: boolean;
  agentVersion?: string;
  /** «Авто» stick-table size for this route (renderer-equal, from node RAM). */
  autoTableEntries: string;
  memoryKnown: boolean;
  showError: (fields: RouteDraftError['field'][]) => string | undefined;
}

function DistributionBlock({ draft, update, agent110, agentVersion, autoTableEntries, memoryKnown, showError }: DistributionBlockProps) {
  const stickyMode = effectiveStickyMode(draft);
  const choice = distributionChoice(stickyMode);
  const algorithm = effectiveBalanceAlgorithm(draft);
  const family = balanceAlgorithmFamily(algorithm);
  const ttl = ttlParts(draft.stickyTTL || DEFAULT_STICKY_TTL);
  const tableAuto = draft.stickyTableEntries === '';
  const tableClients = tableEntriesCount(draft.stickyTableEntries || autoTableEntries);
  // Hash: 0 = bare `balance source` = full address; table: 0 renders /64.
  const ipv6Default = choice === 'hash' ? 128 : 64;
  const ttlError = showError(['stickyTTL']);
  const tableError = showError(['stickyTableEntries']);
  const memory = tableMemoryEstimate(draft.stickyTableEntries || autoTableEntries);

  const algorithmRows = (
    <>
      <SettingRow
        id="dist-algorithm"
        stableHint
        label={choice === 'table' ? 'Первый сервер' : 'Алгоритм'}
        hint={choice === 'table' ? 'Как выбрать сервер для нового клиента.' : balanceAlgorithmOptions.find((o) => o.value === family)?.description}
      >
        <Select
          id="dist-algorithm"
          className="nf-select-md"
          size="sm"
          value={family}
          onChange={(value) => { if (value) update('balanceAlgorithm', value as BalanceAlgorithm); }}
          allowDeselect={false}
          data={balanceAlgorithmOptions.map(({ value, label }) => ({ value, label, disabled: value === 'leastping' && !agent110 }))}
          aria-describedby="dist-algorithm-hint"
        />
      </SettingRow>
      {family === 'roundrobin' && (
        <SettingRow id="dist-dynamic" label="Веса на лету" hint="Выкл — веса фиксированы: вес IP и плавный ввод не действуют.">
          <Switch
            id="dist-dynamic"
            checked={algorithm !== 'static-rr'}
            onChange={(event) => update('balanceAlgorithm', event.currentTarget.checked ? 'roundrobin' : 'static-rr')}
            aria-describedby="dist-dynamic-hint"
          />
        </SettingRow>
      )}
      {family === 'random' && (
        <SettingRow id="dist-draws" label="Выборок" hint="Из стольких случайных серверов берётся менее загруженный." error={showError(['balanceRandomDraws'])}>
          <UnitNumberInput
            id="dist-draws" value={draft.balanceRandomDraws} onValue={(v) => update('balanceRandomDraws', v)}
            placeholder="2" min={1} max={5} allowDecimal={false} inputMode="numeric"
            error={showError(['balanceRandomDraws']) ? true : undefined} aria-describedby="dist-draws-hint"
          />
        </SettingRow>
      )}
      {family === 'leastconn' && (
        <>
          <SettingRow id="dist-tolerance" label="Толерантность" hint="Серверы, где соединений (с учётом стоимости) больше минимума не более чем на столько, считаются равными." aside="Пусто — 0" error={showError(['leastpingTolerancePct'])}>
            <UnitNumberInput
              id="dist-tolerance" unit="%" value={draft.leastpingTolerancePct} onValue={(v) => update('leastpingTolerancePct', v)}
              placeholder="0" min={0} max={100} allowDecimal={false} inputMode="numeric"
              error={showError(['leastpingTolerancePct']) ? true : undefined} aria-describedby="dist-tolerance-hint"
            />
          </SettingRow>
          <SettingsNote>
            Стоимость — в таблице серверов. Толерантность больше 0 применяет агент ноды каждые 5 с.{!leastconnToleranceSupported(agentVersion) ? ` Нужен Node Agent ${LEASTCONN_TOLERANCE_MIN_AGENT}, на ноде ${agentVersion ?? 'неизвестно'}.` : ''}
          </SettingsNote>
        </>
      )}
      {family === 'leastping' && (
        <>
          <SettingRow id="dist-tolerance" label="Толерантность" hint="Кто медленнее лучшего не больше чем на столько — получает полный вес." error={showError(['leastpingTolerancePct'])}>
            <UnitNumberInput
              id="dist-tolerance" unit="%" value={draft.leastpingTolerancePct} onValue={(v) => update('leastpingTolerancePct', v)}
              placeholder={String(DEFAULT_LEASTPING_TOLERANCE_PCT)} min={0} max={100} allowDecimal={false} inputMode="numeric"
              error={showError(['leastpingTolerancePct']) ? true : undefined} aria-describedby="dist-tolerance-hint"
            />
          </SettingRow>
          <SettingRow id="dist-floor" label="Толерантность не меньше" hint="Разница до стольких мс не в счёт. Пусто — выкл." error={showError(['leastpingToleranceMs'])}>
            <UnitNumberInput
              id="dist-floor" unit="мс" value={draft.leastpingToleranceMs} onValue={(v) => update('leastpingToleranceMs', v)}
              placeholder="выкл" min={0} max={1000} allowDecimal={false} inputMode="numeric"
              error={showError(['leastpingToleranceMs']) ? true : undefined} aria-describedby="dist-floor-hint"
            />
          </SettingRow>
          <SettingsNote>
            Задержку меряет проверка здоровья, агент ноды обновляет веса каждые 5 с. Стоимость — в таблице серверов.{!agent110 ? ` Нужен Node Agent ${KERNEL_SHAPER_MIN_AGENT}, на ноде ${agentVersion ?? 'неизвестно'}.` : ''}
          </SettingsNote>
        </>
      )}
    </>
  );

  const ipv6Rows = (
    <>
      <SettingRow id="dist-ipv6-clients" group label="IPv6-клиенты" hint="Выключите, если на ноде нет IPv6: таблица будет только для IPv4.">
        <SegmentedControl
          className="nf-seg"
          size="sm"
          value={draft.clientIPv6 ? 'on' : 'off'}
          onChange={(value) => update('clientIPv6', value === 'on')}
          data={[{ value: 'on', label: 'Вкл' }, { value: 'off', label: 'Выкл' }]}
          aria-labelledby="dist-ipv6-clients-label"
          aria-describedby="dist-ipv6-clients-hint"
        />
      </SettingRow>
      {/* Always in its slot (inactive without IPv6): the rows after it never move. */}
      <SettingRow id="dist-ipv6" stableHint label="Подсеть IPv6-клиента" inactive={!draft.clientIPv6} hint="Все адреса из одной подсети считаются одним клиентом; /128 — каждый адрес отдельно." aside={`Пусто — /${ipv6Default}`} error={draft.clientIPv6 ? showError(['stickyIPv6Prefix']) : undefined}>
        <UnitNumberInput
          id="dist-ipv6" value={!draft.clientIPv6 || draft.stickyIPv6Prefix === 0 ? '' : draft.stickyIPv6Prefix}
          onValue={(v) => update('stickyIPv6Prefix', v === '' || (choice === 'hash' && v === 128) ? 0 : v)}
          placeholder={String(ipv6Default)} min={32} max={128} allowDecimal={false} inputMode="numeric" leftSection="/"
          disabled={!draft.clientIPv6}
          error={draft.clientIPv6 && showError(['stickyIPv6Prefix']) ? true : undefined} aria-describedby="dist-ipv6-hint"
        />
      </SettingRow>
    </>
  );

  return (
    <div className="nf-re-block nf-dist">
      <BlockHead title="Распределение клиентов" hint="Какой сервер получает клиент: выбирается заново или всегда тот же." id="dist-choice-label" />
      <div className="nf-choice-cards" role="radiogroup" aria-labelledby="dist-choice-label">
        {distributionChoiceOptions.map((option) => {
          const selected = option.value === choice;
          return (
            <button
              type="button"
              key={option.value}
              role="radio"
              aria-checked={selected}
              className={`nf-choice-card${selected ? ' is-selected' : ''}`}
              onClick={() => update('stickyMode', stickyModeForChoice(option.value as DistributionChoice))}
            >
              <span className="nf-choice-card__radio" aria-hidden="true" />
              <strong>{option.label}</strong>
              <small>{option.description}</small>
            </button>
          );
        })}
      </div>
      <SettingsGrid data-choice={choice}>
        {choice === 'algorithm' && algorithmRows}
        {choice === 'hash' && (
          <>
            <SettingRow id="dist-hash" group stableHint label="Распределение" hint={draft.stickyHash === 'map-based' ? 'Ровнее, но при смене серверов переезжают почти все.' : 'При смене серверов переезжают только их клиенты.'}>
              <SegmentedControl
                className="nf-seg"
                size="sm"
                value={draft.stickyHash === 'map-based' ? 'even' : 'stable'}
                onChange={(value) => update('stickyHash', value === 'even' ? 'map-based' : '')}
                data={[{ value: 'stable', label: 'Стабильное' }, { value: 'even', label: 'Ровное' }]}
                aria-labelledby="dist-hash-label"
                aria-describedby="dist-hash-hint"
              />
            </SettingRow>
            {/* «Ровное» hash ignores the limit: the row stays, disabled. */}
            <SettingRow id="dist-factor" stableHint inactive={draft.stickyHash === 'map-based'} label="Лимит перегрузки" hint={draft.stickyHash === 'map-based' ? 'Только для стабильного распределения.' : 'Не больше этого % от средней нагрузки. Пусто — выкл.'} error={draft.stickyHash !== 'map-based' ? showError(['stickyHashBalanceFactor']) : undefined}>
              <UnitNumberInput
                id="dist-factor" unit="%" value={draft.stickyHash === 'map-based' ? '' : draft.stickyHashBalanceFactor} onValue={(v) => update('stickyHashBalanceFactor', v)}
                placeholder="выкл" min={101} max={1000} allowDecimal={false} inputMode="numeric"
                disabled={draft.stickyHash === 'map-based'}
                error={draft.stickyHash !== 'map-based' && showError(['stickyHashBalanceFactor']) ? true : undefined} aria-describedby="dist-factor-hint"
              />
            </SettingRow>
            {ipv6Rows}
          </>
        )}
        {choice === 'table' && (
          <>
            <SettingRow id="dist-ttl" label="Помнить" hint="Сколько клиент держится за сервер после последнего соединения." error={ttlError}>
              <div className="nf-combo">
                <UnitNumberInput
                  id="dist-ttl" value={ttl.amount}
                  onValue={(v) => update('stickyTTL', v === '' ? DEFAULT_STICKY_TTL : `${v}${ttl.unit}`)}
                  min={1} allowDecimal={false} inputMode="numeric"
                  error={ttlError ? true : undefined} aria-describedby="dist-ttl-hint"
                />
                <Select
                  className="nf-select-unit"
                  size="sm"
                  value={ttl.unit}
                  onChange={(unit) => { if (unit && ttl.amount !== '') update('stickyTTL', `${ttl.amount}${unit}`); }}
                  allowDeselect={false}
                  data={(ttl.unit === 's' ? ['s', 'm', 'h'] as const : ['m', 'h'] as const).map((unit) => ({ value: unit, label: ttlUnitLabels[unit] }))}
                  aria-label="Единица времени"
                />
              </div>
              <div className="nf-picks" role="group" aria-label="Быстрый выбор срока">
                {stickyTTLQuickPicks.map((pick) => {
                  const selected = (draft.stickyTTL || DEFAULT_STICKY_TTL) === pick;
                  return (
                    <button type="button" key={pick} className={`nf-pick${selected ? ' is-selected' : ''}`} aria-pressed={selected} onClick={() => update('stickyTTL', pick)}>
                      {ttlQuickPickLabel(pick)}
                    </button>
                  );
                })}
              </div>
            </SettingRow>
            <SettingRow
              id="dist-table"
              wide
              stableHint
              group={tableAuto}
              label="Клиентов в памяти"
              hint="Память занимается по мере прихода клиентов; запись удаляется через «Помнить» без соединений. Когда места нет, самые старые забываются."
              aside={tableAuto ? autoTableHint(autoTableEntries, memoryKnown) : memory ? `${memory} при заполнении` : undefined}
              error={tableError}
            >
              <SegmentedControl
                className="nf-seg"
                size="sm"
                value={tableAuto ? 'auto' : 'manual'}
                onChange={(value) => update('stickyTableEntries', value === 'auto' ? '' : tableEntriesToken(tableEntriesCount(autoTableEntries)))}
                data={[{ value: 'auto', label: 'Авто' }, { value: 'manual', label: 'Задать' }]}
                aria-labelledby="dist-table-label"
              />
              {/* Always rendered: «Авто» shows the computed size, disabled. */}
              <UnitNumberInput
                wide unit="клиентов" id="dist-table" value={tableClients || ''}
                onValue={(v) => { if (v !== '') update('stickyTableEntries', tableEntriesToken(v)); }}
                min={1} max={TABLE_MAX_CLIENTS} allowDecimal={false} inputMode="numeric" thousandSeparator=" "
                disabled={tableAuto}
                error={tableError ? true : undefined} aria-describedby="dist-table-hint dist-table-aside"
              />
            </SettingRow>
            {ipv6Rows}
            <SettingsDivider />
            {algorithmRows}
          </>
        )}
        {choice !== 'algorithm' && (
          <SettingsNote>
            {choice === 'table' ? 'Таблица переживает перезагрузку HAProxy. ' : ''}IP клиента берётся из PROXY protocol, если он приходит, иначе — адрес соединения.
          </SettingsNote>
        )}
      </SettingsGrid>
    </div>
  );
}

// ─── Main component ─────────────────────────────────────────────────────────────
export function RouteEditorPage() {
  const { nodeId = '', routeId } = useParams();
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const formRef = useRef<HTMLFormElement>(null);
  const bypassNavigationRef = useRef(false);
  const anchoredRouteIDRef = useRef<string | null>(null);
  const [data, setData] = useState<EditorData | null>(null);
  const [draft, setDraft] = useState<RouteDraft>(emptyRouteDraft);
  const [initialSignature, setInitialSignature] = useState('');
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [submitAttempted, setSubmitAttempted] = useState(false);
  const [saving, setSaving] = useState<SaveIntent | null>(null);
  const [validationIntent, setValidationIntent] = useState<SaveIntent>('enable');
  const [saveError, setSaveError] = useState('');
  const [savedMessage, setSavedMessage] = useState('');
  const [, setAdvancedOpen] = useState(false);
  const [additionalOpen, setAdditionalOpen] = useState(false);
  const [expertEnabled, setExpertEnabled] = useState(false);
  /** Server-side rejection of shaper_mode=kernel (agent too old); cleared on edit. */
  const [shaperError, setShaperError] = useState('');

  const load = async () => {
    setLoading(true); setLoadError(null); setSaveError('');
    try {
      const value = await loadEditor(nodeId, routeId);
      const nextDraft = value.route ? routeToDraft(value.route) : emptyRouteDraft();
      setData(value); setDraft(nextDraft); setExpertEnabled(Boolean(nextDraft.expertOverride));
      // Auto-open Дополнительно if any non-default advanced setting is present.
      const hasAdvanced = Boolean(nextDraft.expertOverride) || nextDraft.quotaEnabled || nextDraft.clientBandwidthEnabled || nextDraft.shaperMode === 'kernel';
      setAdvancedOpen(Boolean(nextDraft.expertOverride)); setAdditionalOpen(hasAdvanced);
      setInitialSignature(JSON.stringify(nextDraft));
    } catch (error) { setLoadError(error); }
    finally { setLoading(false); }
  };

  useEffect(() => {
    if (routeId && anchoredRouteIDRef.current === routeId) {
      anchoredRouteIDRef.current = null;
      return;
    }
    void load();
  }, [nodeId, routeId]); // eslint-disable-line react-hooks/exhaustive-deps
  const peers = data?.routes ?? [];
  const draftErrors = useMemo(() => validateRouteDraft(draft, [], data?.route?.id), [draft, data?.route?.id]);
  const activationErrors = useMemo(() => validateRouteDraft(draft, peers, data?.route?.id), [draft, peers, data?.route?.id]);
  const errors = validationIntent === 'draft' ? draftErrors : activationErrors;
  const memoryTotalBytes = data?.memoryTotalBytes;
  const preview = useMemo(() => renderRoutePreview(draft, peers.filter((route) => route.id !== data?.route?.id), memoryTotalBytes), [draft, peers, data?.route?.id, memoryTotalBytes]);
  // Same value the renderer uses (the edited route counts as one enabled «Авто»
  // table); the stored route's server-computed value wins when it applies.
  const autoTable = useMemo(() => {
    const stored = data?.route;
    if (stored && routeUsesAutoTable({ ...stored, enabled: true }) && stored.sticky_table_entries_effective) return stored.sticky_table_entries_effective;
    return draftAutoTableEntries(peers.filter((route) => route.id !== data?.route?.id), memoryTotalBytes);
  }, [peers, data?.route, memoryTotalBytes]);
  const dirty = Boolean(initialSignature) && JSON.stringify(draft) !== initialSignature;
  const blocker = useBlocker(({ currentLocation, nextLocation }) => (
    !bypassNavigationRef.current && dirty && !saving && currentLocation.pathname !== nextLocation.pathname
  ));
  useEffect(() => { bypassNavigationRef.current = false; }, [location.key]);
  const editing = Boolean(data?.route);
  const editingEnabled = Boolean(data?.route?.enabled);
  const query = explicitDemo ? '?demo=1' : location.search;
  const nodeURL = `/nodes/${encodeURIComponent(nodeId)}${query}`;
  const editURL = (id: string) => `/nodes/${encodeURIComponent(nodeId)}/routes/${encodeURIComponent(id)}/edit${query}`;

  useBeforeUnload((event) => {
    if (!dirty || saving) return;
    event.preventDefault();
    event.returnValue = '';
  });

  const update = <K extends keyof RouteDraft>(key: K, value: RouteDraft[K]) => {
    setDraft((current) => ({ ...current, [key]: value }));
    setSavedMessage(''); setSaveError('');
    if (key === 'shaperMode') setShaperError('');
  };
  const leave = () => navigate(nodeURL);
  const focusFirstError = () => {
    // Two frames: the first renders the now-visible error state, the second finds it.
    window.requestAnimationFrame(() => window.requestAnimationFrame(() => {
      const invalid = formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"], [data-invalid="true"]');
      invalid?.focus({ preventScroll: true }); invalid?.scrollIntoView({ block: 'center', behavior: 'smooth' });
    }));
  };
  const showErrors = () => {
    setSubmitAttempted(true);
    if (errors.some((e) => ['quota', 'clientUploadMbps', 'clientDownloadMbps', 'bandwidth'].includes(e.field))) setAdditionalOpen(true);
    if (errors.some((e) => e.field === 'expert')) setAdvancedOpen(true);
    focusFirstError();
  };

  const save = async (intent: SaveIntent) => {
    setValidationIntent(intent);
    setSubmitAttempted(true); setSaveError(''); setSavedMessage('');
    const intentErrors = intent === 'draft' ? draftErrors : activationErrors;
    if (intentErrors.length) {
      if (intentErrors.some((e) => e.field === 'expert')) setAdvancedOpen(true);
      focusFirstError();
      return;
    }
    if (!data) return;
    setSaving(intent);
    let anchoredCreatedDraft = false;
    try {
      let result: RouteRecord;
      if (explicitDemo) {
        await new Promise((resolve) => window.setTimeout(resolve, 420));
        result = {
          id: data.route?.id ?? randomUUID(), node_id: nodeId, version: (data.route?.version ?? 0) + 1,
          ...routePayload(draft, intent !== 'draft'),
          listener_port: Number(draft.listenerPort), target_port: draft.servers[0]?.targetType === 'unix' ? 0 : Number(draft.servers[0]?.port ?? 443),
          deployed: intent !== 'draft', deployment_state: intent === 'draft' ? 'draft' : 'pending',
          created_at: data.route?.created_at ?? new Date().toISOString(), updated_at: new Date().toISOString(),
        } as RouteRecord;
        const { upsertDemoRoute } = await import('../fixtures/demo');
        upsertDemoRoute(nodeId, result);
      } else if (data.route) {
        result = await api<RouteRecord>(`/api/v1/nodes/${nodeId}/routes/${data.route.id}`, {
          method: 'PUT',
          body: JSON.stringify(routePayload(draft, intent === 'apply' || intent === 'enable', data.route.version)),
        });
      } else {
        const created = await fetchSavedRoute(nodeId, await api<RouteRecord>(`/api/v1/nodes/${nodeId}/routes`, {
          method: 'POST', body: JSON.stringify(routePayload(draft, false)),
        }));
        const createdDraft = savedDraft(draft, created);
        anchoredCreatedDraft = true;
        anchoredRouteIDRef.current = created.id;
        setData((current) => current ? {
          ...current, route: created,
          routes: [...current.routes.filter((route) => route.id !== created.id), created],
        } : current);
        setDraft(createdDraft);
        setInitialSignature(JSON.stringify(createdDraft));
        bypassNavigationRef.current = true;
        navigate(editURL(created.id), { replace: true });
        if (intent === 'draft') result = created;
        else {
          result = await api<RouteRecord>(`/api/v1/nodes/${nodeId}/routes/${created.id}`, {
            method: 'PUT', body: JSON.stringify(routePayload(draft, true, created.version)),
          });
        }
      }
      if (!explicitDemo) {
        result = await fetchSavedRoute(nodeId, result);
        void queryClient.invalidateQueries({ queryKey: ['node-detail', nodeId] });
      }
      const cleanDraft = savedDraft(draft, result);
      setData((current) => current ? {
        ...current, route: result,
        routes: [...current.routes.filter((route) => route.id !== result.id), result],
      } : current);
      setDraft(cleanDraft); setInitialSignature(JSON.stringify(cleanDraft)); setSavedMessage(statusCopy(intent, editing));
      if (!editing && !anchoredCreatedDraft) { bypassNavigationRef.current = true; navigate(editURL(result.id), { replace: true }); }
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Маршрут не сохранён';
      const kernelAgent = error instanceof APIError && error.code === KERNEL_SHAPER_AGENT_CODE;
      if (kernelAgent) {
        setShaperError(`${kernelShaperAgentHint}. Выберите HAProxy или обновите агент.`);
        setAdditionalOpen(true);
      }
      const detail = kernelAgent
        ? `${kernelShaperAgentHint}.`
        : message.includes('version') ? 'Маршрут изменился в другой вкладке. Обновите данные и повторите.' : message;
      setSaveError(anchoredCreatedDraft ? `Черновик сохранён, но включить маршрут не удалось: ${detail}` : detail);
    } finally { setSaving(null); }
  };

  const submit = (event: FormEvent) => { event.preventDefault(); void save(editingEnabled ? 'apply' : 'enable'); };
  const discardAndLeave = () => {
    if (blocker.state !== 'blocked') return;
    blocker.proceed();
  };

  if (loading) return <main className="nf-page nf-route-editor-page"><div className="nf-route-editor-loading"><LoadingOverlay visible /><span>Загружаем маршруты ноды и проверяем конфликты портов…</span></div></main>;
  if (loadError && isUnauthorized(loadError)) return <LoginPanel onSuccess={load} />;
  if (loadError || !data) return <main className="nf-page"><StateView title="Редактор не загрузился" description={loadError instanceof Error ? loadError.message : 'Нода или маршрут не найдены'} tone="error" action={<><Button variant="default" onClick={() => navigate(`/nodes${query}`)}>К нодам</Button><Button onClick={load}>Повторить</Button></>} /></main>;

  const showError = (fields: RouteDraftError['field'][]) => submitAttempted ? errorText(errors, fields) : undefined;
  const serverErrors = submitAttempted ? errors.filter((e) => e.field === 'servers').map((e) => e.message) : [];
  const periodNote = quotaPeriodOptions.find((option) => option.value === draft.quotaPeriod)?.description;
  const valid = errors.length === 0;
  const previewState = valid ? 'valid' : submitAttempted ? 'invalid' : 'incomplete';

  const isWildcardIP = draft.listenerIP === '*' || draft.listenerIP === '0.0.0.0' || draft.listenerIP === '::';
  const listenerIPMode = isWildcardIP ? 'all' : 'specific';

  // «Распределение клиентов»: pool mode with 2+ servers or a DNS pool (several addresses).
  const hasDNSPool = hasDNSPoolServer(draft);
  const showSticky = stickyApplicable(draft);
  const stickyMode = effectiveStickyMode(draft);
  const showBalanceMode = balanceModeApplicable(draft);
  const balanceMode = effectiveBalanceMode(draft);
  const algorithm = effectiveBalanceAlgorithm(draft);
  const healthOn = effectiveHealthCheck(draft);

  // Node Agent >= 1.1.0 gates: kernel shaper, leastping, per-IP weights (same threshold, same mechanism).
  const agent110 = kernelShaperSupported(data.agentVersion);
  const kernelAllowed = agent110;
  const shaperIsError = Boolean(shaperError) || (!kernelAllowed && draft.shaperMode === 'kernel');
  const shaperHint = shaperError
    || (!kernelAllowed ? `${kernelShaperAgentHint} (сейчас ${data.agentVersion}).${draft.shaperMode === 'kernel' ? ' Выберите HAProxy.' : ''}` : '');
  // Weight/cost inputs belong to every pool with several candidates (weights also
  // steer the hash); cost only to latency mode (leastping, not overridden by the IP hash).
  const weightsFixed = (stickyMode !== 'source' && algorithm === 'static-rr') || (stickyMode === 'source' && draft.stickyHash === 'map-based');
  const poolSettings = showSticky
    ? { showCost: stickyMode !== 'source' && (algorithm === 'leastping' || algorithm === 'leastconn'), connections: stickyMode !== 'source' && algorithm === 'leastconn', weightsFixed, agentAllowed: agent110 }
    : null;

  // ── Section summaries (one line of current state next to each title) ──
  const listenerLabel = `${isWildcardIP ? '*' : (draft.listenerIP || '—')}:${draft.listenerPort || '—'}`;
  const connectionSummary = [
    draft.matchMode === 'sni'
      ? (draft.snis.length ? `SNI · ${draft.snis.length} ${plural(draft.snis.length, ['домен', 'домена', 'доменов'])}` : 'SNI')
      : 'Фолбэк',
    listenerLabel,
    draft.acceptProxyEnabled ? 'принимает PROXY' : '',
  ].filter(Boolean).join(' · ');
  const algorithmLabel = balanceAlgorithmOptions.find((o) => o.value === balanceAlgorithmFamily(algorithm))?.label.toLowerCase();
  const distributionLabel = !showSticky ? ''
    : stickyMode === 'source' ? 'закрепление по IP'
      : stickyMode === 'source_table' ? `запоминание · ${algorithmLabel}` : algorithmLabel;
  const targetSummary = [
    `${draft.servers.length} ${plural(draft.servers.length, ['сервер', 'сервера', 'серверов'])}`,
    hasDNSPool ? 'DNS-пул' : '',
    showBalanceMode ? (balanceMode === 'pool' ? 'пул' : 'основной + резервные') : '',
    distributionLabel ?? '',
  ].filter(Boolean).join(' · ');

  const advancedParts: string[] = [];
  if (draft.quotaEnabled && draft.quotaValue !== '') advancedParts.push(`лимит ${draft.quotaValue} ${draft.quotaUnit}`);
  if (draft.clientBandwidthEnabled) {
    const parts = [];
    if (draft.clientUploadMbps !== '') parts.push(`↑${draft.clientUploadMbps}`);
    if (draft.clientDownloadMbps !== '') parts.push(`↓${draft.clientDownloadMbps}`);
    if (parts.length) advancedParts.push(`скорость ${parts.join(' ')} Мбит/с${draft.shaperMode === 'kernel' ? ' · ядро' : ''}`);
  }
  const advancedSummary = advancedParts.join(' · ') || 'Лимит трафика и скорости не заданы';
  const hasAdditionalError = submitAttempted && errors.some((e) =>
    ['quota', 'clientUploadMbps', 'clientDownloadMbps', 'bandwidth'].includes(e.field as string),
  );
  const effectiveAdditionalOpen = additionalOpen || hasAdditionalError;

  // ── Save bar state ──
  const primaryIntent: SaveIntent = editingEnabled ? 'apply' : 'enable';
  // Before the first save attempt the bar only names what is still missing (neutral);
  // the red count appears once the operator tried to save.
  const pendingErrors = submitAttempted ? errors.length : 0;
  const missingHint = !submitAttempted && activationErrors.length > 0 ? missingFieldsHint(activationErrors) : '';
  const barStatus = saving
    ? { tone: 'busy', text: 'Сохраняем…' }
    : saveError ? { tone: 'error', text: 'Не сохранено' }
      : savedMessage && !dirty ? { tone: 'saved', text: savedMessage }
        : dirty ? { tone: 'dirty', text: 'Есть несохранённые изменения' }
          : { tone: 'clean', text: editing ? 'Изменений нет' : 'Новый маршрут' };

  return (
    <main className="nf-page nf-route-editor-page">
      <PageHeader
        className="nf-route-editor-header"
        breadcrumb={<><Link to={`/nodes${query}`}>Ноды</Link><span>/</span><Link to={nodeURL}>{data.node.name}</Link><span>/</span><span aria-current="page">{editing ? 'Маршрут' : 'Новый маршрут'}</span></>}
        backAction={<Button variant="subtle" color="gray" px={6} onClick={leave} aria-label="Назад к ноде"><IconArrowLeft size={20} /></Button>}
        title={editing ? (draft.name.trim() || 'Без имени') : 'Новый маршрут'}
        badge={(
          <span className={`nf-route-state-badge${editingEnabled ? ' is-enabled' : ''}`}>
            <i aria-hidden="true" />
            <span className="nf-visually-hidden">Состояние: </span>
            {editingEnabled ? 'Включён' : editing ? 'Выключен' : 'Черновик'}
          </span>
        )}
      />

      <form ref={formRef} className="nf-route-editor-layout" onSubmit={submit} noValidate aria-busy={Boolean(saving)}>
        <Surface className="nf-route-editor-form">
          {/* 1 — Подключение (имя маршрута — первое поле строки) */}
          <EditorSection id="route-listener" index="1" title="Подключение" summary={connectionSummary}>
            {/* Fixed areas: companion inputs (IP, SNI, trusted addresses) always own their slot. */}
            <SettingsGrid layout="connection">
              <SettingRow id="route-name" area="name" label="Имя маршрута" hint="Видно только оператору." error={showError(['name'])}>
                <TextInput id="route-name" className="nf-input-md" placeholder="api-internal-tls" value={draft.name} onChange={(event) => update('name', event.currentTarget.value)} error={showError(['name']) ? true : undefined} required maxLength={80} aria-describedby="route-name-hint" />
              </SettingRow>
              <SettingRow id="route-match" area="match" group label="Выбор маршрута" hint={draft.matchMode === 'sni' ? 'По SNI; порт можно делить.' : 'Всё, что не совпало по SNI.'}>
                <SegmentedControl className="nf-seg" size="sm" value={draft.matchMode} onChange={(value) => update('matchMode', value as RouteMatchMode)} data={[
                  { value: 'sni', label: 'По SNI' },
                  { value: 'fallback', label: 'Фолбэк' },
                ]} aria-labelledby="route-match-label" aria-describedby="route-match-hint" />
              </SettingRow>
              <SettingRow id="route-listen" area="listen" group label="Слушать" hint={listenerIPMode === 'all' ? 'Все адреса ноды.' : 'Один адрес ноды.'} error={showError(['listenerIP', 'listener'])}>
                <SegmentedControl className="nf-seg" size="sm" value={listenerIPMode} onChange={(value) => {
                  update('listenerIP', value === 'all' ? '*' : (data.node.address || ''));
                }} data={[
                  { value: 'all', label: 'Все IP' },
                  { value: 'specific', label: 'Один IP' },
                ]} aria-labelledby="route-listen-label" />
                {/* Always rendered: «Все IP» keeps the slot as a disabled field, nothing reflows. */}
                <TextInput
                  className="nf-listen-ip"
                  placeholder={listenerIPMode === 'specific' ? '203.0.113.1' : 'все адреса'}
                  value={listenerIPMode === 'specific' ? draft.listenerIP : ''}
                  onChange={(event) => update('listenerIP', event.currentTarget.value)}
                  error={listenerIPMode === 'specific' && showError(['listenerIP', 'listener']) ? true : undefined}
                  disabled={listenerIPMode !== 'specific'}
                  aria-label="IP-адрес listener"
                  required={listenerIPMode === 'specific'}
                />
              </SettingRow>
              <SettingRow id="route-port" area="port" label="Порт" hint="TCP на ноде." error={showError(['listenerPort'])}>
                <NumberInput id="route-port" className="nf-num" hideControls value={draft.listenerPort} onChange={(value) => update('listenerPort', value === '' ? '' : Number(value))} error={showError(['listenerPort']) ? true : undefined} min={1} max={65535} allowDecimal={false} inputMode="numeric" required aria-describedby="route-port-hint" />
              </SettingRow>
              {draft.matchMode === 'sni' ? (
                <SettingRow id="route-sni" area="sni" grow label="Домены SNI" hint="Enter, пробел или запятая — следующий домен." error={showError(['snis'])}>
                  <TagsInput
                    id="route-sni"
                    className="nf-route-sni-input"
                    classNames={{ pill: 'nf-route-sni-pill', pillsList: 'nf-route-sni-pills', inputField: 'nf-route-sni-field' }}
                    placeholder={draft.snis.length ? '' : 'api.example.com'}
                    value={draft.snis}
                    onChange={(value) => update('snis', value)}
                    error={showError(['snis']) ? true : undefined}
                    splitChars={[',', ' ']}
                    clearable
                    required
                    aria-describedby="route-sni-hint"
                  />
                </SettingRow>
              ) : (
                <SettingRow id="route-sni" area="sni" grow inactive label="Домены SNI" hint="Фолбэк получает соединения без совпавшего SNI.">
                  <TextInput id="route-sni" className="nf-route-sni-input" placeholder="не нужны для фолбэка" value="" disabled aria-describedby="route-sni-hint" />
                </SettingRow>
              )}
              <SettingRow id="route-accept-proxy" area="proxy" inline label="Принимать PROXY protocol" hint="Прокси перед нодой передаёт IP клиента.">
                <Switch
                  id="route-accept-proxy"
                  checked={draft.acceptProxyEnabled}
                  onChange={(event) => {
                    const enabled = event.currentTarget.checked;
                    update('acceptProxyEnabled', enabled);
                    if (!enabled) update('acceptProxyFrom', []);
                  }}
                  aria-describedby="route-accept-proxy-hint"
                />
              </SettingRow>
              {/* Dedicated full-width row under the fields; opens with a height animation. */}
              <Collapse in={draft.acceptProxyEnabled} className="nf-setgrid__collapse">
                  <div className="nf-setgrid__collapse-body">
                    <SettingRow id="route-accept-from" grow label="Доверенные адреса" hint="IP, подсети или домены (IP домена обновляются сами). Пусто — ждать PROXY от всех." error={showError(['acceptProxyFrom'])}>
                      <TagsInput
                        id="route-accept-from"
                        className="nf-input-lg"
                        placeholder={draft.acceptProxyFrom.length ? '' : '10.0.0.0/8'}
                        value={draft.acceptProxyFrom}
                        onChange={(value) => update('acceptProxyFrom', value)}
                        error={showError(['acceptProxyFrom']) ? true : undefined}
                        splitChars={[',', ' ']}
                        clearable
                        aria-describedby="route-accept-from-hint"
                      />
                    </SettingRow>
                    {draft.acceptProxyFrom.length === 0 && (
                      <p className="nf-inline-warning" role="note">
                        <IconAlertTriangle size={14} aria-hidden="true" />
                        <span>Клиенты без PROXY protocol не подключатся к этому порту — это действует на все маршруты порта.</span>
                      </p>
                    )}
                  </div>
              </Collapse>
            </SettingsGrid>
          </EditorSection>

          {/* 2 — Назначение */}
          <EditorSection id="route-target" index="2" title="Назначение" summary={targetSummary}>
            <div className="nf-re-block">
              {/* Header row: hint (fixed two-line slot) on the left, the mode switch always in the same place on the right. */}
              <div className="nf-re-block-row nf-re-block-row--servers">
                <BlockHead title="Серверы" hint={showBalanceMode ? balanceExplanation(draft) : 'Добавьте второй сервер или DNS-пул, чтобы делить нагрузку.'} stableHint />
                <Tooltip label="Нужно два сервера или DNS-пул" disabled={showBalanceMode} openDelay={200}>
                  <span className="nf-inline-flex nf-balance-mode">
                    <SegmentedControl
                      className="nf-seg nf-seg--fixed"
                      size="sm"
                      value={showBalanceMode ? draft.balanceMode : 'pool'}
                      disabled={!showBalanceMode}
                      onChange={(value) => {
                        const mode = value as BalanceMode;
                        update('balanceMode', mode);
                        // Roles are meaningless in pool mode; entering failover starts from
                        // «first server primary, the rest reserves».
                        if (mode === 'failover' && draft.balanceMode !== 'failover') update('servers', withCanonicalFailoverRoles(draft.servers));
                      }}
                      data={[
                        { value: 'pool', label: 'Пул' },
                        { value: 'failover', label: 'Основной + резервные' },
                      ]}
                      aria-label="Как распределять трафик"
                    />
                  </span>
                </Tooltip>
              </div>
              <ServersEditor
                servers={draft.servers}
                balanceMode={draft.balanceMode}
                poolSettings={poolSettings}
                onChange={(servers) => update('servers', servers)}
                errors={serverErrors}
              />
            </div>

            {/* Own section under the servers; opens and closes with a height animation. */}
            <Collapse in={showSticky}>
              <DistributionBlock draft={draft} update={update} agent110={agent110} agentVersion={data.agentVersion} autoTableEntries={autoTable} memoryKnown={Boolean(memoryTotalBytes && memoryTotalBytes > 0)} showError={showError} />
            </Collapse>

            <div className="nf-re-block nf-re-block--aside">
              <BlockHead title="Соединение с серверами" hint="Как нода подключается к серверам." />
              <SettingsGrid>
                <SettingRow id="route-health" inline stableHint label="Проверка здоровья" hint={hasDNSPool ? 'Для DNS-пула включена всегда.' : 'TCP каждые 5 с; недоступный сервер исключается.'}>
                  <Switch
                    id="route-health"
                    checked={draft.healthCheck || hasDNSPool}
                    disabled={hasDNSPool}
                    onChange={(event) => update('healthCheck', event.currentTarget.checked)}
                    aria-describedby="route-health-hint"
                  />
                </SettingRow>
                <SettingRow
                  id="route-proxy-out"
                  group
                  stableHint
                  label="PROXY protocol к серверу"
                  hint={draft.proxyProtocol === 'none'
                    ? 'Передать IP клиента; сервер должен принимать PROXY.'
                    : `Без поддержки PROXY на сервере соединения не пройдут.${healthOn ? ' Проверка тоже шлёт заголовок.' : ''}`}
                >
                  <SegmentedControl
                    className="nf-seg"
                    size="sm"
                    value={draft.proxyProtocol}
                    onChange={(value) => update('proxyProtocol', value as ProxyProtocol)}
                    data={[
                      { value: 'none', label: 'Нет' }, { value: 'v1', label: 'v1' }, { value: 'v2', label: 'v2' },
                    ]}
                    aria-labelledby="route-proxy-out-label"
                    aria-describedby="route-proxy-out-hint"
                  />
                </SettingRow>
                {/* Always in its slot: with one plain server it is shown disabled. */}
                <SettingRow
                  id="route-slowstart"
                  stableHint
                  label="Плавный ввод после восстановления"
                  inactive={!showBalanceMode}
                  hint={!showBalanceMode
                    ? 'Нужно два сервера или DNS-пул.'
                    : !healthOn
                      ? 'Требует проверки здоровья.'
                      : weightsFixed
                        ? 'Не действует: веса фиксированы.'
                        : 'Трафик на вернувшийся сервер растёт постепенно. Пусто — сразу.'}
                  error={showBalanceMode ? showError(['slowstart']) : undefined}
                >
                  <UnitNumberInput
                    id="route-slowstart"
                    unit="с"
                    value={showBalanceMode ? slowstartSeconds(draft.slowstart) : ''}
                    onValue={(v) => update('slowstart', v === '' ? '' : `${v}s`)}
                    placeholder="выкл"
                    min={1} max={600} allowDecimal={false} inputMode="numeric"
                    disabled={!healthOn || !showBalanceMode}
                    error={showBalanceMode && showError(['slowstart']) ? true : undefined}
                    aria-describedby="route-slowstart-hint"
                  />
                </SettingRow>
              </SettingsGrid>
            </div>
          </EditorSection>

          {/* 3 — Дополнительно */}
          <section className={`nf-re-section nf-re-section--disclosure${effectiveAdditionalOpen ? ' is-open' : ''}`} aria-labelledby="route-additional-title">
            <button
              type="button"
              className="nf-re-section__head nf-re-section__toggle"
              onClick={() => setAdditionalOpen((v) => !v)}
              aria-expanded={effectiveAdditionalOpen}
              aria-controls="route-additional-body"
            >
              <span className="nf-re-section__index" aria-hidden="true">3</span>
              <h2 id="route-additional-title">Дополнительно</h2>
              <span className="nf-re-section__summary">{advancedSummary}</span>
              <IconChevronDown className="nf-re-section__chevron" size={16} aria-hidden="true" />
            </button>
            <Collapse in={effectiveAdditionalOpen}>
              <div className="nf-re-section__body" id="route-additional-body">
                <OptionList cards>
                  <OptionRow
                    title="Лимит трафика"
                    hint="Счётчик трафика маршрута со сбросом по расписанию."
                    open={draft.quotaEnabled}
                    control={<Switch checked={draft.quotaEnabled} onChange={(event) => update('quotaEnabled', event.currentTarget.checked)} aria-label="Лимит трафика" />}
                  >
                    <SettingsGrid>
                      <SettingRow id="route-quota" label="Лимит" error={showError(['quota'])}>
                        <div className="nf-combo">
                          <NumberInput id="route-quota" className="nf-num nf-num--wide" hideControls value={draft.quotaValue} onChange={(value) => update('quotaValue', value === '' ? '' : Number(value))} error={showError(['quota']) ? true : undefined} min={0.001} decimalScale={3} inputMode="decimal" required={draft.quotaEnabled} />
                          <Select className="nf-select-unit" value={draft.quotaUnit} onChange={(value) => update('quotaUnit', (value ?? 'GiB') as 'GiB' | 'TiB')} allowDeselect={false} data={['GiB', 'TiB']} aria-label="Единица лимита" />
                        </div>
                      </SettingRow>
                      <SettingRow id="route-quota-period" stableHint label="Сброс" hint={periodNote}>
                        <Select id="route-quota-period" className="nf-select-md" value={draft.quotaPeriod} onChange={(value) => update('quotaPeriod', (value ?? 'calendar_month') as QuotaPeriod)} allowDeselect={false} data={quotaPeriodOptions.map(({ value, label }) => ({ value, label }))} />
                      </SettingRow>
                      <SettingRow
                        id="route-quota-action"
                        stableHint
                        label="При достижении"
                        hint={draft.quotaAction === 'block_new'
                          ? 'Новые соединения не пройдут; активные сессии не разрываются.'
                          : 'Панель подсветит превышение, трафик продолжит идти.'}
                      >
                        <Select id="route-quota-action" className="nf-select-md" value={draft.quotaAction} onChange={(value) => update('quotaAction', (value ?? 'observe') as QuotaAction)} allowDeselect={false} data={[{ value: 'observe', label: 'Только уведомить' }, { value: 'block_new', label: 'Блокировать новые' }]} />
                      </SettingRow>
                    </SettingsGrid>
                  </OptionRow>

                  <OptionRow
                    title="Ограничение скорости"
                    hint="На IP клиента, для всех его соединений в маршруте."
                    open={draft.clientBandwidthEnabled}
                    control={<Switch checked={draft.clientBandwidthEnabled} onChange={(event) => update('clientBandwidthEnabled', event.currentTarget.checked)} aria-label="Ограничение скорости" />}
                  >
                    <SettingsGrid>
                      <SettingRow id="route-up" label="От клиента" hint="Пусто — без лимита." error={showError(['clientUploadMbps', 'bandwidth'])}>
                        <UnitNumberInput id="route-up" wide unit="Мбит/с" placeholder="без лимита" value={draft.clientUploadMbps} onValue={(value) => update('clientUploadMbps', value)} error={showError(['clientUploadMbps', 'bandwidth']) ? true : undefined} min={1} max={1_000_000} allowDecimal={false} inputMode="numeric" />
                      </SettingRow>
                      <SettingRow id="route-down" label="К клиенту" hint="Пусто — без лимита." error={showError(['clientDownloadMbps'])}>
                        <UnitNumberInput id="route-down" wide unit="Мбит/с" placeholder="без лимита" value={draft.clientDownloadMbps} onValue={(value) => update('clientDownloadMbps', value)} error={showError(['clientDownloadMbps', 'bandwidth']) ? true : undefined} min={1} max={1_000_000} allowDecimal={false} inputMode="numeric" />
                      </SettingRow>
                      <SettingRow
                        id="route-shaper"
                        group
                        stableHint
                        label="Где ограничивать"
                        hint={draft.shaperMode === 'kernel'
                          ? 'nftables на ноде, splice сохраняется. Маршруты порта должны иметь одинаковые лимиты.'
                          : 'Точно по маршруту (SNI), но отключает splice на этом порту.'}
                        error={shaperHint && shaperIsError ? shaperHint : undefined}
                      >
                        <SegmentedControl
                          className="nf-seg"
                          size="sm"
                          value={draft.shaperMode}
                          onChange={(value) => update('shaperMode', value as ShaperMode)}
                          data={[
                            { value: 'haproxy', label: 'HAProxy' },
                            // A route already stored as kernel stays selectable so it can be switched away.
                            { value: 'kernel', label: 'Ядро (nftables)', disabled: !kernelAllowed && draft.shaperMode !== 'kernel' },
                          ]}
                          aria-labelledby="route-shaper-label"
                          aria-describedby="route-shaper-hint"
                        />
                        {shaperHint && !shaperIsError && <span className="nf-setting-row__aside">Ядро: нужен Node Agent {KERNEL_SHAPER_MIN_AGENT}, на ноде {data.agentVersion ?? 'неизвестно'}</span>}
                      </SettingRow>
                    </SettingsGrid>
                  </OptionRow>
                </OptionList>
              </div>
            </Collapse>
          </section>

          <div className="nf-re-footer">
            {(submitAttempted && errors.length > 0) && (
              <Alert className="nf-route-error-summary" color="red" icon={<IconAlertCircle size={18} />} title={`Нужно исправить: ${errors.length}`} role="alert">
                <ul>{errors.slice(0, 5).map((error, index) => <li key={`${error.field}-${index}`}>{error.message}</li>)}</ul>
              </Alert>
            )}
            {saveError && <Alert color="red" icon={<IconAlertCircle size={18} />} role="alert">{saveError}</Alert>}
          </div>

          <footer className={`nf-save-bar is-${barStatus.tone}`}>
            <div className="nf-save-bar__status" role="status" aria-live="polite">
              <i aria-hidden="true" />
              <span>{barStatus.text}</span>
              {missingHint && <span className="nf-save-bar__hint">{missingHint}</span>}
              {pendingErrors > 0 && (
                <button type="button" className="nf-save-bar__errors" onClick={showErrors}>
                  <IconAlertCircle size={14} aria-hidden="true" />
                  Нужно исправить: {pendingErrors}
                </button>
              )}
            </div>
            <div className="nf-save-bar__actions">
              {!editingEnabled && <Button variant="default" onClick={() => void save('draft')} loading={saving === 'draft'} disabled={Boolean(saving && saving !== 'draft')}>Сохранить черновик</Button>}
              <Button type="submit" loading={saving === primaryIntent} disabled={Boolean(saving && saving !== primaryIntent)}>{editingEnabled ? 'Сохранить и применить' : 'Сохранить и включить'}</Button>
            </div>
          </footer>
        </Surface>

        <aside className="nf-route-preview-column" aria-label="Предпросмотр HAProxy">
          <Surface className="nf-route-preview-surface">
            {/* Предпросмотр HAProxy — закреплённая колонка справа */}
            <section className="nf-re-section nf-route-preview is-open" aria-labelledby="route-preview-title">
              <div className="nf-re-section__head">
                <span className="nf-re-section__index" aria-hidden="true"><IconCode size={13} /></span>
                <h2 id="route-preview-title">Предпросмотр HAProxy</h2>
                <span className={`nf-preview-validation is-${previewState}`} role="status">
                  <span>{valid ? <IconCheck size={14} /> : submitAttempted ? <IconAlertCircle size={14} /> : <IconInfoCircle size={14} />}{valid ? 'Форма заполнена верно' : submitAttempted ? `Ошибок: ${errors.length}` : 'Заполните обязательные поля'}</span>
                  <small>{preview.merged ? `Общий frontend с ${preview.merged} ${plural(preview.merged, ['маршрутом', 'маршрутами', 'маршрутами'])}` : 'Отдельный frontend'}{draft.expertOverride.trim() ? ' · есть ручные директивы' : ''}</small>
                </span>
              </div>
                <div className="nf-re-section__body nf-preview-body" id="route-preview-body">
                  <Note icon={<IconShieldCheck size={14} aria-hidden="true" />}>
                    Перед применением Agent проверит конфиг через <code>haproxy -c</code>; UFW меняется только по политике ноды. Активные соединения не разрываются.
                  </Note>
                  <div className="nf-code-editor" aria-label="Предпросмотр HAProxy, только чтение">
                    <div className="nf-code-editor__bar"><span><i /><i /><i /></span><b>haproxy.cfg</b><em>только чтение</em></div>
                    <pre><code>{preview.config}</code></pre>
                  </div>
                  <section className="nf-preview-expert" aria-labelledby="route-expert-title">
                    <div className="nf-preview-expert__head">
                      <span><strong id="route-expert-title">Ручные директивы backend</strong><small>Секции global, frontend, listen, backend и resolvers запрещены.</small></span>
                      <Switch checked={expertEnabled} onChange={(event) => { setExpertEnabled(event.currentTarget.checked); if (!event.currentTarget.checked) update('expertOverride', ''); }} aria-label="Разрешить ручные директивы" />
                    </div>
                    {expertEnabled && <Textarea aria-label="Директивы backend" value={draft.expertOverride} onChange={(event) => update('expertOverride', event.currentTarget.value)} error={showError(['expert'])} placeholder={'timeout connect 5s\nmaxconn 2000'} autosize minRows={8} maxRows={20} classNames={{ input: 'nf-code-input' }} />}
                  </section>
                </div>
            </section>
          </Surface>
        </aside>
      </form>

      <Modal opened={blocker.state === 'blocked'} onClose={() => blocker.state === 'blocked' && blocker.reset()} title="Выйти без сохранения?" size="sm" closeOnClickOutside closeOnEscape classNames={{ content: 'nf-dialog', header: 'nf-dialog__header', body: 'nf-dialog__body' }}>
        <div className="nf-confirm-dialog"><p>Изменения маршрута будут потеряны. HAProxy и UFW не изменятся.</p><div><Button variant="default" onClick={() => blocker.state === 'blocked' && blocker.reset()}>Продолжить</Button><Button color="red" onClick={discardAndLeave}>Выйти</Button></div></div>
      </Modal>
    </main>
  );
}
