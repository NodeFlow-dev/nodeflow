NODEFLOW PANEL
==============

С версии 2.0.0 Panel ставится из готового образа
ghcr.io/nodeflow-dev/nodeflow-panel:<версия>; сборка на сервере не нужна.

Файлы:
  compose.release.yaml   — production compose (Panel, миграции, PostgreSQL 17)
  nodeflow.env.example   — шаблон .env для ручной установки
  nodeflow-panel-source.tar.gz — исходники (для аудита и сборки своего образа)

Автоматическая установка из корня install kit:

  sudo ./INSTALL-NODEFLOW.sh

Standalone-установка из GitHub:

  curl -fsSL https://raw.githubusercontent.com/NodeFlow-dev/nodeflow/main/install.sh | sudo sh

Скрипт сам выберет последний опубликованный Release (или версию из
NODEFLOW_VERSION) и проверит compose.release.yaml и бинарники Agent по
SHA256SUMS релиза. Поддерживаются Ubuntu и Debian.

Скрипт запросит домен, проверит его DNS против публичного IP сервера и выберет
один из режимов: обычный Caddy HTTPS или дополнительная Caddy cookie-защита.
После установки он покажет реквизиты входа и сохранит их в
nodeflow-credentials.txt домашней папки пользователя.

Ручная установка из готового образа (от root):

  install -d -m 0750 /opt/nodeflow
  cp 01-PANEL/compose.release.yaml /opt/nodeflow/compose.yaml
  install -m 0600 01-PANEL/nodeflow.env.example /opt/nodeflow/.env
  # заполните секреты и домен в /opt/nodeflow/.env
  # CA, сертификат mTLS и ключ подписи обновлений — скриптами из
  # nodeflow-panel-source.tar.gz:
  tar -xzf 01-PANEL/nodeflow-panel-source.tar.gz -C /opt/nodeflow \
    scripts/init-mtls-pki.sh scripts/init-update-signing-key.sh
  /opt/nodeflow/scripts/init-mtls-pki.sh panel.example.com /opt/nodeflow
  cd /opt/nodeflow
  docker compose pull && docker compose up -d
  curl -fsS http://127.0.0.1:8080/healthz

После этого обязательно настройте Nginx или Caddy по 00-START-HERE.html.

Порты:
  80/tcp   — сертификат и редирект на HTTPS
  443/tcp  — браузерная Panel
  4200/tcp — прямой mTLS Agent → Panel
  8080/tcp — только 127.0.0.1, наружу не открывать

Готовые reverse proxy конфиги лежат в подпапке reverse-proxy.

ОБНОВЛЕНИЕ PANEL
----------------

Повторно запустите установщик на сервере Panel:

  curl -fsSL https://raw.githubusercontent.com/NodeFlow-dev/nodeflow/main/install.sh | sudo sh

Найдя /opt/nodeflow/.env, он работает как апгрейдер: делает pg_dump и архив
/opt/nodeflow в /var/backups/nodeflow, сохраняет .env, tls/, pki/ и
Caddy-сниппет, ставит новый compose.yaml, выполняет docker compose pull и
up -d и проверяет версию Panel. Установки 1.0.x, собранные из исходников,
переводятся на готовый образ. При сбое возвращаются прежние compose.yaml и
.env; дамп БД остаётся, миграции автоматически не откатываются.

После обновления:

  cd /opt/nodeflow
  sudo docker compose ps
  curl -fsS http://127.0.0.1:8080/healthz

Не удаляйте /opt/nodeflow/.env, /opt/nodeflow/tls и /opt/nodeflow/pki.
