#!/usr/bin/env bash
set -euo pipefail

ENV_FILE="${1:-.env}"

if [ ! -f "$ENV_FILE" ]; then
  echo "Missing env file: $ENV_FILE"
  echo "Create .env from .env.example, or run 'make worktree-env' and use .env.worktree."
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

POSTGRES_DB="${POSTGRES_DB:-multica}"
POSTGRES_USER="${POSTGRES_USER:-multica}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-multica}"
DATABASE_URL="${DATABASE_URL:-}"
POSTGRES_MODE="${MULTICA_POSTGRES_MODE:-auto}"

export PGPASSWORD="$POSTGRES_PASSWORD"

db_host=""
db_port="${POSTGRES_PORT:-5432}"
db_name="$POSTGRES_DB"
admin_db_url=""
target_db_url=""

parse_database_url() {
  local rest authority hostport path port_part credentials userpass passwordless_authority scheme

  scheme="${DATABASE_URL%%://*}"
  rest="${DATABASE_URL#*://}"
  rest="${rest%%\?*}"
  authority="${rest%%/*}"
  path="${rest#*/}"

  if [ "$authority" = "$rest" ]; then
    path=""
  fi

  hostport="${authority##*@}"
  credentials=""
  if [[ "$authority" == *@* ]]; then
    credentials="${authority%@*}"
  fi

  if [[ "$hostport" == \[* ]]; then
    db_host="${hostport#\[}"
    db_host="${db_host%%]*}"
    port_part="${hostport#*\]}"
    if [[ "$port_part" == :* ]] && [ -n "${port_part#:}" ]; then
      db_port="${port_part#:}"
    fi
  else
    db_host="${hostport%%:*}"
    if [[ "$hostport" == *:* ]] && [ -n "${hostport##*:}" ]; then
      db_port="${hostport##*:}"
    fi
  fi

  if [ -n "$path" ]; then
    db_name="${path%%/*}"
  fi

  passwordless_authority="$hostport"
  if [ -n "$credentials" ]; then
    userpass="${credentials%%:*}"
    passwordless_authority="$userpass@$hostport"
  fi
  admin_db_url="${scheme}://${passwordless_authority}/postgres"
  target_db_url="${scheme}://${passwordless_authority}/${db_name}"
}

if [ -n "$DATABASE_URL" ]; then
  parse_database_url
else
  db_host="localhost"
  admin_db_url="postgres://${POSTGRES_USER}@${db_host}:${db_port}/postgres?sslmode=disable"
  target_db_url="postgres://${POSTGRES_USER}@${db_host}:${db_port}/${POSTGRES_DB}?sslmode=disable"
fi

is_local() {
  [ -z "$DATABASE_URL" ] || [ "$db_host" = "localhost" ] || [ "$db_host" = "127.0.0.1" ] || [ "$db_host" = "::1" ]
}

local_pg_ready() {
  command -v pg_isready > /dev/null 2>&1 && pg_isready -h "$db_host" -p "$db_port" -d postgres > /dev/null 2>&1
}

sql_escape() {
  printf "%s" "$1" | sed "s/'/''/g"
}

ensure_local_database_with_psql() {
  local admin_conn role_exists db_exists escaped_password current_user_admin_url

  if ! command -v psql > /dev/null 2>&1; then
    echo "psql not found. Install PostgreSQL client tools, start Docker, or set MULTICA_POSTGRES_MODE=docker."
    return 1
  fi

  echo "==> Using existing PostgreSQL at $db_host:$db_port..."
  admin_conn="$admin_db_url"
  if ! psql "$admin_conn" -Atqc "SELECT 1" > /dev/null 2>&1; then
    current_user_admin_url="postgres://${USER:-postgres}@${db_host}:${db_port}/postgres?sslmode=disable"
    if psql "$current_user_admin_url" -Atqc "SELECT 1" > /dev/null 2>&1; then
      admin_conn="$current_user_admin_url"
    else
      echo "Could not connect to existing PostgreSQL as '$POSTGRES_USER' or '${USER:-postgres}'."
      echo "Set DATABASE_URL in $ENV_FILE to a reachable database, or create the role/database manually."
      return 1
    fi
  fi

  role_exists="$(psql "$admin_conn" -Atqc "SELECT 1 FROM pg_roles WHERE rolname = '$POSTGRES_USER'" 2>/dev/null || true)"
  if [ "$role_exists" != "1" ]; then
    echo "==> Creating PostgreSQL role '$POSTGRES_USER'..."
    escaped_password="$(sql_escape "$POSTGRES_PASSWORD")"
    psql "$admin_conn" -v ON_ERROR_STOP=1 -c "CREATE ROLE \"$POSTGRES_USER\" WITH LOGIN CREATEDB PASSWORD '$escaped_password'" > /dev/null
  fi

  db_exists="$(psql "$admin_conn" -Atqc "SELECT 1 FROM pg_database WHERE datname = '$POSTGRES_DB'" 2>/dev/null || true)"
  if [ "$db_exists" != "1" ]; then
    echo "==> Creating database '$POSTGRES_DB' on existing PostgreSQL..."
    psql "$admin_conn" -v ON_ERROR_STOP=1 -c "CREATE DATABASE \"$POSTGRES_DB\" OWNER \"$POSTGRES_USER\"" > /dev/null
  fi
  echo "✓ PostgreSQL ready (local existing server). Database: $POSTGRES_DB"
}

ensure_local_database_with_docker() {
  if ! command -v docker > /dev/null 2>&1; then
    echo "Docker not found. Start your local PostgreSQL service or install Docker."
    return 1
  fi
  if ! docker info > /dev/null 2>&1; then
    echo "Docker is not running. Start your local PostgreSQL service or start Docker Desktop."
    return 1
  fi

  echo "==> Ensuring shared PostgreSQL container is running on localhost:5432..."
  docker compose up -d postgres

  echo "==> Waiting for PostgreSQL to be ready..."
  until docker compose exec -T postgres pg_isready -U "$POSTGRES_USER" -d postgres > /dev/null 2>&1; do
    sleep 1
  done

  echo "==> Ensuring database '$POSTGRES_DB' exists..."
  db_exists="$(docker compose exec -T postgres \
    psql -U "$POSTGRES_USER" -d postgres -Atqc "SELECT 1 FROM pg_database WHERE datname = '$POSTGRES_DB'")"

  if [ "$db_exists" != "1" ]; then
    docker compose exec -T postgres \
      psql -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1 \
      -c "CREATE DATABASE \"$POSTGRES_DB\"" \
      > /dev/null
  fi

  echo "✓ PostgreSQL ready (local Docker). Database: $POSTGRES_DB"
}

if is_local; then
  case "$POSTGRES_MODE" in
    local|existing|native)
      ensure_local_database_with_psql
      ;;
    docker)
      ensure_local_database_with_docker
      ;;
    auto)
      if local_pg_ready; then
        ensure_local_database_with_psql
      else
        echo "==> No existing PostgreSQL detected at $db_host:$db_port. Falling back to Docker..."
        if ! ensure_local_database_with_docker; then
          echo "Could not prepare local PostgreSQL. Start Homebrew PostgreSQL, for example:"
          echo "  brew services start postgresql@18"
          echo "or set DATABASE_URL to a running PostgreSQL instance in $ENV_FILE."
          exit 1
        fi
      fi
      ;;
    *)
      echo "Invalid MULTICA_POSTGRES_MODE=$POSTGRES_MODE. Use auto, local, or docker."
      exit 1
      ;;
  esac
else
  echo "==> Remote database detected (host: $db_host). Skipping local PostgreSQL setup."
  if command -v pg_isready > /dev/null 2>&1; then
    echo "==> Waiting for PostgreSQL at $db_host:$db_port to be ready..."
    until pg_isready -d "$DATABASE_URL" > /dev/null 2>&1; do
      sleep 1
    done
    echo "✓ PostgreSQL ready (remote: $db_host:$db_port). Database: $db_name"
  else
    echo "==> pg_isready not found. Skipping remote connectivity preflight."
    echo "✓ PostgreSQL configured (remote: $db_host:$db_port). Database: $db_name"
  fi
fi
