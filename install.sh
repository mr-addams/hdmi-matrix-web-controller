#!/usr/bin/env bash
# Установка страницы управления в Docker: создаёт .env, собирает образ и поднимает контейнер.
# Скрипт сам переходит в свой каталог, где лежат compose.yaml и .env, поэтому запускать
# его можно откуда угодно. Повторный запуск безопасен: существующий .env не перезаписываем,
# иначе сменился бы пароль и текущие пользователи страницы перестали бы входить.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

# Сколько ждём ответа контейнера с логином. Переменная нужна тестам, чтобы не ждать 30 секунд.
READY_TIMEOUT="${READY_TIMEOUT:-30}"

PORT=8080
BIND=127.0.0.1
LOGIN_NAME="admin"
PASSWORD=""
ARGS_GIVEN=0
GENERATED=0

usage() {
  cat <<'EOF'
Использование: ./install.sh [--port N] [--bind ADDR] [--user NAME] [--password PW]

  --port      порт на хосте (по умолчанию 8080)
  --bind      адрес публикации: 127.0.0.1 — только этот хост, 0.0.0.0 — вся локальная сеть
  --user      логин для входа на страницу (по умолчанию admin)
  --password  пароль; если не задан, генерируется случайный и выводится один раз

Параметры применяются только при создании .env. Чтобы пересоздать его, удали .env.
EOF
}

die() {
  echo "$1" >&2
  exit "${2:-2}"
}

# ── Валидация: одни и те же правила для аргументов и для уже существующего .env ──

valid_port() {
  # Длину проверяем до арифметики: переполнение целого молча проходило бы проверку диапазона.
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

# Пароль попадает в .env без кавычек и в curl-конфиг в кавычках. Пробелы, #, кавычки,
# $ и обратный слэш сломали бы один из этих разборов, управляющие символы — оба.
valid_password() {
  [ -n "$1" ] || return 1
  case "$1" in
    *[[:space:]]*|*[[:cntrl:]]*|*'#'*|*'"'*|*"'"*|*'$'*|*\\*) return 1 ;;
  esac
}

# Ключ обязан встречаться в .env ровно один раз: compose и наш разбор должны читать одно и то же.
check_env_file() {
  local key
  for key in UI_USER UI_PASSWORD MATRIX_UI_PORT MATRIX_UI_BIND; do
    if [ "$(grep -c "^${key}=" .env || true)" != "1" ]; then
      die "В .env ключ $key должен встречаться ровно один раз. Исправь .env или удали его."
    fi
  done
  LOGIN_NAME=$(sed -n 's/^UI_USER=//p' .env)
  PASSWORD=$(sed -n 's/^UI_PASSWORD=//p' .env)
  PORT=$(sed -n 's/^MATRIX_UI_PORT=//p' .env)
  BIND=$(sed -n 's/^MATRIX_UI_BIND=//p' .env)
  valid_login "$LOGIN_NAME" || die "В .env некорректный UI_USER: $LOGIN_NAME"
  valid_password "$PASSWORD" || die "В .env некорректный UI_PASSWORD (пустой или с запрещёнными символами)"
  valid_port "$PORT" || die "В .env некорректный MATRIX_UI_PORT: $PORT"
  valid_ipv4 "$BIND" || die "В .env некорректный MATRIX_UI_BIND: $BIND"
}

# ── Разбор аргументов ──

need_value() {
  [ $# -ge 2 ] || die "$1 требует значение"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --port)     need_value "$@"; PORT="$2"; ARGS_GIVEN=1; shift 2 ;;
    --bind)     need_value "$@"; BIND="$2"; ARGS_GIVEN=1; shift 2 ;;
    --user)     need_value "$@"; LOGIN_NAME="$2"; ARGS_GIVEN=1; shift 2 ;;
    --password) need_value "$@"; PASSWORD="$2"; ARGS_GIVEN=1; shift 2 ;;
    -h|--help)  usage; exit 0 ;;
    *) echo "Неизвестный параметр: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if ! command -v docker >/dev/null 2>&1; then
  die "Docker не найден. Установи Docker Engine с плагином Compose." 1
fi
if ! docker compose version >/dev/null 2>&1; then
  die "Нет плагина docker compose. Установи docker-compose-plugin." 1
fi
[ -f compose.yaml ] || die "compose.yaml не найден рядом со скриптом." 1

if [ -f .env ]; then
  if [ "$ARGS_GIVEN" -eq 1 ]; then
    echo "Параметры командной строки проигнорированы: .env уже есть."
  fi
  check_env_file
  echo ".env уже есть — используем его без изменений."
else
  valid_login "$LOGIN_NAME" || die "Логин: только латиница, цифры, точка, подчёркивание и дефис, до 32 знаков: $LOGIN_NAME"
  valid_port "$PORT" || die "Порт должен быть числом 1–65535: $PORT"
  valid_ipv4 "$BIND" || die "Адрес публикации должен быть IPv4: $BIND"

  if [ -z "$PASSWORD" ]; then
    # od вместо openssl: utility есть в любом Linux и macOS, без зависимостей.
    PASSWORD=$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
    GENERATED=1
  else
    valid_password "$PASSWORD" || die "Пароль не должен содержать пробелы, #, \$, кавычки, обратный слэш и управляющие символы."
  fi

  # umask до записи: в .env лежит пароль, файл не должен быть читаем другим пользователям.
  (
    umask 077
    cat > .env <<EOF
UI_USER=$LOGIN_NAME
UI_PASSWORD=$PASSWORD
MATRIX_UI_PORT=$PORT
MATRIX_UI_BIND=$BIND
EOF
  )
  echo ".env создан."
fi

docker compose up -d --build

# Контейнер поднимается не мгновенно. Проверяем вход тем же путём, что и браузер: форму /login.
# Успех — редирект на главную, неверный пароль — редирект на /login?error=1. Это ответ, а не таймаут,
# поэтому неверные данные сообщаем сразу, без ожидания.
wait_ready() {
  local deadline=$((SECONDS + READY_TIMEOUT))
  local base="http://127.0.0.1:${PORT}"
  local result="000"
  while [ "$SECONDS" -lt "$deadline" ]; do
    # Логин и пароль передаём через stdin (-K -): в списке процессов их не видно.
    # Таймауты нужны: без них один зависший адрес держал бы скрипт дольше заявленного срока.
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

LAST_CODE="000"
status=0
wait_ready || status=$?
case "$status" in
  0) ;;
  2) die "Вход отклонён: логин или пароль в .env не совпадают с контейнером. Ответ: ${LAST_CODE}" 1 ;;
  *) die "Контейнер не отвечает за ${READY_TIMEOUT} секунд (последний ответ: ${LAST_CODE}). 000 — порт не отвечает. Логи: docker compose logs matrix-ui" 1 ;;
esac

echo
echo "Готово: http://127.0.0.1:${PORT}"
echo "Логин: ${LOGIN_NAME}"
if [ "$GENERATED" -eq 1 ]; then
  echo "Пароль (сгенерирован, сохранён в .env): ${PASSWORD}"
fi
if [ "$BIND" = "0.0.0.0" ]; then
  echo "Внимание: страница доступна всей локальной сети. Матрицы без аутентификации — см. SECURITY.md."
fi
