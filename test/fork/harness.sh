#!/usr/bin/env bash
# The Docker, the host and the network that scripts/install-fork.sh sees, made of stubs, so
# the installer can be run end to end without a server: a node of another panel, a panel of
# its own, and the ways both go wrong.
#
#   source test/fork/harness.sh
#   install_fork_harness_setup node
#   run_install_fork            # see run.sh
#
# Nothing here is a test by itself: test-install-fork.sh is what holds the expectations.
#
# What it fakes, and how faithfully:
#
#   * docker        every subcommand the script uses: compose ps/up/exec/run/stop/logs,
#                   image inspect, pull, run --rm, version. The stack's state comes from
#                   $ROOT/.env (MIKAN_IMAGE), the compose.yaml the script put there, and
#                   the files of the stack (data/panel/update/status.json). A compose file
#                   that does not name the service the script starts is an error, exactly
#                   as docker reports it: that is how a node's run failed before.
#   * /usr/local/bin  an installed stock command (mikan), its update units, and the fork's
#                   command the script downloads: a real script that answers --version and
#                   writes the install log when asked to.
#   * systemctl     enable/disable/is-enabled over files under $ROOT/systemd.
#   * curl          the release index, its signature, the manifest and the command, from
#                   test/fork/release/; openssl is scripted to say yes to every signature.

set -uo pipefail

HF_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
SCRIPTS_DIR=$(cd "$HF_DIR/../.." && pwd)

# The release the fake index, manifest and command carry. Everything the real script reads
# comes from these files: the versions are the ones a run has to speak about.
HF_VERSION=0.5.1.4
HF_DIGEST=sha256:$(printf 'a%.0s' $(seq 1 64))
HF_IMAGE="ghcr.io/o0hhs/arikan@$HF_DIGEST"
HF_STOCK_IMAGE=ghcr.io/miroshka000/mikan@sha256:$(printf 'b%.0s' $(seq 1 64))
HF_STOCK_VERSION=0.5.1.1

# What the fake fork command does with post-update: install-node or install-panel (its own
# files, as a release's command does), fail (it cannot), nothing (it prints nothing).
# Unset, it follows the role of the server it is on — the same release carries both files,
# and which ones it puts down depends on what the server is.
HF_POST_UPDATE=${HF_POST_UPDATE:-}

fail() { printf '\033[31m✗ %s\033[0m\n' "$*" >&2; return 1; }

# ─────────────────────────── the server the script will see ───────────────────────────
# setup <node|panel>: a fresh /opt/mikan (the one being moved) with the stock command on the
# host. HF_* stray files of the run before are gone.
install_fork_harness_setup() {
  local mode=${1:-node}
  HF_MODE=$mode
  HF_ROOT=$(mktemp -d)
  HF_BIN=$HF_ROOT/usr-local-bin
  HF_SYSTEMD=$HF_ROOT/systemd
  HF_TMP=$HF_ROOT/tmp
  HF_LOG=$HF_ROOT/log
  HF_MIKAN=$HF_ROOT/opt/mikan
  mkdir -p "$HF_BIN" "$HF_SYSTEMD" "$HF_TMP" "$HF_LOG" "$HF_MIKAN/data/panel" "$HF_MIKAN/data/node"

  # What the stock install left on the host: its command, its units, and the run's .env.
  printf '#!/usr/bin/env bash\necho "mikan (stock) %s"\n' "$HF_STOCK_VERSION" >"$HF_BIN/mikan"
  chmod +x "$HF_BIN/mikan"
  : >"$HF_SYSTEMD/mikan-update.timer"
  : >"$HF_SYSTEMD/mikan-update-request.path"
  # Where a panel's settings live; the script reads them through psql.
  HF_SETTINGS_DB=$HF_LOG/settings.db
  : >"$HF_SETTINGS_DB"
  HF_SETTINGS_LOCK=$HF_LOG/settings.lock
  # The database the panel runs on: schema 31 (stock — the fork's 0032 is the next one), the
  # two switches the stock installer turns on, and the fork's tables once its migrations run.
  HF_SCHEMA=${HF_SCHEMA:-31}
  HF_REFERRAL_TABLES=${HF_REFERRAL_TABLES:-5}
  HF_CAMPAIGN_TABLES=${HF_CAMPAIGN_TABLES:-3}
  export HF_SCHEMA HF_REFERRAL_TABLES HF_CAMPAIGN_TABLES
  printf 'auto_update=true\nnodes_follow_panel=true\n' >"$HF_SETTINGS_DB"

  local env="MIKAN_MODE=$mode
MIKAN_IMAGE=$HF_STOCK_IMAGE
MIKAN_VERSION=$HF_STOCK_VERSION
MIKAN_POSTGRES_USER=mikan
MIKAN_POSTGRES_DB=mikan
MIKAN_POSTGRES_PASSWORD=secret"
  case $mode in
    node)
      env="$env
MIKAN_NODE_JOIN=joinkey
NODE_API_PORT=25305"
      # A node of its own server: only the node service, no database, no panel.
      cat >"$HF_MIKAN/compose.yaml" <<'YAML'
name: mikan

services:
  node:
    image: ${MIKAN_IMAGE}
    entrypoint: ["/usr/local/bin/arikan-node"]
YAML
      ;;
    panel)
      cat >"$HF_MIKAN/compose.yaml" <<'YAML'
name: mikan

services:
  panel:
    image: ${MIKAN_IMAGE}
  node:
    image: ${MIKAN_IMAGE}
  database:
    image: postgres
YAML
      ;;
    *) fail "install_fork_harness_setup: $mode is neither node nor panel"; return 1 ;;
  esac
  printf '%s\n' "$env" >"$HF_MIKAN/.env"
  # A real installation keeps the node's own copy beside its data: the installer writes it,
  # the node's compose reads it, and the backup of a node must carry it (an install that has
  # not been used yet has whatever the stock installer left).
  if [ "$mode" = node ]; then
    printf '%s\n' "$env" >"$HF_MIKAN/data/node/.env"
    mkdir -p "$HF_MIKAN/data/node/update"
    : >"$HF_MIKAN/data/node/update/status.json"
  fi

  # What the stack left behind: the panel's database backup directory (the script checks it).
  mkdir -p "$HF_MIKAN/data/panel/backup" "$HF_MIKAN/data/node/update"
  printf 'users=3\ntariffs=2\npayments=5\n' >"$HF_LOG/counts"
  # A server being moved is running: its stack is up. (A test that wants it down sets the
  # state itself, with hf_set_stack.)
  : >"$HF_LOG/stack-up"
  export HF_MODE HF_ROOT HF_BIN HF_SYSTEMD HF_TMP HF_LOG HF_MIKAN HF_SETTINGS_DB
}

# ───────────────────────────── the fake docker ─────────────────────────────
# The container for a service runs the same command the image would; what matters is that
# it fails when the service is not in compose.yaml.
hf_docker() {
  printf 'docker %s\n' "$*" >>"$HF_LOG/docker.log"
  local sub=${1:-}; shift || true
  case $sub in
    version) echo "27.0.0" ;;
    compose)
      local a=("$@") dir=$HF_MIKAN
      if [[ ${a[0]:-} == --project-directory ]]; then
        dir=${a[1]}
        a=("${a[@]:2}")
      fi
      # compose.yaml, when the caller names it with -f: the same file.
      if [[ ${a[0]:-} == -f ]]; then
        a=("${a[@]:2}")
      fi
      local cmd=${a[0]:-}
      a=("${a[@]:1}")
      case $cmd in
        version) echo "Docker Compose version v2.29.0" ;;
        config) hf_compose_config "${a[@]}" ;;
        ps) hf_compose_ps "${a[@]}" ;;
        up) hf_compose_up "${a[@]}" ;;
        exec) hf_compose_exec "${a[@]}" ;;
        run) hf_compose_run "${a[@]}" ;;
        stop) :
          ;;
        down) hf_set_stack down ;;
        restart) hf_set_stack up ;;
        logs) echo "panel | log line" ;;
        *) echo "docker compose $cmd: unknown" >&2; return 1 ;;
      esac ;;
    image)
      case ${1:-} in
        inspect) hf_image_inspect "${2:-}" ;;
        *) return 1 ;;
      esac ;;
    pull) hf_pull "${1:-}" ;;
    run) hf_docker_run "$@" ;;
    *) echo "docker: unknown subcommand $sub" >&2; return 1 ;;
  esac
}

# Where the stack stands: what keeps it up is .env's image and the compose file.
hf_set_stack() {
  case $1 in
    up) : >"$HF_LOG/stack-up" ;;
    down) rm -f "$HF_LOG/stack-up" ;;
  esac
}
hf_stack_up() { [[ -f $HF_LOG/stack-up ]]; }

# The services of compose.yaml, one per line: `docker compose ps --services`. A blank line
# inside the block is just spacing, not the end of it; a new top-level key is.
hf_compose_services() {
  awk '
    /^services:/ { s = 1; next }
    s && /^[[:space:]]*$/ { next }
    s && /^  [A-Za-z0-9_.-]+:/ { line = $0; sub(/:.*/, "", line); sub(/^[[:space:]]+/, "", line); print line; next }
    s && /^[^[:space:]]/ { s = 0 }
  ' "$HF_MIKAN/compose.yaml"
}
hf_has_service() { hf_compose_services | grep -qx "$1"; }

# `docker compose config [--services|--quiet]`: the file as compose understands it. --quiet
# only checks that it parses, which is what a caller wants before trusting it.
hf_compose_config() {
  local args="$*"
  [ -s "$HF_MIKAN/compose.yaml" ] || { echo "no configuration file provided" >&2; return 1; }
  grep -q '^services:' "$HF_MIKAN/compose.yaml" || { echo "compose.yaml: no services" >&2; return 1; }
  [[ $args == *--services* ]] && hf_compose_services
  return 0
}

hf_compose_ps() {
  local args="$*" want_svc=0
  # --services narrows the answer to the service names, whatever else is asked of it.
  [[ $args == *--services* ]] && want_svc=1
  # `--status running` asks only about what is up: a stack that is down names nothing.
  hf_stack_up || return 0
  local s
  for s in $(hf_compose_services); do
    if [[ $want_svc == 1 ]]; then
      echo "$s"
    else
      echo "{\"ID\":\"deadbeef\",\"Service\":\"$s\",\"State\":\"running\",\"Status\":\"Up 25 hours\"}"
    fi
  done
}

# `docker compose up -d [--wait] [service…]`: the services named, or every service. A
# service compose.yaml does not know is refused, as docker refuses it — that is what a
# node's run hit when it asked for postgres.
#
# Starting the panel of a forked release means its own migrations run at boot (that is where
# the fork's 0032–0035 come from: the panel applies them itself, serve.go → migrateUp), so a
# started stack holds schema 32 whenever its image is the fork's. HF_SCHEMA_AFTER_UP lets a
# test keep a broken panel broken — that is the case the verification must catch.
hf_compose_up() {
  local svc found=0
  for svc in "$@"; do
    [[ $svc == -* ]] && continue
    case $svc in
      postgres|panel|node|database)
        hf_has_service "$svc" || { echo "no such service: $svc" >&2; return 1; }
        found=1
        ;;
    esac
  done
  [[ $found == 1 ]] || hf_compose_services >/dev/null || return 1
  hf_set_stack up
  # Starting the panel of a forked release runs its migrations (the fork's 0032–0035 are
  # applied by the panel itself at boot: serve.go → prepareSchema → migrateUp). Which image
  # the stack runs is what the .env names, so that is what decides the schema after the start.
  # The fake docker runs in its own process, so what it learns is written down for the psql
  # fake to read back — the same way a real migration would leave a mark in the database.
  if hf_has_service panel && grep -qF "$HF_DIGEST" "$HF_MIKAN/.env" 2>/dev/null; then
    printf 'schema=%s\nreferral=%s\ncampaign=%s\n' "${HF_SCHEMA_AFTER_UP:-32}" \
      "${HF_REFERRAL_AFTER_UP:-${HF_REFERRAL_TABLES:-5}}" "${HF_CAMPAIGN_TABLES:-3}" >"$HF_LOG/migrated"
  fi
  return 0
}

# `docker compose exec -T SERVICE CMD…`: refuses a service that is not there, like docker.
#
# One difference that matters here: on a node compose.yaml does not name postgres at all, so
# a command against it fails however the flags are read — which is exactly what happens on
# the server. The service is looked for among the arguments first.
hf_compose_exec() {
  local args=("$@") svc="" cmd=() a seen=0
  for a in "${args[@]}"; do
    if [[ $seen == 0 ]]; then
      case $a in
        -T|-e|--env) ;;      # exec's own flags, before the service
        -*|*=*) ;;
        *) svc=$a; seen=1 ;; # the service
      esac
    else
      cmd+=("$a")            # the command inside the container
    fi
  done
  hf_has_service "$svc" || { echo "no such service: $svc" >&2; return 1; }
  hf_stack_up || { echo "service \"$svc\" is not running" >&2; return 1; }
  # A plain assignment before a shell function is not visible inside it (it is for external
  # commands only), so the service travels in an exported variable.
  export HF_CMD_SERVICE=$svc
  hf_panel_cmd "${cmd[@]}"
}

# `docker compose run --rm --no-deps -T panel database backup|restore …`
hf_compose_run() {
  local args=("$@") svc= i=0
  while [[ $i -lt ${#args[@]} ]]; do
    case ${args[$i]} in
      --rm|--no-deps|-T|--name) [[ ${args[$i]} == --name ]] && i=$((i+1)) ;;
      -*) ;;
      *) svc=${args[$i]}; break ;;
    esac
    i=$((i+1))
  done
  args=("${args[@]:$((i+1))}")
  hf_has_service "$svc" || { echo "no such service: $svc" >&2; return 1; }
  export HF_CMD_SERVICE=$svc
  hf_panel_cmd "${args[@]}"
}

# The commands a container answers: the panel image and the node image are different, so the
# service decides which ones exist. hf_compose_exec/hf_compose_run pass on the command they
# were given, with the service name already stripped, and name the service in HF_CMD_SERVICE.
hf_panel_cmd() {
  case ${HF_CMD_SERVICE:-panel} in
    panel)
      case ${1:-} in
        mikan|arikan)
          case ${2:-} in
            version) echo "$HF_STOCK_VERSION" ;;
            health) return 0 ;;
            *) return 0 ;;
          esac ;;
        database)
          case ${2:-} in
            backup) hf_backup "${3:-}" ;;
            restore) hf_restore "${3:-}" ;;
            *) return 1 ;;
          esac ;;
        psql) hf_psql "$@" ;;
        # The panel's own psql, as a shell one-liner: `sh -c 'psql "$MIKAN_DATABASE_URL" …' _ SQL`.
        # The query is the last argument, after the `_` that stands in for $0.
        sh)
          [[ ${2:-} == -c ]] || return 1
          local sql=${*: -1}
          case $sql in
            *psql*) hf_psql "$sql" ;;
            *) return 1 ;;
          esac ;;
        *) return 1 ;;
      esac ;;
    node)
      case ${1:-} in
        mikan-node|arikan-node)
          case ${2:-} in
            version|--version) echo "$HF_STOCK_VERSION" ;;
            key-host) echo "panel.example" ;;
            key-port) echo "${NODE_API_PORT:-25305}" ;;
            *) return 0 ;;
          esac ;;
        *) return 1 ;;
      esac ;;
    # The database's own container: the postgres image carries psql, which is why the script
    # asks it first (the panel may be stopped — it is the thing being replaced).
    postgres|database|db)
      case ${1:-} in
        psql) hf_psql "$@" ;;
        pg_isready) return 0 ;;
        *) return 1 ;;
      esac ;;
    *) return 1 ;;
  esac
}

# `arikan database backup|restore`: the panel's own backup tool, which is what the script
# calls (the panel image holds it). The path it is given is the one inside the container,
# where the panel's data directory is mounted at /data/panel — so that is where the file goes.
hf_backup() {
  local name=${1:-backup.dump}
  local host=$name
  case $name in
    /data/*) host=$HF_MIKAN/data/${name#/data/} ;;
  esac
  mkdir -p "$(dirname "$host")"
  { printf 'PGDMP'; printf 'users=%s\n' "$(awk -F= '$1=="users"{print $2}' "$HF_LOG/counts")"; } >"$host"
}
hf_restore() {
  local path=${1:-} host=$path
  case $path in
    /data/*) host=$HF_MIKAN/data/${path#/data/} ;;
  esac
  [ -f "$host" ] || { echo "restore: $path: no such backup" >&2; return 1; }
  printf 'tariffs=2\npayments=5\nusers=3\n' >"$HF_LOG/counts"
  echo "restored"
}

# psql in the panel's container and in the database's: the same answers.
hf_psql() {
  local q=${@: -1}
  # What the panel's own migrations left behind when the forked release was started.
  local schema=${HF_SCHEMA:-31} referral=${HF_REFERRAL_TABLES:-5} campaign=${HF_CAMPAIGN_TABLES:-3}
  if [ -f "$HF_LOG/migrated" ]; then
    schema=$(sed -n 's/^schema=//p' "$HF_LOG/migrated"); schema=${schema:-31}
    referral=$(sed -n 's/^referral=//p' "$HF_LOG/migrated"); referral=${referral:-5}
    campaign=$(sed -n 's/^campaign=//p' "$HF_LOG/migrated"); campaign=${campaign:-3}
  fi
  case $q in
    *goose_db_version*) echo "$schema" ;;
    *"FROM users"*) awk -F= '$1=="users"{print $2}' "$HF_LOG/counts" ;;
    *"FROM tariffs"*) awk -F= '$1=="tariffs"{print $2}' "$HF_LOG/counts" ;;
    *"FROM payments"*) awk -F= '$1=="payments"{print $2}' "$HF_LOG/counts" ;;
    *information_schema.tables*referral*) echo "$referral" ;;
    *information_schema.tables*campaign*) echo "$campaign" ;;
    *"FROM settings"*)
      local key
      key=$(printf '%s' "$q" | sed -n "s/.*key='\([^']*\)'.*/\1/p")
      awk -F= -v k="$key" '$1==k{print $2}' "$HF_SETTINGS_DB" ;;
    *"UPDATE settings"*)
      local key val
      key=$(printf '%s' "$q" | sed -n "s/.*key='\([^']*\)'.*/\1/p")
      val=$(printf '%s' "$q" | sed -n "s/.*value='\([^']*\)'.*/\1/p")
      { grep -v "^$key=" "$HF_SETTINGS_DB" || true; printf '%s=%s\n' "$key" "$val"; } >"$HF_SETTINGS_DB.new"
      mv "$HF_SETTINGS_DB.new" "$HF_SETTINGS_DB"
      echo "UPDATE 1" ;;
    *) echo "" ;;
  esac
  return 0
}

# `docker run --rm --entrypoint mikan IMAGE version`: the version an image reports.
hf_docker_run() {
  local image="" args=("$@") i
  for ((i = 0; i < ${#args[@]}; i++)); do
    case ${args[$i]} in
      --rm|--entrypoint) [[ ${args[$i]} == --entrypoint ]] && i=$((i+1)) ;;
      -*) ;;
      *) image=${args[$i]}; break ;;
    esac
  done
  [[ $image == *"$HF_DIGEST"* || $image == "$HF_IMAGE" ]] && { echo "$HF_VERSION"; return 0; }
  [[ $image == "$HF_STOCK_IMAGE" ]] && { echo "$HF_STOCK_VERSION"; return 0; }
  echo "unknown image $image" >&2
  return 1
}

hf_image_inspect() {
  local image=$1
  [[ $image == *"$HF_DIGEST"* || $image == *"$HF_STOCK_IMAGE"* || $image == *miroshka000* ]] && return 0
  # The image is on the server only if it was pulled: that is what the second run sees.
  [[ -f $HF_LOG/pulled-$(printf '%s' "$image" | tr -c 'a-zA-Z0-9' '_') ]]
}
hf_pull() {
  local image=$1
  [[ $image == *o0hhs/arikan* ]] || { echo "denied: $image" >&2; return 1; }
  : >"$HF_LOG/pulled-$(printf '%s' "$image" | tr -c 'a-zA-Z0-9' '_')"
  echo "Pulled $image"
}

# ───────────────────────────── the fake host ─────────────────────────────
hf_systemctl() {
  local action=$1; shift || true
  local unit name
  for unit in "$@"; do
    [[ $unit == -* ]] && continue
    name=${unit%.timer}
    case $action in
      enable)
        : >"$HF_SYSTEMD/$unit" ;;
      disable)
        rm -f "$HF_SYSTEMD/$unit" ;;
      is-enabled)
        [[ -f $HF_SYSTEMD/$unit ]] || return 1 ;;
      daemon-reload) ;;
      *) return 0 ;;
    esac
  done
  return 0
}

# The release: index, signature, manifest and the command the script downloads. One copy per
# harness run, so the manifest's sha256 is the sha256 of what the run actually gets.
hf_write_release() {
  HF_RELEASE=$HF_ROOT/release
  mkdir -p "$HF_RELEASE"
  printf '{"version":"%s","channel":"stable"}\n' "$HF_VERSION" >"$HF_RELEASE/index.json"
  # A real 64-byte signature: the script checks its size before openssl, and openssl here is
  # a stub that accepts it (what is under test is the installer, not the release format).
  hf_sig64 >"$HF_RELEASE/index.json.sig"
  cat >"$HF_RELEASE/cmd.sh" <<CMD
#!/bin/sh
# The fork's command, as a release ships it: it says its name and does its own post-update
# (it is what puts the units and compose.yaml of its release in place). POSIX sh, so it runs
# on the CI images this test is run on as well as on a server.
case \${1:-} in
  --version) echo "arikan $HF_VERSION" ;;
  post-update)
    # Unset HF_POST_UPDATE: the release puts down the files of the server's role.
    mode=\${HF_POST_UPDATE:-}
    [ -n "\$mode" ] || mode=\$([ "\$(sed -n 's/^MIKAN_MODE=//p' "\$HF_MIKAN/.env")" = node ] && echo install-node || echo install-panel)
    printf 'post-update %s\n' "\$mode" >>"\$HF_LOG/cmd.log"
    case \$mode in
      install-node)
        cat >"\$HF_MIKAN/compose.yaml" <<'YAML'
name: mikan

services:
  node:
    image: \${MIKAN_IMAGE}
    entrypoint: ["/usr/local/bin/arikan-node"]
YAML
        : >"\$HF_SYSTEMD/arikan-update.timer"
        : >"\$HF_SYSTEMD/arikan-update-request.path"
        rm -f "\$HF_SYSTEMD/mikan-update.timer" "\$HF_SYSTEMD/mikan-update-request.path"
        ;;
      install-panel)
        cat >"\$HF_MIKAN/compose.yaml" <<'YAML'
name: mikan

services:
  panel:
    image: \${MIKAN_IMAGE}
  node:
    image: \${MIKAN_IMAGE}
  postgres:
    image: postgres
YAML
        : >"\$HF_SYSTEMD/arikan-update.timer"
        : >"\$HF_SYSTEMD/arikan-update-request.path"
        printf 'PathExists=%s\n' "\$HF_MIKAN/data/panel/update/request" >"\$HF_SYSTEMD/arikan-update-request.path"
        rm -f "\$HF_SYSTEMD/mikan-update.timer" "\$HF_SYSTEMD/mikan-update-request.path"
        ;;
      nothing) ;;
      fail) echo "post-update: cannot" >&2; exit 1 ;;
    esac
    ;;
  *) : ;;
esac
CMD
  chmod +x "$HF_RELEASE/cmd.sh"
  local sha
  sha=$(hf_sha256 "$HF_RELEASE/cmd.sh")
  cat >"$HF_RELEASE/manifest.json" <<JSON
{"version":"$HF_VERSION","image":"ghcr.io/o0hhs/arikan","digest":"$HF_DIGEST","x86_64":{"url":"https://github.com/o0hhs/arikan/releases/download/v$HF_VERSION/arikan-x86_64","sha256":"$sha"},"aarch64":{"url":"https://github.com/o0hhs/arikan/releases/download/v$HF_VERSION/arikan-aarch64","sha256":"$sha"}}
JSON
  printf 'c2ln\n' >"$HF_RELEASE/manifest.json.sig"
  hf_sig64 >"$HF_RELEASE/manifest.json.sig"
  export HF_RELEASE
}

# sha256 of a file, whichever tool the host has.
hf_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

# A 64-byte signature, base64: what the script decodes and measures before openssl.
hf_sig64() { printf '%064d' 0 | base64 -w0; echo; }

# ───────────────────────────── the fake path and host tools ─────────────────────────────
# Everything the script runs comes from here: docker, curl, openssl, systemctl, and install/ln
# so that the command the script puts in /usr/local/bin lands in this run's own directory
# instead (the tests must not touch — and cannot write — the real one). What is under test is
# the script's logic, not the path it writes to.
hf_make_bin() {
  HF_FAKEBIN=$HF_ROOT/fakebin
  mkdir -p "$HF_FAKEBIN"

  for tool in install ln mv rm; do
    cat >"$HF_FAKEBIN/$tool" <<SH
#!/usr/bin/env bash
# The real tool, by absolute path (the caller may name an absolute one).
for dir in /usr/bin /bin /usr/local/bin; do
  [ -x "\$dir/$tool" ] && { real="\$dir/$tool"; break; }
done
: "\${real:?}"
args=()
for a in "\$@"; do
  case \$a in
    /usr/local/bin/*) args+=("\${a/\\/usr\\/local\\/bin/$HF_BIN}") ;;
    *) args+=("\$a") ;;
  esac
done
exec "\$real" "\${args[@]}"
SH
  done

  cat >"$HF_FAKEBIN/docker" <<'SH'
#!/usr/bin/env bash
source "$HF_HARNESS"
hf_docker "$@"
SH

  cat >"$HF_FAKEBIN/systemctl" <<'SH'
#!/usr/bin/env bash
source "$HF_HARNESS"
hf_systemctl "$@"
SH

  # curl: the release files, and nothing else. -o is where the bytes go; anything else that
  # looks like a URL is the one being fetched.
  cat >"$HF_FAKEBIN/curl" <<'SH'
#!/usr/bin/env bash
source "$HF_HARNESS"
out=""
url=""
expect_out=0
for a in "$@"; do
  if [[ $expect_out == 1 ]]; then out=$a; expect_out=0; continue; fi
  case $a in
    -o|--output) expect_out=1 ;;
    -*) ;;
    *) url=$a ;;
  esac
done
case $url in
  *index.json.sig) src=$HF_RELEASE/index.json.sig ;;
  *index.json) src=$HF_RELEASE/index.json ;;
  *manifest.json.sig) src=$HF_RELEASE/manifest.json.sig ;;
  *manifest.json) src=$HF_RELEASE/manifest.json ;;
  *arikan-x86_64|*arikan-aarch64|*arikan) src=$HF_RELEASE/cmd.sh ;;
  *) echo "curl: (22) $url: not found" >&2; exit 22 ;;
esac
if [[ -n $out ]]; then /bin/cp "$src" "$out"; else /bin/cat "$src"; fi
SH

  # openssl: enough of it for the script's version check and its signature check. Every
  # signature is accepted: what is under test is the installer, not the release format.
  cat >"$HF_FAKEBIN/openssl" <<'SH'
#!/usr/bin/env bash
case ${1:-} in
  version) echo "OpenSSL 3.0.13 30 Jan 2024" ;;
  pkeyutl) exit 0 ;;
  *) exit 0 ;;
esac
SH

  # The two checks of the host itself: id (the script insists on root) and uname (the
  # architecture of the release it takes). The host's own commands answer.
  cat >"$HF_FAKEBIN/id" <<'SH'
#!/usr/bin/env bash
[[ ${1:-} == -u ]] && { echo 0; exit 0; }
exec /usr/bin/id "$@"
SH
  cat >"$HF_FAKEBIN/uname" <<'SH'
#!/usr/bin/env bash
[[ ${1:-} == -m ]] && { echo x86_64; exit 0; }
exec /usr/bin/uname "$@"
SH

  # timeout, when the host has none (macOS): the stack comes up at once here anyway. Every
  # form the script uses is understood, flags included.
  cat >"$HF_FAKEBIN/timeout" <<'SH'
#!/usr/bin/env bash
while [[ ${1:-} == -* ]]; do
  [[ $1 == -s || $1 == --signal ]] && shift
  shift
done
shift
exec "$@"
SH

  chmod +x "$HF_FAKEBIN"/*
}

# ───────────────────────────── running the script ─────────────────────────────
# run_install_fork [ключи…]: scripts/install-fork.sh, with this harness's PATH and files.
# Sets HF_RC and HF_OUT. The script is given a short leash: a test that hangs is a failing
# test, not a suite that never ends (the real script waits for a database, not for magic).
run_install_fork() {
  local script=$SCRIPTS_DIR/scripts/install-fork.sh
  HF_OUT=$HF_ROOT/install.out
  env HF_HARNESS="${HF_HARNESS:-$HF_DIR/harness.sh}" HF_ROOT=$HF_ROOT HF_MIKAN=$HF_MIKAN HF_LOG=$HF_LOG \
    HF_SYSTEMD=$HF_SYSTEMD HF_RELEASE=$HF_RELEASE HF_POST_UPDATE=$HF_POST_UPDATE \
    MIKAN_DIR=$HF_MIKAN MIKAN_FORK_REEXEC=1 PATH="$HF_FAKEBIN:$PATH" \
    BIN_DIR=$HF_BIN SYSTEMD_DIR=$HF_SYSTEMD \
    WAIT_SECONDS=${WAIT_SECONDS:-30} SCHEMA_WAIT=${SCHEMA_WAIT:-6} \
    timeout --signal=KILL "${HF_RUN_LIMIT:-60}" bash "$script" "$@" >"$HF_OUT" 2>&1
  HF_RC=$?
  return 0
}

# What the run said, for a test's message. On the terminal: cases run inside a command
# substitution, so their stdout belongs to the assertion, not to the reader.
hf_log() { sed 's/^/    /' "$HF_OUT" >&2; }
hf_state() { sed -n "s/^$1=//p" "$HF_MIKAN/.env" | head -n1; }
