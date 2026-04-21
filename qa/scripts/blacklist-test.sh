#!/usr/bin/env bash
# blacklist-test.sh — verify nodeBlacklist hides a pubkey from API surface
# while retaining its packets in the DB. Implements QA plan §10.1 + §10.2.
#
# Usage:
#   blacklist-test.sh BASELINE_URL TARGET_URL
#
# BASELINE_URL is currently unused for assertions but kept as a positional
# arg for parity with other qa-suite scripts (always called with two URLs).
#
# Required env (target host control + test data):
#   TEST_NODE_PUBKEY      — hex pubkey of a real, currently-visible node on TARGET_URL
#   TARGET_SSH_HOST       — e.g. runner@example
#   TARGET_SSH_KEY        — path to ssh private key (default: /root/.ssh/id_ed25519)
#   TARGET_CONFIG_PATH    — absolute path to config.json on the target
#   TARGET_CONTAINER      — docker container name on the target
# Optional env:
#   TARGET_DB_PATH        — sqlite db path on the target (for §10.2 sqlite probe)
#   ADMIN_API_TOKEN       — if /api/admin/transmissions exists, use it instead of ssh+sqlite
#                            (read from env, not argv — never appears in ps)
#   CURL_TIMEOUT          — per-request curl timeout, seconds (default 60)
#   RESTART_WAIT_S        — max wait for /api/stats after restart (default 120)
#
# Distinguishes:
#   ssh-failed     → cannot reach/control target
#   restart-stuck  → /api/stats not 200 within RESTART_WAIT_S
#   hide-failed    → blacklisted pubkey still surfaced via API (§10.1 fail)
#   retain-failed  → blacklisted pubkey absent from DB (§10.2 fail)
#   teardown-failed→ post-test removal did not restore listing
#
# Exit code = number of failures (0 = pass).
# PUBLIC repo: zero PII — no real pubkeys, IPs, or hostnames as defaults.

set -uo pipefail

BASELINE_URL="${1:-}"
TARGET_URL="${2:-}"
if [[ -z "$BASELINE_URL" || -z "$TARGET_URL" ]]; then
  echo "usage: $0 BASELINE_URL TARGET_URL  (TEST_NODE_PUBKEY+TARGET_* via env)" >&2
  exit 2
fi

TEST_PUBKEY="${TEST_NODE_PUBKEY:-}"
TARGET_SSH_HOST="${TARGET_SSH_HOST:-}"
TARGET_SSH_KEY="${TARGET_SSH_KEY:-/root/.ssh/id_ed25519}"
TARGET_CONFIG_PATH="${TARGET_CONFIG_PATH:-}"
TARGET_CONTAINER="${TARGET_CONTAINER:-}"
TARGET_DB_PATH="${TARGET_DB_PATH:-}"
ADMIN_API_TOKEN="${ADMIN_API_TOKEN:-}"

if [[ -z "$TEST_PUBKEY" || -z "$TARGET_SSH_HOST" || -z "$TARGET_CONFIG_PATH" || -z "$TARGET_CONTAINER" ]]; then
  echo "error: TEST_NODE_PUBKEY, TARGET_SSH_HOST, TARGET_CONFIG_PATH, TARGET_CONTAINER are required" >&2
  exit 2
fi

# Hard input validation — these strings are interpolated into remote shell/SQL.
# Pubkey must be hex (MeshCore pubkeys are hex-encoded ed25519 prefixes).
if ! [[ "$TEST_PUBKEY" =~ ^[0-9a-fA-F]+$ ]]; then
  echo "error: TEST_NODE_PUBKEY must be hex (got: redacted)" >&2
  exit 2
fi
# Container name must match docker's allowed chars: [a-zA-Z0-9][a-zA-Z0-9_.-]*
if ! [[ "$TARGET_CONTAINER" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]]; then
  echo "error: TARGET_CONTAINER has illegal chars" >&2
  exit 2
fi
# Config path must be an absolute, sane path (no spaces, quotes, $, ;, etc.).
if ! [[ "$TARGET_CONFIG_PATH" =~ ^/[A-Za-z0-9_./-]+$ ]]; then
  echo "error: TARGET_CONFIG_PATH must be a sane absolute path" >&2
  exit 2
fi
if [[ -n "$TARGET_DB_PATH" ]] && ! [[ "$TARGET_DB_PATH" =~ ^/[A-Za-z0-9_./-]+$ ]]; then
  echo "error: TARGET_DB_PATH must be a sane absolute path" >&2
  exit 2
fi

CURL_TIMEOUT="${CURL_TIMEOUT:-60}"
RESTART_WAIT_S="${RESTART_WAIT_S:-120}"

SSH_OPTS=(-i "$TARGET_SSH_KEY" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15 -o BatchMode=yes)
ssh_t() { ssh "${SSH_OPTS[@]}" "$TARGET_SSH_HOST" "$@"; }

TMP=$(mktemp -d)
fails=0
TEARDOWN_DONE=0

# -----------------------------------------------------------------------------
# Teardown — MANDATORY in all exit paths.
# -----------------------------------------------------------------------------
teardown() {
  local rc=$?
  if [[ "$TEARDOWN_DONE" == "1" ]]; then rm -rf "$TMP"; exit "$rc"; fi
  TEARDOWN_DONE=1
  echo "=== teardown: removing $TEST_PUBKEY from nodeBlacklist ==="
  if remove_from_blacklist && restart_target && wait_for_stats; then
    if node_visible; then
      echo "  ✅ teardown ok — node returned to listings"
    else
      echo "  ❌ teardown-failed: node still hidden after removal"
      rc=$((rc + 1))
    fi
  else
    echo "  ❌ teardown-failed: could not restore config / restart / stats"
    rc=$((rc + 1))
  fi
  rm -rf "$TMP"
  exit "$rc"
}
trap teardown EXIT INT TERM

# -----------------------------------------------------------------------------
# Helpers
# -----------------------------------------------------------------------------
fetch_code() {
  local url="$1" out="$2"
  curl -s -m "$CURL_TIMEOUT" -o "$out" -w "%{http_code}" "$url" 2>/dev/null || echo "000"
}

wait_for_stats() {
  local deadline code
  echo "  waiting up to ${RESTART_WAIT_S}s for $TARGET_URL/api/stats ..."
  deadline=$(( $(date +%s) + RESTART_WAIT_S ))
  while (( $(date +%s) < deadline )); do
    code=$(fetch_code "$TARGET_URL/api/stats" "$TMP/stats.json")
    if [[ "$code" == "200" ]]; then echo "  stats OK"; return 0; fi
    sleep 3
  done
  echo "  ❌ restart-stuck: /api/stats never returned 200"
  return 1
}

restart_target() {
  echo "  restarting container $TARGET_CONTAINER ..."
  # TARGET_CONTAINER is validated above; still quote defensively.
  if ! ssh_t "docker restart $(printf %q "$TARGET_CONTAINER")" >/dev/null; then
    echo "  ❌ ssh-failed: docker restart failed"
    return 1
  fi
  return 0
}

# Mutate config.json on target. Values pass via env (printf %q + single-quoted
# heredoc) so $TEST_PUBKEY etc. never enter the remote shell as code.
set_blacklist_state() {
  local mode="$1"  # add | remove
  ssh_t "CFG=$(printf %q "$TARGET_CONFIG_PATH") PK=$(printf %q "$TEST_PUBKEY") MODE=$(printf %q "$mode") bash -s" <<'REMOTE'
set -euo pipefail
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
if command -v jq >/dev/null; then
  if [ "$MODE" = "add" ]; then
    jq --arg pk "$PK" '.nodeBlacklist = ((.nodeBlacklist // []) + [$pk] | unique)' "$CFG" > "$TMP"
  else
    jq --arg pk "$PK" '.nodeBlacklist = ((.nodeBlacklist // []) - [$pk])' "$CFG" > "$TMP"
  fi
else
  python3 - "$CFG" "$PK" "$MODE" "$TMP" <<'PY'
import json, sys
cfg, pk, mode, out = sys.argv[1:]
with open(cfg) as f: d = json.load(f)
bl = list(dict.fromkeys(d.get("nodeBlacklist") or []))
if mode == "add":
    if pk not in bl: bl.append(pk)
else:
    bl = [x for x in bl if x != pk]
d["nodeBlacklist"] = bl
with open(out, "w") as f: json.dump(d, f, indent=2)
PY
fi
# Preserve mode and ownership; mv across same FS is atomic.
chmod --reference="$CFG" "$TMP" 2>/dev/null || true
chown --reference="$CFG" "$TMP" 2>/dev/null || true
mv "$TMP" "$CFG"
trap - EXIT
REMOTE
  local rc=$?
  if (( rc != 0 )); then
    echo "  ❌ ssh-failed: could not edit $TARGET_CONFIG_PATH ($mode)"
    return 1
  fi
  return 0
}

add_to_blacklist()      { set_blacklist_state add; }
remove_from_blacklist() { set_blacklist_state remove; }

node_visible() {
  # Returns 0 if the pubkey is currently visible via API.
  local code
  code=$(fetch_code "$TARGET_URL/api/nodes/$TEST_PUBKEY" "$TMP/node.json")
  if [[ "$code" == "200" ]]; then return 0; fi
  fetch_code "$TARGET_URL/api/nodes?limit=10000" "$TMP/nodes.json" >/dev/null
  if grep -qF -- "\"$TEST_PUBKEY\"" "$TMP/nodes.json" 2>/dev/null; then
    return 0
  fi
  return 1
}

# -----------------------------------------------------------------------------
# §10.1 — hide
# -----------------------------------------------------------------------------
echo "=== §10.1 add $TEST_PUBKEY to nodeBlacklist ==="
if ! add_to_blacklist; then fails=$((fails+1)); exit "$fails"; fi
if ! restart_target;    then fails=$((fails+1)); exit "$fails"; fi
if ! wait_for_stats;    then fails=$((fails+1)); exit "$fails"; fi

detail_code=$(fetch_code "$TARGET_URL/api/nodes/$TEST_PUBKEY" "$TMP/detail.json")
list_code=$(fetch_code "$TARGET_URL/api/nodes?limit=10000" "$TMP/list.json")
in_list=0
if [[ "$list_code" == "200" ]] && grep -qF -- "\"$TEST_PUBKEY\"" "$TMP/list.json"; then
  in_list=1
fi
if [[ "$detail_code" == "404" || "$in_list" == "0" ]]; then
  echo "  ✅ hide ok: detail=$detail_code in_list=$in_list"
else
  echo "  ❌ hide-failed: detail=$detail_code in_list=$in_list — pubkey still surfaced"
  fails=$((fails+1))
fi

topo_code=$(fetch_code "$TARGET_URL/api/topology" "$TMP/topo.json")
if [[ "$topo_code" != "200" ]]; then
  echo "  ⚠️  /api/topology HTTP $topo_code — skipping topology assertion"
elif grep -qF -- "$TEST_PUBKEY" "$TMP/topo.json"; then
  echo "  ❌ hide-failed: /api/topology references blacklisted pubkey"
  fails=$((fails+1))
else
  echo "  ✅ topology clean"
fi

# -----------------------------------------------------------------------------
# §10.2 — DB retain
# -----------------------------------------------------------------------------
echo "=== §10.2 verify packets retained in DB ==="
count=""
if [[ -n "$ADMIN_API_TOKEN" ]]; then
  # Read auth header from stdin so the token never enters argv (ps-safe).
  code=$(printf 'header = "Authorization: Bearer %s"\n' "$ADMIN_API_TOKEN" | \
    curl -s -m "$CURL_TIMEOUT" -K - -o "$TMP/admin.json" -w "%{http_code}" \
      "$TARGET_URL/api/admin/transmissions?from_node=$TEST_PUBKEY&count=1" 2>/dev/null || echo "000")
  if [[ "$code" == "200" ]]; then
    count=$(jq -r '.count // ((.transmissions // []) | length)' "$TMP/admin.json" 2>/dev/null || echo "")
  fi
fi
if [[ -z "$count" ]]; then
  if [[ -z "$TARGET_DB_PATH" ]]; then
    echo "  ❌ retain-failed: TARGET_DB_PATH unset and no ADMIN_API_TOKEN — cannot probe"
    fails=$((fails+1))
  else
    # TEST_PUBKEY is hex-validated → safe to inline single-quoted in SQL.
    # Container/db path also validated; printf %q for defense in depth.
    q="SELECT COUNT(*) FROM transmissions WHERE from_node = '$TEST_PUBKEY';"
    qq=$(printf %q "$q")
    if ! count=$(ssh_t "docker exec $(printf %q "$TARGET_CONTAINER") sqlite3 $(printf %q "$TARGET_DB_PATH") $qq" 2>/dev/null); then
      count=$(ssh_t "sqlite3 $(printf %q "$TARGET_DB_PATH") $qq" 2>/dev/null || echo "")
    fi
  fi
fi

if [[ -z "$count" ]]; then
  echo "  ❌ retain-failed: could not read transmissions count"
  fails=$((fails+1))
elif [[ "$count" =~ ^[0-9]+$ ]] && (( count > 0 )); then
  echo "  ✅ DB retains $count packets from $TEST_PUBKEY"
else
  echo "  ❌ retain-failed: count=$count (expected > 0)"
  fails=$((fails+1))
fi

echo "=== summary: $fails failure(s) before teardown ==="
# trap handles teardown + exit
exit "$fails"
