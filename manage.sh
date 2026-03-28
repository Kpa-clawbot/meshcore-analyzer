#!/bin/bash
# CoreScope — Setup & Management Helper
# Usage: ./manage.sh [command]
#
# All container management goes through docker compose.
# Container config lives in docker-compose.yml — this script is just a wrapper.
#
# Idempotent: safe to cancel and re-run at any point.
# Each step checks what's already done and skips it.
set -e

IMAGE_NAME="corescope"
STATE_FILE=".setup-state"

# Source .env for port/path overrides (same file docker compose reads)
[ -f .env ] && set -a && . ./.env && set +a

# Resolved paths for prod/staging data (must match docker-compose.yml)
PROD_DATA="${PROD_DATA_DIR:-$HOME/meshcore-data}"
STAGING_DATA="${STAGING_DATA_DIR:-$HOME/meshcore-staging-data}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log()  { printf '%b\n' "${GREEN}✓${NC} $1"; }
warn() { printf '%b\n' "${YELLOW}⚠${NC} $1"; }
err()  { printf '%b\n' "${RED}✗${NC} $1"; }
info() { printf '%b\n' "${CYAN}→${NC} $1"; }
step() { printf '%b\n' "\n${BOLD}[$1/$TOTAL_STEPS] $2${NC}"; }

confirm() {
  read -p "   $1 [y/N] " -n 1 -r
  echo
  [[ $REPLY =~ ^[Yy]$ ]]
}

# State tracking — marks completed steps so re-runs skip them
mark_done()  { echo "$1" >> "$STATE_FILE"; }
is_done()    { [ -f "$STATE_FILE" ] && grep -qx "$1" "$STATE_FILE" 2>/dev/null; }

# ─── Helpers ──────────────────────────────────────────────────────────────

# Check config.json for placeholder values
check_config_placeholders() {
  if [ -f config.json ]; then
    if grep -qE 'your-username|your-password|your-secret|example\.com|changeme' config.json 2>/dev/null; then
      warn "config.json contains placeholder values."
      warn "Edit config.json and replace placeholder values before deploying."
    fi
  fi
}

# Verify the running container is actually healthy
verify_health() {
  local container="corescope-prod"
  local use_https=false

  # Check if Caddyfile has a real domain (not :80)
  if [ -f caddy-config/Caddyfile ]; then
    local caddyfile_domain
    caddyfile_domain=$(grep -v '^#' caddy-config/Caddyfile 2>/dev/null | head -1 | tr -d ' {')
    if [ "$caddyfile_domain" != ":80" ] && [ -n "$caddyfile_domain" ]; then
      use_https=true
    fi
  fi

  # Wait for /api/stats response (Go backend loads packets into memory — may take 60s+)
  info "Waiting for server to respond..."
  local healthy=false
  for i in $(seq 1 45); do
    if docker exec "$container" wget -qO- http://localhost:3000/api/stats &>/dev/null; then
      healthy=true
      break
    fi
    sleep 2
  done

  if ! $healthy; then
    err "Server did not respond after 90 seconds."
    warn "Check logs: ./manage.sh logs"
    return 1
  fi
  log "Server is responding."

  # Check for MQTT errors in recent logs
  local mqtt_errors
  mqtt_errors=$(docker logs "$container" --tail 50 2>&1 | grep -i 'mqtt.*error\|mqtt.*fail\|ECONNREFUSED.*1883' || true)
  if [ -n "$mqtt_errors" ]; then
    warn "MQTT errors detected in logs:"
    echo "$mqtt_errors" | head -5 | sed 's/^/   /'
  fi

  # If HTTPS domain configured, try to verify externally
  if $use_https; then
    info "Checking HTTPS for ${caddyfile_domain}..."
    if command -v curl &>/dev/null; then
      if curl -sf --connect-timeout 5 "https://${caddyfile_domain}/api/stats" &>/dev/null; then
        log "HTTPS is working: https://${caddyfile_domain}"
      else
        warn "HTTPS not reachable yet for ${caddyfile_domain}"
        warn "It may take a minute for Caddy to provision the certificate."
      fi
    fi
  fi

  return 0
}

# ─── Setup Wizard ─────────────────────────────────────────────────────────

TOTAL_STEPS=6

cmd_setup() {
  echo ""
  echo "═══════════════════════════════════════"
  echo "  CoreScope Setup"
  echo "═══════════════════════════════════════"
  echo ""

  if [ -f "$STATE_FILE" ]; then
    info "Resuming previous setup. Delete ${STATE_FILE} to start over."
    echo ""
  fi

  # ── Step 1: Check Docker ──
  step 1 "Checking Docker"

  if ! command -v docker &> /dev/null; then
    err "Docker is not installed."
    echo ""
    echo "   Install it:"
    echo "     curl -fsSL https://get.docker.com | sh"
    echo "     sudo usermod -aG docker \$USER"
    echo ""
    echo "   Then log out, log back in, and run ./manage.sh setup again."
    exit 1
  fi

  # Check if user can actually run Docker
  if ! docker info &> /dev/null; then
    err "Docker is installed but your user can't run it."
    echo ""
    echo "   Fix: sudo usermod -aG docker \$USER"
    echo "   Then log out, log back in, and try again."
    exit 1
  fi

  log "Docker $(docker --version | grep -oP 'version \K[^ ,]+')"
  
  # Check docker compose (separate check since it's a plugin/separate binary)
  if ! docker compose version &>/dev/null; then
    err "docker compose is required. Install Docker Desktop or docker-compose-plugin."
    exit 1
  fi
  
  mark_done "docker"

  # ── Step 2: Config ──
  step 2 "Configuration"

  if [ -f config.json ]; then
    log "config.json already exists (not overwriting)."
    # Sanity check the JSON
    if ! python3 -c "import json; json.load(open('config.json'))" 2>/dev/null && \
       ! node -e "JSON.parse(require('fs').readFileSync('config.json'))" 2>/dev/null; then
      err "config.json has invalid JSON. Fix it and re-run setup."
      exit 1
    fi
    log "config.json is valid JSON."
    check_config_placeholders
  else
    info "Creating config.json from example..."
    cp config.example.json config.json

    # Generate a random API key
    if command -v openssl &> /dev/null; then
      API_KEY=$(openssl rand -hex 16)
    else
      API_KEY=$(head -c 32 /dev/urandom | xxd -p | head -c 32)
    fi
    # Replace the placeholder API key
    if command -v sed &> /dev/null; then
      sed -i "s/your-secret-api-key-here/${API_KEY}/" config.json
    fi

    log "Created config.json with random API key."
    check_config_placeholders
    echo ""
    echo "   You can customize config.json later (map center, branding, etc)."
    echo "   Edit with: nano config.json"
    echo ""
  fi
  mark_done "config"

  # ── Step 3: Domain & HTTPS ──
  step 3 "Domain & HTTPS"

  if [ -f caddy-config/Caddyfile ]; then
    EXISTING_DOMAIN=$(grep -v '^#' caddy-config/Caddyfile 2>/dev/null | head -1 | tr -d ' {')
    if [ "$EXISTING_DOMAIN" = ":80" ]; then
      log "Caddyfile exists (HTTP only, no HTTPS)."
    else
      log "Caddyfile exists for ${EXISTING_DOMAIN}"
    fi
  else
    mkdir -p caddy-config
    echo ""
    echo "   How should the analyzer be accessed?"
    echo ""
    echo "   1) Direct with built-in HTTPS — Caddy auto-provisions a TLS cert"
    echo "      (requires ports 80 + 443 open, and a domain pointed at this server)"
    echo ""
    echo "   2) Behind my own reverse proxy — HTTP only, I choose the port"
    echo "      (for Cloudflare Tunnel, nginx, Traefik, etc.)"
    echo ""
    read -p "   Choose [1/2]: " -n 1 -r
    echo ""

    case $REPLY in
      1)
        read -p "   Enter your domain (e.g., analyzer.example.com): " DOMAIN
        if [ -z "$DOMAIN" ]; then
          err "No domain entered. Re-run setup to try again."
          exit 1
        fi

        echo "${DOMAIN} {
    reverse_proxy localhost:3000
}" > caddy-config/Caddyfile
        log "Caddyfile created for ${DOMAIN}"

        # Validate DNS
        info "Checking DNS..."
        RESOLVED_IP=$(dig +short "$DOMAIN" 2>/dev/null | grep -E '^[0-9]+\.' | head -1)
        MY_IP=$(curl -s -4 ifconfig.me 2>/dev/null || curl -s -4 icanhazip.com 2>/dev/null || echo "unknown")

        if [ -z "$RESOLVED_IP" ]; then
          warn "${DOMAIN} doesn't resolve yet."
          warn "Create an A record pointing to ${MY_IP}"
          warn "HTTPS won't work until DNS propagates (1-60 min)."
          echo ""
          if ! confirm "Continue anyway?"; then
            echo "   Run ./manage.sh setup again when DNS is ready."
            exit 0
          fi
        elif [ "$RESOLVED_IP" = "$MY_IP" ]; then
          log "DNS resolves correctly: ${DOMAIN} → ${MY_IP}"
        else
          warn "${DOMAIN} resolves to ${RESOLVED_IP} but this server is ${MY_IP}"
          warn "HTTPS provisioning will fail if the domain doesn't point here."
          if ! confirm "Continue anyway?"; then
            echo "   Fix DNS and run ./manage.sh setup again."
            exit 0
          fi
        fi

        # Check port 80
        if command -v curl &> /dev/null; then
          if curl -s --connect-timeout 3 "http://localhost:80" &>/dev/null || \
             curl -s --connect-timeout 3 "http://${MY_IP}:80" &>/dev/null 2>&1; then
            warn "Something is already listening on port 80."
            warn "Stop it first: sudo systemctl stop nginx apache2"
          fi
        fi
        ;;
      2)
        read -p "   HTTP port [80]: " HTTP_PORT
        HTTP_PORT=${HTTP_PORT:-80}
        echo ":${HTTP_PORT} {
    reverse_proxy localhost:3000
}" > caddy-config/Caddyfile
        log "Caddyfile created (HTTP only on port ${HTTP_PORT})."
        echo "   Point your reverse proxy or tunnel to this server's port ${HTTP_PORT}."
        ;;
      *)
        warn "Invalid choice. Defaulting to HTTP only."
        echo ':80 {
    reverse_proxy localhost:3000
}' > caddy-config/Caddyfile
        ;;
    esac
  fi
  mark_done "caddyfile"

  # ── Step 4: Build ──
  step 4 "Building Docker image"

  # Check if image exists and source hasn't changed
  IMAGE_EXISTS=$(docker images -q "$IMAGE_NAME" 2>/dev/null)
  if [ -n "$IMAGE_EXISTS" ] && is_done "build"; then
    log "Image already built."
    if confirm "Rebuild? (only needed if you updated the code)"; then
      docker compose build prod
      log "Image rebuilt."
    fi
  else
    info "This takes 1-2 minutes the first time..."
    docker compose build prod
    log "Image built."
  fi
  mark_done "build"

  # ── Step 5: Start container ──
  step 5 "Starting container"

  # Detect existing data directories
  if [ -d "$PROD_DATA" ] && [ -f "$PROD_DATA/meshcore.db" ]; then
    info "Found existing data at $PROD_DATA/ — will use bind mount."
  fi

  if docker ps --format '{{.Names}}' | grep -q "^corescope-prod$"; then
    log "Container already running."
  else
    mkdir -p "$PROD_DATA"
    docker compose up -d prod
    log "Container started."
  fi
  mark_done "container"

  # ── Step 6: Verify ──
  step 6 "Verifying"

  if docker ps --format '{{.Names}}' | grep -q "^corescope-prod$"; then
    verify_health

    CADDYFILE_DOMAIN=$(grep -v '^#' caddy-config/Caddyfile 2>/dev/null | head -1 | tr -d ' {')

    echo ""
    echo "═══════════════════════════════════════"
    echo "  Setup complete!"
    echo "═══════════════════════════════════════"
    echo ""
    if [ "$CADDYFILE_DOMAIN" != ":80" ] && [ -n "$CADDYFILE_DOMAIN" ]; then
      echo "   🌐 https://${CADDYFILE_DOMAIN}"
    else
      MY_IP=$(curl -s -4 ifconfig.me 2>/dev/null || echo "your-server-ip")
      echo "   🌐 http://${MY_IP}"
    fi
    echo ""
    echo "   Next steps:"
    echo "   • Connect an observer to start receiving packets"
    echo "   • Customize branding in config.json"
    echo "   • Set up backups: ./manage.sh backup"
    echo ""
    echo "   Useful commands:"
    echo "     ./manage.sh status     Check health"
    echo "     ./manage.sh logs       View logs"
    echo "     ./manage.sh backup     Full backup (DB + config + theme)"
    echo "     ./manage.sh update     Update to latest version"
    echo ""
  else
    err "Container failed to start."
    echo ""
    echo "   Check what went wrong:"
    echo "     docker compose logs prod"
    echo ""
    echo "   Common fixes:"
    echo "     • Invalid config.json — check JSON syntax"
    echo "     • Port conflict — stop other web servers"
    echo "     • Re-run: ./manage.sh setup"
    echo ""
    exit 1
  fi

  mark_done "verify"
}

# ─── Staging Helpers ──────────────────────────────────────────────────────

# Copy production DB to staging data directory
prepare_staging_db() {
  mkdir -p "$STAGING_DATA"
  if [ -f "$PROD_DATA/meshcore.db" ]; then
    info "Copying production database to staging..."
    cp "$PROD_DATA/meshcore.db" "$STAGING_DATA/meshcore.db" 2>/dev/null || true
    log "Database snapshot copied to ${STAGING_DATA}/meshcore.db"
  else
    warn "No production database found at ${PROD_DATA}/meshcore.db — staging starts empty."
  fi
}

# Copy config.prod.json → config.staging.json with siteName change
prepare_staging_config() {
  local prod_config="$PROD_DATA/config.json"
  local staging_config="$STAGING_DATA/config.json"
  if [ ! -f "$prod_config" ]; then
    warn "No config.json found at ${prod_config} — staging may not start correctly."
    return
  fi
  if [ ! -f "$staging_config" ] || [ "$prod_config" -nt "$staging_config" ]; then
    info "Copying production config to staging..."
    cp "$prod_config" "$staging_config"
    sed -i 's/"siteName":\s*"[^"]*"/"siteName": "CoreScope — STAGING"/' "$staging_config"
    log "Staging config created at ${staging_config} with STAGING site name."
  else
    log "Staging config is up to date."
  fi
  # Copy Caddyfile for staging (HTTP-only on staging port)
  local staging_caddy="$STAGING_DATA/Caddyfile"
  if [ ! -f "$staging_caddy" ]; then
    info "Creating staging Caddyfile (HTTP-only on port ${STAGING_HTTP_PORT:-81})..."
    echo ":${STAGING_HTTP_PORT:-81} {" > "$staging_caddy"
    echo "    reverse_proxy localhost:3000" >> "$staging_caddy"
    echo "}" >> "$staging_caddy"
    log "Staging Caddyfile created at ${staging_caddy}"
  fi
}

# Check if a container is running by name
container_running() {
  docker ps --format '{{.Names}}' | grep -q "^${1}$"
}

# Get health status of a container
container_health() {
  docker inspect "$1" --format '{{.State.Health.Status}}' 2>/dev/null || echo "unknown"
}

# ─── Start / Stop / Restart ──────────────────────────────────────────────

cmd_start() {
  local WITH_STAGING=false
  if [ "$1" = "--with-staging" ]; then
    WITH_STAGING=true
  fi

  if $WITH_STAGING; then
    # Prepare staging data and config
    prepare_staging_db
    prepare_staging_config

    info "Starting production container (corescope-prod) on ports ${PROD_HTTP_PORT:-80}/${PROD_HTTPS_PORT:-443}..."
    info "Starting staging container (corescope-staging) on port ${STAGING_HTTP_PORT:-81}..."
    docker compose --profile staging up -d
    log "Production started on ports ${PROD_HTTP_PORT:-80}/${PROD_HTTPS_PORT:-443}/${PROD_MQTT_PORT:-1883}"
    log "Staging started on port ${STAGING_HTTP_PORT:-81} (MQTT: ${STAGING_MQTT_PORT:-1884})"
  else
    info "Starting production container (corescope-prod) on ports ${PROD_HTTP_PORT:-80}/${PROD_HTTPS_PORT:-443}..."
    docker compose up -d prod
    log "Production started. Staging NOT running (use --with-staging to start both)."
  fi
}

cmd_stop() {
  local TARGET="${1:-all}"

  case "$TARGET" in
    prod)
      info "Stopping production container (corescope-prod)..."
      docker compose stop prod
      log "Production stopped."
      ;;
    staging)
      info "Stopping staging container (corescope-staging)..."
      docker compose --profile staging stop staging
      log "Staging stopped."
      ;;
    all)
      info "Stopping all containers..."
      docker compose --profile staging --profile staging-go down
      log "All containers stopped."
      ;;
    *)
      err "Usage: ./manage.sh stop [prod|staging|all]"
      exit 1
      ;;
  esac
}

cmd_restart() {
  local TARGET="${1:-prod}"
  case "$TARGET" in
    prod)
      info "Restarting production container (corescope-prod)..."
      docker compose up -d --force-recreate prod
      log "Production restarted."
      ;;
    staging)
      info "Restarting staging container (corescope-staging)..."
      docker compose --profile staging up -d --force-recreate staging
      log "Staging restarted."
      ;;
    all)
      info "Restarting all containers..."
      docker compose --profile staging up -d --force-recreate
      log "All containers restarted."
      ;;
    *)
      err "Usage: ./manage.sh restart [prod|staging|all]"
      exit 1
      ;;
  esac
}

# ─── Status ───────────────────────────────────────────────────────────────

# Show status for a single container (used in compose mode)
show_container_status() {
  local NAME="$1"
  local LABEL="$2"

  if container_running "$NAME"; then
    local health
    health=$(container_health "$NAME")
    log "${LABEL} (${NAME}): Running — Health: ${health}"
    docker ps --filter "name=${NAME}" --format "   Ports:  {{.Ports}}"

    # Server stats
    if docker exec "$NAME" wget -qO /dev/null http://localhost:3000/api/stats 2>/dev/null; then
      local stats packets nodes
      stats=$(docker exec "$NAME" wget -qO- http://localhost:3000/api/stats 2>/dev/null)
      packets=$(echo "$stats" | grep -oP '"totalPackets":\K[0-9]+' 2>/dev/null || echo "?")
      nodes=$(echo "$stats" | grep -oP '"totalNodes":\K[0-9]+' 2>/dev/null || echo "?")
      info "  ${packets} packets, ${nodes} nodes"
    fi
  else
    if docker ps -a --format '{{.Names}}' | grep -q "^${NAME}$"; then
      warn "${LABEL} (${NAME}): Stopped"
    else
      info "${LABEL} (${NAME}): Not running"
    fi
  fi
}

cmd_status() {
  echo ""
  echo "═══════════════════════════════════════"
  echo "  CoreScope Status"
  echo "═══════════════════════════════════════"
  echo ""

  # Production
  show_container_status "corescope-prod" "Production"
  echo ""

  # Staging
  if container_running "corescope-staging"; then
    show_container_status "corescope-staging" "Staging"
  else
    info "Staging (corescope-staging): Not running (use --with-staging to start both)"
  fi
  echo ""

  # Disk usage
  if [ -d "$PROD_DATA" ] && [ -f "$PROD_DATA/meshcore.db" ]; then
    local db_size
    db_size=$(du -h "$PROD_DATA/meshcore.db" 2>/dev/null | cut -f1)
    info "Production DB: ${db_size}"
  fi
  if [ -d "$STAGING_DATA" ] && [ -f "$STAGING_DATA/meshcore.db" ]; then
    local staging_db_size
    staging_db_size=$(du -h "$STAGING_DATA/meshcore.db" 2>/dev/null | cut -f1)
    info "Staging DB: ${staging_db_size}"
  fi

  echo ""
}

# ─── Logs ─────────────────────────────────────────────────────────────────

cmd_logs() {
  local TARGET="${1:-prod}"
  local LINES="${2:-100}"
  case "$TARGET" in
    prod)
      info "Tailing production logs..."
      docker compose logs -f --tail="$LINES" prod
      ;;
    staging)
      if container_running "corescope-staging"; then
        info "Tailing staging logs..."
        docker compose logs -f --tail="$LINES" staging
      else
        err "Staging container is not running."
        info "Start with: ./manage.sh start --with-staging"
        exit 1
      fi
      ;;
    *)
      err "Usage: ./manage.sh logs [prod|staging] [lines]"
      exit 1
      ;;
  esac
}

# ─── Promote ──────────────────────────────────────────────────────────────

cmd_promote() {
  echo ""
  info "Promotion Flow: Staging → Production"
  echo ""
  echo "This will:"
  echo "  1. Backup current production database"
  echo "  2. Restart production with latest image (same as staging)"
  echo "  3. Wait for health check"
  echo ""

  # Show what's currently running
  local staging_image staging_created prod_image prod_created
  staging_image=$(docker inspect corescope-staging --format '{{.Config.Image}}' 2>/dev/null || echo "not running")
  staging_created=$(docker inspect corescope-staging --format '{{.Created}}' 2>/dev/null || echo "N/A")
  prod_image=$(docker inspect corescope-prod --format '{{.Config.Image}}' 2>/dev/null || echo "not running")
  prod_created=$(docker inspect corescope-prod --format '{{.Created}}' 2>/dev/null || echo "N/A")

  echo "  Staging: ${staging_image} (created ${staging_created})"
  echo "  Prod:    ${prod_image} (created ${prod_created})"
  echo ""

  if ! confirm "Proceed with promotion?"; then
    echo "   Aborted."
    exit 0
  fi

  # Backup production DB
  info "Backing up production database..."
  local BACKUP_DIR="./backups/pre-promotion-$(date +%Y%m%d-%H%M%S)"
  mkdir -p "$BACKUP_DIR"
  if [ -f "$PROD_DATA/meshcore.db" ]; then
    cp "$PROD_DATA/meshcore.db" "$BACKUP_DIR/"
  elif container_running "corescope-prod"; then
    docker cp corescope-prod:/app/data/meshcore.db "$BACKUP_DIR/"
  else
    warn "Could not backup production database."
  fi
  log "Backup saved to ${BACKUP_DIR}/"

  # Restart prod with latest image
  info "Restarting production with latest image..."
  docker compose up -d --force-recreate prod

  # Wait for health
  info "Waiting for production health check..."
  local i health
  for i in $(seq 1 30); do
    health=$(container_health "corescope-prod")
    if [ "$health" = "healthy" ]; then
      log "Production healthy after ${i}s"
      break
    fi
    if [ "$i" -eq 30 ]; then
      err "Production failed health check after 30s"
      warn "Check logs: ./manage.sh logs prod"
      warn "Rollback: cp ${BACKUP_DIR}/meshcore.db ${PROD_DATA}/ && ./manage.sh restart prod"
      exit 1
    fi
    sleep 1
  done

  log "Promotion complete ✓"
  echo ""
  echo "  Production is now running the same image as staging."
  echo "  Backup: ${BACKUP_DIR}/"
  echo ""
}

# ─── Update ───────────────────────────────────────────────────────────────

cmd_update() {
  info "Pulling latest code..."
  git pull

  info "Rebuilding image..."
  docker compose build prod

  info "Restarting with new image..."
  docker compose up -d --force-recreate prod

  log "Updated and restarted. Data preserved."
}

# ─── Backup ───────────────────────────────────────────────────────────────

cmd_backup() {
  TIMESTAMP=$(date +%Y%m%d-%H%M%S)
  BACKUP_DIR="${1:-./backups/corescope-${TIMESTAMP}}"
  mkdir -p "$BACKUP_DIR"

  info "Backing up to ${BACKUP_DIR}/"

  # Database
  # Always use bind mount path (from .env or default)
  DB_PATH="$PROD_DATA/meshcore.db"
  if [ -f "$DB_PATH" ]; then
    cp "$DB_PATH" "$BACKUP_DIR/meshcore.db"
    log "Database ($(du -h "$BACKUP_DIR/meshcore.db" | cut -f1))"
  elif container_running "corescope-prod"; then
    docker cp corescope-prod:/app/data/meshcore.db "$BACKUP_DIR/meshcore.db" 2>/dev/null && \
      log "Database (via docker cp)" || warn "Could not backup database"
  else
    warn "Database not found (container not running?)"
  fi

  # Config
  if [ -f config.json ]; then
    cp config.json "$BACKUP_DIR/config.json"
    log "config.json"
  fi

  # Caddyfile
  if [ -f caddy-config/Caddyfile ]; then
    cp caddy-config/Caddyfile "$BACKUP_DIR/Caddyfile"
    log "Caddyfile"
  fi

  # Theme
  # Always use bind mount path (from .env or default)
  THEME_PATH="$PROD_DATA/theme.json"
  if [ -f "$THEME_PATH" ]; then
    cp "$THEME_PATH" "$BACKUP_DIR/theme.json"
    log "theme.json"
  elif [ -f theme.json ]; then
    cp theme.json "$BACKUP_DIR/theme.json"
    log "theme.json"
  fi

  # Summary
  TOTAL=$(du -sh "$BACKUP_DIR" | cut -f1)
  FILES=$(ls "$BACKUP_DIR" | wc -l)
  echo ""
  log "Backup complete: ${FILES} files, ${TOTAL} total → ${BACKUP_DIR}/"
}

# ─── Restore ──────────────────────────────────────────────────────────────

cmd_restore() {
  if [ -z "$1" ]; then
    err "Usage: ./manage.sh restore <backup-dir-or-db-file>"
    if [ -d "./backups" ]; then
      echo ""
      echo "   Available backups:"
      ls -dt ./backups/meshcore-* ./backups/corescope-* 2>/dev/null | head -10 | while read d; do
        if [ -d "$d" ]; then
          echo "     $d/ ($(ls "$d" | wc -l) files)"
        elif [ -f "$d" ]; then
          echo "     $d ($(du -h "$d" | cut -f1))"
        fi
      done
    fi
    exit 1
  fi

  # Accept either a directory (full backup) or a single .db file
  if [ -d "$1" ]; then
    DB_FILE="$1/meshcore.db"
    CONFIG_FILE="$1/config.json"
    CADDY_FILE="$1/Caddyfile"
    THEME_FILE="$1/theme.json"
  elif [ -f "$1" ]; then
    DB_FILE="$1"
    CONFIG_FILE=""
    CADDY_FILE=""
    THEME_FILE=""
  else
    err "Not found: $1"
    exit 1
  fi

  if [ ! -f "$DB_FILE" ]; then
    err "No meshcore.db found in $1"
    exit 1
  fi

  echo ""
  info "Will restore from: $1"
  [ -f "$DB_FILE" ] && echo "   • Database"
  [ -n "$CONFIG_FILE" ] && [ -f "$CONFIG_FILE" ] && echo "   • config.json"
  [ -n "$CADDY_FILE" ] && [ -f "$CADDY_FILE" ] && echo "   • Caddyfile"
  [ -n "$THEME_FILE" ] && [ -f "$THEME_FILE" ] && echo "   • theme.json"
  echo ""

  if ! confirm "Continue? (current state will be backed up first)"; then
    echo "   Aborted."
    exit 0
  fi

  # Backup current state first
  info "Backing up current state..."
  cmd_backup "./backups/corescope-pre-restore-$(date +%Y%m%d-%H%M%S)"

  docker compose stop prod 2>/dev/null || true

  # Restore database
  mkdir -p "$PROD_DATA"
  DEST_DB="$PROD_DATA/meshcore.db"
  cp "$DB_FILE" "$DEST_DB"
  log "Database restored"

  # Restore config if present
  if [ -n "$CONFIG_FILE" ] && [ -f "$CONFIG_FILE" ]; then
    cp "$CONFIG_FILE" ./config.json
    log "config.json restored"
  fi

  # Restore Caddyfile if present
  if [ -n "$CADDY_FILE" ] && [ -f "$CADDY_FILE" ]; then
    mkdir -p caddy-config
    cp "$CADDY_FILE" caddy-config/Caddyfile
    log "Caddyfile restored"
  fi

  # Restore theme if present
  if [ -n "$THEME_FILE" ] && [ -f "$THEME_FILE" ]; then
    DEST_THEME="$PROD_DATA/theme.json"
    cp "$THEME_FILE" "$DEST_THEME"
    log "theme.json restored"
  fi

  docker compose up -d prod
  log "Restored and restarted."
}

# ─── MQTT Test ────────────────────────────────────────────────────────────

cmd_mqtt_test() {
  if ! container_running "corescope-prod"; then
    err "Container not running. Start with: ./manage.sh start"
    exit 1
  fi

  info "Listening for MQTT messages (10 second timeout)..."
  MSG=$(docker exec corescope-prod mosquitto_sub -h localhost -t 'meshcore/#' -C 1 -W 10 2>/dev/null)
  if [ -n "$MSG" ]; then
    log "Received MQTT message:"
    echo "   $MSG" | head -c 200
    echo ""
  else
    warn "No messages received in 10 seconds."
    echo ""
    echo "   This means no observer is publishing packets."
    echo "   See the deployment guide for connecting observers."
  fi
}

# ─── Reset ────────────────────────────────────────────────────────────────

cmd_reset() {
  echo ""
  warn "This will remove all containers, images, and setup state."
  warn "Your config.json, Caddyfile, and data directory are NOT deleted."
  echo ""
  if ! confirm "Continue?"; then
    echo "   Aborted."
    exit 0
  fi

  docker compose --profile staging --profile staging-go down --rmi local 2>/dev/null || true
  rm -f "$STATE_FILE"

  log "Reset complete. Run './manage.sh setup' to start over."
  echo "   Data directory: $PROD_DATA (not removed)"
}

# ─── Help ─────────────────────────────────────────────────────────────────

cmd_help() {
  echo ""
  echo "CoreScope — Management Script"
  echo ""
  echo "Usage: ./manage.sh <command>"
  echo ""
  printf '%b\n' "  ${BOLD}Setup${NC}"
  echo "    setup              First-time setup wizard (safe to re-run)"
  echo "    reset              Remove container + image (keeps data + config)"
  echo ""
  printf '%b\n' "  ${BOLD}Run${NC}"
  echo "    start              Start production container"
  echo "    start --with-staging  Start production + staging (copies prod DB + config)"
  echo "    stop [prod|staging|all]  Stop specific or all containers (default: all)"
  echo "    restart [prod|staging|all]  Restart specific or all containers"
  echo "    status             Show health, stats, and service status"
  echo "    logs [prod|staging] [N]  Follow logs (default: prod, last 100 lines)"
  echo ""
  printf '%b\n' "  ${BOLD}Maintain${NC}"
  echo "    update             Pull latest code, rebuild, restart (keeps data)"
  echo "    promote            Promote staging → production (backup + restart)"
  echo "    backup [dir]       Full backup: database + config + theme"
  echo "    restore <d>        Restore from backup dir or .db file"
  echo "    mqtt-test          Check if MQTT data is flowing"
  echo ""
  echo "All commands use docker compose with docker-compose.yml."
  echo ""
}

# ─── Main ─────────────────────────────────────────────────────────────────

case "${1:-help}" in
  setup)     cmd_setup ;;
  start)     cmd_start "$2" ;;
  stop)      cmd_stop "$2" ;;
  restart)   cmd_restart "$2" ;;
  status)    cmd_status ;;
  logs)      cmd_logs "$2" "$3" ;;
  update)    cmd_update ;;
  promote)   cmd_promote ;;
  backup)    cmd_backup "$2" ;;
  restore)   cmd_restore "$2" ;;
  mqtt-test) cmd_mqtt_test ;;
  reset)     cmd_reset ;;
  help|*)    cmd_help ;;
esac
