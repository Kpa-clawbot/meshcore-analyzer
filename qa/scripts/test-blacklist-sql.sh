#!/usr/bin/env bash
# test-blacklist-sql.sh — unit tests for the §10.2 SQL construction in
# qa/scripts/blacklist-test.sh (issue #1977). Sources the script and exercises
# its pure helpers, plus a real local sqlite3 against a throwaway fixture DB.
#
# Run: bash qa/scripts/test-blacklist-sql.sh
# Exits non-zero if any case fails.
#
# The point of the sqlite3 group is that BOTH directions are asserted. A test
# that only checks "the injection payload returns 0" passes just as happily when
# the query is silently broken and returns 0 for everything, so the legitimate
# pubkey must be shown to still return the row it should.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=blacklist-test.sh
. "$SCRIPT_DIR/blacklist-test.sh"

PASS=0
FAIL=0

assert_eq() {
    local label="$1" expected="$2" actual="$3"
    if [ "$expected" = "$actual" ]; then
        PASS=$((PASS + 1))
    else
        FAIL=$((FAIL + 1))
        echo "FAIL: $label — expected '$expected' got '$actual'" >&2
    fi
}

assert_match() {
    local label="$1" pattern="$2" actual="$3"
    if [[ "$actual" =~ $pattern ]]; then
        PASS=$((PASS + 1))
    else
        FAIL=$((FAIL + 1))
        echo "FAIL: $label — '$actual' does not match /$pattern/" >&2
    fi
}

# ----- sql_hex_literal ------------------------------------------------------
# The security property: whatever goes in, the SQL text it produces is drawn
# from [0-9a-f] only. No caller-supplied byte can close a string literal or add
# a dot-command argument. Needs no sqlite3, so this group always runs.
assert_eq "hex of deadbeef" "x'6465616462656566'" "$(sql_hex_literal deadbeef)"
assert_eq "hex of empty"    "x''"                 "$(sql_hex_literal "")"

HEX_ONLY="^x'[0-9a-f]*'\$"
assert_match "alphabet: sql quote payload" "$HEX_ONLY" "$(sql_hex_literal "' OR 1=1 --")"
assert_match "alphabet: drop table"        "$HEX_ONLY" "$(sql_hex_literal '"; DROP TABLE transmissions; --')"
assert_match "alphabet: backslash"         "$HEX_ONLY" "$(sql_hex_literal 'a\b')"
assert_match "alphabet: dollar and backtick" "$HEX_ONLY" "$(sql_hex_literal '$(id) `id`')"
assert_match "alphabet: embedded newline"  "$HEX_ONLY" "$(sql_hex_literal "$(printf 'a\nb')")"
assert_match "alphabet: multibyte"         "$HEX_ONLY" "$(sql_hex_literal 'héllo')"

# `od` without -v collapses runs of identical lines to '*'. A long repetitive
# value is the case that catches losing the flag.
LONG=$(printf 'x%.0s' $(seq 1 4096))
LONG_HEX=$(sql_hex_literal "$LONG")
assert_match "alphabet: 4096 repeated bytes" "$HEX_ONLY" "$LONG_HEX"
# 4096 bytes → 8192 hex digits, plus the 3 chars of x''. A collapsed run would
# be far shorter and would also fail the alphabet check on '*'.
assert_eq "no od line-collapse in 4096-byte value" "8192" "$(( ${#LONG_HEX} - 3 ))"

# ----- against a real sqlite3 ----------------------------------------------
if ! command -v sqlite3 >/dev/null 2>&1; then
    echo "SKIP: sqlite3 not on PATH — skipping the ${#SQLITE_ARGS[@]}-flag query group" >&2
    echo "      (the alphabet assertions above still ran)" >&2
else
    FIXTURE_DIR=$(mktemp -d)
    trap 'rm -rf "$FIXTURE_DIR"' EXIT
    DB="$FIXTURE_DIR/fixture.db"
    EMPTY_DB="$FIXTURE_DIR/no-table.db"
    sqlite3 "$DB" \
        "CREATE TABLE transmissions(from_node TEXT); INSERT INTO transmissions VALUES('deadbeef'),('cafebabe');"
    sqlite3 "$EMPTY_DB" "CREATE TABLE unrelated(x);"

    run_local() { sqlite3 "${SQLITE_ARGS[@]}" "$1"; }

    # The capability probe must round-trip on this machine, or the assertions
    # below would be testing nothing.
    assert_eq "probe round-trips" "$SQLITE_PROBE_TOKEN" "$(sqlite_probe_sql | run_local :memory:)"

    # POSITIVE CONTROL: a legitimate pubkey still returns its row. Without this,
    # a silently broken query looks like a passing security fix.
    out=$(transmission_count_sql deadbeef | run_local "$DB"); rc=$?
    assert_eq "legit pubkey → its row"   "1" "$out"
    assert_eq "legit pubkey → exit 0"    "0" "$rc"
    assert_eq "other legit pubkey"       "1" "$(transmission_count_sql cafebabe | run_local "$DB")"
    assert_eq "absent pubkey → 0"        "0" "$(transmission_count_sql abc123   | run_local "$DB")"

    # NEGATIVE: the payload binds as a literal that matches nothing. The table
    # holds 2 rows, so a structural injection would return 2, not 0.
    out=$(transmission_count_sql "' OR 1=1 --" | run_local "$DB"); rc=$?
    assert_eq "injection payload → 0 rows" "0" "$out"
    assert_eq "injection payload → exit 0" "0" "$rc"
    assert_eq "table really does hold 2 rows" "2" \
        "$(run_local "$DB" <<<'SELECT COUNT(*) FROM transmissions;')"

    # Interpolating the same payload the old way returns the whole table. This is
    # the behaviour the change removes; asserting it keeps the test honest about
    # what "0" above is worth.
    legacy="SELECT COUNT(*) FROM transmissions WHERE from_node = '' OR 1=1 --';"
    assert_eq "old interpolated form leaked the table" "2" "$(run_local "$DB" <<<"$legacy")"

    # Multibyte and whitespace values bind as themselves rather than erroring.
    sqlite3 "$DB" "INSERT INTO transmissions VALUES('héllo wörld');"
    assert_eq "multibyte value with a space binds" "1" \
        "$(transmission_count_sql 'héllo wörld' | run_local "$DB")"

    # ERROR SURFACING: a broken query must be distinguishable from an empty
    # result — non-zero exit and something on stderr, not a silent "".
    err_file="$FIXTURE_DIR/err"
    out=$(transmission_count_sql deadbeef | run_local "$EMPTY_DB" 2>"$err_file"); rc=$?
    if [ "$rc" -ne 0 ]; then PASS=$((PASS + 1)); else
        FAIL=$((FAIL + 1)); echo "FAIL: missing table — expected non-zero exit, got $rc" >&2
    fi
    if [ -s "$err_file" ]; then PASS=$((PASS + 1)); else
        FAIL=$((FAIL + 1)); echo "FAIL: missing table — expected a message on stderr" >&2
    fi
    assert_eq "missing table → no count on stdout" "" "$out"

    # run_sqlite with no resolved runner must refuse rather than guess.
    SQLITE_RUNNER=""
    if run_sqlite </dev/null >/dev/null 2>&1; then
        FAIL=$((FAIL + 1)); echo "FAIL: run_sqlite with no runner — expected non-zero exit" >&2
    else
        PASS=$((PASS + 1))
    fi
fi

echo "test-blacklist-sql.sh: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
