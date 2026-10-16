#!/usr/bin/env bash
# Проверки перехода на форк: сначала сам стенд, потом нода, потом панель.
# Файл подключается из test-install-fork.sh; по отдельности не запускается.
#
# Каждая проверка ставит свой стенд (hf_setup) и гоняет на нём НАСТОЯЩИЙ
# scripts/install-fork.sh — с заглушками docker/curl/systemctl/openssl и в mktemp-каталоге.
set -uo pipefail

# ─────────────────────────── стенд: заглушки верны ───────────────────────────
# Если сломан стенд, «тест прошёл» ничего не значит — он проверял не там.
test_00_harness() {
  local bad=0
  hf_setup node || return 1
  hf_has_service node && pass "стенд: compose.yaml ноды читается" || { fail "стенд: compose.yaml ноды не читается"; bad=1; }
  if hf_has_service postgres; then fail "стенд: на ноде вдруг есть postgres"; bad=1; else pass "стенд: postgres на ноде нет"; fi
  hf_set_stack up
  if "$HF_FAKEBIN/docker" compose --project-directory "$HF_MIKAN" exec -T postgres pg_isready -q >/dev/null 2>&1; then
    fail "стенд: postgres отвечает на ноде"
    bad=1
  else
    pass "стенд: postgres не отвечает (как и должен)"
  fi
  if "$HF_FAKEBIN/docker" compose --project-directory "$HF_MIKAN" up -d postgres >/dev/null 2>&1; then
    fail "стенд: up postgres на ноде не падает"
    bad=1
  else
    pass "стенд: up postgres на ноде падает (как docker)"
  fi
  assert_eq "$(hf_sha256 "$HF_RELEASE/cmd.sh")" "$(sed -n 's/.*"sha256":"\([0-9a-f]\{64\}\)".*/\1/p' "$HF_RELEASE/manifest.json")" \
    "стенд: sha256 команды совпадает с манифестом" || bad=1
  if "$HF_FAKEBIN/docker" run --rm --entrypoint mikan "$HF_IMAGE" version 2>/dev/null | grep -qx "$HF_VERSION"; then
    pass "стенд: образ форка отвечает своей версией"
  else
    fail "стенд: образ форка не отвечает версией"
    bad=1
  fi
  assert_eq "$("$HF_FAKEBIN/curl" -fsSL --proto '=https' https://example.invalid/arikan-x86_64 | head -c 2)" "#!" \
    "стенд: curl отдаёт файл команды" || bad=1
  return "$bad"
}

# ─────────────────────────────── нода: полный проход ───────────────────────────────
# Тот самый случай со скриншота: нода стока, у которой нет ни панели, ни postgres.
test_node_full_pass() {
  hf_setup node
  hf_run
  assert_rc 0 "переход на ноде проходит" || { hf_log; return 1; }
  assert_contains "$HF_OUT" "сервер: нода" "скрипт понял, что сервер — нода" || { hf_log; return 1; }
  assert_not_contains "$HF_OUT" "postgres" "на ноде о postgres не спрашивают" || { hf_log; return 1; }
  assert_contains "$HF_OUT" "Готово" "скрипт дошёл до конца" || { hf_log; return 1; }
  assert_eq "$(hf_env MIKAN_IMAGE)" "$HF_IMAGE" "MIKAN_IMAGE переключён на образ форка" || { hf_log; return 1; }
  assert_eq "$(hf_env MIKAN_VERSION)" "$HF_VERSION" "MIKAN_VERSION — версия форка" || { hf_log; return 1; }
  assert_eq "$(hf_env MIKAN_MODE)" "node" "роль в .env осталась нодой" || { hf_log; return 1; }
  assert_eq "$(hf_env MIKAN_NODE_JOIN)" "joinkey" "ключ присоединения ноды не потерян" || { hf_log; return 1; }
  grep -q "arikan-node" "$HF_MIKAN/compose.yaml" || { fail "compose.yaml ноды не тот"; hf_log; return 1; }
  grep -q "postgres" "$HF_MIKAN/compose.yaml" && { fail "в compose.yaml ноды появился postgres"; hf_log; return 1; } ||
    pass "в compose.yaml ноды нет postgres"
  local dump
  dump=$(ls "$HF_MIKAN/data/node/backup"/backup-*-before-fork.tar.gz 2>/dev/null | head -n1)
  assert_file "$dump" "копия данных ноды снята до перехода" || { hf_log; return 1; }
  tar -tzf "$dump" 2>/dev/null | grep -q "data/node/.env" &&
    pass "в копии есть .env ноды (откат вернёт и его)" || { fail "в копии нет data/node/.env"; hf_log; return 1; }
  local listed
  listed=$(tar -tzf "$dump" 2>/dev/null | grep -c "before-fork" || true)
  assert_eq "$listed" "0" "в копии нет самих копий (архив не собирает себя)" || { hf_log; return 1; }
  assert_file "$HF_BIN/arikan" "команда форка стоит" || { hf_log; return 1; }
  assert_file "$HF_BIN/mikan.stock" "стоковая команда сохранена" || { hf_log; return 1; }
  assert_file "$HF_BIN/mikan" "имя mikan на месте" || { hf_log; return 1; }
  assert_file "$HF_SYSTEMD/arikan-update.timer" "юнит форка включён" || { hf_log; return 1; }
  assert_not_file "$HF_SYSTEMD/mikan-update.timer" "стоковый юнит выключен" || { hf_log; return 1; }
  return 0
}

# ─────────────────── нода: к базе и её таблицам не обращаются ───────────────────
# Прежний скрипт падал именно здесь: поднимал postgres (его в compose.yaml ноды нет),
# не мог прочитать схему и останавливался, ничего не сделав.
test_node_never_reads_a_database() {
  hf_setup node
  hf_run
  assert_rc 0 "нода переходит на форк" || { hf_log; return 1; }
  assert_not_contains "$HF_OUT" "база недоступна" "нода не упирается в недоступную базу" || { hf_log; return 1; }
  assert_not_contains "$HF_OUT" "  pg_dump … " "pg_dump на ноде не запускается" || { hf_log; return 1; }
  assert_not_contains "$HF_OUT" "referral_" "таблиц форка на ноде не ищут" || { hf_log; return 1; }
  assert_not_contains "$HF_OUT" "goose_db_version" "схему на ноде не читают" || { hf_log; return 1; }
  # Ни одной команды к службам панели и базы: их на ноде нет.
  if grep -qE "compose .* (exec|run|stop)( |$).*\b(panel|postgres|database)\b" "$HF_LOG/docker.log"; then
    fail "скрипт обращался к службам панели/базы на ноде"
    grep -nE "compose .* (exec|run|stop)( |$).*\b(panel|postgres|database)\b" "$HF_LOG/docker.log" | sed 's/^/    /' >&2
    hf_log
    return 1
  fi
  pass "ни одной команды к panel/postgres"
  return 0
}

# ─────────────────────── повторный запуск ничего не портит ───────────────────────
test_node_second_run() {
  hf_setup node
  hf_run || { hf_log; return 1; }
  local backup1
  backup1=$(ls "$HF_MIKAN/data/node/backup"/backup-*-before-fork.tar.gz | head -n1)
  hf_run || { hf_log; return 1; }
  assert_rc 0 "повторный запуск проходит" || { hf_log; return 1; }
  assert_contains "$HF_OUT" "скрипт уже запускался" "повторный запуск помнит прежний образ" || { hf_log; return 1; }
  assert_eq "$(ls "$HF_MIKAN/data/node/backup"/backup-*-before-fork.tar.gz | tr '\n' ' ')" "$backup1 " \
    "копия до перехода не переснята" || { hf_log; return 1; }
  assert_eq "$(hf_env MIKAN_IMAGE)" "$HF_IMAGE" "образ всё ещё форка" || { hf_log; return 1; }
  assert_file "$HF_BIN/mikan.stock" "стоковая команда не подменена сборкой форка" || { hf_log; return 1; }
  return 0
}

# ───────────────────── откат ноды: копия данных возвращается ─────────────────────
test_node_rollback() {
  hf_setup node
  # Данные, которые нода накопила: их и должен вернуть откат.
  printf 'connection\n' >"$HF_MIKAN/data/node/after-upgrade.txt"
  hf_run || { hf_log; return 1; }
  assert_rc 0 "переход проходит" || { hf_log; return 1; }
  local dump
  dump=$(ls "$HF_MIKAN/data/node/backup"/backup-*-before-fork.tar.gz | head -n1)
  [ -f "$dump" ] || { fail "копии нет"; hf_log; return 1; }
  # То, что нода записала после копии, в архиве отсутствует: значит откат вернёт копию.
  hf_run --rollback "$dump" --yes
  assert_rc 0 "откат ноды проходит" || { hf_log; return 1; }
  assert_contains "$HF_OUT" "Нода на обычном mikan" "откат говорит, что нода вернулась" || { hf_log; return 1; }
  assert_eq "$(hf_env MIKAN_IMAGE)" "$HF_STOCK_IMAGE" "образ снова стоковый" || { hf_log; return 1; }
  assert_eq "$(hf_env MIKAN_MODE)" "node" "роль из копии .env — нода" || { hf_log; return 1; }
  assert_eq "$(hf_env MIKAN_NODE_JOIN)" "joinkey" "ключ из копии .env вернулся" || { hf_log; return 1; }
  assert_file "$HF_BIN/mikan" "стоковая команда вернулась на своё имя" || { hf_log; return 1; }
  assert_not_file "$HF_BIN/arikan" "команда форка снята" || { hf_log; return 1; }
  return 0
}

# ─────────────────────────── панель: прежнее поведение ───────────────────────────
test_panel_unchanged() {
  hf_setup panel
  hf_run || { hf_log; return 1; }
  assert_rc 0 "переход на панели проходит" || { hf_log; return 1; }
  assert_contains "$HF_OUT" "сервер: панель" "скрипт видит панель" || { hf_log; return 1; }
  assert_contains "$HF_OUT" "схема PostgreSQL" "панель читает схему базы" || { hf_log; return 1; }
  assert_contains "$HF_OUT" "копия базы" "копия базы снята" || { hf_log; return 1; }
  assert_contains "$HF_OUT" "пять таблиц referral_* созданы" "таблицы форка найдены" || { hf_log; return 1; }
  assert_eq "$(hf_env MIKAN_IMAGE)" "$HF_IMAGE" "образ переключён" || { hf_log; return 1; }
  return 0
}

# ─────────────────── проверка не пропускает панель без миграций ───────────────────
test_panel_verification_catches_missing_tables() {
  hf_setup panel
  # Панель поднялась, но свои миграции почему-то не применила: проверка обязана это заметить.
  # HF_REFERRAL_AFTER_UP=0 отменяет обычное «панель форка накатывает 0032–0035 на старте».
  HF_REFERRAL_AFTER_UP=0 hf_run
  assert_rc 1 "панель без таблиц форка не признаётся удачной" || { hf_log; return 1; }
  assert_contains "$HF_OUT" "откатываюсь" "скрипт откатывается" || { hf_log; return 1; }
  assert_eq "$(hf_env MIKAN_IMAGE)" "$HF_STOCK_IMAGE" "образ вернулся к стоковому" || { hf_log; return 1; }
  return 0
}

# ─────────────────────────── упавший post-update ───────────────────────────
# Команда форка не смогла поставить свои файлы: скрипт говорит об этом и не делает вид,
# что всё в порядке.
test_node_post_update_failure_is_reported() {
  hf_setup node
  HF_POST_UPDATE=fail hf_run
  assert_contains "$HF_OUT" "post-update не прошёл" "про упавший post-update сказано" || { hf_log; return 1; }
  return 0
}

# ─────────────────── compose без службы, которую поднимают ───────────────────
# post-update не поставил compose форка, а на сервере лежит чужой файл: перезапускать нечего,
# и об этом надо сказать ДО перезапуска, а не упасть на «no such service». Так и выглядел
# прошлый сбой: файл знал только про panel, а скрипт просил postgres.
test_node_foreign_compose_is_named_before_the_restart() {
  hf_setup node
  cat >"$HF_MIKAN/compose.yaml" <<'YAML'
name: mikan

services:
  panel:
    image: ${MIKAN_IMAGE}
YAML
  HF_POST_UPDATE=nothing hf_run
  assert_rc 1 "чужой compose не перезапускается вслепую" || { hf_log; return 1; }
  assert_contains "$HF_OUT" "нет службы node" "сказано, какой именно службы нет" || { hf_log; return 1; }
  assert_not_contains "$HF_OUT" "перезапустить стек" "перезапуск не начат" || { hf_log; return 1; }
  assert_contains "$HF_OUT" "вернуть прежний" "сказано, как вернуть прежний образ" || { hf_log; return 1; }
  return 0
}

# ─────────────────── dry-run ничего не меняет на ноде ───────────────────
test_node_dry_run_changes_nothing() {
  hf_setup node
  local before
  before=$(cat "$HF_MIKAN/.env")
  hf_run --dry-run
  assert_rc 0 "пробный запуск проходит" || { hf_log; return 1; }
  assert_eq "$(cat "$HF_MIKAN/.env")" "$before" ".env не тронут" || { hf_log; return 1; }
  assert_not_file "$HF_BIN/arikan" "команда форка не поставлена" || { hf_log; return 1; }
  local copies
  copies=$(find "$HF_MIKAN/data/node/backup" -name 'backup-*' 2>/dev/null | wc -l | tr -d ' ')
  assert_eq "$copies" "0" "копия не делалась" || { hf_log; return 1; }
  return 0
}

# ─────────────────── роль берётся из compose.yaml, когда .env молчит ───────────────────
test_role_falls_back_to_compose() {
  hf_setup node
  sed -i '/^MIKAN_MODE=/d' "$HF_MIKAN/.env"
  hf_run
  assert_rc 0 "нода переходит и без MIKAN_MODE в .env" || { hf_log; return 1; }
  assert_not_contains "$HF_OUT" "база недоступна" "о базе всё равно не спрашивают" || { hf_log; return 1; }
  return 0
}

# ─────────────────── пустой .env без роли и без панельного compose ───────────────────
# Худший случай: роль неизвестна, compose пустой. Скрипт не должен ничего ломать —
# он говорит, что не понимает файл, и выходит.
test_unknown_compose_is_refused() {
  hf_setup node
  sed -i '/^MIKAN_MODE=/d' "$HF_MIKAN/.env"
  printf 'name: mikan\n' >"$HF_MIKAN/compose.yaml"
  hf_run
  assert_rc 1 "чужой compose без служб не принимается" || { hf_log; return 1; }
  assert_eq "$(hf_env MIKAN_IMAGE)" "$HF_STOCK_IMAGE" "образ не переключён" || { hf_log; return 1; }
  return 0
}
