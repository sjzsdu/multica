#!/usr/bin/env bash
# Bootstraps a self-hosted Multica instance from zero into a ready-to-use
# workspace with the engineering loop squads.
#
# Steps:
#   1. Ensure the `multica` CLI is installed and logged in.
#   2. Create a workspace (or reuse one you already have).
#   3. Confirm a runtime is registered — loop squads need agents, and every
#      agent must be bound to a runtime (a running `multica daemon`).
#   4. Provision the selected squad presets from docs/engineering/*-squad.
#
# Prerequisites:
#   - Multica server is running and reachable.
#   - `multica` CLI is installed and authenticated
#     (`multica login` or a token in MULTICA_API_TOKEN).
#   - At least one `multica daemon` connected to this workspace before you run
#     the script (runtimes self-register; the server does not create them).
#     See Usage notes below.
#
# Usage:
#   # Minimal — just a workspace, no squads:
#   ./scripts/bootstrap-squads.sh --slug my-team --name "My Team"
#
#   # One squad:
#   ./scripts/bootstrap-squads.sh --slug my-team devloop
#
#   # All four loop squads (the default when no squad args are given):
#   ./scripts/bootstrap-squads.sh --slug my-team
#
#   # Reuse an existing workspace (skip creation, still provisions squads):
#   ./scripts/bootstrap-squads.sh --slug my-team devloop bugloop
#
#   # Explicit workspace ID — useful when the slug is hard to grep:
#   ./scripts/bootstrap-squads.sh --workspace-id <uuid> devloop
#
#   MAX_ROUNDS=5 ./scripts/bootstrap-squads.sh --slug my-team devloop
#   RUNTIME_ID=<uuid> ./scripts/bootstrap-squads.sh --slug my-team refactorloop
#   MODEL_COORD="" MODEL_CODE="" ./scripts/bootstrap-squads.sh --slug my-team bugloop
#
# Squad aliases: devloop, bugloop, refactorloop, upgradeloop, all.
# `all` is the default — provision every squad preset in docs/engineering.
#
# Daemon setup (before you run this script):
#   MULTICA_SERVER_URL="https://your-server.example.com" \
#   MULTICA_API_TOKEN="$(multica auth token)" \
#   multica daemon start
#
#   The daemon registers itself as a runtime in whatever workspace the CLI is
#   currently scoped to (`multica workspace switch <slug>` first, if needed).
#   `multica runtime list` shows the registration once it's alive.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
SQUADS_DIR="$ROOT_DIR/docs/engineering"
MULTICA="${MULTICA:-multica}"
UUID_RE='[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}'

squad_dir_for() {
  local key="$1"
  case "$key" in
    devloop)     echo "devloop-squad" ;;
    bugloop)     echo "bugloop-squad" ;;
    refactorloop) echo "refactorloop-squad" ;;
    upgradeloop) echo "upgradeloop-squad" ;;
    *) echo "" ;;
  esac
}

VALID_SQUADS="devloop bugloop refactorloop upgradeloop"

# -- argparse ---------------------------------------------------------------

usage() {
  sed -n '2,22p' "$0" | sed 's/^# \?//' >&2
  exit 1
}

WORKSPACE_SLUG=""
WORKSPACE_NAME=""
WORKSPACE_ID=""
SKIP_RUNTIME_CHECK=""
REQUESTED_SQUADS=()

while [ $# -gt 0 ]; do
  case "$1" in
    --slug)       WORKSPACE_SLUG="$2"; shift 2 ;;
    --name)       WORKSPACE_NAME="$2"; shift 2 ;;
    --workspace-id) WORKSPACE_ID="$2"; shift 2 ;;
    --skip-runtime-check) SKIP_RUNTIME_CHECK=1; shift ;;
    -h|--help)    usage ;;
    --*)          echo "unknown flag: $1" >&2; usage ;;
    *)            REQUESTED_SQUADS+=("$1"); shift ;;
  esac
done

# -- CLI readiness ---------------------------------------------------------

if ! command -v "$MULTICA" >/dev/null 2>&1; then
  echo "error: $MULTICA CLI not found in PATH" >&2
  echo "install it from the Multica repo (make cli) or the release binary" >&2
  exit 1
fi

if ! "$MULTICA" auth status >/dev/null 2>&1; then
  echo "error: multica CLI is not authenticated" >&2
  echo "run 'multica login' or set MULTICA_API_TOKEN" >&2
  exit 1
fi

# -- workspace -------------------------------------------------------------

extract_uuid_from_json() {
  # extract_uuid_from_json <text>
  # Greps the first "id" UUID from a JSON blob. Accepts both `"id": "xxx"` and
  # `"id":"xxx"` (no space after colon) — JSON emitters are not consistent.
  local text="$1"
  printf '%s' "$text" \
    | grep -oE '"id"[[:space:]]*:[[:space:]]*"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"' \
    | head -1 \
    | grep -oE "$UUID_RE" || true
}

find_workspace_by_slug() {
  # find_workspace_by_slug <slug> → prints UUID or nothing
  local target="$1" list_out
  list_out="$("$MULTICA" workspace list --output json 2>/dev/null || true)"
  [ -z "$list_out" ] && return 0

  # Multica slugs are stored lowercase in the DB (and the server's unique
  # constraint is case-insensitive). Normalise the user arg before matching.
  target="$(printf '%s' "$target" | tr '[:upper:]' '[:lower:]')"

  if command -v python3 >/dev/null 2>&1; then
    TARGET_SLUG="$target" python3 -c "
import json, os, sys
try:
    arr = json.loads(sys.stdin.read())
    t = os.environ['TARGET_SLUG'].lower()
    for ws in arr:
        if ws.get('slug','').lower() == t:
            print(ws.get('id',''))
            break
except Exception:
    pass
" <<< "$list_out"
    return 0
  fi

  # Fallback: rough grep window.
  local window matched_id
  window="$(printf '%s' "$list_out" \
    | grep -B30 -A1 "\"slug\": \"$target\"" || true)"
  matched_id="$(extract_uuid_from_json "$window")"
  printf '%s' "$matched_id"
}

ensure_workspace() {
  if [ -n "$WORKSPACE_ID" ]; then
    # User handed us a UUID directly — trust it.
    echo "workspace id provided: $WORKSPACE_ID" >&2
    MULTICA_WORKSPACE_ID="$WORKSPACE_ID"
    "$MULTICA" config set workspace_id "$WORKSPACE_ID" 2>/dev/null || true
    return
  fi

  if [ -z "$WORKSPACE_SLUG" ]; then
    echo "error: --slug is required (or pass --workspace-id)" >&2
    usage
  fi

  # Multica treats slugs as lowercase in the DB and enforces case-insensitive
  # uniqueness; normalise the user-supplied slug upfront so both lookup and
  # create use the canonical form.
  WORKSPACE_SLUG="$(printf '%s' "$WORKSPACE_SLUG" | tr '[:upper:]' '[:lower:]')"

  # Try to find an existing workspace with this slug before creating one.
  local existing_id
  existing_id="$(find_workspace_by_slug "$WORKSPACE_SLUG")"

  if [ -n "$existing_id" ]; then
    echo "workspace '$WORKSPACE_SLUG' already exists ($existing_id), skipping create" >&2
    WORKSPACE_ID="$existing_id"
  else
    local name="${WORKSPACE_NAME:-$WORKSPACE_SLUG}"
    echo "creating workspace '$name' (slug: $WORKSPACE_SLUG)..." >&2
    local create_out
    create_out="$("$MULTICA" workspace create \
      --name "$name" --slug "$WORKSPACE_SLUG" --output json 2>&1)" || {
      echo "failed to create workspace:" >&2
      printf '%s\n' "$create_out" >&2
      exit 1
    }
    WORKSPACE_ID="$(extract_uuid_from_json "$create_out")"
    if [ -z "$WORKSPACE_ID" ]; then
      echo "could not parse workspace id from create output:" >&2
      printf '%s\n' "$create_out" >&2
      exit 1
    fi
    echo "workspace created: $WORKSPACE_ID" >&2
  fi

  MULTICA_WORKSPACE_ID="$WORKSPACE_ID"
  "$MULTICA" config set workspace_id "$WORKSPACE_ID" 2>/dev/null || true
}

ensure_workspace
export MULTICA_WORKSPACE_ID="$WORKSPACE_ID"
"$MULTICA" workspace switch "$WORKSPACE_SLUG" 2>/dev/null || true

# -- runtime ---------------------------------------------------------------

echo "checking for runtimes in workspace..." >&2
runtime_out="$("$MULTICA" runtime list --output json 2>/dev/null || true)"
runtime_id="$(extract_uuid_from_json "$runtime_out")"

if [ -z "$runtime_id" ]; then
  cat >&2 <<EOF

⚠️  没有在 workspace $WORKSPACE_SLUG ($WORKSPACE_ID) 里找到 runtime。

Loop squads 的每个 agent 都必须绑定到一个 runtime。runtime 是 `multica daemon`
启动后主动向服务器注册的——服务器自己不会创建。

在你的架构里（Traefik + Docker 部署），daemon 应该跑在**本地开发机上**，
而不是服务器容器里。daemon 需要完整的本地工具链（git、编译器、CLI）。

  # 1. 先构建 CLI（在你本地 Mac 上，不是服务器）
  cd /path/to/multica
  make cli

  # 2. 指向你的服务器
  ./multica login --server-url https://multica.wnbgj.com

  # 3. 启动 daemon（自动注册为 runtime）
  ./multica daemon start

  # 4. 确认 runtime 已上线
  ./multica runtime list

之后重跑本脚本即可。或者加上 --skip-runtime-check 先只创建 workspace、
等 daemon 连上再补跑 squad provision。

EOF
  if [ "${SKIP_RUNTIME_CHECK:-}" = "1" ]; then
    echo "--skip-runtime-check: 继续，但 squad provision 会失败。" >&2
    runtime_id=""
  else
    exit 1
  fi
else
  echo "runtime ready: $runtime_id" >&2
  echo
fi

# -- squad provision -------------------------------------------------------

if [ ${#REQUESTED_SQUADS[@]} -eq 0 ]; then
  REQUESTED_SQUADS=(all)
fi

resolve_squads() {
  local resolved=()
  local key
  for key in "$@"; do
    case "$key" in
      all)
        resolved+=(devloop bugloop refactorloop upgradeloop) ;;
      devloop|bugloop|refactorloop|upgradeloop)
        resolved+=("$key") ;;
      *)
        echo "unknown squad: $key (valid: $VALID_SQUADS, all)" >&2
        exit 1 ;;
    esac
  done
  # de-dup preserving order
  printf '%s\n' "${resolved[@]}" | awk '!seen[$0]++'
}

resolved_squads=()
while IFS= read -r sq; do
  resolved_squads+=("$sq")
done < <(resolve_squads "${REQUESTED_SQUADS[@]}")

provision_one() {
  local key="$1"
  local dir_name setup squad_name
  dir_name="$(squad_dir_for "$key")"
  if [ -z "$dir_name" ]; then
    echo "skipping unknown squad: $key" >&2
    return
  fi
  setup="$SQUADS_DIR/$dir_name/setup.sh"

  if [ ! -x "$setup" ]; then
    echo "skipping $key: $setup not found or not executable" >&2
    return
  fi

  # Default squad name per preset — matches what setup.sh would create.
  case "$key" in
    devloop)     squad_name="${SQUAD_NAME:-Dev Loop}" ;;
    bugloop)     squad_name="${SQUAD_NAME:-Bug Loop}" ;;
    refactorloop) squad_name="${SQUAD_NAME:-Refactor Loop}" ;;
    upgradeloop) squad_name="${SQUAD_NAME:-Upgrade Loop}" ;;
  esac

  # Skip if a squad with this name already exists in the workspace — the
  # underlying setup.sh is not idempotent and would fail on the agent-name
  # collision. --force re-runs regardless (will still fail on collision).
  if [ -z "${FORCE:-}" ]; then
    local existing
    existing="$("$MULTICA" squad list 2>/dev/null \
      | grep -i -F "$squad_name" || true)"
    if [ -n "$existing" ]; then
      echo "skipping $key: '$squad_name' already exists in this workspace (use FORCE=1 to re-run)" >&2
      return
    fi
  fi

  echo "────────────────────────────────────────────────────" >&2
  echo "provisioning $key ..." >&2
  echo "────────────────────────────────────────────────────" >&2
  (
    export MULTICA RUNTIME_ID="${RUNTIME_ID:-$runtime_id}"
    # User-supplied MAX_ROUNDS / MODEL_COORD / MODEL_CODE / SQUAD_NAME pass
    # through automatically if they're already in the environment.
    bash "$setup"
  ) || {
    echo "failed to provision $key" >&2
    exit 1
  }
  echo
}

for sq in "${resolved_squads[@]}"; do
  provision_one "$sq"
done

# -- done ------------------------------------------------------------------

cat <<EOF

All done. Workspace: $WORKSPACE_SLUG ($WORKSPACE_ID)

You're ready to kick off your first iteration. For example (devloop):

  multica issue create \\
    --title "Add inline comments to the issue detail view" \\
    --description-file $SQUADS_DIR/devloop-squad/issue-template.md \\
    --assignee-id <squad-id from the devloop output above>

Assigning the issue wakes the leader; the loop runs unattended from there.

EOF
