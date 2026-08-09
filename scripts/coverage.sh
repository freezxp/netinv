#!/usr/bin/env bash
# Coverage ratchet for the backend (doc 24 §1, doc 25 §5.1).
#
# Doc 24 sets the target at >=70% overall. The measured figure is nowhere near
# that, and pretending otherwise by gating at 70% would mean a permanently red
# build that everybody learns to ignore — the worst of both worlds.
#
# So this is a ratchet, not a bar. It fails when coverage drops below the floor
# recorded in scripts/coverage-floor.txt, which is the level already achieved.
# New code arrives with tests or the number falls and the build goes red;
# raising the floor is a deliberate commit, so progress is visible in history
# rather than asserted in a doc.
#
# Run from backend/. NETINV_TEST_PG_DSN should point at a scratch PostgreSQL —
# the integration tests skip without it, which lowers the measured figure.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
floor_file="$root/scripts/coverage-floor.txt"
floor="$(tr -d '[:space:]' <"$floor_file")"
profile="${COVERAGE_PROFILE:-/tmp/netinv-cover.out}"

if [ -z "${NETINV_TEST_PG_DSN:-}" ]; then
	printf 'warning: NETINV_TEST_PG_DSN is unset — integration tests will skip and coverage will read low\n' >&2
fi

go test ./... -coverprofile="$profile" -coverpkg=./... >/dev/null
total="$(go tool cover -func="$profile" | awk '/^total:/ {gsub("%","",$3); print $3}')"

printf 'backend coverage: %s%%  (floor %s%%, doc 24 target 70%%)\n' "$total" "$floor"

if awk -v t="$total" -v f="$floor" 'BEGIN {exit !(t < f)}'; then
	printf '\nFAIL — coverage fell below the floor.\n'
	printf 'Add tests for what you changed, or if the drop is legitimate and\n'
	printf 'understood, lower %s in the same commit with the reason.\n' "${floor_file#"$root"/}"
	printf '\nLeast-covered packages:\n'
	go tool cover -func="$profile" |
		awk '$1 !~ /^total:/ {split($1,p,":"); gsub("%","",$3); sum[p[1]]+=$3; n[p[1]]++}
		     END {for (f in sum) printf "%6.1f%%  %s\n", sum[f]/n[f], f}' |
		sort -n | head -10
	exit 1
fi

# Ratcheting up is the point, so say so when it is possible.
if awk -v t="$total" -v f="$floor" 'BEGIN {exit !(t > f + 1.0)}'; then
	printf 'Coverage is %s%% above the floor — consider raising %s.\n' \
		"$(awk -v t="$total" -v f="$floor" 'BEGIN {printf "%.1f", t - f}')" \
		"${floor_file#"$root"/}"
fi
