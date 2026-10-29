#!/usr/bin/env bash
# Download the offline databases netinv-flow uses to name flow addresses with
# an AS and a country (ADR-026, doc 34). Neither is in the repository.
#
#   scripts/fetch-flow-geo.sh [target-dir]      (default ./flow-geo)
#
# Re-run monthly; the collector notices changed files and reloads them without
# a restart. Each file is written to a temporary name and moved into place, so
# a failed or interrupted download never replaces a good file.
set -euo pipefail

dir=${1:-./flow-geo}
mkdir -p "$dir"

ASN_URL=https://iptoasn.com/data/ip2asn-combined.tsv.gz
DBIP_BASE=https://download.db-ip.com/free/dbip-country-lite

tmp=$(mktemp -d "$dir/.fetch.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

# fetch URL FILE — fails on HTTP errors, rejects anything that is not gzip.
fetch() {
	curl -fsSL --retry 3 --max-time 600 -o "$2" "$1" && gzip -t "$2"
}

echo "Fetching AS data from $ASN_URL"
fetch "$ASN_URL" "$tmp/ip2asn-combined.tsv.gz"
mv -f "$tmp/ip2asn-combined.tsv.gz" "$dir/ip2asn-combined.tsv.gz"

# DB-IP publishes one file per month; early in a month the new one may not be
# out yet, so fall back to last month's.
this_month=$(date +%Y-%m)
last_month=$(date -d "$(date +%Y-%m-01) -1 day" +%Y-%m 2>/dev/null ||
	date -v-1m +%Y-%m)
got=""
for month in "$this_month" "$last_month"; do
	url="$DBIP_BASE-$month.csv.gz"
	echo "Fetching country data from $url"
	if fetch "$url" "$tmp/dbip-country-lite.csv.gz"; then
		got=$month
		break
	fi
done
if [ -z "$got" ]; then
	echo "error: no DB-IP country file for $this_month or $last_month" >&2
	exit 1
fi
mv -f "$tmp/dbip-country-lite.csv.gz" "$dir/dbip-country-lite.csv.gz"

cat <<EOF

Wrote to $dir:
  ip2asn-combined.tsv.gz     iptoasn.com — Public Domain (PDDL v1.0)
  dbip-country-lite.csv.gz   DB-IP country lite $got — CC BY 4.0

DB-IP's licence requires attribution: any page showing these results must
credit "IP Geolocation by DB-IP" with a link to https://db-ip.com. The NetInv
Flows page shows that credit whenever country data is loaded.
EOF
