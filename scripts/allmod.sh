#!/usr/bin/env bash
# Run a go command in every module of this multi-module repo.
# Usage: bash scripts/allmod.sh vet ./...
#        bash scripts/allmod.sh test ./...
set -uo pipefail
cd "$(dirname "$0")/.."
rc=0
while IFS= read -r mod; do
	dir=$(dirname "$mod")
	printf '== %-26s ' "$dir"
	out=$( (cd "$dir" && go "$@" 2>&1) )
	if [ $? -eq 0 ]; then
		echo "OK"
	else
		echo "FAIL"
		echo "$out" | sed 's/^/    /'
		rc=1
	fi
done < <(find . -name go.mod -not -path './node_modules/*' | sort)
exit $rc
