#!/usr/bin/env bash
# Inventory the licence of every dependency NetInv ships.
#
# NetInv is Apache-2.0 (ADR-019), which means we must not redistribute copyleft
# code inside the binaries or the web bundle. This checks that claim rather than
# assuming it: a new transitive dependency under GPL/AGPL/SSPL is a licensing
# problem for every downstream operator, and it is far cheaper to catch here
# than in someone's legal review.
#
# Exits non-zero if anything copyleft or unidentifiable appears, so CI can gate
# on it. Build-time-only tooling is reported but does not fail the run: it never
# reaches a user's machine.
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
printf '=== backend (Go modules) ===\n'
cd backend
go list -deps -f '{{if not .Standard}}{{.Module.Path}}{{end}}' ./... 2>/dev/null |
	sort -u | grep -v '^github.com/freezxp/netinv' |
	while read -r mod; do
		dir=$(go list -m -f '{{.Dir}}' "$mod" 2>/dev/null) || continue
		[ -n "$dir" ] || continue
		lic=$(find "$dir" -maxdepth 2 \( -iname 'LICENSE*' -o -iname 'COPYING*' \) 2>/dev/null | head -1)
		if [ -z "$lic" ]; then
			printf 'NO-LICENSE-FILE  %s\n' "$mod"
			continue
		fi
		if grep -qi 'GNU AFFERO\|GNU GENERAL PUBLIC\|Server Side Public' "$lic"; then
			kind=COPYLEFT
		elif grep -qi 'Apache License' "$lic"; then kind=Apache-2.0
		elif grep -qi 'MIT License\|Permission is hereby granted, free of charge' "$lic"; then kind=MIT
		elif grep -qi 'ISC License' "$lic"; then kind=ISC
		elif grep -q 'Redistribution and use in source and binary' "$lic"; then kind=BSD
		elif grep -qi 'Mozilla Public License' "$lic"; then kind=MPL-2.0
		else kind=UNKNOWN
		fi
		printf '%-16s %s\n' "$kind" "$mod"
	done | sort | tee /tmp/netinv-licenses-go.txt

if grep -qE '^(COPYLEFT|UNKNOWN|NO-LICENSE-FILE)' /tmp/netinv-licenses-go.txt; then
	printf '\n!! backend: copyleft or unidentified licence above\n'
	fail=1
fi

cd ..
printf '\n=== frontend (npm) ===\n'
if [ ! -d frontend/node_modules ]; then
	# shellcheck disable=SC2016  # the backticks are literal text, not a subshell
	printf 'node_modules absent — run `npm ci` in frontend/ first (skipped)\n'
else
	node - <<'NODE' || fail=1
const fs = require("fs"), path = require("path");
const runtime = new Set(Object.keys(
  JSON.parse(fs.readFileSync("frontend/package.json")).dependencies || {}));
const found = {};
(function walk(dir, depth) {
  if (depth > 3) return;
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    if (!e.isDirectory()) continue;
    const p = path.join(dir, e.name);
    if (e.name === "node_modules") { walk(p, depth + 1); continue; }
    if (e.name.startsWith("@")) { walk(p, depth); continue; }
    try {
      const j = JSON.parse(fs.readFileSync(path.join(p, "package.json")));
      let l = j.license || (j.licenses && j.licenses[0] && j.licenses[0].type) || "UNKNOWN";
      if (typeof l === "object") l = l.type || "UNKNOWN";
      found[j.name] = l;
    } catch { /* not a package dir */ }
  }
})("frontend/node_modules", 0);

const counts = {}, problems = [];
for (const [name, lic] of Object.entries(found)) {
  counts[lic] = (counts[lic] || 0) + 1;
  if (/GPL|AGPL|SSPL|Commons Clause|UNKNOWN/i.test(lic)) {
    // Copyleft matters only if it reaches the browser. Build tooling does not.
    problems.push(`${/^(GPL|AGPL|SSPL)/i.test(lic) && runtime.has(name) ? "SHIPPED" : "build-only"}  ${lic}  ${name}`);
  }
}
console.log(Object.entries(counts).sort((a, b) => b[1] - a[1])
  .map(([l, n]) => `${String(n).padStart(5)}  ${l}`).join("\n"));
if (problems.length) {
  console.log("\n--- copyleft / unidentified ---\n" + problems.join("\n"));
  if (problems.some((p) => p.startsWith("SHIPPED"))) {
    console.log("\n!! frontend: copyleft code would ship in the bundle");
    process.exit(1);
  }
}
NODE
fi

printf '\n'
if [ "$fail" -ne 0 ]; then
	printf 'FAIL — see above. NetInv ships Apache-2.0; copyleft dependencies cannot be redistributed with it.\n'
	exit 1
fi
printf 'OK — every shipped dependency is permissively licensed.\n'
