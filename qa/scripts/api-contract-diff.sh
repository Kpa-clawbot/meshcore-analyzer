#!/usr/bin/env bash
# api-contract-diff.sh — diff CoreScope API endpoints between two deployments.
# Usage: api-contract-diff.sh BASELINE_URL TARGET_URL [-k AUTH_HEADER]
#
# Compares JSON shape (recursive key set) per endpoint and asserts presence of
# `resolved_path` where contract requires it. Prints a per-endpoint result line
# (✅/❌) and a summary. Exit code = number of failures.
#
# Distinguishes:
#   curl-failed  → HTTP error or network timeout (real outage)
#   parse-empty  → curl succeeded but response shape unexpected (probable
#                  contract drift in this script or in the API)
#   shape-diff   → recursive key set differs between baseline and target
#   rp-missing   → resolved_path absent on target where it was promised
#
# PUBLIC repo: do not commit URLs or keys here. Caller passes them.

set -uo pipefail

OLD="${1:-}"; NEW="${2:-}"
[[ -z "$OLD" || -z "$NEW" ]] && { echo "usage: $0 BASELINE_URL TARGET_URL [-k AUTH_HEADER]" >&2; exit 2; }
shift 2 || true
AUTH=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -k) AUTH="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT

# Wrapper: fetch URL, return body on stdout, exit 1 on HTTP error / timeout.
fetch() {
  local url="$1" out="$2"
  local code
  code=$(curl -s -m 30 -o "$out" -w "%{http_code}" ${AUTH:+-H "$AUTH"} "$url" 2>/dev/null) || code="000"
  if [[ "$code" != "2"* ]]; then
    echo "  HTTP $code"
    return 1
  fi
  return 0
}

# Seed lookups from TARGET (so the picked IDs are guaranteed present there).
seed_packets="$TMP/seed_packets.json"
seed_observers="$TMP/seed_observers.json"
seed_nodes="$TMP/seed_nodes.json"

if ! fetch "$NEW/api/packets?limit=1" "$seed_packets"; then echo "seed /api/packets failed" >&2; fi
if ! fetch "$NEW/api/observers"       "$seed_observers"; then echo "seed /api/observers failed" >&2; fi
if ! fetch "$NEW/api/nodes?limit=1"   "$seed_nodes"; then echo "seed /api/nodes failed" >&2; fi

HASH=$(jq -r '.packets[0].hash // empty'         "$seed_packets"   2>/dev/null || true)
OBSID=$(jq -r '.observers[0].id // empty'        "$seed_observers" 2>/dev/null || true)
NODEPK=$(jq -r '.nodes[0].public_key // empty'   "$seed_nodes"     2>/dev/null || true)

[[ -z "$HASH"   ]] && echo "warn: no packet hash from /api/packets — packet-detail endpoints will be skipped" >&2
[[ -z "$OBSID"  ]] && echo "warn: no observer id   from /api/observers — observer-detail endpoints will be skipped" >&2
[[ -z "$NODEPK" ]] && echo "warn: no node pubkey   from /api/nodes — node-detail endpoints will be skipped" >&2

# Endpoints to diff: path | jq filter (selects subobject to compare) | RP-required(yes/no)
declare -a ENDPOINTS
ENDPOINTS+=("/api/packets?limit=20|.packets[0]|yes")
ENDPOINTS+=("/api/packets?limit=20&expandObservations=true|.packets[0]|yes")
ENDPOINTS+=("/api/observers|.observers[0]|no")
[[ -n "$HASH"   ]] && ENDPOINTS+=("/api/packets/$HASH|.|yes")
[[ -n "$OBSID"  ]] && ENDPOINTS+=("/api/observers/$OBSID|.|no")
[[ -n "$OBSID"  ]] && ENDPOINTS+=("/api/observers/$OBSID/analytics|.|no")
[[ -n "$NODEPK" ]] && ENDPOINTS+=("/api/nodes/$NODEPK/health|.recentPackets[0]|yes")
[[ -n "$NODEPK" ]] && ENDPOINTS+=("/api/nodes/$NODEPK/paths|.|no")

# Strip volatile fields (timestamps + counters) from a JSON value.
STRIP='walk(if type=="object" then del(.timestamp, .first_seen, .last_seen, .last_heard, .updated_at, .server_time, .packet_count, .packetsLastHour, .uptime_secs, .battery_mv, .noise_floor, .observation_count, .advert_count) else . end)'

fails=0
for ep in "${ENDPOINTS[@]}"; do
  IFS='|' read -r path filter need_rp <<<"$ep"
  echo "=== $path  (resolved_path required: $need_rp) ==="

  oldfile="$TMP/old.json"; newfile="$TMP/new.json"
  if ! fetch "$OLD$path" "$oldfile"; then echo "  ❌ baseline curl-failed"; fails=$((fails+1)); continue; fi
  if ! fetch "$NEW$path" "$newfile"; then echo "  ❌ target curl-failed";   fails=$((fails+1)); continue; fi

  # Selector + strip on each side. jq stderr is preserved so script bugs surface.
  oldj=$(jq "$filter | $STRIP" "$oldfile")
  jq_old_rc=$?
  newj=$(jq "$filter | $STRIP" "$newfile")
  jq_new_rc=$?

  if [[ $jq_old_rc -ne 0 ]]; then
    echo "  ❌ baseline jq-error (filter='$filter') — likely script bug or API shape changed"
    fails=$((fails+1)); continue
  fi
  if [[ $jq_new_rc -ne 0 ]]; then
    echo "  ❌ target jq-error (filter='$filter') — likely script bug or API shape changed"
    fails=$((fails+1)); continue
  fi
  if [[ -z "$oldj" || "$oldj" == "null" ]]; then
    echo "  ❌ baseline parse-empty (filter returned empty/null; check API shape)"
    fails=$((fails+1)); continue
  fi
  if [[ -z "$newj" || "$newj" == "null" ]]; then
    echo "  ❌ target parse-empty (filter returned empty/null; check API shape)"
    fails=$((fails+1)); continue
  fi

  # Recursive key-set diff. Canonicalize array indices (numbers) → "[]" so two
  # different sample responses with different array lengths don't false-positive.
  KEYS_FILTER='[paths(scalars or type=="null" or (type=="array" and length==0) or (type=="object" and length==0)) | map(if type=="number" then "[]" else . end) | join(".")] | unique | .[]'
  oldkeys=$(echo "$oldj" | jq -r "$KEYS_FILTER" | sort -u)
  newkeys=$(echo "$newj" | jq -r "$KEYS_FILTER" | sort -u)
  if ! diff <(echo "$oldkeys") <(echo "$newkeys") >/dev/null; then
    echo "  ❌ shape-diff (key set differs):"
    diff <(echo "$oldkeys") <(echo "$newkeys") | sed 's/^/    /'
    fails=$((fails+1))
    continue
  fi

  # If RP expected, assert present on target (any value, may be null).
  if [[ "$need_rp" == "yes" ]]; then
    if ! echo "$newj" | jq -e '.. | objects | select(has("resolved_path")) | .resolved_path' >/dev/null 2>&1; then
      echo "  ❌ rp-missing (resolved_path not present anywhere in selector)"
      fails=$((fails+1))
      continue
    fi
  fi

  echo "  ✅ ok"
done

echo
echo "failures: $fails / ${#ENDPOINTS[@]}"
exit $fails
