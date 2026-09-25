<p align="center">
  <img src="docs/brand/nodeflow-hero.svg" width="100%" alt="NodeFlow — HAProxy Infrastructure Control">
</p>

<p align="center">
  <a href="#быстрая-установка">Установка</a> ·
  <a href="#обновление-с-10x">Обновление</a> ·
  <a href="#скриншоты">Скриншоты</a> ·
  <a href="docs/releases/2.0.1.md">Что нового в 2.0.1</a> ·
  <a href="../../issues">Issues</a> ·
  <a href="#поддержать-проект">Поддержка</a>
</p>

NodeFlow — панель управления парком HAProxy-нод для TCP- и SNI-маршрутизации. Panel (Go API + React UI + PostgreSQL) хранит маршруты и неизменяемые ревизии конфигурации, а Node Agent на каждой ноде сам подключается к Panel по mTLS, проверяет и применяет конфиг HAProxy, сообщает метрики и обновляется по подписанным релизам. Ручного редактирования `haproxy.cfg` и входящего доступа к нодам для управления не требуется.

![Обзор нод](docs/images/nodes-overview.png)

## Возможности 2.0

- **SNI-маршрутизация и фолбэк.** Общие listener'ы на порт, несколько SNI на маршрут, «Фолбэк» для всего, что не совпало; IP-, доменные и Unix-socket backend'ы.
- **Несколько серверов и DNS-пулы.** До 16 серверов на маршрут в режимах «Пул» и «Основной + резервные»; домен как DNS-пул (все A/AAAA-адреса, с исключением недоступных), отдельные веса для IP пула.
- **Балансировка.** `roundrobin`, `static-rr`, `random`, `leastconn` и `leastping` («Меньше задержка»). Вес сервера и отдельного IP DNS-пула, для `leastping` — ещё стоимость (множитель задержки); толерантность: близкие по нагрузке или задержке серверы делят клиентов по весу, веса на лету выставляет Agent через runtime API, без reload.
- **Закрепление клиента.** Хеш IP (consistent или map-based, группировка IPv6 по префиксу) или таблица с TTL, переживающая reload; размер «Авто» подбирается от RAM ноды.
- **PROXY protocol.** Приём от доверенных IP, подсетей и доменов (Agent резолвит их и обновляет ACL без reload), вариант «от всех»; health-check с PROXY-заголовком.
- **Ограничение скорости.** Лимиты upload/download на IP клиента — фильтрами HAProxy или в ядре через nftables (`shaper_mode=kernel`), без потери splice.
- **Производительность.** Zero-copy splice в обе стороны; Agent поднимает `fs.pipe-max-size`, и Panel рендерит буферы 1 МиБ вместо 256 КиБ.
- **Квоты и учёт трафика.** Помесячный трафик нод, backend'ов и маршрутов, квоты на маршрут.
- **Безопасное применение.** Desired/actual-ревизии, полная проверка `haproxy -c` до замены конфига, graceful reload с откатом.
- **Prometheus `/metrics`** в Panel: состояние нод и маршрутов, соединения, трафик, синхронизация ревизий (bearer-токен, allowlist CIDR).
- **Подписанные обновления Agent.** Ed25519 + SHA-256, монотонная последовательность, атомарная активация и автооткат.
- **mTLS.** Отдельный клиентский сертификат на ноду, ротация и staged renewal; локальный API Agent доступен только с loopback.
- **UFW.** Правила listener-портов строятся из применённой ревизии, чужие правила не трогаются.

## Архитектура

![Архитектура NodeFlow](docs/brand/architecture.svg)

Agent сам открывает соединение с Panel: получает ревизии и команды, отправляет heartbeat с метриками. SSH нужен один раз — для первичной установки Agent.

## Требования

- **Panel:** Ubuntu 22.04+/Debian 12+ (amd64 или arm64), 2 vCPU, 2 ГБ RAM, 15 ГБ диска, домен с A/AAAA-записью на сервер, открытые `80`, `443` и `4200/tcp`. Docker Engine с Compose и Caddy установщик поставит сам.
- **Ноды:** Linux amd64 или arm64 с systemd и HAProxy 3.x. Автоматическая установка из Panel поддерживает Ubuntu 24.04 (noble) и 26.04 (resolute) — Panel ставит HAProxy 3.4 из официального репозитория HAProxy Performance и не понижает более новый установленный HAProxy — и Debian: если HAProxy ещё нет, ставится пакет `haproxy` из репозитория дистрибутива (в Debian 12 это 2.6, поэтому HAProxy 3.x поставьте заранее). nftables — для ограничения скорости в ядре. SSH-доступ root или пользователем с sudo, по паролю или ключу.

## Быстрая установка

### Panel

На чистом сервере:

```bash
curl -fsSL https://raw.githubusercontent.com/NodeFlow-dev/nodeflow/main/install.sh | sudo sh
```

Установщик:

1. спросит домен и проверит, что его DNS указывает на этот сервер;
2. предложит режим доступа: Caddy cookie-gate (ссылка активации ставит защищённую cookie) или без него — токен администратора Panel нужен в любом случае;
3. установит Docker и **Caddy** — Caddy сам получит сертификат Let's Encrypt и будет проксировать HTTPS на Panel, которая слушает только `127.0.0.1:8080`;
4. скачает install kit последнего релиза, проверит его по SHA-256, который GitHub публикует для каждого asset релиза, возьмёт из него `compose.release.yaml`, сгенерирует `.env`, CA, сертификат mTLS и ключ подписи обновлений в `/opt/nodeflow`;
5. выполнит `docker compose pull && docker compose up -d` с образом `ghcr.io/nodeflow-dev/nodeflow-panel:2.0.1` — на сервере ничего не собирается, миграции БД применяет сам образ;
6. опубликует подписанные релизы Node Agent (amd64 и arm64) и сохранит реквизиты входа в `~/nodeflow-credentials.txt` (права `0600`).

Файрвол установщик не меняет. Для автоматизации без вопросов передайте переменные `NODEFLOW_DOMAIN`, `NODEFLOW_AUTH_MODE=cookie|none` и при необходимости `NODEFLOW_VERSION=2.0.1` (по умолчанию — последний релиз):

```bash
curl -fsSL https://raw.githubusercontent.com/NodeFlow-dev/nodeflow/main/install.sh \
  | sudo NODEFLOW_DOMAIN=panel.example.com NODEFLOW_AUTH_MODE=cookie sh
```

Установщик есть и в install kit (`NodeFlow-Panel-<версия>-Agent-<версия>-install-kit.tar.gz` в assets релиза): распакуйте его и запустите `sudo ./install.sh` — тогда `compose.release.yaml` берётся из комплекта, а из GitHub скачиваются только бинарники Agent для публикации в Panel.

Assets релиза — ровно три файла: install kit (установщики, `compose.release.yaml`, шаблон `.env`, скрипты PKI и ноды, systemd-юнит Agent, инструкции) и бинарники `nodeflow-node-agent-<версия>-linux-amd64` / `-linux-arm64`. Panel поставляется только образом `ghcr.io/nodeflow-dev/nodeflow-panel`. Отдельного `SHA256SUMS` нет: установщики сверяют каждый скачанный файл с SHA-256, который GitHub публикует для asset'а (поле `digest` в API релиза), и прерываются при расхождении или отсутствии digest.

### Ноды

В Panel: **Ноды → Добавить ноду** → IP, SSH-порт, пользователь и пароль или ключ → сверьте fingerprint хоста → **«Установить Node Agent»**. Panel один раз подключится по SSH, установит подписанный Agent, updater и mTLS-сертификат ноды; дальше нода управляется только через исходящий mTLS-канал на `4200/tcp`.

Для восстановления без Panel есть `install-node.sh`: он скачивает Agent для архитектуры ноды из последнего релиза (или из `NODEFLOW_VERSION`) и проверяет его по SHA-256 asset'а из GitHub API; systemd-юнит встроен в скрипт. Нужны `curl`, `jq` или `python3` и установленный HAProxy:

```bash
curl -fsSL https://raw.githubusercontent.com/NodeFlow-dev/nodeflow/main/scripts/install-node.sh -o install-node.sh
sudo NODE_AGENT_TOKEN=<токен> bash install-node.sh
```

Тот же скрипт лежит в install kit: `scripts/install-node.sh`.

### Ручная установка через Docker Compose

Команды выполняются от root (`sudo -i`): каталог `/opt/nodeflow` закрыт для остальных пользователей.

```bash
v=2.0.1
kit=NodeFlow-Panel-$v-Agent-$v-install-kit
curl -fsSLO "https://github.com/NodeFlow-dev/nodeflow/releases/download/v$v/$kit.tar.gz"
# сверьте с digest asset'а на странице релиза или в API:
curl -fsSL "https://api.github.com/repos/NodeFlow-dev/nodeflow/releases/tags/v$v" \
  | jq -r --arg n "$kit.tar.gz" '.assets[] | select(.name == $n) | .digest'
sha256sum "$kit.tar.gz"
tar -xzf "$kit.tar.gz" && cd "$kit" && sha256sum -c SHA256SUMS
install -d -m 0750 /opt/nodeflow
install -m 0644 compose.release.yaml /opt/nodeflow/compose.yaml
install -m 0600 nodeflow.env.example /opt/nodeflow/.env   # заполните секреты и домен
install -d /opt/nodeflow/scripts && install -m 0755 scripts/init-*.sh /opt/nodeflow/scripts/
/opt/nodeflow/scripts/init-mtls-pki.sh panel.example.com /opt/nodeflow   # CA, mTLS, ключ подписи
cd /opt/nodeflow && docker compose pull && docker compose up -d
curl -fsS http://127.0.0.1:8080/healthz
```

Перед Panel нужен HTTPS reverse proxy: готовые примеры для Caddy и Nginx — в [`docs/install/reverse-proxy`](docs/install/reverse-proxy). Порт `4200` не проксируется: ноды подключаются к нему напрямую.

## Обновление с 1.0.x

Повторно запустите тот же установщик на сервере Panel:

```bash
curl -fsSL https://raw.githubusercontent.com/NodeFlow-dev/nodeflow/main/install.sh | sudo sh
```

Найдя `/opt/nodeflow/.env`, он работает как апгрейдер: делает `pg_dump` и архив конфигурации в `/var/backups/nodeflow/`, сохраняет `.env`, `tls/`, `pki/` и Caddy-сниппет `/etc/caddy/conf.d/nodeflow-panel.caddy`, переводит установку со сборки из исходников на образ ghcr.io, запускает миграции и проверяет, что Panel отвечает новой версией. При ошибке возвращаются прежние `compose.yaml` и `.env`; дамп БД остаётся (миграции автоматически не откатываются).

Затем в **Настройки → Node Agent** обновите ноды до 2.0.1 — updater заменит Agent атомарно и откатит его при неудачном запуске. Самообновление не меняет systemd-юнит, поэтому на нодах, установленных версиями 1.0.x, один раз добавьте права на настройку pipe-буферов ядра (без этого splice остаётся на 256 КиБ) — или переустановите Agent из меню ноды:

```bash
sudo install -d /etc/systemd/system/nodeflow-node-agent.service.d
printf '[Service]\nReadWritePaths=-/etc/sysctl.d -/proc/sys/fs/pipe-max-size -/proc/sys/fs/pipe-user-pages-soft\n' \
  | sudo tee /etc/systemd/system/nodeflow-node-agent.service.d/20-kernel-pipes.conf
sudo systemctl daemon-reload && sudo systemctl restart nodeflow-node-agent
```

Подробности, изменения поведения и список миграций — [в заметках к релизу 2.0.0](docs/releases/2.0.0.md).

## Скриншоты

### Карточка ноды

![Карточка ноды](docs/images/node-detail.png)

### Редактор маршрута с предпросмотром HAProxy

![Редактор маршрута](docs/images/route-editor.png)

### Трафик и топ маршрутов

![Трафик](docs/images/traffic-overview.png)

### Настройки: релизы Node Agent, Panel и Prometheus

![Настройки](docs/images/settings.png)

## Сборка из исходников

Нужны Go 1.25+ и Node.js 22+.

```bash
git clone https://github.com/NodeFlow-dev/nodeflow.git && cd nodeflow
(cd frontend && npm ci && npm run build)
./scripts/build-panel.sh                  # panel-api со встроенным UI
go build -trimpath -o node-agent ./cmd/node-agent
go build -trimpath -o node-updater ./cmd/node-updater
go test ./...
```

Локальный стенд: `cp .env.example .env`, заполните секреты и выполните `docker compose up -d --build` — `compose.yaml` в корне собирает образ из `Dockerfile.panel`. Интеграционные тесты PostgreSQL включаются переменной `NODEFLOW_TEST_DATABASE_URL`.

## Документация

- Заметки к релизам [2.0.1](docs/releases/2.0.1.md) и [2.0.0](docs/releases/2.0.0.md), [CHANGELOG](CHANGELOG.md)
- [API Panel](docs/api-contract.md)
- [Архитектура](docs/architecture.md)
- [Первичная установка ноды](docs/bootstrap-flow.md)
- [Самообновление Agent](docs/node-self-update.md)
- [Установка вручную](docs/install/README-PANEL.txt)

## Безопасность

- Node Agent не публикуется в интернет: локальный API принимает только loopback-трафик, управление идёт по исходящему mTLS-каналу.
- Для каждой ноды выпускается отдельный клиентский сертификат.
- Релизы Agent проверяются по подписи Ed25519, SHA-256, платформе и последовательности версий.
- Panel по умолчанию слушает только `127.0.0.1`; публикация HTTP наружу требует явного `ALLOW_INSECURE_HTTP=true`.
- Действия с повышенными правами пишутся в журнал аудита.

Об уязвимостях сообщайте по [SECURITY.md](SECURITY.md), не через публичные Issues.

## Обратная связь

Баг-репорты, идеи и вопросы по установке — в [Issues](../../issues). Укажите версии Panel/Agent, ОС и HAProxy ноды, шаги воспроизведения и обезличенные логи — без токенов, ключей, сертификатов и IP-адресов клиентов. Правила для PR — в [CONTRIBUTING.md](CONTRIBUTING.md).

## Поддержать проект

Поддержка проекта очень важна для продолжения обновлений NodeFlow.

[Tribute](https://web.tribute.tg/d/Pat)

Tron: `TNUe93tFxeHj4avBY8s3NWzjaiyZfPWD9T`

## Лицензия

© 2026 NodeFlow. Все права защищены. Исходный код открыт для ознакомления, самостоятельной установки пользователями NodeFlow и внесения вклада в этот репозиторий. Распространение, в том числе изменённых версий, и коммерческое использование кода требуют письменного разрешения правообладателя. Полный текст — в [LICENSE](LICENSE).
