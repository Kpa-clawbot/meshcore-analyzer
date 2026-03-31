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
# Strip \r (Windows line endings) to avoid "$'\r': command not found"
if [ -f .env ]; then
  set -a
  eval "$(sed 's/\r$//' .env)"
  set +a
fi

# Resolved paths for prod/staging data (must match docker-compose.yml)
PROD_DATA="${PROD_DATA_DIR:-$HOME/meshcore-data}"
STAGING_DATA="${STAGING_DATA_DIR:-$HOME/meshcore-staging-data}"
STAGING_COMPOSE_FILE="docker-compose.staging.yml"

# Build metadata — exported so docker compose build picks them up via args
export APP_VERSION=$(node -p "require('./package.json').version" 2>/dev/null || echo "unknown")
export GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
export BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# Docker Compose — detect v2 plugin vs v1 standalone
if docker compose version &>/dev/null 2>&1; then
  DC="docker compose"
elif command -v docker-compose &>/dev/null; then
  DC="docker-compose"
else
  echo "ERROR: Neither '$DC' nor 'docker-compose' found." >&2
  exit 1
fi

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

PORT_CHECK_METHOD=""

resolve_port_check_method() {
  if [ -n "$PORT_CHECK_METHOD" ]; then
    return 0
  fi

  if command -v ss &>/dev/null; then
    PORT_CHECK_METHOD="ss"
  elif command -v lsof &>/dev/null; then
    PORT_CHECK_METHOD="lsof"
  elif command -v netstat &>/dev/null; then
    PORT_CHECK_METHOD="netstat"
  elif command -v nc &>/dev/null; then
    PORT_CHECK_METHOD="nc"
  else
    PORT_CHECK_METHOD="none"
  fi
}

# Returns 0 when in use, 1 when free, 2 when unavailable
is_port_in_use() {
  local port="$1"
  resolve_port_check_method

  case "$PORT_CHECK_METHOD" in
    ss)
      ss -tlnp 2>/dev/null | grep -E "[[:space:]]LISTEN[[:space:]].*[:.]${port}([[:space:]]|$)" >/dev/null
      return $?
      ;;
    lsof)
      lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1
      return $?
      ;;
    netstat)
      netstat -tlnp 2>/dev/null | grep -E "[[:space:]]${port}[[:space:]]" >/dev/null
      if [ $? -eq 0 ]; then
        return 0
      fi
      netstat -tlnp 2>/dev/null | grep -E "[:.]${port}[[:space:]]" >/dev/null
      return $?
      ;;
    nc)
      local bind_pid=""
      ( nc -l 127.0.0.1 "$port" >/dev/null 2>&1 ) &
      bind_pid=$!
      sleep 0.2
      if kill -0 "$bind_pid" 2>/dev/null; then
        kill "$bind_pid" 2>/dev/null || true
        wait "$bind_pid" 2>/dev/null || true
        return 1
      fi
      wait "$bind_pid" 2>/dev/null || true
      return 0
      ;;
    *)
      return 2
      ;;
  esac
}

port_in_use_details() {
  local port="$1"
  resolve_port_check_method

  case "$PORT_CHECK_METHOD" in
    ss)
      ss -tlnp 2>/dev/null | grep -E "[[:space:]]LISTEN[[:space:]].*[:.]${port}([[:space:]]|$)" | head -1
      ;;
    lsof)
      lsof -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null | sed -n '2p'
      ;;
    netstat)
      netstat -tlnp 2>/dev/null | grep -E "[:.]${port}[[:space:]]" | head -1
      ;;
    *)
      echo ""
      ;;
  esac
}

find_next_available_port() {
  local start="$1"
  local candidate=$((start + 1))
  while [ "$candidate" -le 65535 ]; do
    is_port_in_use "$candidate"
    local rc=$?
    if [ "$rc" -eq 0 ]; then
      candidate=$((candidate + 1))
      continue
    fi
    if [ "$rc" -eq 1 ]; then
      echo "$candidate"
      return 0
    fi
    break
  done
  echo ""
  return 1
}

is_valid_port() {
  local value="$1"
  [[ "$value" =~ ^[0-9]+$ ]] && [ "$value" -ge 1 ] && [ "$value" -le 65535 ]
}

show_env_port_summary() {
  local http_port="$1"
  local https_port="$2"
  local mqtt_port="$3"
  local data_dir="$4"
  echo ""
  echo "   Current .env values:"
  echo "     PROD_HTTP_PORT=${http_port}"
  echo "     PROD_HTTPS_PORT=${https_port}"
  echo "     PROD_MQTT_PORT=${mqtt_port}"
  echo "     PROD_DATA_DIR=${data_dir}"
  echo ""
}

get_env_value() {
  local key="$1"
  local env_file="${2:-.env}"
  if [ ! -f "$env_file" ]; then
    echo ""
    return 1
  fi
  sed -n "s/^[[:space:]]*${key}[[:space:]]*=[[:space:]]*//p" "$env_file" | head -1
}

write_env_managed_values() {
  local http_port="$1"
  local https_port="$2"
  local mqtt_port="$3"
  local data_dir="$4"
  local env_file=".env"
  local tmp_file=".env.tmp.$$"

  if [ ! -f "$env_file" ]; then
    cp .env.example "$env_file"
  fi

  local seen_http=0
  local seen_https=0
  local seen_mqtt=0
  local seen_data=0

  : > "$tmp_file"
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      PROD_HTTP_PORT=*)
        echo "PROD_HTTP_PORT=${http_port}" >> "$tmp_file"
        seen_http=1
        ;;
      PROD_HTTPS_PORT=*)
        echo "PROD_HTTPS_PORT=${https_port}" >> "$tmp_file"
        seen_https=1
        ;;
      PROD_MQTT_PORT=*)
        echo "PROD_MQTT_PORT=${mqtt_port}" >> "$tmp_file"
        seen_mqtt=1
        ;;
      PROD_DATA_DIR=*)
        echo "PROD_DATA_DIR=${data_dir}" >> "$tmp_file"
        seen_data=1
        ;;
      *)
        echo "$line" >> "$tmp_file"
        ;;
    esac
  done < "$env_file"

  [ "$seen_http" -eq 1 ] || echo "PROD_HTTP_PORT=${http_port}" >> "$tmp_file"
  [ "$seen_https" -eq 1 ] || echo "PROD_HTTPS_PORT=${https_port}" >> "$tmp_file"
  [ "$seen_mqtt" -eq 1 ] || echo "PROD_MQTT_PORT=${mqtt_port}" >> "$tmp_file"
  [ "$seen_data" -eq 1 ] || echo "PROD_DATA_DIR=${data_dir}" >> "$tmp_file"

  mv "$tmp_file" "$env_file"
}

prompt_for_port() {
  local label="$1"
  local current="$2"
  local prompt_default="$3"

  while true; do
    if [ -n "$prompt_default" ] && [ "$prompt_default" != "$current" ]; then
      read -p "   ${label} port [${prompt_default}] (current ${current}): " selected
      selected=${selected:-$prompt_default}
    else
      read -p "   ${label} port [${current}]: " selected
      selected=${selected:-$current}
    fi

    if ! is_valid_port "$selected"; then
      warn "Invalid port '${selected}'. Enter a value between 1 and 65535."
      continue
    fi

    is_port_in_use "$selected"
    local rc=$?
    if [ "$rc" -eq 0 ]; then
      warn "Port ${selected} is in use."
      local details
      details=$(port_in_use_details "$selected")
      [ -n "$details" ] && echo "     ${details}"
      if confirm "Use ${selected} anyway? (start will fail if still occupied)"; then
        echo "$selected"
        return 0
      fi
      continue
    fi
    if [ "$rc" -eq 2 ]; then
      warn "Port detection unavailable on this host. Proceeding with chosen value."
    fi

    echo "$selected"
    return 0
  done
}

preflight_validate_prod_ports() {
  local http_port="${PROD_HTTP_PORT:-80}"
  local https_port="${PROD_HTTPS_PORT:-443}"
  local mqtt_port="${PROD_MQTT_PORT:-1883}"
  local failed=0

  info "Preflight: validating configured ports are free..."
  for port in "$http_port" "$https_port" "$mqtt_port"; do
    if is_port_in_use "$port"; then
      err "Port ${port} is in use."
      local details
      details=$(port_in_use_details "$port")
      [ -n "$details" ] && echo "   ${details}"
      failed=1
    fi
  done

  if [ "$failed" -eq 1 ]; then
    echo ""
    echo "   Remediation:"
    echo "     • Stop the process using the conflicting port(s)"
    echo "     • Or run ./manage.sh setup and re-negotiate ports"
    echo "     • Then re-run this command"
    return 1
  fi

  log "Preflight port validation passed."
  return 0
}

# Check config.json for placeholder values
check_config_placeholders() {
  local cfg="${1:-$PROD_DATA/config.json}"
  if [ -f "$cfg" ]; then
    if grep -qE 'your-username|your-password|your-secret|example\.com|changeme' "$cfg" 2>/dev/null; then
      warn "config.json contains placeholder values."
      warn "Edit ${cfg} and replace placeholder values before deploying."
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
  log "Compose: $DC"
  
  mark_done "docker"

  # ── Step 2: Config ──
  step 2 "Configuration"

  if [ -f "$PROD_DATA/config.json" ]; then
    log "config.json found in data directory."
    # Sanity check the JSON
    if ! python3 -c "import json; json.load(open('$PROD_DATA/config.json'))" 2>/dev/null && \
       ! node -e "JSON.parse(require('fs').readFileSync('$PROD_DATA/config.json'))" 2>/dev/null; then
      err "config.json has invalid JSON. Fix it and re-run setup."
      exit 1
    fi
    log "config.json is valid JSON."
    check_config_placeholders "$PROD_DATA/config.json"
  elif [ -f config.json ]; then
    # Legacy: config in repo root — move it to data dir
    info "Found config.json in repo root — moving to data directory..."
    mkdir -p "$PROD_DATA"
    cp config.json "$PROD_DATA/config.json"
    log "Config moved to ${PROD_DATA}/config.json"
    check_config_placeholders "$PROD_DATA/config.json"
  else
    info "Creating config.json in data directory from example..."
    mkdir -p "$PROD_DATA"
    cp config.example.json "$PROD_DATA/config.json"

    # Generate a random API key
    if command -v openssl &> /dev/null; then
      API_KEY=$(openssl rand -hex 16)
    else
      API_KEY=$(head -c 32 /dev/urandom | xxd -p | head -c 32)
    fi
    # Replace the placeholder API key
    if command -v sed &> /dev/null; then
      sed -i "s/your-secret-api-key-here/${API_KEY}/" "$PROD_DATA/config.json"
    fi

    log "Created config.json with random API key."
    check_config_placeholders "$PROD_DATA/config.json"
    echo ""
    echo "   Config saved to: ${PROD_DATA}/config.json"
    echo "   Edit with: nano ${PROD_DATA}/config.json"
    echo ""
  fi
  mark_done "config"

  # ── Step 3: Ports & Networking ──
  step 3 "Ports & Networking"

  local default_http=80
  local default_https=443
  local default_mqtt=1883
  local selected_http="$default_http"
  local selected_https="$default_https"
  local selected_mqtt="$default_mqtt"
  local selected_data_dir="${PROD_DATA_DIR:-$HOME/meshcore-data}"

  local env_http=""
  local env_https=""
  local env_mqtt=""
  local env_data_dir=""

  if [ -f .env ]; then
    env_http=$(get_env_value "PROD_HTTP_PORT" ".env")
    env_https=$(get_env_value "PROD_HTTPS_PORT" ".env")
    env_mqtt=$(get_env_value "PROD_MQTT_PORT" ".env")
    env_data_dir=$(get_env_value "PROD_DATA_DIR" ".env")
    [ -n "$env_data_dir" ] && selected_data_dir="$env_data_dir"
    show_env_port_summary "${env_http:-<unset>}" "${env_https:-<unset>}" "${env_mqtt:-<unset>}" "${env_data_dir:-<unset>}"
  else
    info ".env not found. It will be created from .env.example."
  fi

  local has_current_ports=false
  if is_valid_port "$env_http" && is_valid_port "$env_https" && is_valid_port "$env_mqtt"; then
    has_current_ports=true
  fi

  local renegotiate=true
  if [ -f .env ] && $has_current_ports; then
    if confirm "Keep current ports from .env?"; then
      renegotiate=false
      selected_http="$env_http"
      selected_https="$env_https"
      selected_mqtt="$env_mqtt"
      log "Keeping current ports from .env."
    fi
  fi

  if $renegotiate; then
    resolve_port_check_method
    if [ "$PORT_CHECK_METHOD" = "none" ]; then
      warn "No supported port detection tool found (ss/lsof/netstat/nc)."
      warn "You'll still be prompted, but conflicts cannot be detected now."
    else
      info "Detecting listeners using ${PORT_CHECK_METHOD}..."
    fi

    local suggested_http="$default_http"
    local suggested_https="$default_https"
    local suggested_mqtt="$default_mqtt"

    if is_port_in_use "$default_http"; then
      warn "Port ${default_http} is in use."
      local details_http
      details_http=$(port_in_use_details "$default_http")
      [ -n "$details_http" ] && echo "     ${details_http}"
      suggested_http=$(find_next_available_port "$default_http")
      [ -n "$suggested_http" ] && info "Suggested HTTP port: ${suggested_http}"
    fi

    if is_port_in_use "$default_https"; then
      warn "Port ${default_https} is in use."
      local details_https
      details_https=$(port_in_use_details "$default_https")
      [ -n "$details_https" ] && echo "     ${details_https}"
      suggested_https=$(find_next_available_port "$default_https")
      [ -n "$suggested_https" ] && info "Suggested HTTPS port: ${suggested_https}"
    fi

    if is_port_in_use "$default_mqtt"; then
      warn "Port ${default_mqtt} is in use."
      local details_mqtt
      details_mqtt=$(port_in_use_details "$default_mqtt")
      [ -n "$details_mqtt" ] && echo "     ${details_mqtt}"
      suggested_mqtt=$(find_next_available_port "$default_mqtt")
      [ -n "$suggested_mqtt" ] && info "Suggested MQTT port: ${suggested_mqtt}"
    fi

    selected_http=$(prompt_for_port "HTTP" "$default_http" "$suggested_http")
    selected_https=$(prompt_for_port "HTTPS" "$default_https" "$suggested_https")
    selected_mqtt=$(prompt_for_port "MQTT" "$default_mqtt" "$suggested_mqtt")
  fi

  if [ -f caddy-config/Caddyfile ]; then
    EXISTING_DOMAIN=$(grep -v '^#' caddy-config/Caddyfile 2>/dev/null | head -1 | tr -d ' {')
    if [ "$EXISTING_DOMAIN" = ":80" ] || [ "$EXISTING_DOMAIN" = ":${selected_http}" ]; then
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
    echo "   2) Behind my own reverse proxy — HTTP only"
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
        ;;
      2)
        echo ":${selected_http} {
    reverse_proxy localhost:3000
}" > caddy-config/Caddyfile
        log "Caddyfile created (HTTP only on port ${selected_http})."
        echo "   Point your reverse proxy or tunnel to this server's port ${selected_http}."
        ;;
      *)
        warn "Invalid choice. Defaulting to HTTP only."
        echo ":${selected_http} {
    reverse_proxy localhost:3000
}" > caddy-config/Caddyfile
        ;;
    esac
  fi

  write_env_managed_values "$selected_http" "$selected_https" "$selected_mqtt" "$selected_data_dir"
  log "Saved negotiated ports to .env"
  show_env_port_summary "$selected_http" "$selected_https" "$selected_mqtt" "$selected_data_dir"

  echo "   Resolved port mapping:"
  echo "     UI HTTP:  ${selected_http}"
  echo "     UI HTTPS: ${selected_https}"
  echo "     MQTT:     ${selected_mqtt}"
  echo ""
  if ! confirm "Proceed to build/start with these ports?"; then
    echo "   Setup cancelled. Re-run ./manage.sh setup when ready."
    exit 0
  fi

  export PROD_HTTP_PORT="$selected_http"
  export PROD_HTTPS_PORT="$selected_https"
  export PROD_MQTT_PORT="$selected_mqtt"
  export PROD_DATA_DIR="$selected_data_dir"
  PROD_DATA="$PROD_DATA_DIR"
  mark_done "caddyfile"

  # ── Step 4: Build ──
  step 4 "Building Docker image"

  # Check if image exists and source hasn't changed
  IMAGE_EXISTS=$(docker images -q "$IMAGE_NAME" 2>/dev/null)
  if [ -n "$IMAGE_EXISTS" ] && is_done "build"; then
    log "Image already built."
    if confirm "Rebuild? (only needed if you updated the code)"; then
      $DC build prod
      log "Image rebuilt."
    fi
  else
    info "This takes 1-2 minutes the first time..."
    $DC build prod
    log "Image built."
  fi
  mark_done "build"

  # ── Step 5: Start container ──
  step 5 "Starting container"

  if docker ps --format '{{.Names}}' | grep -q "^corescope-prod$"; then
    info "Production container already running — skipping preflight port check."
  else
    if ! preflight_validate_prod_ports; then
      exit 1
    fi
  fi

  # Detect existing data directories
  if [ -d "$PROD_DATA" ] && [ -f "$PROD_DATA/meshcore.db" ]; then
    info "Found existing data at $PROD_DATA/ — will use bind mount."
  fi

  if docker ps --format '{{.Names}}' | grep -q "^corescope-prod$"; then
    log "Container already running."
  else
    mkdir -p "$PROD_DATA"
    $DC up -d prod
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
    echo "     $DC logs prod"
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
  mkdir -p "$STAGING_DATA"

  # Docker may have created config.json as a directory
  [ -d "$staging_config" ] && rmdir "$staging_config" 2>/dev/null || true

  if [ ! -f "$prod_config" ]; then
    warn "No production config at ${prod_config} — staging may use defaults."
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

# Ensure config.json exists in the data directory before starting
ensure_config() {
  local data_dir="$1"
  local config="$data_dir/config.json"
  mkdir -p "$data_dir"

  # Docker may have created config.json as a directory from a prior failed mount
  [ -d "$config" ] && rmdir "$config" 2>/dev/null || true

  if [ -f "$config" ]; then
    return 0
  fi

  # Try to copy from repo root (legacy location)
  if [ -f ./config.json ]; then
    info "No config in data directory — copying from ./config.json"
    cp ./config.json "$config"
    return 0
  fi

  # Prompt admin
  echo ""
  warn "No config.json found in ${data_dir}/"
  echo ""
  echo "   CoreScope needs a config.json to connect to MQTT brokers."
  echo ""
  echo "   Options:"
  echo "     1) Create from example (you'll edit MQTT settings after)"
  echo "     2) I'll put one there myself (abort for now)"
  echo ""
  read -p "   Choose [1/2]: " -n 1 -r
  echo ""

  case $REPLY in
    1)
      cp config.example.json "$config"
      # Generate a random API key
      if command -v openssl &>/dev/null; then
        API_KEY=$(openssl rand -hex 16)
      else
        API_KEY=$(head -c 32 /dev/urandom | xxd -p | head -c 32)
      fi
      sed -i "s/your-secret-api-key-here/${API_KEY}/" "$config" 2>/dev/null || true
      log "Created ${config} from example with random API key."
      warn "Edit MQTT settings before connecting observers:"
      echo "     nano ${config}"
      echo ""
      ;;
    *)
      echo "   Place your config.json at: ${config}"
      echo "   Then run this command again."
      exit 0
      ;;
  esac
}

cmd_start() {
  local WITH_STAGING=false
  if [ "$1" = "--with-staging" ]; then
    WITH_STAGING=true
  fi

  if docker ps --format '{{.Names}}' | grep -q "^corescope-prod$"; then
    info "Production container already running — skipping preflight port check."
  else
    if ! preflight_validate_prod_ports; then
      exit 1
    fi
  fi

  # Always check prod config
  ensure_config "$PROD_DATA"

  if $WITH_STAGING; then
    # Prepare staging data and config
    prepare_staging_db
    prepare_staging_config

    info "Starting production container (corescope-prod) on ports ${PROD_HTTP_PORT:-80}/${PROD_HTTPS_PORT:-443}..."
    info "Starting staging container (corescope-staging-go) on port ${STAGING_GO_HTTP_PORT:-82}..."
    $DC up -d prod
    $DC -f "$STAGING_COMPOSE_FILE" -p corescope-staging up -d staging-go
    log "Production started on ports ${PROD_HTTP_PORT:-80}/${PROD_HTTPS_PORT:-443}/${PROD_MQTT_PORT:-1883}"
    log "Staging started on port ${STAGING_GO_HTTP_PORT:-82} (MQTT: ${STAGING_GO_MQTT_PORT:-1885})"
  else
    info "Starting production container (corescope-prod) on ports ${PROD_HTTP_PORT:-80}/${PROD_HTTPS_PORT:-443}..."
    $DC up -d prod
    log "Production started. Staging NOT running (use --with-staging to start both)."
  fi
}

cmd_stop() {
  local TARGET="${1:-all}"

  case "$TARGET" in
    prod)
      info "Stopping production container (corescope-prod)..."
      $DC stop prod
      log "Production stopped."
      ;;
    staging)
      info "Stopping staging container (corescope-staging-go)..."
      $DC -f "$STAGING_COMPOSE_FILE" -p corescope-staging rm -sf staging-go 2>/dev/null || true
      docker rm -f corescope-staging-go meshcore-staging-go corescope-staging meshcore-staging 2>/dev/null || true
      log "Staging stopped and cleaned up."
      ;;
    all)
      info "Stopping all containers..."
      $DC stop prod
      $DC -f "$STAGING_COMPOSE_FILE" -p corescope-staging rm -sf staging-go 2>/dev/null || true
      docker rm -f corescope-staging-go meshcore-staging-go corescope-staging meshcore-staging 2>/dev/null || true
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
      $DC up -d --force-recreate prod
      log "Production restarted."
      ;;
    staging)
      info "Restarting staging container (corescope-staging-go)..."
      # Stop and remove old container
      $DC -f "$STAGING_COMPOSE_FILE" -p corescope-staging rm -sf staging-go 2>/dev/null || true
      docker rm -f corescope-staging-go 2>/dev/null || true
      # Wait for container to be fully gone and memory to be reclaimed
      # This prevents OOM when old + new containers overlap on small VMs
      for i in $(seq 1 15); do
        if ! docker ps -a --format '{{.Names}}' | grep -q 'corescope-staging-go'; then
          break
        fi
        sleep 1
      done
      sleep 3  # extra pause for OS to reclaim memory
      # Verify config exists before starting
      local staging_config="${STAGING_DATA_DIR:-$HOME/meshcore-staging-data}/config.json"
      if [ ! -f "$staging_config" ]; then
        warn "Staging config not found at $staging_config — creating from prod config..."
        prepare_staging_config
      fi
      $DC -f "$STAGING_COMPOSE_FILE" -p corescope-staging up -d staging-go
      log "Staging restarted."
      ;;
    all)
      info "Restarting all containers..."
      $DC up -d --force-recreate prod
      $DC -f "$STAGING_COMPOSE_FILE" -p corescope-staging rm -sf staging-go 2>/dev/null || true
      docker rm -f corescope-staging-go 2>/dev/null || true
      $DC -f "$STAGING_COMPOSE_FILE" -p corescope-staging up -d staging-go
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
  if container_running "corescope-staging-go"; then
    show_container_status "corescope-staging-go" "Staging"
  else
    info "Staging (corescope-staging-go): Not running (use --with-staging to start both)"
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
      $DC logs -f --tail="$LINES" prod
      ;;
    staging)
      if container_running "corescope-staging"; then
        info "Tailing staging logs..."
        $DC -f "$STAGING_COMPOSE_FILE" -p corescope-staging logs -f --tail="$LINES" staging-go
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
  staging_image=$(docker inspect corescope-staging-go --format '{{.Config.Image}}' 2>/dev/null || echo "not running")
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
  $DC up -d --force-recreate prod

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
  git pull --ff-only

  info "Rebuilding image..."
  $DC build prod

  info "Restarting with new image..."
  $DC up -d --force-recreate prod

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

  # Config (now lives in data dir)
  if [ -f "$PROD_DATA/config.json" ]; then
    cp "$PROD_DATA/config.json" "$BACKUP_DIR/config.json"
    log "config.json"
  elif [ -f config.json ]; then
    cp config.json "$BACKUP_DIR/config.json"
    log "config.json (legacy repo root)"
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

  $DC stop prod 2>/dev/null || true

  # Restore database
  mkdir -p "$PROD_DATA"
  DEST_DB="$PROD_DATA/meshcore.db"
  cp "$DB_FILE" "$DEST_DB"
  log "Database restored"

  # Restore config if present
  if [ -n "$CONFIG_FILE" ] && [ -f "$CONFIG_FILE" ]; then
    cp "$CONFIG_FILE" "$PROD_DATA/config.json"
    log "config.json restored to ${PROD_DATA}/"
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

  $DC up -d prod
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

  $DC down --rmi local 2>/dev/null || true
  $DC -f "$STAGING_COMPOSE_FILE" -p corescope-staging down --rmi local 2>/dev/null || true
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
  echo "    start --with-staging  Start production + staging-go (copies prod DB + config)"
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
  echo "Prod uses docker-compose.yml; staging uses ${STAGING_COMPOSE_FILE}."
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
