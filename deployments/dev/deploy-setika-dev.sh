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

install_dev_nginx_site() {
  if [ "${SETIKA_DEV_MANAGE_NGINX:-1}" != "1" ]; then
    return 0
  fi
  if ! command -v nginx >/dev/null 2>&1; then
    echo "nginx not found; skipping public dev proxy update"
    return 0
  fi

  local site_path="${SETIKA_DEV_NGINX_SITE:-/etc/nginx/sites-available/setika-dev}"
  local enabled_path="${SETIKA_DEV_NGINX_ENABLED_SITE:-/etc/nginx/sites-enabled/setika-dev}"
  local backend_port="${DOCKER_BACKEND_PORT:-8087}"
  local frontend_port="${DOCKER_FRONTEND_PORT:-3003}"
  local minio_port="${DOCKER_MINIO_PORT:-9002}"

  mkdir -p "$(dirname "$site_path")"
  cat >"$site_path" <<NGINX
server {
    server_name dev.setika.one *.dev.setika.one;

    client_max_body_size 50m;
    client_header_buffer_size 64k;
    large_client_header_buffers 8 128k;
    proxy_buffer_size 128k;
    proxy_buffers 8 128k;
    proxy_busy_buffers_size 256k;

    access_log /var/log/nginx/dev.setika.one.access.log;
    error_log /var/log/nginx/dev.setika.one.error.log;

    proxy_set_header Host \$host;
    proxy_set_header X-Forwarded-Host \$host;
    proxy_set_header X-Real-IP \$remote_addr;
    proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto \$scheme;

    location ~ ^/(healthz|readyz)$ {
        proxy_pass http://127.0.0.1:${backend_port};
    }

    location /files/ {
        rewrite ^/files/(.*)$ /\$1 break;
        proxy_pass http://127.0.0.1:${minio_port};
    }

    # Dev API routes must reach the Go backend. If identity routes such as
    # /roles/ or /users/{id}/roles/ fall through to Next.js, role management
    # surfaces show frontend 404s even though the backend route exists.
    location ~ ^/(auth|setika|signup|admin|users|roles|permissions|master-data|hrms|document-sign)(/|$) {
        proxy_pass http://127.0.0.1:${backend_port};
    }

    location / {
        proxy_pass http://127.0.0.1:${frontend_port};
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
    }

    listen 443 ssl;
    ssl_certificate /etc/letsencrypt/live/dev.setika.one/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/dev.setika.one/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
}

server {
    listen 80;
    listen [::]:80;
    server_name dev.setika.one *.dev.setika.one;
    return 301 https://\$host\$request_uri;
}
NGINX

  if [ -d "$(dirname "$enabled_path")" ]; then
    ln -sf "$site_path" "$enabled_path"
  fi
  nginx -t
  nginx -s reload || systemctl reload nginx
}

install_dev_nginx_site

echo "Setika dev deploy completed from $BRANCH at $timestamp"
