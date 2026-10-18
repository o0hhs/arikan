#!/usr/bin/env bash
#
# Arikan: проверка цепочки «клиент → CDN → нода» по слоям.
#
#   bash scripts/cdn-check.sh --cdn cdn.example.com --origin origin.example.com
#   bash scripts/cdn-check.sh --cdn cdn.example.com --origin origin.example.com --path /ваш-путь/
#
# Зачем: схема обхода белых списков через CDN ломается на любом из пяти слоёв, и код
# ответа почти никогда не называет слой сам. 502 может вернуть CDN, nginx или само ядро.
# Скрипт идёт по слоям снизу вверх и на каждом говорит, прошёл он или нет — до того, как
# кто-то начнёт менять настройки XHTTP, которые ни при чём.
#
# Слои:
#   1. DNS: куда ведут оба имени
#   2. Сертификат источника: срок, имена, цепочка — CDN проверяет его при соединении
#   3. Источник по HTTPS напрямую, в обход CDN
#   4. CDN отвечает и доходит до источника
#   5. Транспортный путь доходит до ноды (POST, как аплоад XHTTP)
#
# Ничего не меняет: только читает. Ненулевой код возврата — есть провал, и он назван.

set -uo pipefail

CDN=""
ORIGIN=""
PATH_=""
PORT="443"
TIMEOUT="15"
VERBOSE="0"

usage() {
  cat <<'EOF'
Проверка цепочки CDN для обхода белых списков.

  --cdn HOST      домен раздачи (то, что видят клиенты)          [обязателен]
  --origin HOST   домен источника (то, что видит CDN на ноде)     [обязателен]
  --path PATH     транспортный путь XHTTP, например /abc123/      [необязателен]
  --port PORT     порт HTTPS (по умолчанию 443)
  --timeout SEC   таймаут на запрос (по умолчанию 15)
  -v              показывать заголовки и тела ответов
  -h              эта справка

Пример:
  bash scripts/cdn-check.sh --cdn cdn.example.com --origin origin.example.com --path /abc123/
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --cdn)     CDN="${2:-}"; shift 2 ;;
    --origin)  ORIGIN="${2:-}"; shift 2 ;;
    --path)    PATH_="${2:-}"; shift 2 ;;
    --port)    PORT="${2:-}"; shift 2 ;;
    --timeout) TIMEOUT="${2:-}"; shift 2 ;;
    -v|--verbose) VERBOSE="1"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "неизвестный ключ: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [ -z "$CDN" ] || [ -z "$ORIGIN" ]; then
  echo "нужны --cdn и --origin: это два разных имени одной схемы" >&2
  usage >&2
  exit 2
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "нужен curl" >&2
  exit 2
fi

# Цвета только в терминале.
if [ -t 1 ]; then
  R=$'\033[31m'; G=$'\033[32m'; Y=$'\033[33m'; B=$'\033[1m'; N=$'\033[0m'
else
  R=""; G=""; Y=""; B=""; N=""
fi

FAILED="0"
ok()   { printf '  %s✓%s %s\n' "$G" "$N" "$1"; }
bad()  { printf '  %s✗%s %s\n' "$R" "$N" "$1"; FAILED="1"; }
warn() { printf '  %s!%s %s\n' "$Y" "$N" "$1"; }
info() { printf '      %s\n' "$1"; }
layer(){ printf '\n%s%s%s\n' "$B" "$1" "$N"; }

CURL="curl -sS --max-time $TIMEOUT"
[ "$VERBOSE" = "1" ] && CURL="$CURL -v"

# ---- 1. DNS ----
layer "1. DNS"

resolve_a() {
  # getent есть не везде; host и dig — не везде тоже; на Windows нет ни одного из них.
  # Берём то, что нашлось, и добираем последним способом: curl резолвит имя сам.
  out=""
  if command -v getent >/dev/null 2>&1; then
    out="$(getent ahostsv4 "$1" 2>/dev/null | awk '{print $1}' | sort -u | tr '\n' ' ')"
  elif command -v dig >/dev/null 2>&1; then
    out="$(dig +short A "$1" 2>/dev/null | tr '\n' ' ')"
  elif command -v host >/dev/null 2>&1; then
    out="$(host -t A "$1" 2>/dev/null | awk '/has address/{print $NF}' | tr '\n' ' ')"
  fi
  if [ -z "${out// /}" ]; then
    # %{remote_ip}: curl резолвит имя своим резолвером и печатает адрес, к которому
    # подключился. Работает и там, где системных утилит DNS нет.
    out="$(curl -sS --max-time "$TIMEOUT" -o /dev/null -w '%{remote_ip}' "https://$1/" 2>/dev/null)"
  fi
  printf '%s' "$out"
}

for pair in "раздача:$CDN" "источник:$ORIGIN"; do
  kind="${pair%%:*}"; name="${pair#*:}"
  ips="$(resolve_a "$name")"
  if [ -z "$ips" ]; then
    bad "$kind $name не разрешается: A-записи нет"
  else
    ok "$kind $name → $ips"
  fi
done

if [ "$CDN" = "$ORIGIN" ]; then
  warn "домен раздачи и домен источника совпадают: это два разных участка соединения, и один домен их не обслужит"
fi

# ---- 2. Сертификат источника ----
layer "2. Сертификат источника (его проверяет CDN)"

cert_info() {
  host="$1"
  if command -v openssl >/dev/null 2>&1; then
    echo | timeout "$TIMEOUT" openssl s_client -servername "$host" -connect "$host:$PORT" 2>/dev/null \
      | openssl x509 -noout -subject -issuer -dates -ext subjectAltName 2>/dev/null
  else
    return 1
  fi
}

if command -v openssl >/dev/null 2>&1; then
  ci="$(cert_info "$ORIGIN")"
  if [ -z "$ci" ]; then
    bad "не удалось получить сертификат $ORIGIN:$PORT"
    info "CDN не сможет проверить источник — это самая частая причина 502"
  else
    subj="$(printf '%s\n' "$ci" | sed -n 's/^subject=//p')"
    iss="$(printf '%s\n'  "$ci" | sed -n 's/^issuer=//p')"
    notafter="$(printf '%s\n' "$ci" | sed -n 's/^notAfter=//p')"
    ok "сертификат получен: $subj"
    info "издатель: $iss"
    info "действует до: $notafter"
    case "$subj$ci" in
      *CN=*\ *.*|*DNS:*.*) ;;
    esac
    if printf '%s\n' "$ci" | grep -q "DNS:"; then
      ok "в сертификате есть доменные имена (SAN)"
    else
      warn "в сертификате нет SAN с доменом: похоже, он выпущен на IP-адрес"
      info "CDN обычно не принимает такой сертификат: привяжите к ноде домен источника"
    fi
  fi
else
  warn "openssl не найден — шаг пропущен"
fi

# ---- 3. Источник напрямую ----
layer "3. Источник по HTTPS, напрямую (в обход CDN)"

direct_code="$($CURL -o /dev/null -w '%{http_code}' "https://$ORIGIN:$PORT/" 2>/dev/null)"
if [ -z "$direct_code" ] || [ "$direct_code" = "000" ]; then
  bad "источник не ответил на https://$ORIGIN:$PORT/"
  info "проверьте: порт открыт, сертификат валиден, nginx/нода слушают"
else
  case "$direct_code" in
    2*|3*|4*) ok "источник отвечает: HTTP $direct_code" ;;
    5*)       bad "источник отвечает ошибкой: HTTP $direct_code" ;;
    *)        warn "неожиданный код: $direct_code" ;;
  esac
fi

if [ -n "$PATH_" ]; then
  tp_code="$($CURL -o /dev/null -w '%{http_code}' -X POST "https://$ORIGIN:$PORT$PATH_" 2>/dev/null)"
  case "$tp_code" in
    2*|4*) ok "транспортный путь на источнике: HTTP $tp_code (405/404 здесь ожидаемы без данных XHTTP)" ;;
    5*)    bad "транспортный путь на источнике: HTTP $tp_code — смотрите логи ядра и nginx" ;;
    *)     warn "транспортный путь на источнике: HTTP $tp_code" ;;
  esac
fi

# ---- 4. CDN ----
layer "4. CDN отвечает и доходит до источника"

cdn_code="$($CURL -o /dev/null -w '%{http_code}' "https://$CDN:$PORT/" 2>/dev/null)"
if [ -z "$cdn_code" ] || [ "$cdn_code" = "000" ]; then
  bad "CDN не ответил на https://$CDN:$PORT/"
  info "проверьте: CNAME на технический домен CDN, сертификат домена раздачи, статус ресурса"
else
  case "$cdn_code" in
    2*|3*) ok "CDN отвечает: HTTP $cdn_code" ;;
    403)   bad "CDN вернул 403: правила доступа или WAF" ;;
    404)   warn "CDN вернул 404: ресурс есть, но путь или Host не тот" ;;
    429)   bad "CDN вернул 429: лимит частоты запросов" ;;
    502)   bad "CDN вернул 502: он не достучался до источника"
           info "разрешение домена источника, его порт, TLS/SNI, сертификат, заголовок Host" ;;
    504)   bad "CDN вернул 504: источник не ответил за отведённое время"
           info "у части CDN окно фиксировано (Yandex — 5 секунд); таймаут в nginx его не меняет" ;;
    5*)    bad "CDN вернул $cdn_code" ;;
    *)     warn "CDN вернул $cdn_code" ;;
  esac
fi

# Сравнение времени ответа: где именно теряется время.
time_at() {
  $CURL -o /dev/null -w '%{time_total}' "$1" 2>/dev/null
}
t_origin="$(time_at "https://$ORIGIN:$PORT/")"
t_cdn="$(time_at "https://$CDN:$PORT/")"
if [ -n "$t_origin" ] && [ -n "$t_cdn" ]; then
  ok "время ответа: источник ${t_origin}s, CDN ${t_cdn}s"
fi

# ---- 5. Транспортный путь через CDN ----
layer "5. Транспортный путь через CDN"

if [ -z "$PATH_" ]; then
  warn "путь не задан (--path): шаг проверяет только корень"
  tp=""
else
  tp="$PATH_"
fi

# Аплоад XHTTP: POST у пресета «CDN», GET с данными в заголовках у пресета
# «CDN · GET-аплоад». 405 на POST про первый скажет всё, а для второго не драма —
# смотрите GET ниже.
post_code="$($CURL -o /dev/null -w '%{http_code}' -X POST "https://$CDN:$PORT${tp}" 2>/dev/null)"
case "$post_code" in
  405) warn "CDN ответил 405 на POST: он пропускает только GET/HEAD/OPTIONS"
       info "для пресета «CDN» (stream-one) это конец — смените CDN"
       info "для пресета «CDN · GET-аплоад» это не приговор: его аплоад — GET с данными в заголовках, смотрите GET ниже" ;;
  000|"") bad "запрос через CDN не прошёл" ;;
  2*|4*) ok "POST через CDN: HTTP $post_code" ;;
  5*)    bad "POST через CDN: HTTP $post_code" ;;
  *)     warn "POST через CDN: HTTP $post_code" ;;
esac

get_code="$($CURL -o /dev/null -w '%{http_code}' "https://$CDN:$PORT${tp}" 2>/dev/null)"
case "$get_code" in
  000|"") bad "GET через CDN не прошёл" ;;
  *)     ok "GET через CDN: HTTP $get_code" ;;
esac

# ---- Итог ----
printf '\n'
if [ "$FAILED" = "1" ]; then
  printf '%sЕсть провал%s: он назван выше. Чините слой снизу вверх — настройки XHTTP ниже провалившегося слоя ни при чём.\n' "$R" "$N"
  exit 1
fi
printf '%sВсе слои прошли%s. Если клиент всё равно не подключается, проверьте подписку: адрес для клиентов должен быть доменом раздачи, а не адресом ноды.\n' "$G" "$N"
exit 0
