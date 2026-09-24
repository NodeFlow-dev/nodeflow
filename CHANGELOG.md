# Changelog

## 2.0.0 — Prebuilt images and advanced balancing

Panel and Node Agent are both 2.0.0. Full Russian release notes: [docs/releases/2.0.0.md](docs/releases/2.0.0.md).

- **Поставка.** Multi-arch образ `ghcr.io/nodeflow-dev/nodeflow-panel` (amd64/arm64) со встроенными миграциями (`panel-api migrate`); `compose.release.yaml` для production; `install.sh` ставит и обновляет Panel из образа (pg_dump перед обновлением, сохраняет `.env`, `tls/`, `pki/` и Caddy-сниппет) и публикует релизы Agent для amd64/arm64; `install-node.sh` берёт Agent из релиза с проверкой SHA-256. GitHub Actions: CI и release по тегу `v*`.
- Версии Panel и Node Agent выровнены на 2.0.0; проверки возможностей Agent сравнивают версии численно.
- Статические ассеты Vite с `-` в хеше снова кешируются как immutable.
- Удалён устаревший веб-интерфейс `web/`.
- IPv6-клиенты можно полностью выключить для нод без IPv6 (IPv4-таблицы) — поле маршрута `client_ipv6` (migration 000054).

- **IPv6 клиентов можно выключить для маршрута** (migration 000054). Новое поле `client_ipv6` (по умолчанию `true`, все существующие маршруты рендерятся байт-в-байт как раньше, версия рендерера не меняется). `false` — для нод без IPv6: таблицы клиентов маршрута только для IPv4 — `stick-table type ip` + `stick on src` для «Запоминать клиента», `balance source` для хеша IP, `type ip` и ключ `src` у таблиц ограничения скорости HAProxy; `sticky_ipv6_prefix` при этом обнуляется. Строки `bind` не меняются (`bind :<port>` и так слушает только IPv4). В редакторе вместо вводившего в заблуждение переключателя «IPv6-клиенты» (он лишь открывал поле префикса и не сохранялся) — «IPv6-клиенты» Вкл/Выкл и поле «Подсеть IPv6-клиента /__» (пусто — /64), неактивное при «Выкл»; превью совпадает с рендером.
- **Домены в «Доверенных адресах» PROXY protocol.** `accept_proxy_from` принимает, кроме IP и подсетей, домены (до 32 на маршрут): домен означает все его A/AAAA-адреса (DNS-пул). Для порта с доменами рендерер добавляет `acl nf_pp_trusted src -f /etc/haproxy/nodeflow/pp-trusted-<frontend>.acl` и `expect-proxy … if { src <статические> } || nf_pp_trusted` с аннотацией `# nf-pp-trusted`; порты без доменов рендерятся байт-в-байт как раньше (версия рендерера не меняется). Node Agent **1.1.3** резолвит домены перед каждой проверкой/перезагрузкой HAProxy и затем каждые 30 с; при изменении набора атомарно переписывает файл и подменяет ACL через runtime API (`prepare/add/commit acl`) без reload; без изменений — ни одной команды в сокет; при сбое DNS остаётся последний известный набор. Панель требует Agent ≥ 1.1.3: `422 pp_trusted_domains_requires_agent_1_1_3` при сохранении и публикации. В редакторе подсказка «IP, подсети или домены…», предпросмотр совпадает с рендерером.
- **Splice-буферы 1 МиБ, когда ядро позволяет.** Node Agent **1.1.2** при старте и в каждом heartbeat поднимает `fs.pipe-max-size` до 1048576 (никогда не понижает) и ставит `fs.pipe-user-pages-soft = 0`: пишет прямо в `/proc/sys/fs/*` (без `sysctl`), сохраняет `/etc/sysctl.d/90-nodeflow-pipes.conf`; `NODE_AGENT_TUNE_SYSCTL=off` — только отчёт. Heartbeat сообщает `metrics.kernel_pipes {pipe_max_size, pipe_user_pages_soft, tuned, managed}`. systemd-юнит агента открывает на запись только эти два файла `/proc/sys` и `/etc/sysctl.d` (`ProtectKernelTunables` остаётся). Панель рендерит `tune.pipesize 1048576` для ноды, где `tuned=true`, `pipe_max_size ≥ 1048576` и `pipe_user_pages_soft = 0`, иначе прежние 262144.
- **«Авто» размер stick-table от RAM ноды — изменение поведения.** `sticky_table_entries=''` для `sticky_mode=source_table` больше не означает фиксированные 100k: размер = clamp(5 % RAM / 228 Б / N, 100k, 10m), округлённый вниз до k, где N — число включённых «Авто»-таблиц на ноде; RAM из последнего heartbeat (`memory_total_bytes`), округлённый вниз до 256 МиБ, чтобы колебания не публиковали ревизии; RAM неизвестна — 1m. Память занимается только по мере прихода клиентов. Явные размеры не меняются. `GET` маршрута/списка отдаёт read-only `sticky_table_entries_effective` (например `919k`); редактор показывает «Авто: ~N клиентов (5% RAM ноды), ≈X МБ при полной таблице», превью совпадает с рендером.
- HAProxy renderer **v21** (v20 и старше остаются поддерживаемыми). Нода без tuned-пайпов и без «Авто»-таблиц рендерит байт-в-байт прежний v20 (включая заголовок). Метаданные ревизии хранят `pipe_size` и `auto_sticky_table_entries`; когда heartbeat меняет эти факты (агент настроил ядро, RAM перешла в другой 256-МиБ-бакет, у существующих «Авто»-маршрутов прежние 100k), панель сама публикует новую ревизию маршрутов (`node facts changed`), без правки маршрутов. Ревизии, созданные вручную (admin), не трогаются.
- **«Меньше соединений»: стоимость и толерантность.** Для `balance_algorithm=leastconn` действуют `servers[].cost` и `ip_weights[].cost` (0,01..100): соединение с сервером стоимости *c* считается за *c*, панель рендерит статический `weight` = base/cost × K (без Agent; без стоимости конфиг байт-в-байт прежний). Новое поле `balance_tolerance` (алиас `leastping_tolerance`, та же колонка, без миграции): серверы, чья нагрузка (соединения × стоимость / вес) не больше минимума на столько процентов, считаются равными и делят новых клиентов по весу; при толерантности > 0 Node Agent 1.1.1 каждые 5 с читает `scur` и выставляет runtime-веса (гистерезис ×1,5, только изменённые веса). Требует Agent ≥ 1.1.1 (`422 leastconn_tolerance_requires_agent_1_1_1`); Node Agent поднят до 1.1.1. В редакторе «Стоимость» теперь видна и для «Меньше соединений», в блоке «Распределение клиентов» — «Толерантность, %».

## 1.1.0 — Advanced route features

- **Балансировка и закрепление клиента** (migrations 000051, 000052): в pool-режиме две независимые настройки. `balance_algorithm` — выбор сервера для нового клиента: `roundrobin` (по умолчанию, веса динамические) или `static-rr`, `random` (1 или 2 выборки, по умолчанию 2), `leastconn` («Меньше соединений»), `leastping` («Меньше задержка», аналог Xray leastLoad/leastPing: Node Agent выставляет runtime-веса по задержке health-check × `cost`, допуск `leastping_tolerance` 0..1, по умолчанию 0.2, и `leastping_tolerance_ms`). `sticky_mode` — закрепление: `none`, `source` (хеш IP: `hash-type` consistent/map-based, `hash-balance-factor`, группировка IPv6 через `balance hash src,ipmask(32,N)`) или `source_table` (запоминание на `sticky_ttl`, пресеты 1 мин..24 ч, старые значения до 7d читаются; размер таблицы 10k/100k/1m ≈2/22/220 МБ; IPv6 /128../48; таблица переживает reload через локальную секцию `peers nf_peers`). Общие: `slowstart` и вес сервера `weight` 1..256. Для DNS-пула — `ip_weights` (свой вес для отдельного IP), применяет Node Agent. `leastping` и `ip_weights` требуют Agent ≥1.1.0 (`422 leastping_requires_agent_1_1` / `ip_weights_requires_agent_1_1`). Режим `sni` удалён (балансировал целые домены, а не клиентов): ввод отклоняется, миграция 000052 переводит такие маршруты в `source`; `leastconn`/`roundrobin` из `sticky_mode` переносятся в `balance_algorithm`. Без новых полей значения выводятся из `sticky_enabled` и DNS-пулов; `sticky_enabled` остаётся в ответах API. В редакторе вместо «Распределение клиентов» — строки «Балансировка», «Закрепление клиента» и «Плавный ввод после восстановления», в таблице серверов — «Вес», «Стоимость» и «Веса IP».
- **Исправлена регрессия DNS-пула**: маршрут с DNS-пулом в `servers[]` без sticky снова распределяет клиентов по `balance source`, как в 1.0.8 (раньше получал roundrobin). Это единственное изменение вывода для существующих маршрутов; golden и fingerprints существующих маршрутов не изменились, ревизия рендерера остаётся v19.
- **Редизайн редактора маршрутов** (NodeFlow Panel): секция «1 Входящее соединение» — переключатель «Принимать PROXY protocol» с chips-вводом доверенных адресов (IP/CIDR, без примеров, с валидацией). Секция «2 Назначение» — единый список серверов с режимами «Пул» (все активны, consistent-hash при sticky) и «Основной + резервные» (failover по порядку). Секции 3–5 убраны; «Дополнительно» (закрыто по умолчанию) содержит «Лимит трафика», «Ограничение скорости» и подсказки; авто-открывается при ненулевых настройках или ошибках валидации.
- **Sticky sessions** упрощены: `balance source` + `hash-type consistent sdbm avalanche` без stick-table и без параметров size/expire. Migration 000030 удаляет `sticky_table_size` и `sticky_expire`.
- **Balance mode** (`balance_mode: pool | failover`): в pool всем серверам — одинаковый приоритет; failover — порядок = приоритет, position > 1 → `backup`. Migration 000031 добавляет колонку `balance_mode` + `route_servers.dns_pool` + `route_servers.preferred_ip`.
- **DNS-пул per-server**: каждый сервер типа «Домен» может иметь `dns_pool=true` — рендерится как `server-template` с resolvers. Смешивание статических и dns-пул серверов разрешено. В failover-режиме dns-пул-сервер на позиции > 1 получает `backup` + `option allbackups`. Опциональный `preferred_ip` для dns-пул-сервера на первой позиции failover: статичный основной + template как резерв.
- **F1 — Conditional PROXY protocol acceptance**: new `accept_proxy_from` field (list of IPv4/IPv6 addresses or CIDRs, max 256). The renderer emits `tcp-request connection expect-proxy layer4 if { src ... }` once per frontend using the union of all enabled routes on that listener. Direct clients without PROXY headers keep working on the same bind; `accept-proxy` is never emitted on the bind line.
- **F2 — Health-check PROXY protocol**: when `health_check=true` and `proxy_protocol=v1|v2`, the `server` line now includes `check-send-proxy` so health probes carry the same PROXY header as live traffic.
- **F3 — Multi-server backends** (GitHub issue #7): new `servers` array per route (max 16, unique names/positions). Each entry renders as a separate `server` line with optional `backup` keyword. When `servers` is empty the existing single-target fallback is used — fully backward compatible. New table `route_servers` (migration 000027).
- **F5 — Panel /metrics endpoint** (Prometheus text format): opt-in `/metrics` serving node telemetry already collected by the panel — node up/last_seen, route counts, connections, backend health, traffic bytes, config revision sync state. Disabled by default; requires `PANEL_METRICS_ENABLED=true` + `PANEL_METRICS_TOKEN=<≥32 chars>`. Optional source allowlist via `PANEL_METRICS_ALLOW_CIDRS`. No new dependencies, no Agent changes.
- **F6 — Simplified match_mode**: new canonical value `fallback` replaces the deprecated `any_tcp` and `destination_ip` modes. Both legacy values are accepted on input and normalized to `fallback`; the wildcard vs concrete-IP distinction is already expressed by `listener_ip`. Migration 000029 updates existing rows; down migration restores them from `listener_ip`.
- Migration 000029 adds NOT NULL constraints on match_mode/target_type; publish path loads route_servers; servers-only payload accepted with optional position; compose passes PANEL_METRICS_*; metrics without explicit timestamps + node_name label.
- HAProxy renderer bumped to **v18**: sticky uses consistent hash (no stick-table); balance_mode; per-server dns_pool with server-template; preferred_ip static primary + template backup.
- **Review fixes (route servers)**: `servers[]` — единственный источник истины; route-level `dns_pool` вместе с `servers[]` игнорируется (больше не теряются остальные серверы). Один сервер без `preferred_ip` сохраняется как обычный single-target маршрут (`nf_srv_<id>`, quota `block_new` работает). `block_new` отклоняется для multi-server и DNS-пулов. Без `balance_mode` явные `backup` сохраняются (совместимость с rc-клиентами); `sticky_table_size`/`sticky_expire` принимаются и игнорируются. Failover: основной — первый по позиции (а не `position=1`). `preferred_ip` — только первый сервер в failover; при DNS-пуле-резерве (включая template за `preferred_ip`) эмитится `option allbackups`, и такой резерв должен быть единственным — HAProxy включает все backup одновременно. DNS-пул форсирует health check. Имена серверов: `A-Z a-z 0-9 _ -`, до 58 символов, без суффикса `_pref` и без коллизий со слотами template. Fingerprint существующих маршрутов не меняется; ledger fingerprint включает `servers`. Migration 000041: `balance_mode='failover'` для маршрутов с backup, нормализация single-server маршрутов, CHECK на `preferred_ip`/`dns_pool`.
- Panel version **1.1.0**, Node Agent **1.1.0**. `shaper_mode=kernel` requires Agent ≥1.1.0 (with nftables): the Panel rejects kernel-shaped routes for nodes whose last heartbeat reports an older or unknown Agent (`422 kernel_shaper_requires_agent_1_1`), both when saving the route and when publishing a config revision.

## 1.0.8 — Sticky DNS pools

- Added an optional DNS pool mode for domain TCP/SNI backends: HAProxy resolves all available addresses and distributes new client connections between healthy targets using consistent source-IP hashing.
- Unavailable pool members are removed from new-connection selection while healthy members continue serving traffic.
- Route and traffic views now count only resolved DNS-pool members instead of all reserved `server-template` slots.
- Unassigned `MAINT (resolution)` slots no longer incorrectly mark a healthy node as degraded.
- HAProxy renderer revision bumped to v16; existing route revisions remain readable.

## 1.0.7 — Full HAProxy TCP splice

- HAProxy attempts zero-copy splicing for both client-to-server and server-to-client TCP payloads instead of relying on the conservative automatic splice heuristic.
- SNI ClientHello inspection still completes before bulk tunnel traffic becomes eligible for splicing.
- HAProxy renderer revision bumped to v15; bandwidth filters and unsupported transports continue to use the buffered fallback path.

## 1.0.6 — HAProxy idle connection cleanup

- HAProxy TCP client and server sockets use per-connection keepalive probes after 300 seconds, every 30 seconds, with three failed probes before removal.
- Fully inactive TCP tunnels now expire after 15 minutes instead of 24 hours.
- HAProxy renderer revision bumped to v14; historical route revisions remain readable.

## 1.0.5 — Route shaping and HAProxy control

- Added optional per-client-IP bandwidth limits for TCP and SNI routes.
- Added a deliberate hard restart action for HAProxy nodes.
- Improved route editor preview and shaper controls.
- New Panel installations use the Rose theme by default.

## 1.0.4 — Resilient backend DNS

- Domain backends use explicit local AdGuard, systemd-resolved, Cloudflare, Google and Quad9 fallbacks without depending on `/etc/resolv.conf`.
- HAProxy renderer revision bumped to v12 so newly published route revisions carry the resolver policy.
- Panel and Node Agent release versions aligned at 1.0.4.

## 1.0.2 — Node ordering and service control

- Added persistent drag-and-drop ordering for nodes and per-node routes.
- Added confirmed HAProxy stop/start control over the existing mTLS heartbeat channel.
- Fixed primary and disabled button contrast for custom accent themes.

## 1.0.1 — Initial release hotfix

- Reinstall skips HAProxy package replacement when the installed runtime is already current.
- Reinstall synchronizes the selected signed Agent release with Panel state.
- Agent accepts current short NodeFlow and legacy quota runtime object names.

## 1.0.0 — Initial release

- NodeFlow Panel: управление HAProxy-нодами, маршрутами, трафиком, квотами и UFW.
- Node Agent: исходящий mTLS-канал, безопасное применение конфигурации и signed updates с автооткатом.
- Bootstrap по SSH: root, пользователь с sudo, пароль или приватный SSH-ключ.
- HAProxy TCP/SNI: общие listener-frontend, IP/SNI/Any TCP маршруты, Unix socket и PROXY protocol.
- Наблюдаемость: RX/TX, соединения, TCP-сессии, backend health и журнал действий.
- Релизный комплект с Panel source, Node Agent binary, reverse-proxy примерами и контрольными суммами.
