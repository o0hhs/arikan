#!/usr/bin/env bash
# Интеграционный тест scripts/install-fork.sh: переход с обычного mikan на форк — и на
# сервере-панели, и на сервере-НОДЕ (у которого нет ни панели, ни базы).
#
# Docker не нужен: docker, curl, systemctl и openssl подменяются заглушками
# (test/fork/harness.sh), а сам скрипт запускается в mktemp-каталоге. Root не нужен.
#
#   bash test-install-fork.sh              # всё
#   bash test-install-fork.sh node         # проверки, в имени которых есть node
#   bash test/fork/preflight.sh            # только «нашлись ли файлы и цел ли синтаксис»
#
# Код 0 — всё прошло, 1 — есть упавшие проверки.

set -uo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# The harness is re-sourced by every fake the script runs; it finds itself through this name.
export HF_HARNESS=$HERE/test/fork/harness.sh
# shellcheck source=test/fork/harness.sh
. "$HERE/test/fork/harness.sh"
# shellcheck source=test/fork/lib.sh
. "$HERE/test/fork/lib.sh"
# shellcheck source=test/fork/cases.sh
. "$HERE/test/fork/cases.sh"

hf_main "$@"
