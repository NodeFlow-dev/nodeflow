NODEFLOW NODE AGENT — SIGNED RELEASE UPLOAD
===========================================

Бинарники Agent — отдельные assets GitHub Release:
  nodeflow-node-agent-2.0.1-linux-amd64
  nodeflow-node-agent-2.0.1-linux-arm64

install.sh публикует их в Panel сам. Для ручной загрузки скачайте файл и
сверьте SHA-256 с digest, который GitHub показывает для asset'а:

  v=2.0.1 f=nodeflow-node-agent-2.0.1-linux-amd64
  curl -fsSLO https://github.com/NodeFlow-dev/nodeflow/releases/download/v$v/$f
  curl -fsSL https://api.github.com/repos/NodeFlow-dev/nodeflow/releases/tags/v$v \
    | jq -r --arg n "$f" '.assets[] | select(.name == $n) | .digest'
  sha256sum "$f"

В Panel откройте:
  Настройки → Node Agent (подписанные релизы)

Версия 2.0.1, ОС linux, архитектура amd64 или arm64 — по файлу. Выберите
бинарник и нажмите «Загрузить и подписать». Затем назначьте релиз на
странице нужной ноды.

Подписанный совместимый релиз обязателен и для первоначального добавления
ноды. После публикации откройте «Ноды» → «Добавить ноду»: Panel выберет
новейший совместимый релиз автоматически либо использует выбранную версию.

Ручная установка Agent на ноду без Panel (восстановление): скрипт
scripts/install-node.sh из комплекта или

  curl -fsSL https://raw.githubusercontent.com/NodeFlow-dev/nodeflow/main/scripts/install-node.sh -o install-node.sh
  sudo NODE_AGENT_TOKEN=<токен> bash install-node.sh

Он скачивает Agent для архитектуры ноды и проверяет его по digest GitHub.
