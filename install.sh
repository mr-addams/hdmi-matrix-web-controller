#!/usr/bin/env bash
# Installs and updates the HDMI matrix web controller from its published container image.
#
#   install.sh [install] [options]  First installation. Re-running keeps the existing .env.
#   install.sh update [options]     Pulls the newest release (or --version) and recreates the
#                                   container only if the image changed. Meant for cron.
#
# Nothing is built here: the image is pulled from ghcr.io.
set -euo pipefail

REPO_RAW="https://raw.githubusercontent.com/mr-addams/hdmi-matrix-web-controller/main"
API_LATEST="https://api.github.com/repos/mr-addams/hdmi-matrix-web-controller/releases/latest"

# Seconds to wait for the container to accept sign-in. Tests lower it.
READY_TIMEOUT="${READY_TIMEOUT:-30}"

MODE="install"
DIR="hdmi-matrix"
VERSION=""
VERSION_GIVEN=0
PORT=8080
BIND=127.0.0.1
LOGIN_NAME="admin"
PASSWORD=""
ARGS_GIVEN=0
GENERATED=0

usage() {
  cat <<'EOT'
Использование:
  ./install.sh [install] [--dir DIR] [--version X.Y.Z] [--port N] [--bind ADDR] [--user NAME] [--password PW]
  ./install.sh update [--dir DIR] [--version X.Y.Z]

  install     первая установка (по умолчанию)
  update      обновить образ до последнего релиза или до --version; для cron

  --dir       папка установки (по умолчанию ./hdmi-matrix)
  --version   версия образа, например 0.1.0; по умолчанию — последний релиз
  --port      порт на хосте (по умолчанию 8080)
  --bind      адрес публикации: 127.0.0.1 — только этот хост, 0.0.0.0 — вся локальная сеть
  --user      логин для входа на страницу (по умолчанию admin)
  --password  пароль; если не задан, генерируется случайный и выводится один раз

Параметры --port, --bind, --user и --password применяются только при создании .env.
EOT
}

die() {
  echo "$1" >&2
  exit "${2:-2}"
}

# ── Validation: the same rules apply to arguments and to an existing .env ──

valid_port() {
  # Check the length before arithmetic: an overflowing integer would pass the range check.
  [[ "$1" =~ ^[0-9]{1,5}$ ]] || return 1
  local value=$((10#$1))
  (( value >= 1 && value <= 65535 ))
}

valid_ipv4() {
  [[ "$1" =~ ^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})$ ]] || return 1
  local octet
  for octet in "${BASH_REMATCH[@]:1}"; do
    (( 10#$octet <= 255 )) || return 1
  done
}

valid_login() {
  [[ "$1" =~ ^[A-Za-z0-9._-]{1,32}$ ]]
}

valid_version() {
  [[ "$1" =~ ^(latest|[0-9]+\.[0-9]+\.[0-9]+)$ ]]
}

# The password goes into .env unquoted and into a quoted curl config. Spaces, '#', quotes,
# '$' and backslashes would break one of these parsers; control characters break both.
valid_password() {
  [ -n "$1" ] || return 1
  case "$1" in
    *[[:space:]]*|*[[:cntrl:]]*|*'#'*|*'"'*|*"'"*|*'$'*|*\\*) return 1 ;;
  esac
}

read_env_value() {
  sed -n "s/^$1=//p" .env
}

# Each key must appear exactly once: compose and this script must read the same value.
check_env_file() {
  local key
  for key in UI_USER UI_PASSWORD MATRIX_UI_PORT MATRIX_UI_BIND; do
    if [ "$(grep -c "^${key}=" .env || true)" != "1" ]; then
      die "В .env ключ $key должен встречаться ровно один раз. Исправь .env или удали его."
    fi
  done
  LOGIN_NAME=$(read_env_value UI_USER)
  PASSWORD=$(read_env_value UI_PASSWORD)
  PORT=$(read_env_value MATRIX_UI_PORT)
  BIND=$(read_env_value MATRIX_UI_BIND)
  valid_login "$LOGIN_NAME" || die "В .env некорректный UI_USER: $LOGIN_NAME"
  valid_password "$PASSWORD" || die "В .env некорректный UI_PASSWORD (пустой или с запрещёнными символами)"
  valid_port "$PORT" || die "В .env некорректный MATRIX_UI_PORT: $PORT"
  valid_ipv4 "$BIND" || die "В .env некорректный MATRIX_UI_BIND: $BIND"
  if grep -q '^HDMI_UI_VERSION=' .env; then
    valid_version "$(read_env_value HDMI_UI_VERSION)" || die "В .env некорректный HDMI_UI_VERSION: $(read_env_value HDMI_UI_VERSION)"
  fi
}

# Changes a key in .env or appends it. The file stays mode 600 because it holds the password.
set_env_value() {
  local key="$1" value="$2" tmp
  tmp=$(mktemp)
  if grep -q "^${key}=" .env; then
    sed "s|^${key}=.*|${key}=${value}|" .env > "$tmp"
  else
    cat .env > "$tmp"
    echo "${key}=${value}" >> "$tmp"
  fi
  mv "$tmp" .env
}

# Image version: the value from --version, or the latest GitHub release.
resolve_version() {
  if [ "$VERSION_GIVEN" -eq 1 ]; then
    printf '%s' "$VERSION"
    return 0
  fi
  local tag
  tag=$(curl -fsSL --max-time 10 "$API_LATEST" 2>/dev/null | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1) || true
  tag="${tag#v}"
  [ -n "$tag" ] || die "Не удалось узнать последний релиз с GitHub. Укажи версию: --version X.Y.Z" 1
  valid_version "$tag" || die "Неожиданный тег релиза: $tag. Укажи версию явно: --version X.Y.Z" 1
  printf '%s' "$tag"
}

# ── Argument parsing ──

need_value() {
  [ $# -ge 2 ] || die "$1 требует значение"
}

if [ $# -gt 0 ]; then
  case "$1" in
    install|update) MODE="$1"; shift ;;
  esac
fi

while [ $# -gt 0 ]; do
  case "$1" in
    --dir)      need_value "$@"; DIR="$2"; shift 2 ;;
    --version)  need_value "$@"; VERSION="${2#v}"; VERSION_GIVEN=1; shift 2 ;;
    --port)     need_value "$@"; PORT="$2"; ARGS_GIVEN=1; shift 2 ;;
    --bind)     need_value "$@"; BIND="$2"; ARGS_GIVEN=1; shift 2 ;;
    --user)     need_value "$@"; LOGIN_NAME="$2"; ARGS_GIVEN=1; shift 2 ;;
    --password) need_value "$@"; PASSWORD="$2"; ARGS_GIVEN=1; shift 2 ;;
    -h|--help)  usage; exit 0 ;;
    *) echo "Неизвестный параметр: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [ "$VERSION_GIVEN" -eq 1 ]; then
  valid_version "$VERSION" || die "Версия должна быть вида X.Y.Z или latest: $VERSION"
fi

if ! command -v docker >/dev/null 2>&1; then
  die "Docker не найден. Установи Docker Engine с плагином Compose." 1
fi
if ! docker compose version >/dev/null 2>&1; then
  die "Нет плагина docker compose. Установи docker-compose-plugin." 1
fi

# Waits until the container accepts sign-in, using the same path as the browser: the /login form.
# Success is a redirect to the main page; a wrong password redirects to /login?error=1. Both are
# answers from the server, so wrong credentials are reported at once instead of after a timeout.
wait_ready() {
  local deadline=$((SECONDS + READY_TIMEOUT))
  local base="http://127.0.0.1:${PORT}"
  local result="000"
  while [ "$SECONDS" -lt "$deadline" ]; do
    # Credentials go through stdin (-K -) so they do not appear in the process list.
    # Timeouts keep one stuck address from holding the script longer than the stated limit.
    result=$(printf 'url = "%s/login"\ndata-urlencode = "login=%s"\ndata-urlencode = "password=%s"\n' \
        "$base" "$LOGIN_NAME" "$PASSWORD" |
      curl -s -o /dev/null -w '%{http_code} %{redirect_url}' --connect-timeout 2 --max-time 3 -X POST -K - || true)
    case "$result" in
      "303 ${base}/") return 0 ;;
      "303 ${base}/login?error=1") LAST_CODE="$result"; return 2 ;;
    esac
    sleep 1
  done
  LAST_CODE="$result"
  return 1
}

# Reports the outcome of wait_ready and exits on failure.
check_ready() {
  local status=0
  LAST_CODE="000"
  wait_ready || status=$?
  case "$status" in
    0) ;;
    2) die "Вход отклонён: логин или пароль в .env не совпадают с контейнером. Ответ: ${LAST_CODE}" 1 ;;
    *) die "Контейнер не отвечает за ${READY_TIMEOUT} секунд (последний ответ: ${LAST_CODE}). 000 — порт не отвечает. Логи: docker compose logs matrix-ui" 1 ;;
  esac
}

mkdir -p "$DIR"
cd "$DIR"

# ── update: move to a newer image; a failed pull leaves the running container untouched ──

if [ "$MODE" = "update" ]; then
  [ -f .env ] && [ -f compose.yaml ] || die "В $DIR нечего обновлять: сначала выполни установку." 1
  check_env_file

  # A lock directory stops two cron runs from overlapping. mkdir is atomic and works on macOS too.
  if ! mkdir .update.lock 2>/dev/null; then
    die "Обновление уже выполняется (если это не так — удали $DIR/.update.lock)." 1
  fi
  trap 'rmdir .update.lock 2>/dev/null || true' EXIT

  OLD_VERSION=$(read_env_value HDMI_UI_VERSION)
  NEW_VERSION=$(resolve_version)
  if [ "$NEW_VERSION" != "$OLD_VERSION" ]; then
    set_env_value HDMI_UI_VERSION "$NEW_VERSION"
  fi

  echo "Скачиваю образ ghcr.io/mr-addams/hdmi-matrix-web-controller:${NEW_VERSION}…"
  if ! docker compose pull; then
    # Compose reads the image tag from .env, so put the old version back: the next run then
    # starts from a consistent state and the running container is left as it was.
    if [ "$NEW_VERSION" != "$OLD_VERSION" ] && [ -n "$OLD_VERSION" ]; then
      set_env_value HDMI_UI_VERSION "$OLD_VERSION"
    fi
    die "Не удалось скачать образ версии ${NEW_VERSION}. Контейнер не тронут." 1
  fi
  # up -d recreates the container only when the image or the configuration changed.
  docker compose up -d || die "Не удалось перезапустить контейнер. Логи: docker compose logs" 1
  check_ready

  if [ "$NEW_VERSION" = "$OLD_VERSION" ]; then
    echo "Уже актуально: версия ${NEW_VERSION}."
  else
    echo "Обновлено: ${OLD_VERSION:-неизвестно} → ${NEW_VERSION}."
  fi
  exit 0
fi

# ── install ──

if [ ! -f compose.yaml ]; then
  echo "Скачиваю compose.yaml…"
  # Download to a temporary name so an interrupted transfer never leaves a partial compose.yaml.
  curl -fsSL --max-time 30 -o compose.yaml.part "$REPO_RAW/compose.yaml" \
    || die "Не удалось скачать compose.yaml с GitHub" 1
  grep -q '^services:' compose.yaml.part || die "Скачанный compose.yaml не похож на compose-файл" 1
  mv compose.yaml.part compose.yaml
fi

if [ -f .env ]; then
  if [ "$ARGS_GIVEN" -eq 1 ]; then
    echo "Параметры --port, --bind, --user и --password проигнорированы: .env уже есть."
  fi
  check_env_file
  if [ "$VERSION_GIVEN" -eq 1 ] || ! grep -q '^HDMI_UI_VERSION=' .env; then
    set_env_value HDMI_UI_VERSION "$(resolve_version)"
  fi
  echo ".env уже есть — настройки сохранены."
else
  valid_login "$LOGIN_NAME" || die "Логин: только латиница, цифры, точка, подчёркивание и дефис, до 32 знаков: $LOGIN_NAME"
  valid_port "$PORT" || die "Порт должен быть числом 1–65535: $PORT"
  valid_ipv4 "$BIND" || die "Адрес публикации должен быть IPv4: $BIND"

  if [ -z "$PASSWORD" ]; then
    # od instead of openssl: od ships with every Linux and macOS system, no extra dependency.
    PASSWORD=$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
    GENERATED=1
  else
    valid_password "$PASSWORD" || die "Пароль не должен содержать пробелы, #, \$, кавычки, обратный слэш и управляющие символы."
  fi
  VERSION_RESOLVED=$(resolve_version)

  # Set umask before writing: .env holds the password and must not be readable by others.
  (
    umask 077
    cat > .env <<EOT
UI_USER=$LOGIN_NAME
UI_PASSWORD=$PASSWORD
MATRIX_UI_PORT=$PORT
MATRIX_UI_BIND=$BIND
HDMI_UI_VERSION=$VERSION_RESOLVED
EOT
  )
  echo ".env создан."
fi

IMAGE_VERSION=$(read_env_value HDMI_UI_VERSION)
IMAGE_VERSION="${IMAGE_VERSION:-latest}"
echo "Скачиваю образ ghcr.io/mr-addams/hdmi-matrix-web-controller:${IMAGE_VERSION}…"
docker compose pull || die "Не удалось скачать образ версии ${IMAGE_VERSION}. Проверь версию и доступ к ghcr.io" 1
docker compose up -d || die "Не удалось запустить контейнер. Логи: docker compose logs" 1

check_ready

echo
echo "Готово: http://127.0.0.1:${PORT}"
echo "Логин: ${LOGIN_NAME}"
echo "Версия образа: ${IMAGE_VERSION}"
if [ "$GENERATED" -eq 1 ]; then
  echo "Пароль (сгенерирован, сохранён в .env): ${PASSWORD}"
fi
if [ "$BIND" = "0.0.0.0" ]; then
  echo "Внимание: страница доступна всей локальной сети. Матрицы без аутентификации — см. SECURITY.md."
fi
echo "Обновить: ./install.sh update (в этой папке) или ./install.sh update --version X.Y.Z"
