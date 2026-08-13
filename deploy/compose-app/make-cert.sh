#!/usr/bin/env bash
# Generate a self-signed certificate for the single-host deployment, if one is
# not already present.
#
# A self-signed certificate is not a good certificate. It is better than the
# alternative this replaces, which was a login form posting a password in
# cleartext over the LAN — and unlike that, it is visible: the browser warning
# is a standing reminder to install a real one. Replacing it is a file copy;
# see doc 20 §12.
set -euo pipefail

dir="${1:-$(cd "$(dirname "$0")" && pwd)/certs}"
host="${NETINV_TLS_HOST:-$(hostname -f 2>/dev/null || hostname)}"
days="${NETINV_TLS_DAYS:-825}"

mkdir -p "$dir"
if [ -s "$dir/netinv.crt" ] && [ -s "$dir/netinv.key" ]; then
	printf 'certificate already present in %s — leaving it alone\n' "$dir"
	exit 0
fi

# SANs matter more than the CN: browsers have ignored CN for years, and a
# certificate without a matching SAN fails even when the name is right. Both
# the hostname and localhost go in, because this is reached both ways.
alt="DNS:${host},DNS:localhost,IP:127.0.0.1"
for ip in $(hostname -I 2>/dev/null || true); do
	alt="${alt},IP:${ip}"
done

openssl req -x509 -newkey rsa:2048 -sha256 -nodes \
	-keyout "$dir/netinv.key" -out "$dir/netinv.crt" \
	-days "$days" -subj "/CN=${host}" -addext "subjectAltName=${alt}" \
	-addext "keyUsage=critical,digitalSignature,keyEncipherment" \
	-addext "extendedKeyUsage=serverAuth" 2>/dev/null

# The key is readable by nginx inside the container only; on the host it should
# not be world-readable, which is the default umask outcome otherwise.
chmod 600 "$dir/netinv.key"
chmod 644 "$dir/netinv.crt"

printf 'self-signed certificate written to %s\n' "$dir"
printf '  subject: CN=%s\n  SANs:    %s\n  expires: %s days\n' "$host" "$alt" "$days"
printf '\nReplace it with a real certificate by overwriting netinv.crt and\n'
printf 'netinv.key in that directory and restarting the frontend container.\n'
