#!/usr/bin/env bash
# Мини-фреймворк тестов установщика: assert'ы, общий счёт и печать результата.
# Каждый файл cases-*.sh описывает свои проверки и в конце зовёт hf_main "$@".

set -uo pipefail

HF_FAILED=0
HF_TESTS=0
HF_CASE=""

fail() { printf '  \033[31m✗ %s\033[0m\n' "$*" >&2; HF_FAILED=$((HF_FAILED + 1)); return 1; }
pass() { printf '  \033[32m✓\033[0m %s\n' "$*"; }

assert_rc() {
  local want=$1 msg=${2:-}
  if [ "${HF_RC:-}" = "$want" ]; then pass "$msg (код $HF_RC)"; else fail "$msg: код ${HF_RC:-?}, ждали $want"; fi
}
assert_eq() {
  local got=$1 want=$2 msg=${3:-}
  if [ "$got" = "$want" ]; then pass "$msg"; else fail "$msg: получили «$got», ждали «$want»"; fi
}
assert_contains() {
  local file=$1 needle=$2 msg=${3:-}
  if grep -qF -- "$needle" "$file"; then pass "$msg"; else fail "$msg: в выводе нет «$needle»"; fi
}
assert_not_contains() {
  local file=$1 needle=$2 msg=${3:-}
  if grep -qF -- "$needle" "$file"; then fail "$msg: в выводе есть «$needle»"; else pass "$msg"; fi
}
assert_file() {
  local path=$1 msg=${2:-}
  if [ -n "$path" ] && [ -e "$path" ]; then pass "$msg"; else fail "$msg: нет $path"; fi
}
assert_not_file() {
  local path=$1 msg=${2:-}
  if [ -e "$path" ]; then fail "$msg: есть $path"; else pass "$msg"; fi
}

# Что именно упало: без этого тест говорит «код 1», а не почему.
hf_log() {
  printf '\n  ── вывод установщика ──\n' >&2
  sed 's/^/  /' "${HF_OUT:-/dev/null}" >&2
  printf '  ──────────────────────\n' >&2
}

# Всё, что тесту нужно от harness.sh, под одним префиксом: имена читаются.
hf_setup() { install_fork_harness_setup "$@"; hf_write_release; hf_make_bin; }
hf_run() { run_install_fork "$@"; }
hf_env() { hf_state "$1"; }

hf_main() {
  local filter=${1:-}
  local t
  for t in $(declare -F | awk '{print $3}' | grep '^test_' | sort); do
    [ -n "$filter" ] && [[ $t != *"$filter"* ]] && continue
    HF_TESTS=$((HF_TESTS + 1))
    printf '\n▸ %s\n' "$t"
    if "$t"; then :; else HF_FAILED=$((HF_FAILED + 1)); fi
  done
  printf '\n'
  if [ "$HF_FAILED" = 0 ]; then
    printf '\033[32m✓ все %s проверок прошли\033[0m\n' "$HF_TESTS"
    return 0
  fi
  printf '\033[31m✗ %s из %s проверок упали\033[0m\n' "$HF_FAILED" "$HF_TESTS"
  return 1
}
