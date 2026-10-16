#!/usr/bin/env bash
#
# Arikan: одна команда вместо прежней, переход с обычного mikan на этот форк без потерь.
#
#   curl -fsSL https://raw.githubusercontent.com/o0hhs/arikan/main/scripts/install-fork.sh \
#     | sudo bash
#
# Без ключей берётся свежий стабильный релиз форка: указатель релизов (index.json на
# пре-релизе updates) и манифест проверяются подписью ключа форка, и уже оттуда берутся
# образ (пином по digest), версия и команда для хоста. Ключ --image (или FORK_IMAGE=...)
# называет образ сам — для беты, локальной сборки или зеркала.
#
# Что он делает и почему так:
#
#   * Переносить данные не нужно. Форк добавляет четыре миграции — 0032_referrals.sql
#     (пять таблиц), 0033_campaigns.sql (три), 0034_node_clients.sql и 0035_node_turn.sql
#     (по одной). Все ПУСТЫЕ: ни одна существующая строка не переписывается. Панель на
#     старте сама применяет недостающие миграции (serve.go → store.OpenPostgres →
#     prepareSchema → migrateUp). Поэтому здесь нет «переливки данных»: есть безопасное
#     переключение образа с резервной копией.
#
#   * Апдейтер на хосте переходит форку. У стокового mikan это команда
#     /usr/local/bin/mikan и юниты mikan-update.*, которые пускают её по ночному таймеру и
#     по кнопке «Обновить» в панели; у свежих сборок форка — /usr/local/bin/arikan и юниты
#     arikan-update.*, а у сборок до переименования (как 0.5.1.1) — /usr/local/bin/mikan и
#     те же mikan-update.*: команда форка ставится под тем именем, каким себя называет.
#     Если стоковое оставить, панель форка обновляет апстрим: его команда ходит за релизами
#     Miroshka000/mikan и однажды положит обычный mikan поверх форка.
#     Поэтому скрипт ставит команду форка, её юниты — и лишь тогда выключает стоковые,
#     оставляя их на месте: стоковую команду в /usr/local/bin/mikan.stock, файлы юнитов —
#     как есть. Откат возвращает их обратно. Обратной стороной это и работает: без команды
#     форка кнопка «Обновить» в панели форка достаётся стоковому апдейтеру.
#
#   * Порядок отката противоречит интуиции: образ переключается на обычный mikan ПЕРЕД
#     возвратом базы. «database restore» после загрузки копии поднимает схему до версии
#     бинаря, который её выполняет (internal/panel/store/restore.go, RestorePostgres →
#     migrateUp). Вернуть базу бинарём форка — значит тут же накатить 0032–0035 обратно, и
#     обычный mikan откажется стартовать («schema 32 is newer than this binary supports»).
#
#   * Автообновление больше не опасно — теперь по-настоящему: команда и юниты на хосте
#     тоже форка, а адрес релизов форка (internal/release/release.go, Repo = o0hhs/arikan)
#     и ключ подписи у него свои. Команду скрипт ставит до переключения образа и
#     останавливается, если не смог: с чужим апдейтером сервер не оставляют.
#     Версию форка скрипт сверяет и предупреждает, если она не новее стоящей.
#     Ключ --disable-auto-update остался для тех, у кого образ форка старой сборки, с
#     апстримным адресом релизов.
#
#   * Отдельный установщик с нуля не переписывается: для чистой установки сначала пройди
#     штатный install.sh (он ставит Docker, файрвол, compose и саму панель), потом запусти
#     эту команду ещё раз — она переключит уже установленный mikan на форк.
#
#   * Сервер-Нода — это не панель, и это здесь отдельная ветка. У ноды нет ни панели, ни
#     postgres, ни даже панельного compose.yaml: только служба node (скрипт смотрит роль в
#     .env: MIKAN_MODE=node, как её пишут оба форка и сток; если строки нет, решает и то,
#     какая служба в compose.yaml). Поэтому на ноде:
#       — контейнеры читаются и поднимаются службой node, а не panel и не postgres;
#       — резервная копия — это не pg_dump, а архив её собственных данных (data/node:
#         ключи, настройки, соединения) в $MIKAN_DIR/data/node/backup/;
#       — «схема базы» не читается и на ≥32 не проверяется: таблицы referral_* и
#         campaign_* создаёт панель на своей стороне, на ноде их нет и быть не должно;
#       — обновления ноды ведёт панель через её API (обновления нод — на странице Nodes);
#         скрипт ставит только стоковую автоматику и не трогает панельную.
#     Раньше скрипт про роль не знал: на ноде он поднимал postgres (его в её compose.yaml
#     нет), не мог прочитать схему и падал на шаге 2/9 — перейти со стока на форк на ноде
#     было нельзя вовсе.
#
#   * Панель и нода — это .env-решение панели: админ переводит ноду на форк.
#
# Ключи ноды: --node-panel-address IP (или NODE_PANEL_ADDRESS=...) пригодится там, где у
# панели несколько адресов и нода сама не угадывает нужный.

set -Eeuo pipefail

MIKAN_DIR=${MIKAN_DIR:-/opt/mikan}

# Почему всё перезапускается: переданный по пайпу скрипт (curl … | sudo bash -s -- …) bash
# читает из stdin, и любая команда внутри наследует этот же stdin. `docker compose exec`
# съедает остаток скрипта, и установщик молча останавливается на середине — с кодом 0, то
# есть как будто всё прошло. Поэтому скрипт, пришедший не с терминала, читается целиком
# один раз и запускается заново с закрытым stdin.
if [ "${MIKAN_FORK_REEXEC:-0}" != 1 ] && [ ! -t 0 ] && [ ! -f "$0" ]; then
  _self=$(mktemp) || exit 1
  cat >"$_self"
  # Пустой файл — значит stdin оказался не тем, чем казался: запускать нечего.
  if [ ! -s "$_self" ]; then
    rm -f "$_self"
    echo "install-fork: не удалось прочитать скрипт из stdin" >&2
    exit 1
  fi
  MIKAN_FORK_REEXEC=1 MIKAN_DIR="$MIKAN_DIR" FORK_IMAGE="${FORK_IMAGE:-}" bash "$_self" "$@" </dev/null
  _rc=$?
  rm -f "$_self"
  exit "$_rc"
fi

# REV печатается при каждом запуске: видно, какая именно копия скрипта сейчас выполняется
# (за прокси или кэшем curl на сервере может отдаться не та).
REV="2026-10-14.7"
TRACE=0
DIED=0
die() { DIED=1; printf '\n\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }
# Ловушка ERR с set -E срабатывает ещё и в подпроцессах (каждая неудачная docker-команда
# внутри подстановки) — это шум. Поэтому ловим выход, а не ошибку команды: один раз, и только
# если скрипт упал сам, а не через die() с уже написанным объяснением.
on_exit() {
  local rc=$?
  [ -n "${TMP:-}" ] && rm -rf "$TMP"
  [ "$rc" -eq 0 ] || [ "$DIED" = 1 ] && return 0
  printf '\n\033[31m✗ упали на строке %s (код %s), ничего не сделано\033[0m\n' "$LINENO" "$rc" >&2
  printf '  \033[2mскрипт %s. Повтори с --trace и пришли последние строки.\033[0m\n' "$REV" >&2
}
trap on_exit EXIT
TMP=$(mktemp -d) || exit 1

FORK_IMAGE=${FORK_IMAGE:-}
STOCK_IMAGE=${STOCK_IMAGE:-ghcr.io/miroshka000/mikan:latest}
BACKUP=${BACKUP:-}
DRY_RUN=0
DISABLE_AUTO_UPDATE=0
ASSUME_YES=0
WAIT_SECONDS=${WAIT_SECONDS:-180}
NODE_PANEL_ADDRESS=${NODE_PANEL_ADDRESS:-}
NODE_BACKUP=${NODE_BACKUP:-}
# Где на хосте живут команда форка и файлы юнитов. На сервере это ровно эти два места; в
# тестах подменяются, чтобы проверять скрипт, ничего не ставя в /usr/local/bin.
BIN_DIR=${BIN_DIR:-/usr/local/bin}
SYSTEMD_DIR=${SYSTEMD_DIR:-/etc/systemd/system}
# Сколько ждать схему панели после её перезапуска: образ форка применяет четыре свои
# миграции (0032–0035) на старте, и на большой базе это не мгновенно.
SCHEMA_WAIT=${SCHEMA_WAIT:-120}

step() { printf '\n\033[1;36m▸ %s\033[0m\n' "$*"; }
ok() { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
info() { printf '  \033[2m%s\033[0m\n' "$*"; }
run() { # run <описание> <команда...>
  if [ "$DRY_RUN" = 1 ]; then
    printf '  \033[2m(пробный запуск) %s\033[0m\n' "$1"
    return 0
  fi
  printf '  %s … ' "$1"; shift
  if "$@"; then printf '\033[32mок\033[0m\n'; else printf '\033[31mошибка\033[0m\n'; return 1; fi
}
usage() {
  # Шапка скрипта и есть справка: со строк комментариев снимается решётка.
  sed -n '2,/^$/p' "$0" | sed 's/^#//' | sed 's/^ //' | sed '/^$/d'
  cat <<'EOF'

Ключи:
  --image ОБРАЗ          образ Arikan вместо свежего релиза (или FORK_IMAGE=... в
                         окружении): для беты, своей сборки или зеркала
  --rollback АРХИВ        вернуться на обычный mikan: на панели архив базы, на ноде —
                         архив её данных (оба называются в конце перехода)
  --node-panel-address IP адрес панели для файрвола ноды (или NODE_PANEL_ADDRESS=...)
  --dry-run              ничего не менять, только показать
  --trace                показать каждую команду (когда нужно понять, где оно встало)
  --disable-auto-update  выключить автообновление (нужно только для образа со старым
                         апстримным адресом релизов; в актуальной сборке оно своё и не опасно)
  --yes                  не спрашивать подтверждения
Примеры:
  curl -fsSL <url> | sudo bash                      # свежий стабильный релиз форка
  curl -fsSL <url> | sudo bash -s -- --image ghcr.io/o0hhs/arikan:0.5.99.99
  curl -fsSL <url> | sudo bash -s -- --rollback /opt/mikan/data/panel/backup-*.dump
  curl -fsSL <url> | sudo bash -s -- --rollback /opt/mikan/data/node/backup-*-before-fork.tar.gz
EOF
}

while [ $# -gt 0 ]; do
  case $1 in
    --image)
      [ $# -ge 2 ] || die "--image требует значение"
      FORK_IMAGE=$2; shift 2 ;;
    --image=*) FORK_IMAGE=${1#*=}; shift ;;
    --rollback)
      [ $# -ge 2 ] || die "--rollback требует значение"
      BACKUP=$2; shift 2 ;;
    --rollback=*) BACKUP=${1#*=}; shift ;;
    --node-panel-address)
      [ $# -ge 2 ] || die "--node-panel-address требует значение"
      NODE_PANEL_ADDRESS=$2; shift 2 ;;
    --node-panel-address=*) NODE_PANEL_ADDRESS=${1#*=}; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    --trace) TRACE=1; shift ;;
    --disable-auto-update) DISABLE_AUTO_UPDATE=1; shift ;;
    --yes|-y) ASSUME_YES=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "неизвестный ключ: $1 (--help)" ;;
  esac
done

[ "$TRACE" = 1 ] && set -x
[ "$(id -u)" = 0 ] || die "запусти от root: curl -fsSL <url> | sudo bash"
command -v docker >/dev/null || die "docker не найден"
docker compose version >/dev/null 2>&1 || die "нужен docker compose v2"
command -v curl >/dev/null || die "нужен curl"
command -v sha256sum >/dev/null || die "нужен sha256sum (пакет coreutils)"
command -v base64 >/dev/null || die "нужен base64 (пакет coreutils)"
# Команда форка есть для двух архитектур, как и образ; проверяем до всего остального, чтобы
# не менять ничего на сервере, которому эта сборка не подходит.
case "$(uname -m)" in
  x86_64 | amd64) ARCH=x86_64 ;;
  aarch64 | arm64) ARCH=aarch64 ;;
  *) die "для $(uname -m) команды форка нет: Arikan собирается для x86_64 и aarch64" ;;
esac

dc() { docker compose --project-directory "$MIKAN_DIR" -f "$MIKAN_DIR/compose.yaml" "$@"; }
env_get() { sed -n "s/^$1=//p" "$MIKAN_DIR/.env" | head -n1; }
# ───────────────────────────── кто этот сервер: панель или нода ─────────────────────────────
# Нода — это не панель: у неё нет ни postgres, ни панели, ни панельного compose.yaml, и
# половина шагов ниже (схема, pg_dump, таблицы форка) на ней не только бесполезна, но и
# невозможна. Роль берётся из .env: MIKAN_MODE=node пишут и сток, и форк (архив .env — единственный
# документ, который на ноде что-то говорит о её роли). Если строки нет, решает и
# compose.yaml: служба node без службы panel — это нода, panel — панель; в спорном случае
# решает тот же MIKAN_MODE, а без него — панель (там, где есть сомнение, безопаснее
# панельный путь: он ничего не портит и просто честно скажет, что базы нет).
services=$(dc config --services 2>/dev/null || true)
if [ "$(env_get MIKAN_MODE)" = node ]; then
  ROLE=node
elif printf '%s\n' "$services" | grep -qx node && ! printf '%s\n' "$services" | grep -qx panel; then
  ROLE=node
  [ -n "$services" ] && warn "в .env нет MIKAN_MODE, но служба panel в compose.yaml не значится: считаю сервер нодой"
else
  ROLE=panel
fi
IS_NODE=0
[ "$ROLE" = node ] && IS_NODE=1
# Имена контейнеров: у панели и у ноды это одна и та же служба node, но у панели рядом есть
# panel и postgres, которых на ноде нет и быть не должно.
NODE_SERVICE=node
# Службы панели и базы зовутся по-разному у разных сборок стока (postgres/database,
# panel/mikan), поэтому они берутся из самого compose.yaml, а не зашиваются: на панели, где
# базы нет вовсе, имя пустое и все обращения к базе отсекаются ещё до запуска.
#
# Функция, а не один расчёт: compose.yaml на шаге 6 заменяет команда форка, и имя службы
# базы может смениться вместе с ним — тогда повторный вызов возвращает уже новые имена.
resolve_services() {
  services=$(dc config --services 2>/dev/null || true)
  PANEL_SERVICE=""
  DB_SERVICE=""
  [ "$IS_NODE" = 1 ] && return 0
  for cand in panel mikan web; do
    if printf '%s\n' "$services" | grep -qx "$cand"; then PANEL_SERVICE=$cand; break; fi
  done
  for cand in postgres database db; do
    if printf '%s\n' "$services" | grep -qx "$cand"; then DB_SERVICE=$cand; break; fi
  done
  if [ -z "$DB_SERVICE" ]; then
    warn "в compose.yaml нет службы базы (postgres/database) — смотрю базу глазами панели"
  fi
  return 0
}
resolve_services
# Кто и что в базе: compose инсталлера задаёт их константами, но читать их из .env надёжнее.
DBPASS=$(env_get MIKAN_POSTGRES_PASSWORD)
DBUSER=$(env_get MIKAN_POSTGRES_USER); DBUSER=${DBUSER:-mikan}
DBNAME=$(env_get MIKAN_POSTGRES_DB); DBNAME=${DBNAME:-mikan}
SQL_ERR=""
env_set() { # в пробном запуске не пишет ничего
  local key=$1 val=$2
  [ "$DRY_RUN" = 1 ] && return 0
  if grep -q "^$key=" "$MIKAN_DIR/.env"; then
    sed -i.bak "s|^$key=.*|$key=$val|" "$MIKAN_DIR/.env"
  else
    printf '%s=%s\n' "$key" "$val" >>"$MIKAN_DIR/.env"
  fi
}
# psql через контейнер базы, а не через панель: панель может быть остановлена (её как раз и
# собираемся менять), а psql в образе postgres есть всегда. Если не вышло — пробуем панель.
# На ноде базы нет: sql честно говорит об этом, а не стучится в отсутствующую службу.
sql() { # sql ЗАПРОС → строка ответа; при ошибке — сама ошибка в stderr и код 1
  local out err
  if [ "$IS_NODE" = 1 ]; then
    echo "базы на этом сервере нет: он нода" >&2
    return 1
  fi
  err=$(mktemp) || return 1
  # stderr docker compose пишет и сам (+++ docker compose …, служебные строки) — в ответ он
  # попадать не должен, поэтому он уходит в отдельный файл, а не склеивается через 2>&1.
  if [ -n "$DB_SERVICE" ] &&
    out=$(dc exec -T -e PGPASSWORD="$DBPASS" "$DB_SERVICE" psql -U "$DBUSER" -d "$DBNAME" -tAX -v ON_ERROR_STOP=1 -c "$1" 2>"$err"); then
    rm -f "$err"; printf '%s' "$out" | tr -d '[:space:]'; return 0
  fi
  if out=$(dc exec -T "$PANEL_SERVICE" sh -c 'psql "$MIKAN_DATABASE_URL" -tAX -v ON_ERROR_STOP=1 -c "$1"' _ "$1" 2>>"$err"); then
    rm -f "$err"; printf '%s' "$out" | tr -d '[:space:]'; return 0
  fi
  cat "$err" >&2
  rm -f "$err"
  return 1
}
# Схема панели: сколько версий применено. Пусто — не прочитали (или это нода).
schema_now() { sql "SELECT COALESCE(max(version_id),0) FROM goose_db_version WHERE is_applied" 2>/dev/null; }
# Ждём, пока панель сама накатит миграции форка: их применяет её бинарь на старте
# (serve.go → store.OpenPostgres → prepareSchema → migrateUp), и только после этого схема
# становится 32+. Без ожидания проверка ругалась бы на панель, которой нужно ещё время.
wait_for_schema() { # wait_for_schema МИНИМУМ СЕКУНДЫ → «схема»; код 1, если не дождались
  local want=$1 limit=$2 got start pause=2
  start=$(date +%s)
  while :; do
    got=$(schema_now)
    case $got in
      '' | *[!0-9]*) ;;
      *) [ "$got" -ge "$want" ] && { printf '%s' "$got"; return 0; } ;;
    esac
    [ "$(($(date +%s) - start))" -ge "$limit" ] && { printf '%s' "${got:-}"; return 1; }
    sleep "$pause"
    [ "$pause" -lt 10 ] && pause=$((pause + 2))
  done
}
newer_than() { # newer_than A B → 0, если A строго новее B
  local a b
  a=$(printf '%s' "${1#v}" | awk -F'[.-]' '{printf "%d.%d.%d.%d", $1, $2, $3, ($4 ~ /^[0-9]+$/ ? $4 : 0)}')
  b=$(printf '%s' "${2#v}" | awk -F'[.-]' '{printf "%d.%d.%d.%d", $1, $2, $3, ($4 ~ /^[0-9]+$/ ? $4 : 0)}')
  [ -n "$a" ] && [ -n "$b" ] || return 1
  [ "$a" = "$b" ] && return 1
  [ "$(printf '%s\n%s\n' "$b" "$a" | sort -V | tail -n1)" = "$a" ]
}
switch_off() { # выключает переключатель, только если он был включён; 0 — был и выключен
  local was
  # Нода: выключателя нет (им распоряжается панель), да и базы, где он лежит, тоже нет.
  [ "$IS_NODE" = 1 ] && return 1
  was=$(sql "SELECT value FROM settings WHERE key='$1'" 2>/dev/null)
  if [ "$was" = "true" ]; then
    sql "UPDATE settings SET value='false' WHERE key='$1'" >/dev/null
    warn "$1 был включён и выключен"
    return 0
  fi
  return 1
}

# ───────────────────────────── релиз форка и его подпись ─────────────────────────────
# Образ, версия и хостовая команда берутся из подписанного манифеста релиза: указатель
# релизов и манифест проверяются ключом форка (тем же, что в installer/install.sh и
# internal/release.PublicKey — тест в installer/src/release.rs сверяет их). Тот же разбор
# делает installer/install.sh: правишь здесь — проверь и там.
FORK_REPO=${FORK_REPO:-o0hhs/arikan}
INDEX_URL="https://github.com/$FORK_REPO/releases/download/updates/index.json"
PUBKEY='-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAayuo0yLe0M0N5QQPaxkjAA0ltZcXSG7LRANrgyFAhUg=
-----END PUBLIC KEY-----'

# Живой маршрут должен кончаться ошибкой, а не тишиной: соединение без ответа за 15 с или
# передача медленнее 1 КБ/с за 30 с повторяются, потом сдаются. curl до 7.71 (Ubuntu 20.04)
# умеет повторы не для всякой ошибки; новые — для любой.
CURL_RETRY="--retry 3"
if { curl --help all || curl --help; } 2>/dev/null | grep -q -- '--retry-all-errors'; then
  CURL_RETRY="$CURL_RETRY --retry-all-errors"
fi
curl_get() { # curl_get URL ФАЙЛ: скачать; ложь — не скачалось
  # shellcheck disable=SC2086 # CURL_RETRY — список флагов
  curl -fsSL --proto '=https' --tlsv1.2 --connect-timeout 15 --speed-limit 1024 --speed-time 30 $CURL_RETRY "$1" -o "$2" 2>/dev/null
}
# Подпись Ed25519 проверяет openssl 3.0 или новее (Debian 12, Ubuntu 22.04+, Alpine 3.17+);
# у сервера без него его ставят так же, как это делает install.sh.
ensure_openssl() {
  command -v openssl >/dev/null 2>&1 && return 0
  warn "openssl нужен, чтобы проверить подпись релиза: ставлю"
  if command -v apt-get >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get install -y openssl >/dev/null 2>&1 || true
  elif command -v apk >/dev/null 2>&1; then
    apk add --no-cache openssl >/dev/null 2>&1 || true
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y openssl >/dev/null 2>&1 || true
  fi
  command -v openssl >/dev/null 2>&1
}
# signed ФАЙЛ ПОДПИСЬ: подпись — ключом форка над точными байтами файла.
signed() {
  tr -d ' \n\r' <"$2" | base64 -d >"$TMP/check.bin" 2>/dev/null || return 1
  [ "$(wc -c <"$TMP/check.bin" | tr -d ' ')" = 64 ] || return 1
  openssl pkeyutl -verify -pubin -inkey "$TMP/key.pem" -rawin -in "$1" -sigfile "$TMP/check.bin" >/dev/null 2>&1
}
# Манифест — плоский JSON; поля ищутся по имени, без jq (как в install.sh).
flat() { tr -d '\n ' <"$1"; }
field() { sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p"; }
# Самый новый стабильный релиз указателя: записи плоские, каждая — своя строка; нечитаемая
# пропускается, пре-релизы не считаются (install.sh читает так же).
newest_stable() {
  { tr -d ' \t\r\n' <"$1" | tr '{' '\n' && echo; } | while IFS= read -r e; do
    v=$(printf '%s' "$e" | sed -n 's/.*"version":"\([0-9.]*\)".*/\1/p')
    c=$(printf '%s' "$e" | sed -n 's/.*"channel":"\([a-z]*\)".*/\1/p')
    if [ "$c" = stable ] && printf '%s\n' "$v" | grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+(\.[0-9]+)?'; then
      printf '%s\n' "$v"
    fi
  done | sort -t. -k1,1n -k2,2n -k3,3n -k4,4n | tail -n 1
}
# release_resolve: читает свежий стабильный релиз форка и раскладывает его по переменным —
# RELEASE_VERSION, RELEASE_IMAGE (образ, пин по digest), INSTALLER_URL, INSTALLER_SHA256.
# Всё, что не подписано ключом форка, не принимается: менять сервер по такому нельзя.
release_resolve() {
  ensure_openssl || die "проверить подпись релиза нечем: поставь openssl 3.0 или новее и повтори"
  case "$(openssl version 2>/dev/null)" in
    "OpenSSL 3."* | "OpenSSL 4."*) ;;
    *) die "openssl $(openssl version 2>/dev/null) не проверяет подписи Ed25519: нужен openssl 3.0 или новее" ;;
  esac
  printf '%s\n' "$PUBKEY" >"$TMP/key.pem"
  printf '  указатель релизов … '
  if ! curl_get "$INDEX_URL" "$TMP/index.json" || ! curl_get "$INDEX_URL.sig" "$TMP/index.json.sig"; then
    printf '\033[31mне скачался\033[0m\n'
    die "указатель релизов форка недоступен: $INDEX_URL (сервер без интернета? укажи --image)"
  fi
  signed "$TMP/index.json" "$TMP/index.json.sig" || die "указатель релизов не подписан ключом форка: ничего не меняю"
  RELEASE_VERSION=$(newest_stable "$TMP/index.json")
  [ -n "$RELEASE_VERSION" ] || die "в указателе релизов нет ни одного стабильного релиза: укажи образ ключом --image"
  printf '\033[32mок\033[0m (свежий стабильный — %s)\n' "$RELEASE_VERSION"
  printf '  манифест %s … ' "$RELEASE_VERSION"
  MANIFEST_URL="https://github.com/$FORK_REPO/releases/download/v$RELEASE_VERSION/manifest.json"
  if ! curl_get "$MANIFEST_URL" "$TMP/manifest.json" || ! curl_get "$MANIFEST_URL.sig" "$TMP/manifest.json.sig"; then
    printf '\033[31mне скачался\033[0m\n'
    die "манифест релиза Arikan $RELEASE_VERSION не скачался: $MANIFEST_URL"
  fi
  signed "$TMP/manifest.json" "$TMP/manifest.json.sig" ||
    die "манифест Arikan $RELEASE_VERSION не подписан ключом форка: ничего не меняю"
  MANIFEST=$(flat "$TMP/manifest.json")
  [ "$(printf '%s' "$MANIFEST" | field version)" = "$RELEASE_VERSION" ] ||
    die "манифест релиза — не Arikan $RELEASE_VERSION: ничего не меняю"
  image=$(printf '%s' "$MANIFEST" | field image)
  digest=$(printf '%s' "$MANIFEST" | field digest)
  case "$image" in ghcr.io/o0hhs/*) ;; *) die "манифест указывает образ $image — не из ghcr.io/o0hhs: ничего не меняю" ;; esac
  printf '%s' "$digest" | grep -Eq '^sha256:[0-9a-f]{64}$' || die "в манифесте нет digest образа: ничего не меняю"
  RELEASE_IMAGE="$image@$digest"
  # Команда для хоста: файл релиза под этой архитектурой, его же sha256 из манифеста.
  entry=$(printf '%s' "$MANIFEST" | sed -n "s/.*\"$ARCH\":{\([^}]*\)}.*/\1/p")
  INSTALLER_URL=$(printf '%s' "$entry" | field url)
  INSTALLER_SHA256=$(printf '%s' "$entry" | field sha256)
  [ -n "$INSTALLER_URL" ] && [ -n "$INSTALLER_SHA256" ] || die "в релизе Arikan $RELEASE_VERSION нет команды для $ARCH"
  # Адрес — из релизов проекта (имя репозитория могло смениться при переименовании: старые
  # адреса GitHub перенаправляет, но файл должен быть из v<версия> этого проекта).
  case "$INSTALLER_URL" in
    "https://github.com/o0hhs/"*"/releases/download/v$RELEASE_VERSION/"*) ;;
    *) die "манифест указывает команду вне релизов проекта: $INSTALLER_URL" ;;
  esac
  printf '\033[32mок\033[0m (%s)\n' "$RELEASE_IMAGE"
}

# Апдейтер на хосте — снова стоковый: юниты форка выключаются, стоковая команда возвращается
# из /usr/local/bin/mikan.stock на своё имя, стоковые юниты включаются обратно. Так откат
# (ручной или автоматический) отдаёт панели её апдейтер: команда форка ходит за релизами
# форка и вернула бы откаченную панель обратно.
#
# На ноде стоковые юниты — это её собственный апдейтер, и возвращать его некуда: сама она
# за релизами не ходит, её обновляет панель через API (MIKAN_AUTO_UPDATE у неё не включён).
# Поэтому на ноде откат только снимает команду и юниты форка.
restore_stock_updater() {
  if [ -d /run/systemd/system ]; then
    systemctl disable --now arikan-update.timer arikan-update-request.path >/dev/null 2>&1 || true
    if [ "$IS_NODE" = 1 ]; then
      info "нода: апдейтер форка снят; стоковые mikan-update.* остаются выключенными — ноду обновляет её панель (Nodes)"
    else
      # У сборок до переименования юниты форка — те же mikan-update.*: их текст остаётся, но
      # запускает он уже стоковую команду, которая вернётся на своё имя ниже.
      systemctl enable --now mikan-update.timer mikan-update-request.path >/dev/null 2>&1 ||
        warn "юниты mikan-update.* не включились — включи их вручную: systemctl enable --now mikan-update.timer mikan-update-request.path"
    fi
  fi
  # Оба имени сборки форка уходят: ссылка — как ссылка, сама команда — как файл. Иначе после
  # отката в /usr/local/bin остаётся arikan, который ходит за релизами форка и вернул бы
  # откаченную панель обратно. Стоковая копия (mikan.stock) при этом не трогается — она
  # вернётся на имя mikan ниже.
  rm -f "$BIN_DIR/arikan"
  [ -f "$BIN_DIR/mikan.stock" ] && rm -f "$BIN_DIR/mikan"
  if [ -f "$BIN_DIR/mikan.stock" ]; then
    mv "$BIN_DIR/mikan.stock" "$BIN_DIR/mikan"
    ok "команда mikan — стоковая (из $BIN_DIR/mikan.stock)"
  else
    warn "нет $BIN_DIR/mikan.stock: стоковую команду вернёт установщик апстрима (install.sh)"
  fi
}

# ─────────────────────────────── откат на обычный mikan ───────────────────────────────
if [ -n "$BACKUP" ]; then
  [ -f "$BACKUP" ] || die "нет файла $BACKUP"
  [ -f "$MIKAN_DIR/compose.yaml" ] || die "не найден $MIKAN_DIR/compose.yaml"
  BASE=$(basename "$BACKUP")
  case "$BACKUP" in
    *.dump) BACKUP_KIND=panel ;;
    *.tar.gz | *.tgz) BACKUP_KIND=node ;;
    *)
      printf '\n\033[31m✗ не понимаю, что за копия: %s\033[0m\n' "$BACKUP" >&2
      printf '  панель: копия базы *.dump (pg_dump --format=custom)\n' >&2
      printf '  нода:   копия её данных *.tar.gz (её делает шаг 4/9 перехода на ноде)\n\n' >&2
      exit 1
      ;;
  esac
  if [ "$BACKUP_KIND" = panel ]; then
    head -c 5 "$BACKUP" | grep -q PGDMP || die "$BACKUP не архив PostgreSQL (нужен pg_dump --format=custom)"
    [ -f "$MIKAN_DIR/data/panel/$BASE" ] || die "архив вне $MIKAN_DIR/data/panel — положи его туда: cp '$BACKUP' '$MIKAN_DIR/data/panel/'"
  else
    gzip -t "$BACKUP" 2>/dev/null || die "$BACKUP не читается как .tar.gz (нужна копия, сделанная этим скриптом)"
    tar -tzf "$BACKUP" >/dev/null 2>&1 || die "$BACKUP не читается как .tar.gz"
    [ "$IS_NODE" = 1 ] || die "это панель, а копия $BASE — от ноды: панель возвращают из копии базы (*.dump)"
  fi

  if [ "$BACKUP_KIND" = panel ]; then
    printf 'Сейчас образ: %s\nВозврат к:   %s\nБаза из:    %s\n' "$(env_get MIKAN_IMAGE)" "$STOCK_IMAGE" "$BASE"
    printf 'Все платежи, клиенты и тарифы вернутся к состоянию на момент копии.\n'
    printf 'Реферальная программа (referral_*) и метки кампаний (campaign_*) исчезнут: их создаёт форк.\n'
  else
    printf 'Сейчас образ: %s\nВозврат к:   %s\nДанные из:   %s\n' "$(env_get MIKAN_IMAGE)" "$STOCK_IMAGE" "$BASE"
    printf 'Ключи, соединения и настройки ноды вернутся к состоянию на момент копии.\n'
    printf 'Клиенты ноды на это время отключатся: контейнер будет остановлен и поднят снова.\n'
  fi
  if [ "$ASSUME_YES" = 0 ] && [ "$DRY_RUN" = 0 ]; then
    read -r -p 'Продолжить? (yes/no) ' answer </dev/tty || answer=no
    [ "$answer" = "yes" ] || die "отменено"
  fi
  [ "$DRY_RUN" = 1 ] || cp -a "$MIKAN_DIR/.env" "$MIKAN_DIR/.env.rollback-$(date +%Y%m%d-%H%M%S)"

  step "Откат 1/4. Образ обычного mikan — ДО возврата базы"
  env_set MIKAN_IMAGE "$STOCK_IMAGE"
  # И версия в .env — снова стоковая: с версией форка стоковый апдейтер считал бы себя новее
  # стоящего и не предложил бы обновлений.
  STOCK_RUN_VERSION=$(docker run --rm --entrypoint mikan "$STOCK_IMAGE" version 2>/dev/null | tr -d '[:space:]' || true)
  [ -n "$STOCK_RUN_VERSION" ] && env_set MIKAN_VERSION "$STOCK_RUN_VERSION"
  [ "$DRY_RUN" = 1 ] || docker pull "$STOCK_IMAGE" >/dev/null 2>&1 || docker image inspect "$STOCK_IMAGE" >/dev/null 2>&1 || die "нет образа $STOCK_IMAGE"
  ok "MIKAN_IMAGE=$STOCK_IMAGE${STOCK_RUN_VERSION:+ · MIKAN_VERSION=$STOCK_RUN_VERSION}"

  if [ "$BACKUP_KIND" = node ]; then
    # Нода: базы нет ни у форка, ни у стока, и возвращать нечего, кроме её собственных
    # данных. Распаковываются они ДО запуска: контейнер пишет в data/node и на ходу
    # перезаписал бы половину. Поэтому сначала нода останавливается, потом кладутся данные.
    step "Откат 2/4. Данные ноды из копии"
    if [ "$DRY_RUN" = 1 ]; then
      info "(пробный запуск) остановить ноду, распаковать data/node из $BASE, вернуть её .env"
    else
      dc stop "$NODE_SERVICE" >/dev/null 2>&1 || true
      tar -xzf "$BACKUP" -C "$MIKAN_DIR/data/node" ||
        die "распаковать копию не удалось: данные ноды не изменены. Разберись и повтори с --rollback"
      ok "данные ноды возвращены ($BASE)"
      # .env ноды хранит её роль, версию и образ (их читает нода при разборе .env): строки
      # MIKAN_* возвращаются к состоянию копии, всё остальное (NODE_API_PORT, PANEL_IPS,
      # MIKAN_UFW…) остаётся как есть.
      if [ -f "$MIKAN_DIR/data/node/.env" ]; then
        while IFS= read -r line; do
          case "$line" in
            MIKAN_*=*) env_set "${line%%=*}" "${line#*=}" ;;
          esac
        done <"$MIKAN_DIR/data/node/.env"
        ok ".env ноды вернулся к копии (MIKAN_MODE=$(env_get MIKAN_MODE), MIKAN_IMAGE=$(env_get MIKAN_IMAGE))"
      else
        warn "в копии нет .env ноды: роль и образ остаются в $MIKAN_DIR/.env как есть"
      fi
    fi
  else
    step "Откат 2/4. База из копии"
    dc stop panel node >/dev/null 2>&1 || true
    if [ "$DRY_RUN" = 1 ]; then
      info "(пробный запуск) pg_restore обычным бинарём"
    elif dc run --rm --no-deps -T "$PANEL_SERVICE" database restore "/data/panel/$BASE" >/tmp/mikan-restore.log 2>&1; then
      ok "база возвращена и поднята до схемы обычного mikan"
    else
      sed -n '1,20p' /tmp/mikan-restore.log >&2
      die "возврат не удался — база не тронута, прежняя схема на месте"
    fi
  fi

  step "Откат 3/4. Обновления — снова стоковые"
  # Наоборот к переходу: юниты форка выключаются, стоковая команда возвращается на своё имя,
  # стоковые юниты включаются — иначе панель после отката обновлял бы форк.
  if [ "$DRY_RUN" = 1 ]; then
    info "(пробный запуск) вернуть /usr/local/bin/mikan из mikan.stock и юниты mikan-update.*"
  else
    restore_stock_updater
    ok "апдейтер — снова стоковый: /usr/local/bin/mikan"
  fi

  step "Откат 4/4. Запуск и проверка"
  dc up -d --wait --wait-timeout "$WAIT_SECONDS" || warn "стек не стал здоровым — docker compose logs"
  [ "$DRY_RUN" = 1 ] && exit 0
  if [ "$BACKUP_KIND" = node ]; then
    # Проверяется то, что у ноды есть: контейнер запущен и она снова ждёт свою панель.
    NODE_PORT_STATE=$(env_get NODE_API_PORT)
    sleep 5
    dc exec -T "$NODE_SERVICE" mikan-node key-port >/dev/null 2>&1 ||
      dc exec -T "$NODE_SERVICE" /usr/local/bin/arikan-node --version >/dev/null 2>&1 || {
        dc logs "$NODE_SERVICE" --tail 15 2>&1 | sed 's/^/    /' >&2
        die "нода не поднялась — данные на месте, логи выше"
      }
    ok "нода отвечает${NODE_PORT_STATE:+ (ждёт панель на порту $NODE_PORT_STATE)}"
    printf '\n\033[32mГотово.\033[0m Нода на обычном mikan, данные — на состояние копии %s.\n' "$BASE"
    printf '  Панель обновит ноду сама (Nodes): версия у неё снова стоковая.\n'
    exit 0
  fi
  sleep 5
  dc exec -T "$PANEL_SERVICE" mikan health >/dev/null 2>&1 || { dc logs "$PANEL_SERVICE" --tail 15 2>&1 | sed 's/^/    /' >&2; die "панель не поднялась — база на месте, логи выше"; }
  ok "панель отвечает"
  SCHEMA=$(schema_now)
  ok "схема: ${SCHEMA:-?}"
  printf '\n\033[32mГотово.\033[0m Панель на обычном mikan, база — на состояние копии %s.\n' "$BASE"
  exit 0
fi

# ─────────────────────────────── переход на форк ───────────────────────────────
if [ ! -f "$MIKAN_DIR/compose.yaml" ]; then
  cat >&2 <<EOF

mikan не найден в $MIKAN_DIR.

Arikan — это тот же mikan, только с реферальной программой, поэтому с нуля его ставят так же,
как обычный mikan, а потом переключают образ:

  1) установка обычного mikan (Docker, файрвол, compose, панель):
     curl -fsSL https://github.com/Miroshka000/mikan/releases/latest/download/install.sh | sudo bash

  2) переход на форк (эта же команда, уже установленного mikan):
     curl -fsSL <url-этого-скрипта> | sudo bash

EOF
  exit 1
fi
[ -f "$MIKAN_DIR/.env" ] || die "не найден $MIKAN_DIR/.env"

STAMP=$(date +%Y%m%d-%H%M%S)
# Копия для отката — ПЕРВАЯ, снятая до перехода.
#
# Панель: копии, снятые позже, уже содержат миграции форка, и обычный mikan на них не
# поднимется («schema 32 is newer than this binary supports», 0032 — первая миграция форка),
# поэтому на повторных запусках новую не делаем, а помним первую.
#
# Нода: базы нет вовсе, поэтому копия — это её собственные данные (data/node: ключи,
# соединения, настройки), но по той же причине берётся первая: она ближе всего к состоянию
# до перехода.
if [ "$IS_NODE" = 1 ]; then
  BACKUP_DIR="$MIKAN_DIR/data/node/backup"
  BACKUP_SUFFIX=tar.gz
  BACKUP_PATTERN="$BACKUP_DIR"/backup-*-before-fork.tar.gz
  # Копия может ехать на другой носитель: место там, где данные, а не там, где /opt.
  [ -n "$NODE_BACKUP" ] && BACKUP_DIR=$NODE_BACKUP
else
  BACKUP_DIR="$MIKAN_DIR/data/panel"
  BACKUP_SUFFIX=dump
  BACKUP_PATTERN="$BACKUP_DIR"/backup-*-before-fork.dump
fi
FIRST_BACKUP=""
# `ls` без совпадений возвращает 1, а с pipefail и set -e это молча оборвало бы весь
# скрипт, поэтому пустой результат — обычный случай, а не ошибка.
for f in $BACKUP_PATTERN; do
  [ -e "$f" ] || break
  FIRST_BACKUP="$f"; break
done
if [ -n "$FIRST_BACKUP" ]; then
  BACKUP_NAME=$(basename "$FIRST_BACKUP"); BACKUP_HOST=$FIRST_BACKUP
  if [ "$DRY_RUN" = 0 ]; then ok "копия для отката уже есть: $BACKUP_NAME"; fi
else
  BACKUP_NAME="backup-${STAMP}-before-fork.$BACKUP_SUFFIX"
  BACKUP_HOST="$BACKUP_DIR/$BACKUP_NAME"
fi
ENV_BACKUP="$MIKAN_DIR/.env.before-fork-${STAMP}"

OLD_IMAGE=$(env_get MIKAN_IMAGE)
[ -n "$OLD_IMAGE" ] || die "в .env нет MIKAN_IMAGE — не знаю, что сейчас запущено"
# Повторный запуск не должен забыть, куда откатываться: образ, с которого ушли, запоминается
# один раз и дальше переиспользуется.
STATE_FILE="$MIKAN_DIR/.fork-original-image"
if [ -f "$STATE_FILE" ]; then
  SAVED=$(cat "$STATE_FILE")
  OLD_IMAGE=$SAVED
  warn "скрипт уже запускался: прежним образом считается $OLD_IMAGE"
elif [ "$DRY_RUN" != 1 ]; then
  printf '%s\n' "$OLD_IMAGE" >"$STATE_FILE"
fi

step "1/9. Релиз форка и образ"
# Релиз читается первым: из подписанного манифеста берутся образ (пином по digest), версия и
# команда для хоста — и всё это до единого изменения на сервере. Образ, названный ключом
# --image, оставляем как есть: его версию скрипт спросит у самого образа (шаг 3/9).
release_resolve
if [ -n "$FORK_IMAGE" ]; then
  info "образ задан ключом: $FORK_IMAGE; команда и версия — из релиза Arikan $RELEASE_VERSION"
else
  FORK_IMAGE=$RELEASE_IMAGE
fi
# Тянем ДО того, как что-либо меняем. Реестр может отказать (пакет в ghcr по умолчанию
# закрытый), и тогда сбой стоит ноль секунд и не требует отката — в прошлый раз образ не
# тянулся, стек уже переключился, и всё пришлось возвращать назад.
if docker image inspect "$FORK_IMAGE" >/dev/null 2>&1; then
  ok "$FORK_IMAGE уже есть на сервере"
elif [ "$DRY_RUN" = 1 ]; then
  printf '  \033[2m(пробный запуск) docker pull %s\033[0m\n' "$FORK_IMAGE"
else
  printf '  docker pull %s … ' "$FORK_IMAGE"
  if docker pull "$FORK_IMAGE" >/tmp/mikan-pull.log 2>&1; then
    printf '\033[32mок\033[0m\n'
  else
    printf '\033[31mне скачался\033[0m\n'
    sed -n '1,6p' /tmp/mikan-pull.log | sed 's/^/    /' >&2
    # Подсказка про закрытый пакет — по имени из самого образа, а не зашитая.
    reg=$(printf '%s' "$FORK_IMAGE" | sed -E 's|^([^/]+)/.*|\1|')
    case $FORK_IMAGE in */*/*) owner=$(printf '%s' "$FORK_IMAGE" | sed -E 's|^[^/]+/||; s|/.*||') ;; *) owner="" ;; esac
    {
      printf '\n  Образ %s сервер не достаёт. Два пути:\n\n' "$FORK_IMAGE"
      if [ -n "$owner" ]; then
        printf '  1) Открыть пакет — тогда тянет кто угодно, без входа:\n'
        printf '     github.com/%s → Packages → %s → Package settings → Change visibility → Public\n\n' \
          "$owner" "$(printf '%s' "$FORK_IMAGE" | sed -E 's|^[^/]+/[^/]+/||; s|:[^:]*$||')"
      fi
      printf '  2) Войти на сервере под своим аккаунтом:\n'
      printf '     docker login %s -u %s\n' "$reg" "${owner:-$reg}"
      printf '     (токен: github.com/settings/tokens → Generate new token → read:packages)\n\n'
      printf '  Ничего не изменено: образ прежний, база на месте.\n\n'
    } >&2
    exit 1
  fi
fi

step "2/9. Что стоит сейчас"
if [ "$IS_NODE" = 1 ]; then
  info "каталог $MIKAN_DIR · образ $OLD_IMAGE · скрипт $REV · сервер: нода"
  ok "панели и базы на ноде нет — ни схемы, ни pg_dump здесь не будет"
  info "смотреть за ней — на стороне панели: Nodes"
  BEFORE_SCHEMA=""
else
  info "каталог $MIKAN_DIR · образ $OLD_IMAGE · скрипт $REV · сервер: панель"

  # Стек может быть не поднят — тогда читать схему нечем. Поднимаем базу и пробуем ещё раз.
  # Время ограничено: сервер без доступа к реестру иначе висел бы тут минутами.
  if [ -n "$DB_SERVICE" ] && ! dc ps --status running --services 2>/dev/null | grep -qx "$DB_SERVICE"; then
    warn "база не запущена — поднимаю (не дольше двух минут)"
    if command -v timeout >/dev/null 2>&1; then
      timeout 60 dc up -d "$DB_SERVICE" >/dev/null 2>&1 || true
    else
      dc up -d "$DB_SERVICE" >/dev/null 2>&1 || true
    fi
    for _ in $(seq 1 20); do
      dc exec -T "$DB_SERVICE" pg_isready -q >/dev/null 2>&1 && break
      sleep 3
    done
  fi
  if ! BEFORE_SCHEMA=$(schema_now); then
    printf '\n  \033[31mне читаю базу — postgres и панель не ответили на запрос схемы.\033[0m\n\n'
    printf '  \033[2mсостояние стека:\033[0m\n'
    dc ps 2>&1 | sed 's/^/    /'
    printf '\n'
    die "база недоступна. Если панель не запущена — cd $MIKAN_DIR && docker compose up -d, потом повтори."
  fi
  # Сколько чего было ДО: по этим числам шаг 8/9 убедится, что данные не потерялись.
  USERS=$(sql "SELECT count(*) FROM users")
  TARIFFS=$(sql "SELECT count(*) FROM tariffs")
  PAYMENTS=$(sql "SELECT count(*) FROM payments")
  ok "схема PostgreSQL $BEFORE_SCHEMA · $USERS клиентов · $TARIFFS тарифов · $PAYMENTS платежей"
fi

step "3/9. Обновления"
if [ "$IS_NODE" = 1 ]; then
  # У ноды нет панели и её кнопки «Обновить»: её ведёт панель через API. Поэтому здесь важно
  # одно: не ставим ли мы сборку, которую нода потом не сможет получить (она не новее).
  STOCK_VERSION=$(docker run --rm --entrypoint mikan "$OLD_IMAGE" version 2>/dev/null | tr -d '[:space:]' || true)
  FORK_VERSION=$(docker run --rm --entrypoint mikan "$FORK_IMAGE" version 2>/dev/null | tr -d '[:space:]' || true)
  ok "сейчас ${STOCK_VERSION:-?}, в форке ${FORK_VERSION:-?}"
  if [ -n "$STOCK_VERSION" ] && [ -n "$FORK_VERSION" ] && [ "$FORK_VERSION" != "$STOCK_VERSION" ] &&
    ! newer_than "$FORK_VERSION" "$STOCK_VERSION"; then
    warn "версия Arikan не новее стоящей — панель такую сборку ноде не предложит: собирай с большей VERSION"
  fi
  info "обновления ноды ведёт её панель (Nodes), а не апдейтер на этом хосте"
  if [ "$DISABLE_AUTO_UPDATE" = 1 ]; then
    info "сама нода обновляется только при MIKAN_AUTO_UPDATE=1 в её .env — так и оставлено"
  fi
else
  STOCK_VERSION=$(dc exec -T "$PANEL_SERVICE" mikan version 2>/dev/null | tr -d '[:space:]' || true)
  FORK_VERSION=$(docker run --rm --entrypoint mikan "$FORK_IMAGE" version 2>/dev/null | tr -d '[:space:]' || true)
  ok "сейчас ${STOCK_VERSION:-?}, в форке ${FORK_VERSION:-?}"
  if [ -n "$STOCK_VERSION" ] && [ -n "$FORK_VERSION" ]; then
    if [ "$FORK_VERSION" = "$STOCK_VERSION" ]; then
      info "на сервере уже эта сборка: переключение будет вхолостую"
    elif ! newer_than "$FORK_VERSION" "$STOCK_VERSION"; then
      warn "версия Arikan не новее стоящей — такую сборку панель не предложит обновить себе"
      warn "пересобери образ: docker buildx build --build-arg VERSION=0.5.99.99 -t <образ> . && docker push <образ>"
    fi
  fi
  if [ "$DISABLE_AUTO_UPDATE" = 1 ]; then
    # Нужно только для образа старой сборки, где Repo ещё указывает на апстрим: там панель
    # сравнивает себя с ЕГО релизами и может заменить себя обычным mikan.
    switch_off auto_update || ok "автообновление выключено"
    switch_off nodes_follow_panel || ok "автообновление нод выключено"
  else
    info "автообновление оставлено включённым: у этой сборки адрес релизов и ключ свои (o0hhs/arikan)"
  fi
fi

step "4/9. Резервная копия"
if [ "$DRY_RUN" = 1 ]; then
  if [ "$IS_NODE" = 1 ]; then
    info "(пробный запуск) копия .env и данных ноды (data/node) в $BACKUP_NAME"
  else
    info "(пробный запуск) копия .env и pg_dump всей схемы в $BACKUP_NAME"
  fi
else
  cp -a "$MIKAN_DIR/.env" "$ENV_BACKUP"
  if [ ! -e "$BACKUP_HOST" ]; then
    if [ "$IS_NODE" = 1 ]; then
      # Базы у ноды нет: её состояние — это её собственные данные. Внутри архива и .env
      # ноды (роль, версия, образ: их читает compose ноды), поэтому откат вернёт и его.
      # Каталог копий исключён: архив лежит в data/node/backup, и tar, собирающий сам себя,
      # ловит «file changed as we read it» и делает копию негодной.
      printf '  данные ноды … '
      mkdir -p "$BACKUP_DIR" || die "не создать $BACKUP_DIR"
      tar -czf "$BACKUP_HOST" --exclude=data/node/backup -C "$MIKAN_DIR" data/node ||
        { printf '\033[31mошибка\033[0m\n'; die "копия данных ноды не удалась — не продолжаю"; }
      printf '\033[32mок\033[0m\n'
    else
      printf '  pg_dump … '
      dc run --rm --no-deps -T "$PANEL_SERVICE" database backup "/data/panel/$BACKUP_NAME" >/dev/null ||
        { printf '\033[31mошибка\033[0m\n'; die "резервная копия не удалась — не продолжаю"; }
      printf '\033[32mок\033[0m\n'
    fi
  fi
  [ -s "$BACKUP_HOST" ] || die "копия пуста"
  if [ "$IS_NODE" = 1 ]; then
    tar -tzf "$BACKUP_HOST" >/dev/null 2>&1 || die "$BACKUP_HOST не читается как tar.gz"
    ok "данные ноды: $BACKUP_HOST ($(du -h "$BACKUP_HOST" | cut -f1))"
  else
    head -c 5 "$BACKUP_HOST" | grep -q PGDMP || die "$BACKUP_HOST не архив PostgreSQL"
    ok "база: $BACKUP_HOST ($(du -h "$BACKUP_HOST" | cut -f1))"
  fi
  ok ".env:  $(basename "$ENV_BACKUP")"
fi

step "5/9. Команда форка"
# Кто отвечает на кнопку «Обновить» и на ночной таймер — см. шапку: это должна быть команда
# форка. Она ставится до переключения образа и до всего, что меняет сервер: не вышло — не
# тронуто ничего, и апдейтер остаётся тем, чем был. Файл взят из релиза и сверен с его
# подписанным манифестом; рабочий файл на месте, пока новый не лёг целиком.
#
# Своё имя команда называет сама (`--version`): релизы до переименования форка называются
# mikan и ставят юниты mikan-update.*, с 0.5.1.2 — arikan и arikan-update.*. Ставим её под
# её именем — там её ищут её же юниты и её самообновление, — а второе имя делаем ссылкой на
# неё, как в образе mikan и mikan-node ссылаются на arikan и arikan-node. Стоковая команда
# при этом уезжает в /usr/local/bin/mikan.stock: её хранит первая же миграция, её и вернёт
# откат.
if [ "$DRY_RUN" = 1 ]; then
  info "(пробный запуск) поставить команду Arikan $RELEASE_VERSION в $BIN_DIR и её юниты (шаг 7)"
else
  printf '  скачать команду … '
  if ! curl_get "$INSTALLER_URL" "$TMP/arikan"; then
    printf '\033[31mошибка\033[0m\n'
    die "команда форка не скачалась: $INSTALLER_URL (доступ в интернет? укажи --image)"
  fi
  got=$(sha256sum "$TMP/arikan" | cut -d' ' -f1)
  if [ "$got" != "$INSTALLER_SHA256" ]; then
    printf '\033[31mне сошлась\033[0m\n'
    die "скачанная команда не совпала с подписанным манифестом (sha256): ничего не меняю"
  fi
  printf '\033[32mок\033[0m\n'
  chmod +x "$TMP/arikan"
  CMD_NAME=$("$TMP/arikan" --version 2>/dev/null | awk 'NR==1{print $1}')
  case "$CMD_NAME" in
    mikan) CMD=$BIN_DIR/mikan; ALIAS=$BIN_DIR/arikan ;;
    arikan) CMD=$BIN_DIR/arikan; ALIAS=$BIN_DIR/mikan ;;
    *) die "команда из релиза называет себя «${CMD_NAME:-никак}»: не понимаю, чья она — пришли это разработчику" ;;
  esac
  # Убираем то, что стоит на обоих именах: ссылки — как есть, а стоковую команду — в
  # mikan.stock. Стоковая хранится только первая: её вернёт откат, и подменять её сборкой
  # форка нельзя.
  mkdir -p "$BIN_DIR" || die "не создать $BIN_DIR"
  for name in mikan arikan; do
    target=$BIN_DIR/$name
    if [ -L "$target" ]; then
      rm -f "$target"
    elif [ -e "$target" ]; then
      if [ "$name" = mikan ] && [ ! -e "$BIN_DIR/mikan.stock" ]; then
        mv -f "$target" "$BIN_DIR/mikan.stock"
      else
        rm -f "$target"
      fi
    fi
  done
  install -m 755 "$TMP/arikan" "$BIN_DIR/.${CMD_NAME}.new" && mv -f "$BIN_DIR/.${CMD_NAME}.new" "$CMD" ||
    die "не удалось положить $CMD: ничего не меняю"
  ln -sfn "$CMD" "$ALIAS"
  said=$("$CMD" --version 2>/dev/null | tr -d '[:space:]')
  case "$said" in
    *"$RELEASE_VERSION"*) ok "команда на месте: $CMD ($said)" ;;
    *) die "команда отвечает «${said:-ничего}», а релиз — Arikan $RELEASE_VERSION: ничего не меняю" ;;
  esac
fi

step "6/9. Переключение на образ форка"
if [ "$DRY_RUN" = 1 ]; then
  info "(пробный запуск) MIKAN_IMAGE=$FORK_IMAGE${FORK_VERSION:+ · MIKAN_VERSION=$FORK_VERSION}"
  info "(пробный запуск) post-update и docker compose up -d $NODE_SERVICE"
else
  env_set MIKAN_IMAGE "$FORK_IMAGE"
  # Версия в .env — то, чем панель и команда считают себя; с чужой версией апдейтер выбирает
  # релиз по неверной отметке (переезд со стока оставлял там версию стока, и обновление
  # вечно шло через несуществующий промежуточный релиз).
  [ -n "$FORK_VERSION" ] && env_set MIKAN_VERSION "$FORK_VERSION"
  if [ "$IS_NODE" = 1 ] && [ -n "$NODE_PANEL_ADDRESS" ]; then
    # Панель — единственный, кто ходит на ноду по API, и единственный, кто её обновляет.
    # Адрес панели открывается здесь, а не после: не выйдет — сказано, но не смертельно.
    NODE_PORT_NOW=$(env_get NODE_API_PORT)
    if [ -n "$NODE_PORT_NOW" ] && command -v ufw >/dev/null 2>&1; then
      ufw allow from "$NODE_PANEL_ADDRESS" to any port "$NODE_PORT_NOW" proto tcp >/dev/null 2>&1 &&
        ok "порт ноды $NODE_PORT_NOW открыт только для панели $NODE_PANEL_ADDRESS" ||
        warn "ufw не принял правило для панели $NODE_PANEL_ADDRESS:$NODE_PORT_NOW — открой его сам"
    else
      info "ключ --node-panel-address задан, но NODE_API_PORT в .env или ufw не нашлись: правило не тронуто"
    fi
  fi
  ok "MIKAN_IMAGE=$FORK_IMAGE${FORK_VERSION:+ · MIKAN_VERSION=$FORK_VERSION}"
  # Файлы этого релиза — юниты и compose.yaml: команда форка ставит их сама (`post-update`,
  # тем же кодом, что и при обновлении), и это же переносит compose на имена её сборки. Стек
  # поднимается уже новым образом и новым compose — один перезапуск, а не два.
  if "$CMD" post-update >/dev/null 2>&1; then
    ok "юниты и compose.yaml этого релиза на месте"
    # Файл сменился — вместе с ним могли смениться и имена служб (у сборок форка база
    # называется postgres, у некоторых сборок стока — database). Спрашиваем заново.
    resolve_services
  else
    warn "post-update не прошёл: юниты и compose могли не встать (подробнее: $CMD post-update)"
  fi
fi
# Compose мог остаться стоковым: команда форка не отвечает на post-update, на ноде лежит
# чужой файл. Тогда перезапускать нечего — в файле нет службы, которую просят поднять, и
# docker скажет «no such service» (ровно это и случилось в прошлый раз, только на postgres).
# Лучше сказать об этом здесь и до перезапуска.
if [ "$IS_NODE" = 1 ]; then SERVICES_WANT=node; else SERVICES_WANT=panel; fi
SERVICES_NOW=$(dc config --services 2>/dev/null || true)
if ! printf '%s\n' "$SERVICES_NOW" | grep -qx "$SERVICES_WANT"; then
  printf '\n\033[31m✗ в %s/compose.yaml нет службы %s (есть: %s)\033[0m\n' \
    "$MIKAN_DIR" "$SERVICES_WANT" "$(printf '%s' "$SERVICES_NOW" | tr '\n' ' ')" >&2
  if [ "$IS_NODE" = 1 ]; then
    printf '  Нода — это служба node: такой compose.yaml пишет установщик mikan. Посмотри, что видит docker:\n' >&2
    printf '    cd %s && docker compose config\n' "$MIKAN_DIR" >&2
  fi
  printf '  Ничего не перезапущено. Образ в .env уже переключён: вернуть прежний — %s\n\n' "$OLD_IMAGE" >&2
  exit 1
fi
ok "образ уже на сервере (первый шаг)"
run "перезапустить стек" dc up -d --wait --wait-timeout "$WAIT_SECONDS"

step "7/9. Автоматика на хосте — команды форка"
# Юниты этого релиза поставила его же команда (шаг 6). Осталось убедиться, что рядом не
# осталось чужой автоматики: свежие сборки ставят юниты arikan-update.* и запускают
# /usr/local/bin/arikan, а стоковые mikan-update.* (и сборки форка до переименования) — это
# юниты mikan-update.*, запускающие /usr/local/bin/mikan. Где какая — зависит от сборки:
# до переименования юниты у форка как раз mikan-update.*, и команда под этим именем уже его
# (шаг 5), поэтому выключать нечего; начиная с 0.5.1.2 стоковые выключаются — их файлы
# остаются на месте, как и команда в mikan.stock: откат включает и её, и их.
# На ноде у юнита форка одно дело — принять запрос панели на обновление ноды; поэтому
# «чужая автоматика» здесь только стоковая. Свежий форк для ноды ставит юнит с запросом
# ноды (data/node/update/request), сборка до переименования — тот же mikan-update.*; в обоих
# случаях проверяется то, что есть, и ничего не выключается вслепую.
if [ "$DRY_RUN" = 1 ]; then
  info "(пробный запуск) команда и юниты релиза — на месте, стоковая автоматика выключена"
elif [ ! -d /run/systemd/system ]; then
  info "systemd нет: таймеров и юнитов на этом сервере нет, обновления — только вручную"
  [ "$IS_NODE" = 0 ] && info "обновить панель: $CMD update"
elif [ "$CMD_NAME" = mikan ]; then
  # Юниты форка до переименования — те же mikan-update.*, и команда под этим именем уже его.
  if systemctl is-enabled --quiet mikan-update.timer 2>/dev/null; then
    ok "юниты mikan-update.* — команды форка (так называются её же юниты до переименования)"
  else
    warn "юниты mikan-update.* не включились — включи вручную: systemctl enable --now mikan-update.timer mikan-update-request.path"
  fi
elif [ "$IS_NODE" = 1 ]; then
  if systemctl is-enabled --quiet arikan-update.timer 2>/dev/null; then
    if grep -q 'data/node/update/request' /etc/systemd/system/arikan-update-request.path 2>/dev/null; then
      ok "юнит форка слушает запросы панели на обновление ноды (data/node/update/request)"
    else
      info "юнит форка слушает запросы панели: что именно — видно в /etc/systemd/system/arikan-update-request.path"
    fi
    systemctl disable --now mikan-update.timer mikan-update-request.path >/dev/null 2>&1 || true
    ok "стоковые юниты mikan-update.* выключены; стоковая команда сохранена в /usr/local/bin/mikan.stock"
  else
    warn "юнит arikan-update.timer не включён: панель не сможет обновить ноду по своему запросу"
    warn "включи: systemctl enable --now arikan-update.timer arikan-update-request.path"
  fi
  info "ночной таймер ноде не нужен: обновления ноды запускает её панель (кнопкой на странице Nodes)"
else
  if systemctl is-enabled --quiet arikan-update.timer 2>/dev/null || systemctl is-enabled --quiet arikan-update-request.path 2>/dev/null; then
    systemctl disable --now mikan-update.timer mikan-update-request.path >/dev/null 2>&1 || true
    ok "юниты: arikan-update.* включены, стоковые mikan-update.* выключены"
    ok "стоковая команда сохранена: /usr/local/bin/mikan.stock (имя mikan ведёт на arikan)"
  else
    warn "юниты arikan-update.* не включились — стоковый апдейтер оставлен как есть (иначе обновлений не было бы вовсе)"
  fi
fi

step "8/9. Проверка"
FAILED=0
if [ "$DRY_RUN" = 1 ]; then
  info "(пробный запуск) проверка пропущена"
elif [ "$IS_NODE" = 1 ]; then
  # У ноды проверяется то, что у неё есть: контейнер запущен и нода отвечает своей командой.
  # Таблицы referral_*/campaign_* и схему создаёт панель на СВОЕЙ стороне — на ноде их нет и
  # требовать их здесь нельзя (именно на этом прежний скрипт и спотыкался: он искал базу там,
  # где её нет). Смотреть ноду — на странице Nodes её панели.
  sleep 5
  if dc exec -T "$NODE_SERVICE" mikan-node key-host >/dev/null 2>&1 ||
    dc exec -T "$NODE_SERVICE" /usr/local/bin/arikan-node --version >/dev/null 2>&1; then
    ok "нода отвечает (той же командой, что и в образе)"
  else
    warn "нода не отвечает своей командой — docker compose logs node"
    FAILED=1
  fi
  if [ -d /run/systemd/system ]; then
    if [ "$CMD_NAME" = mikan ]; then UNITS=mikan-update; OTHER=arikan-update; else UNITS=arikan-update; OTHER=mikan-update; fi
    if systemctl is-enabled --quiet "$OTHER.timer" 2>/dev/null; then
      warn "чужой юнит $OTHER.timer ещё включён"
      FAILED=1
    else
      ok "чужого апдейтера нет ($OTHER.* выключен)"
    fi
    if systemctl is-enabled --quiet "$UNITS.timer" 2>/dev/null; then
      ok "юнит форка включён ($UNITS.timer): панель обновит ноду по запросу"
    else
      warn "юнит $UNITS.timer не включён"
      FAILED=1
    fi
  fi
  info "нода готова: дальше её видит панель — Nodes (кнопка обновит её до $FORK_VERSION)"
else
  sleep 5
  dc exec -T "$PANEL_SERVICE" mikan health >/dev/null 2>&1 ||
    { warn "health не проходит, панель ещё поднимается"; sleep 20; dc exec -T "$PANEL_SERVICE" mikan health >/dev/null 2>&1 || FAILED=1; }
  # Миграции 0032–0035 накатывает сама панель на старте (serve.go → prepareSchema → migrateUp):
  # ждём их, а не ругаемся сразу — на большой базе это не мгновенно.
  if AFTER=$(wait_for_schema 32 "$SCHEMA_WAIT"); then
    ok "схема PostgreSQL $BEFORE_SCHEMA → $AFTER (миграции форка применились)"
  else
    warn "схема ${AFTER:-?} через ${SCHEMA_WAIT} с ожидания, нужно ≥ 32 (0032 — первая миграция форка)"
    FAILED=1
  fi
  T=$(sql "SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name LIKE 'referral%'")
  [ "$T" = "5" ] && ok "пять таблиц referral_* созданы" || { warn "таблиц referral_*: ${T:-0}"; FAILED=1; }
  C=$(sql "SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name LIKE 'campaign%'")
  [ "$C" = "3" ] && ok "три таблицы campaign_* созданы" || { warn "таблиц campaign_*: ${C:-0}"; FAILED=1; }
  for pair in "users:$USERS" "tariffs:$TARIFFS"; do
    table=${pair%%:*}; want=${pair##*:}
    got=$(sql "SELECT count(*) FROM $table")
    [ "$got" = "$want" ] && ok "$table на месте: $got" || { warn "$table: стало $got, было $want"; FAILED=1; }
  done
  P=$(sql "SELECT count(*) FROM payments")
  [ -n "$P" ] && [ "$P" -ge "$PAYMENTS" ] 2>/dev/null && ok "платежей на месте: $P" || { warn "платежей стало ${P:-0}, было $PAYMENTS"; FAILED=1; }
  # Автоматика на хосте — команды форка: иначе кнопка «Обновить» и ночной таймер уйдут к
  # апстриму. Как называются её юниты, зависит от сборки (до переименования mikan-update.*,
  # с 0.5.1.2 — arikan-update.*): своя пара проверяется включённой, чужая — выключенной.
  if [ -d /run/systemd/system ]; then
    if [ "$CMD_NAME" = mikan ]; then UNITS=mikan-update; OTHER=arikan-update; else UNITS=arikan-update; OTHER=mikan-update; fi
    if systemctl is-enabled --quiet "$UNITS.timer" 2>/dev/null; then
      ok "юниты команды форка включены ($UNITS.timer, $UNITS-request.path)"
    else
      warn "юнит $UNITS.timer не включён: обновлений по таймеру и по кнопке не будет"
      FAILED=1
    fi
    if systemctl is-enabled --quiet "$OTHER.timer" 2>/dev/null; then
      warn "чужой юнит $OTHER.timer ещё включён: обновления придут от апстрима"
      FAILED=1
    else
      ok "чужого апдейтера нет ($OTHER.* выключен)"
    fi
  fi
fi

if [ "$FAILED" = 1 ] && [ "$DRY_RUN" != 1 ]; then
  printf '\n\033[31m✗ проверка не прошла — откатываюсь на %s\033[0m\n' "$OLD_IMAGE"
  env_set MIKAN_IMAGE "$OLD_IMAGE"
  OLD_RUN_VERSION=$(docker run --rm --entrypoint mikan "$OLD_IMAGE" version 2>/dev/null | tr -d '[:space:]' || true)
  [ -n "$OLD_RUN_VERSION" ] && env_set MIKAN_VERSION "$OLD_RUN_VERSION"
  # И апдейтер обратно на стоковый: команда форка на откаченной панели ходит за релизами
  # форка и вернула бы его обратно первой же ночной проверкой.
  restore_stock_updater
  if [ "$IS_NODE" = 1 ]; then
    # Данных ноды это не касается: их не трогали, а образ сейчас переключится обратно.
    dc stop "$NODE_SERVICE" >/dev/null 2>&1 || true
    dc up -d --wait --wait-timeout "$WAIT_SECONDS" || true
    die "откат выполнен, нода на обычном mikan"
  fi
  dc stop panel node >/dev/null 2>&1 || true
  dc run --rm --no-deps -T "$PANEL_SERVICE" database restore "/data/panel/$BACKUP_NAME" ||
    die "откат не удался: верни $ENV_BACKUP и $BACKUP_HOST вручную"
  dc up -d --wait --wait-timeout "$WAIT_SECONDS" || true
  die "откат выполнен, панель на обычном mikan"
fi

step "9/9. Готово"
[ "$DRY_RUN" = 1 ] || info "$(dc ps --format '{{.Service}}: {{.Status}}' 2>/dev/null | tr '\n' ' ')"
if [ "$IS_NODE" = 1 ]; then
  cat <<EOF

  Это нода: панели и базы на ней нет, поэтому программа форка живёт на стороне панели.
  Нода переведена на сборку форка ($FORK_VERSION) — её видно на странице Nodes панели.
  Дальше: панель сама предложит ей обновления (Nodes → Обновления), а запрос панели нода
  принимает через юнит форка — тот же API, что и у стока.
  Что открыто снаружи, не менялось: единственный порт ноды — её API для панели.
  Стоковая команда сохранена в /usr/local/bin/mikan.stock.
  Откат: та же команда с --rollback $BACKUP_NAME (вернёт данные ноды и её .env)

  Образ прежней версии  $OLD_IMAGE
  копия данных ноды      $BACKUP_HOST
  копия .env             $ENV_BACKUP
EOF
else
  cat <<EOF

  Включить программу: Панель → Платежи → Рефералы (тумблер, правила, тарифы).
  Метки кампаний: ссылка вида t.me/<бот>?start=camp_<метка>; статистика на той же странице.
  Обновления: панель сама смотрит релизы o0hhs/arikan; обновить можно кнопкой или \`arikan update\`.
  Апдейтер на хосте — команды форка (её юниты; у сборки 0.5.1.1 они ещё mikan-update.*).
  Стоковая команда сохранена в /usr/local/bin/mikan.stock — откат вернёт её и её юниты.
  Откат: та же команда с --rollback $BACKUP_NAME

  Образ прежней версии  $OLD_IMAGE
  копия базы            $BACKUP_HOST
  копия .env             $ENV_BACKUP
EOF
fi