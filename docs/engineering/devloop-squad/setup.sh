#!/usr/bin/env bash
# Provision the dev loop squad preset: four agents (leader, product, developer,
# reviewer) plus a squad whose policy enables the server-enforced verdict loop.
#
# Prerequisites:
#   - a running Multica server and a logged-in `multica` CLI with workspace
#     context (--workspace-id, MULTICA_WORKSPACE_ID, or `multica config set
#     workspace_id <id>`)
#   - at least one runtime in that workspace (agents run on it)
#
# Usage:
#   ./setup.sh                          # picks the first runtime from the list
#   RUNTIME_ID=<uuid> ./setup.sh        # explicit runtime
#   MAX_ROUNDS=5 ./setup.sh             # rework rounds before escalation (default 3)
#   SQUAD_NAME="Release Pipeline" ./setup.sh
#
# Run once per workspace: re-running creates a second set of agents.
set -euo pipefail
cd "$(dirname "$0")"

MULTICA="${MULTICA:-multica}"
MAX_ROUNDS="${MAX_ROUNDS:-3}"
SQUAD_NAME="${SQUAD_NAME:-Dev Loop}"
RUNTIME_ID="${RUNTIME_ID:-}"

case "$MAX_ROUNDS" in
  ''|*[!0-9]*) echo "MAX_ROUNDS must be a positive integer, got '$MAX_ROUNDS'" >&2; exit 1 ;;
esac
if [ "$MAX_ROUNDS" -lt 1 ]; then
  echo "MAX_ROUNDS must be >= 1, got '$MAX_ROUNDS'" >&2; exit 1
fi

command -v "$MULTICA" >/dev/null 2>&1 || { echo "multica CLI not found in PATH" >&2; exit 1; }

UUID_RE='[0-9a-f]\{8\}-[0-9a-f]\{4\}-[0-9a-f]\{4\}-[0-9a-f]\{4\}-[0-9a-f]\{12\}'

# -- runtime ----------------------------------------------------------------
if [ -z "$RUNTIME_ID" ]; then
  RUNTIME_ID="$("$MULTICA" runtime list --output json 2>/dev/null \
    | grep -oE '"id": ?"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"' \
    | head -1 \
    | grep -oE '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' || true)"
fi
[ -n "$RUNTIME_ID" ] || { echo "no runtime found; pass RUNTIME_ID=<uuid> (see 'multica runtime list')" >&2; exit 1; }
echo "runtime: $RUNTIME_ID"

# -- helpers ----------------------------------------------------------------
create_agent() { # create_agent <name> <description> -> prints the new agent uuid
  local name="$1" desc="$2" out id
  out="$("$MULTICA" agent create \
    --name "$name" \
    --description "$desc" \
    --instructions "Provisioning — final instructions are set in the next step." \
    --runtime-id "$RUNTIME_ID" \
    --permission-mode public_to \
    --public-to-workspace \
    --output table)"
  id="$(printf '%s\n' "$out" | sed -n "s/.*(\($UUID_RE\)).*/\1/p" | head -1)"
  if [ -z "$id" ]; then
    echo "failed to create agent '$name':" >&2
    printf '%s\n' "$out" >&2
    exit 1
  fi
  echo "created agent $name ($id)" >&2
  printf '%s\n' "$id"
}

add_member() { # add_member <squad-id> <agent-id>
  "$MULTICA" squad member add "$1" --member-id "$2" --type agent --role member >/dev/null
}

# -- agents -----------------------------------------------------------------
LEADER_ID="$(create_agent devloop-leader "Coordinates the dev loop squad: dispatch, rounds, stand-down.")"
PRODUCT_ID="$(create_agent devloop-product "Product manager and QA for the dev loop squad: explore, verify, verdict.")"
DEVELOPER_ID="$(create_agent devloop-developer "Implements dispatched changes for the dev loop squad.")"
REVIEWER_ID="$(create_agent devloop-reviewer "Independent code reviewer for the dev loop squad.")"

# -- squad ------------------------------------------------------------------
squad_out="$("$MULTICA" squad create \
  --name "$SQUAD_NAME" \
  --description "Unattended feature-iteration squad: product explores and verdicts, developer implements, reviewer reviews." \
  --leader "$LEADER_ID" \
  --pipeline-max-rounds "$MAX_ROUNDS" \
  --pipeline-product-agent "$PRODUCT_ID" \
  --output table)"
SQUAD_ID="$(printf '%s\n' "$squad_out" | sed -n "s/.*(\($UUID_RE\)).*/\1/p" | head -1)"
if [ -z "$SQUAD_ID" ]; then
  echo "failed to create squad:" >&2
  printf '%s\n' "$squad_out" >&2
  exit 1
fi
echo "created squad $SQUAD_NAME ($SQUAD_ID)" >&2

add_member "$SQUAD_ID" "$PRODUCT_ID"
add_member "$SQUAD_ID" "$DEVELOPER_ID"
add_member "$SQUAD_ID" "$REVIEWER_ID"

# -- instructions (need every id before they can reference each other) -------
render() { # render <file>
  sed -e "s|{{LEADER_ID}}|$LEADER_ID|g" \
      -e "s|{{PRODUCT_ID}}|$PRODUCT_ID|g" \
      -e "s|{{DEVELOPER_ID}}|$DEVELOPER_ID|g" \
      -e "s|{{REVIEWER_ID}}|$REVIEWER_ID|g" \
      -e "s|{{SQUAD_ID}}|$SQUAD_ID|g" \
      "$1"
}

update_instructions() { # update_instructions <agent-id> <file>
  "$MULTICA" agent update "$1" --instructions "$(render "$2")" >/dev/null
  echo "set instructions from $2" >&2
}

update_instructions "$LEADER_ID" instructions/leader.md
update_instructions "$PRODUCT_ID" instructions/product.md
update_instructions "$DEVELOPER_ID" instructions/developer.md
update_instructions "$REVIEWER_ID" instructions/reviewer.md

# -- summary ----------------------------------------------------------------
cat <<EOF

Dev Loop squad ready.

  squad:    $SQUAD_NAME  $SQUAD_ID
  leader:   devloop-leader    $LEADER_ID
  product:  devloop-product   $PRODUCT_ID
  dev:      devloop-developer $DEVELOPER_ID
  reviewer: devloop-reviewer  $REVIEWER_ID
  rounds:   $MAX_ROUNDS (product_agent=$PRODUCT_ID)

Start an iteration by filing an issue assigned to the squad (run from this
directory so --description-file resolves):

  multica issue create \\
    --title "<iteration direction title>" \\
    --description-file issue-template.md \\
    --assignee-id $SQUAD_ID

Assigning the issue wakes the leader; from there the loop runs unattended.
EOF
