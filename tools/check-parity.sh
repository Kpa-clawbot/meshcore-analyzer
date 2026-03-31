#!/usr/bin/env bash
# tools/check-parity.sh — Compare Node.js and Go API response shapes
#
# Usage:
#   bash tools/check-parity.sh                    # run on VM (default ports)
#   bash tools/check-parity.sh NODE_PORT GO_PORT  # custom ports
#   ssh deploy@<VM_HOST> 'bash ~/meshcore-analyzer/tools/check-parity.sh'
#
# Compares response SHAPES (keys + types), not values.
# Requires: curl, python3

set -euo pipefail

NODE_PORT="${1:-3000}"
GO_PORT="${2:-3001}"
NODE_BASE="http://localhost:${NODE_PORT}"
GO_BASE="http://localhost:${GO_PORT}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m'

PASS=0
FAIL=0
SKIP=0

ENDPOINTS=(
    "/api/stats"
    "/api/nodes?limit=5"
    "/api/packets?limit=5"
    "/api/packets?limit=5&groupByHash=true"
    "/api/observers"
    "/api/channels"
    "/api/channels/public/messages?limit=5"
    "/api/analytics/rf?days=7"
    "/api/analytics/topology?days=7"
    "/api/analytics/hash-sizes?days=7"
    "/api/analytics/distance?days=7"
    "/api/analytics/subpaths?days=7"
    "/api/nodes/bulk-health"
    "/api/health"
    "/api/perf"
)

# Python helper to extract shape and compare
SHAPE_SCRIPT='
import json, sys

def extract_shape(val, depth=0, max_depth=4):
    if val is None:
        return "null"
    if isinstance(val, bool):
        return "boolean"
    if isinstance(val, (int, float)):
        return "number"
    if isinstance(val, str):
        return "string"
    if isinstance(val, list):
        if len(val) > 0 and depth < max_depth:
            return {"array": extract_shape(val[0], depth + 1)}
        return "array"
    if isinstance(val, dict):
        if depth >= max_depth:
            return "object"
        return {k: extract_shape(v, depth + 1) for k, v in sorted(val.items())}
    return "unknown"

def compare_shapes(node_shape, go_shape, path="$"):
    """Compare two shapes recursively. Returns list of mismatch strings."""
    mismatches = []

    if isinstance(node_shape, str) and isinstance(go_shape, str):
        # Both are scalar types
        if node_shape == "null":
            return []  # null in node is OK (nullable field)
        if go_shape == "null" and node_shape != "null":
            mismatches.append(f"{path}: Node={node_shape}, Go=null")
        elif node_shape != go_shape:
            mismatches.append(f"{path}: Node={node_shape}, Go={go_shape}")
        return mismatches

    if isinstance(node_shape, str) and isinstance(go_shape, dict):
        mismatches.append(f"{path}: Node={node_shape}, Go=object/array")
        return mismatches

    if isinstance(node_shape, dict) and isinstance(go_shape, str):
        if go_shape == "null":
            mismatches.append(f"{path}: Node=object/array, Go=null (nil slice/map?)")
        else:
            mismatches.append(f"{path}: Node=object/array, Go={go_shape}")
        return mismatches

    if isinstance(node_shape, dict) and isinstance(go_shape, dict):
        # Check for array shape
        if "array" in node_shape and "array" not in go_shape:
            mismatches.append(f"{path}: Node=array, Go=object")
            return mismatches
        if "array" in node_shape and "array" in go_shape:
            mismatches.extend(compare_shapes(node_shape["array"], go_shape["array"], path + "[0]"))
            return mismatches

        # Object: check Node keys exist in Go
        for key in node_shape:
            if key not in go_shape:
                mismatches.append(f"{path}: Go missing field \"{key}\" (Node has it)")
            else:
                mismatches.extend(compare_shapes(node_shape[key], go_shape[key], f"{path}.{key}"))

        # Check Go has extra keys not in Node (warning only)
        for key in go_shape:
            if key not in node_shape:
                mismatches.append(f"{path}: Go has extra field \"{key}\" (not in Node) [WARN]")

    return mismatches

try:
    node_json = json.loads(sys.argv[1])
    go_json = json.loads(sys.argv[2])
except (json.JSONDecodeError, IndexError) as e:
    print(f"JSON parse error: {e}", file=sys.stderr)
    sys.exit(2)

node_shape = extract_shape(node_json)
go_shape = extract_shape(go_json)

mismatches = compare_shapes(node_shape, go_shape)
if mismatches:
    for m in mismatches:
        print(m)
    sys.exit(1)
else:
    sys.exit(0)
'

echo "============================================"
echo "  Node.js vs Go API Parity Check"
echo "  Node: ${NODE_BASE}  |  Go: ${GO_BASE}"
echo "============================================"
echo ""

for ep in "${ENDPOINTS[@]}"; do
    printf "%-50s " "$ep"

    # Fetch Node response
    node_resp=$(curl -sf "${NODE_BASE}${ep}" 2>/dev/null) || {
        printf "${YELLOW}SKIP${NC} (Node unreachable)\n"
        SKIP=$((SKIP + 1))
        continue
    }

    # Fetch Go response
    go_resp=$(curl -sf "${GO_BASE}${ep}" 2>/dev/null) || {
        printf "${YELLOW}SKIP${NC} (Go unreachable)\n"
        SKIP=$((SKIP + 1))
        continue
    }

    # Compare shapes
    result=$(python3 -c "$SHAPE_SCRIPT" "$node_resp" "$go_resp" 2>&1) || {
        printf "${RED}FAIL${NC}\n"
        echo "$result" | sed 's/^/    /'
        FAIL=$((FAIL + 1))
        continue
    }

    printf "${GREEN}PASS${NC}\n"
    PASS=$((PASS + 1))
done

echo ""
echo "============================================"
echo "  Results: ${PASS} pass, ${FAIL} fail, ${SKIP} skip"
echo "============================================"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
