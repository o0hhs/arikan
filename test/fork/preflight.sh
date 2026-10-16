#!/usr/bin/env bash
# preflight настоящего теста: docker не нужен, bash и несколько утилит — все.
# Использование: bash test/fork/preflight.sh [путь-к-репозиторию]
set -uo pipefail

# Этот файл лежит в test/fork, поэтому корень репозитория — на две ступени выше.
ROOT=$(cd "${1:-$(dirname "${BASH_SOURCE[0]}")/../..}" && pwd)
fails=0
say() { printf '  %-56s %s\n' "$1" "$2"; }

command -v bash >/dev/null || { echo "нужен bash"; exit 1; }

for f in scripts/install-fork.sh test/fork/lib.sh test/fork/harness.sh test/fork/cases.sh; do
  if [ -f "$ROOT/$f" ]; then
    if bash -n "$ROOT/$f" 2>/dev/null; then say "$f синтаксис" "ок"; else say "$f синтаксис" "ОШИБКА"; fails=$((fails + 1)); fi
  else
    say "$f существует" "НЕТ"; fails=$((fails + 1))
  fi
done

# То, без чего переход на ноде не имеет смысла: скрипт должен видеть роль сервера.
if [ -f "$ROOT/scripts/install-fork.sh" ]; then
  if grep -q 'MIKAN_MODE' "$ROOT/scripts/install-fork.sh"; then say "скрипт читает MIKAN_MODE" "ок"; else say "скрипт читает MIKAN_MODE" "НЕТ"; fails=$((fails + 1)); fi
  if grep -q 'IS_NODE' "$ROOT/scripts/install-fork.sh"; then say "в скрипте есть ветка ноды" "ок"; else say "в скрипте есть ветка ноды" "НЕТ"; fails=$((fails + 1)); fi
  # Нода не должна ни спрашивать базу, ни делать pg_dump: на этом падал прежний скрипт.
  if grep -q 'базы на этом сервере нет' "$ROOT/scripts/install-fork.sh"; then
    say "нода отсекает обращения к базе" "ок"
  else
    say "нода отсекает обращения к базе" "НЕТ"
    fails=$((fails + 1))
  fi
fi

printf '\n'
if [ "$fails" = 0 ]; then
  printf '\033[32m✓ preflight прошёл\033[0m\n'
  exit 0
fi
printf '\033[31m✗ preflight: %s проверок не прошло\033[0m\n' "$fails"
exit 1
