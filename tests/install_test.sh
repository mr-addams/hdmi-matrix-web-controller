#!/usr/bin/env bash
# Тесты install.sh без Docker и сети: docker и curl подменяются заглушками через PATH.
# Каждый случай работает в отдельной копии скрипта, поэтому настоящий .env репозитория не трогаем.
# Запуск: bash tests/install_test.sh
set -uo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK_DIR=$(mktemp -d)
trap 'rm -rf "$WORK_DIR"' EXIT

PASSED=0
FAILED=0

pass() {
  PASSED=$((PASSED + 1))
  echo "ok   - $1"
}

fail() {
  FAILED=$((FAILED + 1))
  echo "FAIL - $1"
  echo "       вывод: $OUT"
}

# Заглушка docker пишет вызовы в лог; FAKE_DOCKER_EXIT имитирует падение compose.
# Заглушка curl читает конфиг из stdin (так его видит и настоящий скрипт), отдаёт FAKE_HTTP_CODE.
make_case() {
  CASE_DIR=$(mktemp -d "$WORK_DIR/case.XXXX")
  cp "$REPO_DIR/install.sh" "$CASE_DIR/install.sh"
  touch "$CASE_DIR/compose.yaml"
  mkdir -p "$CASE_DIR/bin"
  cat > "$CASE_DIR/bin/docker" <<'EOF'
#!/usr/bin/env bash
echo "docker $*" >> "$STUB_LOG"
exit "${FAKE_DOCKER_EXIT:-0}"
EOF
  cat > "$CASE_DIR/bin/curl" <<'EOF'
#!/usr/bin/env bash
echo "curl $*" >> "$STUB_LOG"
config=$(cat)
echo "curl-config: $config" >> "$STUB_LOG"
# Вход в установщике проверяет редирект: по умолчанию имитируем успешный вход на главную.
if [ -n "${FAKE_HTTP_CODE:-}" ]; then printf '%s' "$FAKE_HTTP_CODE"; exit 0; fi
base=$(printf '%s\n' "$config" | sed -n 's|^url = "\(.*\)/login"$|\1|p')
printf '303 %s/' "$base"
EOF
  chmod +x "$CASE_DIR/bin/docker" "$CASE_DIR/bin/curl"
  STUB_LOG="$CASE_DIR/stub.log"
  : > "$STUB_LOG"
  export STUB_LOG
  export PATH="$CASE_DIR/bin:$PATH"
  export READY_TIMEOUT=2
  unset FAKE_HTTP_CODE FAKE_DOCKER_EXIT
}

# Запускает install.sh из копии случая; результат в OUT и RC.
run_install() {
  OUT=$(cd "$CASE_DIR" && bash ./install.sh "$@" 2>&1)
  RC=$?
}

expect_rc() {
  local want="$1" name="$2"
  if [ "$RC" -eq "$want" ]; then pass "$name"; else fail "$name: код $RC, ожидали $want"; fi
}

expect_output() {
  local needle="$1" name="$2"
  if grep -qF -- "$needle" <<<"$OUT"; then pass "$name"; else fail "$name: нет строки «$needle»"; fi
}

expect_no_output() {
  local needle="$1" name="$2"
  if grep -qF -- "$needle" <<<"$OUT"; then fail "$name: есть строка «$needle»"; else pass "$name"; fi
}

expect_log() {
  local needle="$1" name="$2"
  if grep -qF -- "$needle" "$STUB_LOG"; then pass "$name"; else fail "$name: нет в логе «$needle»"; fi
}

expect_no_log() {
  local needle="$1" name="$2"
  if grep -qF -- "$needle" "$STUB_LOG"; then fail "$name: есть в логе «$needle»"; else pass "$name"; fi
}

# ── Валидация аргументов: при любом неверном значении — код 2 и без вызова docker ──

make_case
run_install --port 99999999999999999999
expect_rc 2 "порт с переполнением отклоняется"
expect_no_log "compose up" "при неверном порте docker не запускается"

make_case; run_install --port 0;     expect_rc 2 "порт 0 отклоняется"
make_case; run_install --port 65536; expect_rc 2 "порт 65536 отклоняется"
make_case; run_install --port abc;   expect_rc 2 "порт не число отклоняется"
make_case; run_install --bind 999.1.1.1; expect_rc 2 "октет больше 255 в --bind отклоняется"
make_case; run_install --bind 1;     expect_rc 2 "--bind не IPv4 отклоняется"
make_case; run_install --user "ad min"; expect_rc 2 "логин с пробелом отклоняется"
make_case; run_install --user "a:b";  expect_rc 2 "логин с двоеточием отклоняется"
make_case; run_install --password "a b"; expect_rc 2 "пароль с пробелом отклоняется"
make_case; run_install --password 'a#b'; expect_rc 2 "пароль с # отклоняется"
make_case; run_install --port; expect_rc 2 "--port без значения отклоняется"
make_case; run_install --nope; expect_rc 2 "неизвестный параметр отклоняется"

make_case; run_install --help
expect_rc 0 "--help завершается успешно"
expect_output "Использование" "--help печатает справку"

# ── Новая установка ──

make_case
run_install --port 18080 --bind 0.0.0.0 --user ops --password s3cret
expect_rc 0 "установка с заданными параметрами проходит"
expect_log "docker compose up -d --build" "compose поднимается с пересборкой"
expect_output "Готово: http://127.0.0.1:18080" "печатается адрес"
expect_output "Внимание: страница доступна всей локальной сети" "при 0.0.0.0 есть предупреждение"
expect_no_output "Пароль (сгенерирован" "заданный пароль не печатается"
if [ "$(cat "$CASE_DIR/.env")" = "$(printf 'UI_USER=ops\nUI_PASSWORD=s3cret\nMATRIX_UI_PORT=18080\nMATRIX_UI_BIND=0.0.0.0')" ]; then
  pass ".env содержит заданные значения"
else
  fail ".env содержит не то: $(cat "$CASE_DIR/.env")"
fi
if [ "$(stat -c %a "$CASE_DIR/.env" 2>/dev/null || stat -f %Lp "$CASE_DIR/.env")" = "600" ]; then
  pass ".env создан с правами 600"
else
  fail ".env создан не с правами 600"
fi
if grep '^curl ' "$STUB_LOG" | grep -qF "s3cret"; then
  fail "пароль попал в аргументы curl"
else
  pass "пароль не попадает в аргументы curl"
fi
expect_log "data-urlencode = \"password=s3cret\"" "пароль передаётся curl через stdin"

make_case
run_install
expect_rc 0 "установка без параметров проходит"
if grep -qE '^UI_PASSWORD=[0-9a-f]{24}$' "$CASE_DIR/.env"; then
  pass "пароль сгенерирован: 24 hex-символа"
else
  fail "сгенерированный пароль не похож на ожидаемый"
fi
expect_output "Пароль (сгенерирован, сохранён в .env):" "сгенерированный пароль печатается один раз"
expect_output "Логин: admin" "логин по умолчанию admin"

# ── Ошибки контейнера ──

make_case
FAKE_HTTP_CODE="303 http://127.0.0.1:8080/login?error=1" run_install --password s3cret
expect_rc 1 "неверный пароль в контейнере даёт код 1"
expect_output "Вход отклонён" "неверный пароль назван отказом входа"

make_case
FAKE_DOCKER_EXIT=1 run_install --password s3cret
if [ "$RC" -ne 0 ]; then pass "падение compose останавливает установку"; else fail "падение compose не остановило установку"; fi

# ── Существующий .env ──

make_case
printf 'UI_USER=admin\nUI_PASSWORD=keepme\nMATRIX_UI_PORT=8080\nMATRIX_UI_BIND=127.0.0.1\n' > "$CASE_DIR/.env"
cp "$CASE_DIR/.env" "$CASE_DIR/env.before"
run_install
expect_rc 0 "существующий корректный .env принимается"
expect_output "уже есть" "существующий .env не перезаписывается"
if cmp -s "$CASE_DIR/.env" "$CASE_DIR/env.before"; then pass ".env не изменён"; else fail ".env изменился"; fi

make_case
printf 'UI_USER=admin\nUI_PASSWORD=one\nUI_PASSWORD=two\nMATRIX_UI_PORT=8080\nMATRIX_UI_BIND=127.0.0.1\n' > "$CASE_DIR/.env"
run_install
expect_rc 2 "дублирующийся ключ в .env отклоняется"
expect_no_log "compose up" "при битом .env docker не запускается"

make_case
printf 'UI_USER=admin\nUI_PASSWORD="quoted"\nMATRIX_UI_PORT=8080\nMATRIX_UI_BIND=127.0.0.1\n' > "$CASE_DIR/.env"
run_install
expect_rc 2 "пароль в кавычках в .env отклоняется"

make_case
printf 'UI_USER=admin\r\nUI_PASSWORD=abc\r\nMATRIX_UI_PORT=8080\r\nMATRIX_UI_BIND=127.0.0.1\r\n' > "$CASE_DIR/.env"
run_install
expect_rc 2 ".env с CRLF отклоняется"

make_case
printf 'UI_USER=admin\nUI_PASSWORD=\nMATRIX_UI_PORT=8080\nMATRIX_UI_BIND=127.0.0.1\n' > "$CASE_DIR/.env"
run_install
expect_rc 2 "пустой пароль в .env отклоняется"

echo
echo "Пройдено: $PASSED, провалено: $FAILED"
[ "$FAILED" -eq 0 ]
