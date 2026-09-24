NODEFLOW — НАЧНИТЕ ОТСЮДА
=========================

1. Для автоматической установки на чистый сервер Ubuntu или Debian запустите
   из корня распакованного комплекта:

     sudo ./install.sh

   Скрипт проверит, что домен указывает на публичный IP сервера, запросит режим
   доступа, установит Docker, Caddy и Panel из готового образа
   ghcr.io/nodeflow-dev/nodeflow-panel (compose.release.yaml берётся из
   комплекта), скачает из GitHub Release бинарники Node Agent для amd64 и
   arm64, проверит их SHA-256 по digest GitHub, опубликует их в Panel как
   подписанные релизы и сохранит реквизиты в nodeflow-credentials.txt
   домашней папки запустившего пользователя. Серверу нужен доступ к GitHub
   и ghcr.io.
2. Для ручной установки откройте docs/install/index.html в браузере.
3. Состав комплекта:
     install.sh                  — установщик и апгрейдер Panel
     compose.release.yaml        — production compose (Panel, миграции, PostgreSQL)
     nodeflow.env.example        — шаблон .env для ручной установки
     scripts/init-mtls-pki.sh    — CA, mTLS-сертификат Panel и ключ подписи
     scripts/init-update-signing-key.sh
     scripts/install-node.sh     — ручная установка Node Agent на ноду
     scripts/prepare-node-firewall.sh
     configs/systemd/nodeflow-node-agent.service
     docs/install/               — инструкции и примеры Nginx/Caddy
     SHA256SUMS                  — контрольные суммы файлов комплекта

Бинарники Node Agent (nodeflow-node-agent-<версия>-linux-amd64 и -arm64)
лежат отдельными файлами в assets GitHub Release, в комплект они не входят.

ВАЖНО:
Без опубликованного подписанного релиза Agent установка новой ноды
заблокирована. install.sh публикует его сам; если он сообщил, что не смог,
скачайте бинарник из assets релиза и загрузите его в «Настройки → Node Agent».

После этого откройте «Ноды» → «Добавить ноду», укажите IP и SSH-доступ.
Panel один раз подключится по SSH, установит выбранный либо новейший
совместимый Agent и дальше будет управлять нодой по mTLS.
