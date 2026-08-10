#!/usr/bin/env bash
# Enforce the connector plugin contract (doc 10 §6).
#
# The whole point of the framework is that adding a vendor is additive: one new
# package plus one registry line, and zero core diffs (NFR-72). That property
# only holds if connectors stay dependency-free — the moment one imports a core
# internal package, connectors and core are welded together and the
# out-of-process seam in ADR-014 quietly dies.
#
# Checks, per connector package:
#   1. imports nothing beyond stdlib, connectors/sdk, and connectors/generic
#   2. carries tests
#   3. is registered in connectors/registry
set -euo pipefail
cd "$(dirname "$0")/../connectors"

fail=0
note() { printf '  %-9s %s\n' "$1" "$2"; }

for dir in */; do
	name=${dir%/}
	case "$name" in sdk | registry) continue ;; esac
	[ -d "$name" ] || continue

	printf '%s\n' "$name"

	# 1. Import hygiene. Anything namespaced (a dot before the first slash) that
	#    is not the SDK or the generic base is an escape from the sandbox.
	bad=$(go list -f '{{range .Imports}}{{.}}
{{end}}{{range .TestImports}}{{.}}
{{end}}' "./$name" 2>/dev/null |
		grep -E '^[^/]+\.[^/]+/' |
		grep -vE '^github\.com/freezxp/netinv/connectors/(sdk(/[a-z]+)?|generic)$' |
		sort -u || true)
	if [ -n "$bad" ]; then
		note FAIL "imports outside sdk/generic/stdlib:"
		# Unquoted on purpose: $bad holds a newline-separated list, and the
		# splitting is what makes printf repeat the format once per import.
		# Quoting it would print the whole list on a single line.
		# shellcheck disable=SC2086
		printf '            %s\n' $bad
		fail=1
	else
		note ok "imports clean"
	fi

	# 2. Tests. A connector is a pile of OID-to-value mappings; untested, it is
	#    a pile of guesses.
	if ! ls "$name"/*_test.go >/dev/null 2>&1; then
		note FAIL "no tests"
		fail=1
	else
		if ls "$name"/testdata/*.snmpwalk >/dev/null 2>&1; then
			note ok "tests + recorded fixtures"
		else
			note warn "tests, but no testdata/*.snmpwalk fixture"
		fi
	fi

	# 3. Registration, or the connector exists and is never reachable.
	if grep -q "connectors/$name\"" registry/registry.go; then
		note ok "registered"
	else
		note FAIL "missing from connectors/registry/registry.go"
		fail=1
	fi
done

printf '\n'
if [ "$fail" -ne 0 ]; then
	printf 'FAIL — see doc 10 §6 for the connector checklist.\n'
	exit 1
fi
printf 'OK — every connector satisfies the plugin contract.\n'
