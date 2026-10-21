#!/usr/bin/env bash
# Provision the refactor loop squad preset: four agents (leader, architect,
# refactorer, reviewer) plus a squad whose policy enables the server-enforced
# verdict loop.
#
# Prerequisites:
#   - a running Multica server and a logged-in `multica` CLI with workspace
#     context (--workspace-id, MULTICA_WORKSPACE_ID, or `multica config set
#     workspace_id <id>`)
#   - at least one runtime in that workspace (agents run on it)
#
# Usage:
#   ./setup.sh                          # picks the first runtime from the list
#   RUNTIME_ID=<uuid> ./setup.sh        # runtime for leader + architect + reviewer
#   CODE_RUNTIME_ID=<uuid> ./setup.sh   # runtime for the refactorer (default: RUNTIME_ID)
#   MAX_ROUNDS=5 ./setup.sh             # rework rounds before escalation (default 8)
#   MODEL_COORD="" MODEL_CODE="" ./setup.sh   # skip model pinning (server default)
#   SQUAD_NAME="Refactor Loop" ./setup.sh
#
# Run once per workspace: re-running creates a second set of agents.
set -euo pipefail
cd "$(dirname "$0")"

MULTICA="${MULTICA:-multica}"
MAX_ROUNDS="${MAX_ROUNDS:-8}"
SQUAD_NAME="${SQUAD_NAME:-Refactor Loop}"
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
  if [ -n "$model" ]; then
    out="$("$MULTICA" agent create \
      --name "$name" \
      --description "$desc" \
      --instructions "Provisioning — final instructions are set in the next step." \
      --runtime-id "$runtime" \
      --model "$model" \
      --permission-mode public_to \
      --public-to-workspace \
      --output table)"
  else
    out="$("$MULTICA" agent create \
      --name "$name" \
      --description "$desc" \
      --instructions "Provisioning — final instructions are set in the next step." \
      --runtime-id "$runtime" \
      --permission-mode public_to \
      --public-to-workspace \
      --output table)"
  fi
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
LEADER_ID="$(create_agent refactorloop-leader "Coordinates the refactor loop squad: dispatch, rounds, stand-down." "$RUNTIME_ID" "$MODEL_COORD")"
ARCHITECT_ID="$(create_agent refactorloop-architect "Mines refactor points, defines the target design, records the only verdict for the refactor loop squad." "$RUNTIME_ID" "$MODEL_COORD")"
REFACTORER_ID="$(create_agent refactorloop-refactorer "Applies dispatched refactors incrementally, behavior preserved, for the refactor loop squad." "$CODE_RUNTIME_ID" "$MODEL_CODE")"
REVIEWER_ID="$(create_agent refactorloop-reviewer "Independent review: behavior preservation and principle application for the refactor loop squad." "$RUNTIME_ID" "$MODEL_COORD")"

# -- squad ------------------------------------------------------------------
squad_out="$("$MULTICA" squad create \
  --name "$SQUAD_NAME" \
  --description "Unattended behavior-preserving refactoring squad: architect mines refactor points and verdicts, refactorer refactors incrementally, reviewer guards behavior preservation." \
  --leader "$LEADER_ID" \
  --pipeline-max-rounds "$MAX_ROUNDS" \
  --pipeline-product-agent "$ARCHITECT_ID" \
  --output table)"
SQUAD_ID="$(printf '%s\n' "$squad_out" | sed -n "s/.*(\($UUID_RE\)).*/\1/p" | head -1)"
if [ -z "$SQUAD_ID" ]; then
  echo "failed to create squad:" >&2
  printf '%s\n' "$squad_out" >&2
  exit 1
fi
echo "created squad $SQUAD_NAME ($SQUAD_ID)" >&2

add_member "$SQUAD_ID" "$ARCHITECT_ID"
add_member "$SQUAD_ID" "$REFACTORER_ID"
add_member "$SQUAD_ID" "$REVIEWER_ID"

# -- instructions (need every id before they can reference each other) -------
render() { # render <file>
  sed -e "s|{{LEADER_ID}}|$LEADER_ID|g" \
      -e "s|{{ARCHITECT_ID}}|$ARCHITECT_ID|g" \
      -e "s|{{REFACTORER_ID}}|$REFACTORER_ID|g" \
      -e "s|{{REVIEWER_ID}}|$REVIEWER_ID|g" \
      -e "s|{{SQUAD_ID}}|$SQUAD_ID|g" \
      "$1"
}

update_instructions() { # update_instructions <agent-id> <file>
  "$MULTICA" agent update "$1" --instructions "$(render "$2")" >/dev/null
  echo "set instructions from $2" >&2
}

update_instructions "$LEADER_ID" instructions/leader.md
update_instructions "$ARCHITECT_ID" instructions/architect.md
update_instructions "$REFACTORER_ID" instructions/refactorer.md
update_instructions "$REVIEWER_ID" instructions/reviewer.md

# -- summary ----------------------------------------------------------------
cat <<EOF

Refactor Loop squad ready.

  squad:       $SQUAD_NAME  $SQUAD_ID
  leader:      refactorloop-leader      $LEADER_ID
  architect:   refactorloop-architect   $ARCHITECT_ID
  refactorer:  refactorloop-refactorer  $REFACTORER_ID
  reviewer:    refactorloop-reviewer    $REVIEWER_ID
  rounds:      $MAX_ROUNDS (verdict agent=$ARCHITECT_ID)

Start a refactoring by filing an issue assigned to the squad (run from this
directory so --description-file resolves):

  multica issue create \\
    --title "<refactoring direction title>" \\
    --description-file issue-template.md \\
    --assignee-id $SQUAD_ID

Assigning the issue wakes the leader; from there the loop runs unattended.
EOF
