#!/bin/bash
# MeshCore Analyzer — Setup & Management Helper
# Usage: ./manage.sh [command]
#
# Idempotent: safe to cancel and re-run at any point.
# Each step checks what's already done and skips it.
set -e

CONTAINER_NAME="meshcore-analyzer"
IMAGE_NAME="meshcore-analyzer"
DATA_VOLUME="meshcore-data"
CADDY_VOLUME="caddy-data"
STATE_FILE=".setup-state"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log()  { echo -e "${GREEN}✓${NC} $1"; }
warn() { echo -e "${YELLOW}⚠${NC} $1"; }
err()  { echo -e "${RED}✗${NC} $1"; }
info() { echo -e "${CYAN}→${NC} $1"; }
step() { echo -e "\n${BOLD}[$1/$TOTAL_STEPS] $2${NC}"; }

confirm() {
  read -p "   $1 [y/N] " -n 1 -r
  echo
  [[ $REPLY =~ ^[Yy]$ ]]
}

# State tracking — marks completed steps so re-runs skip them
mark_done()  { echo "$1" >> "$STATE_FILE"; }
is_done()    { [ -f "$STATE_FILE" ] && grep -qx "$1" "$STATE_FILE" 2>/dev/null; }

# ─── Setup Wizard ─────────────────────────────────────────────────────────

TOTAL_STEPS=6

cmd_setup() {
  echo ""
  echo "═══════════════════════════════════════"
  echo "  MeshCore Analyzer Setup"
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
  mark_done "docker"

  # ── Step 2: Config ──
  step 2 "Configuration"

  if [ -f config.json ]; then
    log "config.json exists."
    # Sanity check the JSON
    if ! python3 -c "import json; json.load(open('config.json'))" 2>/dev/null && \
       ! node -e "JSON.parse(require('fs').readFileSync('config.json'))" 2>/dev/null; then
      err "config.json has invalid JSON. Fix it and re-run setup."
      exit 1
    fi
    log "config.json is valid JSON."
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
    echo "   How do you want to handle HTTPS?"
    echo ""
    echo "   1) I have a domain pointed at this server (automatic HTTPS)"
    echo "   2) I'll use Cloudflare Tunnel or my own proxy (HTTP only)"
    echo "   3) Just HTTP for now, I'll set up HTTPS later"
    echo ""
    read -p "   Choose [1/2/3]: " -n 1 -r
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
      2|3)
        echo ':80 {
    reverse_proxy localhost:3000
}' > caddy-config/Caddyfile
        log "Caddyfile created (HTTP only on port 80)."
        if [ "$REPLY" = "2" ]; then
          echo "   Point your Cloudflare Tunnel or proxy to this server's port 80."
        fi
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
      docker build -t "$IMAGE_NAME" .
      log "Image rebuilt."
    fi
  else
    info "This takes 1-2 minutes the first time..."
    docker build -t "$IMAGE_NAME" .
    log "Image built."
  fi
  mark_done "build"

  # ── Step 5: Start container ──
  step 5 "Starting container"

  if docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    log "Container already running."
  elif docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    # Exists but stopped — check if it needs recreating (new image)
    info "Container exists but is stopped. Starting..."
    docker start "$CONTAINER_NAME"
    log "Started."
  else
    # Determine ports
    PORTS="-p 80:80 -p 443:443"
    CADDYFILE_DOMAIN=$(grep -v '^#' caddy-config/Caddyfile 2>/dev/null | head -1 | tr -d ' {')
    if [ "$CADDYFILE_DOMAIN" = ":80" ]; then
      PORTS="-p 80:80"
    fi

    docker run -d \
      --name "$CONTAINER_NAME" \
      --restart unless-stopped \
      $PORTS \
      -v "$(pwd)/config.json:/app/config.json:ro" \
      -v "$(pwd)/caddy-config/Caddyfile:/etc/caddy/Caddyfile:ro" \
      -v "${DATA_VOLUME}:/app/data" \
      -v "${CADDY_VOLUME}:/data/caddy" \
      "$IMAGE_NAME"
    log "Container started."
  fi
  mark_done "container"

  # ── Step 6: Verify ──
  step 6 "Verifying"

  info "Waiting for startup..."
  sleep 5

  if docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    # Check if Node.js is responding
    HEALTHY=false
    for i in 1 2 3; do
      if docker exec "$CONTAINER_NAME" wget -qO- http://localhost:3000/api/stats &>/dev/null; then
        HEALTHY=true
        break
      fi
      sleep 2
    done

    if $HEALTHY; then
      log "All services running."
    else
      warn "Container is running but Node.js hasn't responded yet."
      warn "Check logs: ./manage.sh logs"
    fi

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
    echo "     ./manage.sh backup     Backup database"
    echo "     ./manage.sh update     Update to latest version"
    echo ""
  else
    err "Container failed to start."
    echo ""
    echo "   Check what went wrong:"
    echo "     docker logs ${CONTAINER_NAME}"
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

# ─── Start / Stop / Restart ──────────────────────────────────────────────

cmd_start() {
  if docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    warn "Already running."
  elif docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    docker start "$CONTAINER_NAME"
    log "Started."
  else
    err "Container doesn't exist. Run './manage.sh setup' first."
    exit 1
  fi
}

cmd_stop() {
  docker stop "$CONTAINER_NAME" 2>/dev/null && log "Stopped." || warn "Not running."
}

cmd_restart() {
  docker restart "$CONTAINER_NAME" 2>/dev/null && log "Restarted." || err "Not running. Use './manage.sh start'."
}

# ─── Status ───────────────────────────────────────────────────────────────

cmd_status() {
  echo ""
  if docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    log "Container is running."
    echo ""
    docker ps --filter "name=${CONTAINER_NAME}" --format "   Status: {{.Status}}"
    docker ps --filter "name=${CONTAINER_NAME}" --format "   Ports:  {{.Ports}}"
    echo ""

    info "Service health:"
    # Node.js
    if docker exec "$CONTAINER_NAME" wget -qO /dev/null http://localhost:3000/api/stats 2>/dev/null; then
      STATS=$(docker exec "$CONTAINER_NAME" wget -qO- http://localhost:3000/api/stats 2>/dev/null)
      PACKETS=$(echo "$STATS" | grep -oP '"totalPackets":\K[0-9]+' 2>/dev/null || echo "?")
      NODES=$(echo "$STATS" | grep -oP '"totalNodes":\K[0-9]+' 2>/dev/null || echo "?")
      log "  Node.js — ${PACKETS} packets, ${NODES} nodes"
    else
      err "  Node.js — not responding"
    fi

    # Mosquitto
    if docker exec "$CONTAINER_NAME" pgrep mosquitto &>/dev/null; then
      log "  Mosquitto — running"
    else
      err "  Mosquitto — not running"
    fi

    # Caddy
    if docker exec "$CONTAINER_NAME" pgrep caddy &>/dev/null; then
      log "  Caddy — running"
    else
      err "  Caddy — not running"
    fi

    # Disk usage
    DB_SIZE=$(docker exec "$CONTAINER_NAME" du -h /app/data/meshcore.db 2>/dev/null | cut -f1)
    if [ -n "$DB_SIZE" ]; then
      echo ""
      info "Database size: ${DB_SIZE}"
    fi
  else
    err "Container is not running."
    if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
      echo "   Start with: ./manage.sh start"
    else
      echo "   Set up with: ./manage.sh setup"
    fi
  fi
  echo ""
}

# ─── Logs ─────────────────────────────────────────────────────────────────

cmd_logs() {
  docker logs -f "$CONTAINER_NAME" --tail "${1:-100}"
}

# ─── Update ───────────────────────────────────────────────────────────────

cmd_update() {
  info "Pulling latest code..."
  git pull

  info "Rebuilding image..."
  docker build -t "$IMAGE_NAME" .

  # Capture the run config before removing
  CADDYFILE_DOMAIN=$(grep -v '^#' caddy-config/Caddyfile 2>/dev/null | head -1 | tr -d ' {')
  PORTS="-p 80:80 -p 443:443"
  if [ "$CADDYFILE_DOMAIN" = ":80" ]; then
    PORTS="-p 80:80"
  fi

  info "Restarting with new image..."
  docker stop "$CONTAINER_NAME" 2>/dev/null || true
  docker rm "$CONTAINER_NAME" 2>/dev/null || true

  docker run -d \
    --name "$CONTAINER_NAME" \
    --restart unless-stopped \
    $PORTS \
    -v "$(pwd)/config.json:/app/config.json:ro" \
    -v "$(pwd)/caddy-config/Caddyfile:/etc/caddy/Caddyfile:ro" \
    -v "${DATA_VOLUME}:/app/data" \
    -v "${CADDY_VOLUME}:/data/caddy" \
    "$IMAGE_NAME"

  log "Updated and restarted. Data preserved."
}

# ─── Backup ───────────────────────────────────────────────────────────────

cmd_backup() {
  DEST="${1:-./backups/meshcore-$(date +%Y%m%d-%H%M%S).db}"
  mkdir -p "$(dirname "$DEST")"

  # Try volume path first, fall back to docker cp
  DB_PATH=$(docker volume inspect "$DATA_VOLUME" --format '{{ .Mountpoint }}' 2>/dev/null)/meshcore.db

  if [ -f "$DB_PATH" ]; then
    cp "$DB_PATH" "$DEST"
  elif docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    docker cp "${CONTAINER_NAME}:/app/data/meshcore.db" "$DEST" 2>/dev/null
  else
    err "Can't find database. Is the container running?"
    exit 1
  fi

  if [ -f "$DEST" ]; then
    SIZE=$(du -h "$DEST" | cut -f1)
    log "Backed up to ${DEST} (${SIZE})"
  else
    err "Backup failed."
    exit 1
  fi
}

# ─── Restore ──────────────────────────────────────────────────────────────

cmd_restore() {
  if [ -z "$1" ]; then
    err "Usage: ./manage.sh restore <backup-file.db>"
    # List available backups
    if [ -d "./backups" ]; then
      echo ""
      echo "   Available backups:"
      ls -lh ./backups/*.db 2>/dev/null | awk '{print "     " $NF " (" $5 ")"}'
    fi
    exit 1
  fi
  if [ ! -f "$1" ]; then
    err "File not found: $1"
    exit 1
  fi

  warn "This will stop the analyzer, replace the database with $1, and restart."
  if ! confirm "Continue?"; then
    echo "   Aborted."
    exit 0
  fi

  # Backup current DB first
  info "Backing up current database first..."
  cmd_backup "./backups/meshcore-pre-restore-$(date +%Y%m%d-%H%M%S).db"

  docker stop "$CONTAINER_NAME" 2>/dev/null || true

  DB_PATH=$(docker volume inspect "$DATA_VOLUME" --format '{{ .Mountpoint }}' 2>/dev/null)/meshcore.db
  if [ -f "$DB_PATH" ] || [ -d "$(dirname "$DB_PATH")" ]; then
    cp "$1" "$DB_PATH"
  else
    docker cp "$1" "${CONTAINER_NAME}:/app/data/meshcore.db"
  fi

  docker start "$CONTAINER_NAME"
  log "Restored from $1 and restarted."
}

# ─── MQTT Test ────────────────────────────────────────────────────────────

cmd_mqtt_test() {
  if ! docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    err "Container not running. Start with: ./manage.sh start"
    exit 1
  fi

  info "Listening for MQTT messages (10 second timeout)..."
  MSG=$(docker exec "$CONTAINER_NAME" mosquitto_sub -h localhost -t 'meshcore/#' -C 1 -W 10 2>/dev/null)
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
  warn "This will remove the container, image, and setup state."
  warn "Your config.json, Caddyfile, and data volume are NOT deleted."
  echo ""
  if ! confirm "Continue?"; then
    echo "   Aborted."
    exit 0
  fi

  docker stop "$CONTAINER_NAME" 2>/dev/null || true
  docker rm "$CONTAINER_NAME" 2>/dev/null || true
  docker rmi "$IMAGE_NAME" 2>/dev/null || true
  rm -f "$STATE_FILE"

  log "Reset complete. Run './manage.sh setup' to start over."
  echo "   Data volume preserved. To delete it: docker volume rm ${DATA_VOLUME}"
}

# ─── Help ─────────────────────────────────────────────────────────────────

cmd_help() {
  echo ""
  echo "MeshCore Analyzer — Management Script"
  echo ""
  echo "Usage: ./manage.sh <command>"
  echo ""
  echo "  ${BOLD}Setup${NC}"
  echo "    setup        First-time setup wizard (safe to re-run)"
  echo "    reset        Remove container + image (keeps data + config)"
  echo ""
  echo "  ${BOLD}Run${NC}"
  echo "    start        Start the container"
  echo "    stop         Stop the container"
  echo "    restart      Restart the container"
  echo "    status       Show health, stats, and service status"
  echo "    logs [N]     Follow logs (last N lines, default 100)"
  echo ""
  echo "  ${BOLD}Maintain${NC}"
  echo "    update       Pull latest code, rebuild, restart (keeps data)"
  echo "    backup [f]   Backup database (default: ./backups/timestamped)"
  echo "    restore <f>  Restore database from backup (backs up current first)"
  echo "    mqtt-test    Check if MQTT data is flowing"
  echo ""
}

# ─── Main ─────────────────────────────────────────────────────────────────

case "${1:-help}" in
  setup)     cmd_setup ;;
  start)     cmd_start ;;
  stop)      cmd_stop ;;
  restart)   cmd_restart ;;
  status)    cmd_status ;;
  logs)      cmd_logs "$2" ;;
  update)    cmd_update ;;
  backup)    cmd_backup "$2" ;;
  restore)   cmd_restore "$2" ;;
  mqtt-test) cmd_mqtt_test ;;
  reset)     cmd_reset ;;
  help|*)    cmd_help ;;
esac
