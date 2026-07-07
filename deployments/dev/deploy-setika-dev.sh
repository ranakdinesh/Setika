#!/usr/bin/env bash
set -euo pipefail

APP_DIR="${SETIKA_DEV_APP_DIR:-/opt/setika-dev/app}"
SETIKA_REPO="${SETIKA_REPO:-https://github.com/ranakdinesh/Setika.git}"
FRONTEND_REPO="${SETIKA_FRONTEND_REPO:-https://github.com/ranakdinesh/spur-hrms-ui.git}"
BRANCH="${SETIKA_DEV_BRANCH:-main}"
PROJECT_NAME="${SETIKA_COMPOSE_PROJECT:-setika-dev}"
COMPOSE_FILE="${SETIKA_COMPOSE_FILE:-docker-compose.dev.yml}"
BACKUP_ROOT="${SETIKA_BACKUP_ROOT:-/opt/setika-dev/backups}"

timestamp="$(date -u +%Y%m%d%H%M%S)"
backup_dir="$BACKUP_ROOT/deploy-$timestamp"

mkdir -p "$APP_DIR" "$backup_dir"
cd "$APP_DIR"

backup_if_exists() {
  local path="$1"
  if [ -e "$path" ]; then
    mkdir -p "$backup_dir/$(dirname "$path")"
    cp -a "$path" "$backup_dir/$path"
  fi
}

restore_if_exists() {
  local path="$1"
  if [ -e "$backup_dir/$path" ]; then
    mkdir -p "$(dirname "$path")"
    rm -rf "$path"
    cp -a "$backup_dir/$path" "$path"
  fi
}

backup_if_exists ".env"
backup_if_exists "setika/.env"
backup_if_exists "setika/keys"
backup_if_exists "frontend/.env.local"

checkout_repo() {
  local repo="$1"
  local dir="$2"
  if [ -d "$dir/.git" ]; then
    git -C "$dir" fetch origin "$BRANCH"
    git -C "$dir" checkout "$BRANCH"
    git -C "$dir" reset --hard "origin/$BRANCH"
  else
    if [ -e "$dir" ]; then
      mkdir -p "$backup_dir/pre-git"
      mv "$dir" "$backup_dir/pre-git/$dir"
    fi
    git clone --branch "$BRANCH" "$repo" "$dir"
  fi
}

checkout_repo "$SETIKA_REPO" "setika"
checkout_repo "$FRONTEND_REPO" "frontend"

restore_if_exists ".env"
restore_if_exists "setika/.env"
restore_if_exists "setika/keys"
restore_if_exists "frontend/.env.local"

docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" up -d --build
docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" ps

wait_for_url() {
  local url="$1"
  local label="$2"
  for attempt in $(seq 1 60); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "$label did not become healthy at $url" >&2
  return 1
}

wait_for_url "http://127.0.0.1:${DOCKER_BACKEND_PORT:-8087}/healthz" "backend"
wait_for_url "http://127.0.0.1:${DOCKER_FRONTEND_PORT:-3003}/" "frontend"

echo "Setika dev deploy completed from $BRANCH at $timestamp"
