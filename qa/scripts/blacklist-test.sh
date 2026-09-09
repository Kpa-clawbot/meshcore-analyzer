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
#   retain-failed  → blacklisted pubkey absent from DB (§10.2 fail), or the
#                    §10.2 probe could not run at all — no sqlite3 on the target
#                    able to bind a parameter. The message names what is needed;
#                    there is no fallback to interpolated SQL.
#   teardown-failed→ post-test removal did not restore listing
#
# Exit code = number of failures (0 = pass).
# PUBLIC repo: zero PII — no real pubkeys, IPs, or hostnames as defaults.
#
# Structure: helpers live at top level and the imperative body lives in main(),
# so test-blacklist-sql.sh can source this file and exercise individual helpers
# without running the suite. Same idiom as scripts/staging/disk-monitor.sh.

set -uo pipefail

SSH_OPTS=()  # populated by main() from TARGET_SSH_KEY
ssh_t() { ssh "${SSH_OPTS[@]}" "$TARGET_SSH_HOST" "$@"; }

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
# §10.2 DB probe — bind the pubkey, do not interpolate it (issue #1977)
# -----------------------------------------------------------------------------
# Batch flags, all in service of "the count is parseable and errors are visible":
#   -bail            stop at the first SQL error instead of running on
#   -init /dev/null  ignore the operator's ~/.sqliterc — a stray .mode there
#                    would make the count unparseable
#   -noheader -list  stdout is exactly the number, nothing else
SQLITE_ARGS=(-batch -bail -init /dev/null -noheader -list)
# Round-trip probe token. The value is arbitrary; it only has to come back intact.
SQLITE_PROBE_TOKEN="corescope-probe-ok"
SQLITE_RUNNER=""   # "container" | "host", set by resolve_sqlite_runner
RETAIN_COUNT=""    # set by read_retain_count

# Hex-encode a value for embedding in SQL as a blob literal.
#
# Why hex rather than quoting: the output alphabet is [0-9a-f], so no byte the
# caller passes can terminate a string literal or add a dot-command argument.
# That holds for arbitrary input, which is the point — the SQL layer stops
# depending on main()'s hex gate in order to be safe.
#
# `od -v` is load-bearing: without it od collapses runs of identical lines to
# '*' and long repetitive values encode wrongly.
sql_hex_literal() {
  printf "x'%s'" "$(printf '%s' "$1" | od -An -v -tx1 | tr -d ' \n')"
}

# SQL for the §10.2 count, fed to sqlite3 on stdin. The SELECT text is a
# constant; the pubkey arrives as a bound parameter.
#
# Note the nested cast rather than `.parameter set :pubkey '<value>'`:
# dot-command arguments are split on whitespace, so a value containing a space
# (e.g. "' OR 1=1 --") makes sqlite3 print the .parameter help to STDOUT, exit
# 0, and leave :pubkey unbound. COUNT(*) then returns 0 — which reads exactly
# like a passing security fix. -bail does not catch it either.
transmission_count_sql() {
  printf '.parameter init\n'
  printf '.parameter set :pubkey "cast(%s as text)"\n' "$(sql_hex_literal "$1")"
  printf 'SELECT COUNT(*) FROM transmissions WHERE from_node = :pubkey;\n'
}

# Capability probe: bind a known value and read it back. A version number only
# implies that .parameter works; binding something and getting it back proves it
# on the binary actually in front of us, which is the operator's, not ours.
sqlite_probe_sql() {
  printf '.parameter init\n'
  printf '.parameter set :probe "cast(%s as text)"\n' "$(sql_hex_literal "$SQLITE_PROBE_TOKEN")"
  printf 'SELECT :probe;\n'
}

# Find a sqlite3 that can bind a parameter — in the container first, then on the
# host. Sets SQLITE_RUNNER; returns 1 if neither qualifies. There is deliberately
# no interpolating fallback: that would leave the vulnerable path in place under
# a nicer name.
#
# Probe stderr is collected rather than discarded, but only printed if BOTH
# probes fail. The container miss is the known-normal case — the app image has
# no sqlite3 (pure-Go driver, no CGO; Dockerfile:15) — so surfacing it on every
# run would be noise.
resolve_sqlite_runner() {
  local probe out
  probe=$(sqlite_probe_sql)
  SQLITE_RUNNER=""
  out=$(ssh_t "docker exec -i $(printf %q "$TARGET_CONTAINER") sqlite3 ${SQLITE_ARGS[*]} :memory:" \
    <<<"$probe" 2>>"$TMP/sqlite-probe.err")
  if [[ "$out" == "$SQLITE_PROBE_TOKEN" ]]; then SQLITE_RUNNER="container"; return 0; fi
  out=$(ssh_t "sqlite3 ${SQLITE_ARGS[*]} :memory:" <<<"$probe" 2>>"$TMP/sqlite-probe.err")
  if [[ "$out" == "$SQLITE_PROBE_TOKEN" ]]; then SQLITE_RUNNER="host"; return 0; fi
  return 1
}

# Run SQL from stdin against TARGET_DB_PATH via the resolved runner. Stderr is
# left alone so the caller can capture it, and the exit status is sqlite3's.
# The SQL crosses on stdin, so only the container name and db path still need
# printf %q for the remote shell. docker exec needs -i to attach stdin.
run_sqlite() {
  case "$SQLITE_RUNNER" in
    container) ssh_t "docker exec -i $(printf %q "$TARGET_CONTAINER") sqlite3 ${SQLITE_ARGS[*]} $(printf %q "$TARGET_DB_PATH")" ;;
    host)      ssh_t "sqlite3 ${SQLITE_ARGS[*]} $(printf %q "$TARGET_DB_PATH")" ;;
    *)         echo "run_sqlite: no runner resolved" >&2; return 127 ;;
  esac
}

# Read the retained-transmission count into RETAIN_COUNT. Prints a classified
# "retain-failed" line and returns 1 on failure, so §10.2 has exactly one place
# that increments $fails.
read_retain_count() {
  RETAIN_COUNT=""
  local code
  if [[ -n "$ADMIN_API_TOKEN" ]]; then
    # Read auth header from stdin so the token never enters argv (ps-safe).
    code=$(printf 'header = "Authorization: Bearer %s"\n' "$ADMIN_API_TOKEN" | \
      curl -s -m "$CURL_TIMEOUT" -K - -o "$TMP/admin.json" -w "%{http_code}" \
        "$TARGET_URL/api/admin/transmissions?from_node=$TEST_PUBKEY&count=1" 2>/dev/null || echo "000")
    if [[ "$code" == "200" ]]; then
      RETAIN_COUNT=$(jq -r '.count // ((.transmissions // []) | length)' "$TMP/admin.json" 2>/dev/null || echo "")
    fi
    if [[ -n "$RETAIN_COUNT" ]]; then return 0; fi
  fi

  if [[ -z "$TARGET_DB_PATH" ]]; then
    echo "  ❌ retain-failed: TARGET_DB_PATH unset and no ADMIN_API_TOKEN — cannot probe"
    return 1
  fi
  if ! resolve_sqlite_runner; then
    echo "  ❌ retain-failed: no sqlite3 able to bind a parameter on the target"
    echo "     tried: docker exec -i $TARGET_CONTAINER sqlite3, then sqlite3 on $TARGET_SSH_HOST"
    echo "     need:  the sqlite3 CLI reachable over ssh, supporting '.parameter set'"
    cat "$TMP/sqlite-probe.err" >&2
    return 1
  fi
  echo "  sqlite3 runner: $SQLITE_RUNNER"
  if ! RETAIN_COUNT=$(run_sqlite <<<"$(transmission_count_sql "$TEST_PUBKEY")" 2>"$TMP/sqlite.err"); then
    echo "  ❌ retain-failed: sqlite3 query failed via $SQLITE_RUNNER"
    cat "$TMP/sqlite.err" >&2
    RETAIN_COUNT=""
    return 1
  fi
  return 0
}

# -----------------------------------------------------------------------------
# main
# -----------------------------------------------------------------------------
main() {
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

  # Hard input validation — these strings are interpolated into the remote shell.
  # §10.2's SQL binds TEST_PUBKEY as a parameter rather than interpolating it, so
  # for the SQL layer this gate is defence in depth rather than the only guard
  # (issue #1977). Keep it: redundant is not the same as wrong.
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

  TMP=$(mktemp -d)
  fails=0
  TEARDOWN_DONE=0
  trap teardown EXIT INT TERM

  # ---------------------------------------------------------------------------
  # §10.1 — hide
  # ---------------------------------------------------------------------------
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

  # ---------------------------------------------------------------------------
  # §10.2 — DB retain
  # ---------------------------------------------------------------------------
  echo "=== §10.2 verify packets retained in DB ==="
  if ! read_retain_count; then
    # read_retain_count already printed the classified reason. Counting here and
    # nowhere else: the old code incremented $fails for the "TARGET_DB_PATH
    # unset" case and then again for the empty count it left behind.
    fails=$((fails+1))
  elif [[ "$RETAIN_COUNT" =~ ^[0-9]+$ ]] && (( RETAIN_COUNT > 0 )); then
    echo "  ✅ DB retains $RETAIN_COUNT packets from $TEST_PUBKEY"
  else
    echo "  ❌ retain-failed: count=$RETAIN_COUNT (expected > 0)"
    fails=$((fails+1))
  fi

  echo "=== summary: $fails failure(s) before teardown ==="
  # trap handles teardown + exit
  exit "$fails"
}

# Only run main when executed directly (not when sourced by tests).
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main "$@"
fi
