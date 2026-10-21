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
#   RUNTIME_ID=<uuid> ./setup.sh        # runtime for leader + product + reviewer
#   CODE_RUNTIME_ID=<uuid> ./setup.sh   # runtime for the developer (default: RUNTIME_ID)
#   MAX_ROUNDS=5 ./setup.sh             # rework rounds before escalation (default 3)
#   MODEL_COORD="" MODEL_CODE="" ./setup.sh   # skip model pinning (server default)
#   SQUAD_NAME="Release Pipeline" ./setup.sh
#
# Design note on runtime split (same pattern as bugloop / upgradeloop):
#   Coord runtime (leader + product + reviewer): these agents reason, dispatch,
#   and verify. They almost never run heavy tooling (no compile, no git push),
#   so they share a single runtime and benefit from a capable reasoning model.
#   Code runtime (developer): writes code, runs builds, executes tests. This is
#   the heavy-worker role — give it the strongest coding model you have, and a
#   separate runtime if compilation costs would otherwise starve the coord tier.
#
# Run once per workspace: re-running creates a second set of agents.

set -euo pipefail
cd "$(dirname "$0")"

MULTICA="${MULTICA:-multica}"
MAX_ROUNDS="${MAX_ROUNDS:-3}"
SQUAD_NAME="${SQUAD_NAME:-Dev Loop}"
RUNTIME_ID="${RUNTIME_ID:-}"
CODE_RUNTIME_ID="${CODE_RUNTIME_ID:-}"
# Known-good pairing; ${VAR-word} keeps an explicit empty value (skip --model).
MODEL_COORD="${MODEL_COORD-opencode/mimo-v2.6-flash-free}"
MODEL_CODE="${MODEL_CODE-glm-5.3-flash}"

case "$MAX_ROUNDS" in
  ''|*[!0-9]*) echo "MAX_ROUNDS must be a positive integer, got '$MAX_ROUNDS'" >&2; exit 1 ;;
esac
if [ "$MAX_ROUNDS" -lt 1 ]; then
  echo "MAX_ROUNDS must be >= 1, got '$MAX_ROUNDS'" >&2; exit 1
fi

command -v "$MULTICA" >/dev/null 2>&1 || { echo "multica CLI not found in PATH" >&2; exit 1; }

UUID_RE='[0-9a-f]\{8\}-[0-9a-f]\{4\}-[0-9a-f]\{4\}-[0-9a-f]\{4\}-[0-9a-f]\{12\}'

# -- runtimes ----------------------------------------------------------------
if [ -z "$RUNTIME_ID" ]; then
  RUNTIME_ID="$("$MULTICA" runtime list --output json 2>/dev/null \
    | grep -oE '"id": ?"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"' \
    | head -1 \
    | grep -oE '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' || true)"
fi
[ -n "$RUNTIME_ID" ] || { echo "no runtime found; pass RUNTIME_ID=<uuid> (see 'multica runtime list')" >&2; exit 1; }
[ -n "$CODE_RUNTIME_ID" ] || CODE_RUNTIME_ID="$RUNTIME_ID"
echo "coord runtime: $RUNTIME_ID"
echo "code  runtime: $CODE_RUNTIME_ID"

# -- helpers ----------------------------------------------------------------
create_agent() { # create_agent <name> <description> <runtime-id> <model> -> prints the new agent uuid
  local name="$1" desc="$2" runtime="$3" model="$4" out id
  local cmd=(
    "$MULTICA" agent create
    --name "$name"
    --description "$desc"
    --instructions "Provisioning — final instructions are set in the next step."
    --runtime-id "$runtime"
    --permission-mode public_to
    --public-to-workspace
  )
  if [ -n "$model" ]; then
    cmd+=(--model "$model")
  fi
  cmd+=(--output table)
  out="$("${cmd[@]}")"
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
# Coord tier: leader (dispatch/rounds), product (requirements/verdict),
# reviewer (independent code review). All reasoning-heavy, light tool use.
LEADER_ID="$(create_agent devloop-leader "Coordinates the dev loop squad: dispatch, rounds, stand-down." "$RUNTIME_ID" "$MODEL_COORD")"
PRODUCT_ID="$(create_agent devloop-product "Product manager and QA for the dev loop squad: explore, verify, verdict." "$RUNTIME_ID" "$MODEL_COORD")"
REVIEWER_ID="$(create_agent devloop-reviewer "Independent code reviewer for the dev loop squad." "$RUNTIME_ID" "$MODEL_COORD")"

# Code tier: developer does the actual implementation — heaviest tool use
# (compiles, runs tests, pushes branches). Give it the coding model.
DEVELOPER_ID="$(create_agent devloop-developer "Implements dispatched changes for the dev loop squad." "$CODE_RUNTIME_ID" "$MODEL_CODE")"

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
  coord runtime: $RUNTIME_ID  (leader + product + reviewer)
  code  runtime: $CODE_RUNTIME_ID  (developer)
  rounds:   $MAX_ROUNDS (product_agent=$PRODUCT_ID)

  leader:   devloop-leader    $LEADER_ID      model: ${MODEL_COORD:-server default}
  product:  devloop-product   $PRODUCT_ID     model: ${MODEL_COORD:-server default}
  reviewer: devloop-reviewer  $REVIEWER_ID    model: ${MODEL_COORD:-server default}
  dev:      devloop-developer $DEVELOPER_ID   model: ${MODEL_CODE:-server default}

Start an iteration by filing an issue assigned to the squad (run from this
directory so --description-file resolves):

  multica issue create \\
    --title "<iteration direction title>" \\
    --description-file issue-template.md \\
    --assignee-id $SQUAD_ID

Assigning the issue wakes the leader; from there the loop runs unattended.
EOF
