# Redact identity from an snmpwalk recording.
#
# Reads `OID = TYPE: VALUE` lines and rewrites only the VALUE side. That split
# is the whole point: OIDs are dotted decimal, so a naive IP-address regex
# matches `.1.3.6.1.2.1.1.1.0` and destroys the fixture while leaving the
# hostname untouched. That is not hypothetical — it is what the first version
# of this script did.
#
# Redacted:
#   IpAddress values                     -> 198.51.100.1      (RFC 5737)
#   Hex-STRING values shaped like a MAC  -> 00 00 5E 00 53 00 (RFC 7042)
#   Known identity OIDs (name/contact/location/serial, standard and vendor)
#   STRING values that are a bare run of 8+ digits (serial-shaped)
#   Table index suffixes listed in REDACT_INDEX_OIDS
#
# That last one matters more than it sounds. OID *indices* routinely carry
# identity: a Ruckus per-AP table is indexed by the AP's MAC in dotted decimal,
# and ipAddrTable is indexed by IP. Redacting only values leaves those in
# plain sight. Distinct indices are mapped to distinct placeholders, so row
# counts and per-row distinctness — which is what the tests actually assert —
# survive intact.
#
# Preserved: every OID, every type, every counter, gauge and timetick. What the
# tests exercise is which OIDs exist, which are absent, and what shape the
# values take — none of which depends on the device's identity.

BEGIN { FS = " = "; OFS = " = " }

# Comment and blank lines pass through untouched.
/^[[:space:]]*(#|$)/ { print; next }

# Anything that is not `OID = ...` passes through untouched.
NF < 2 { print; next }

{
	oid = $1
	rest = substr($0, length(oid) + 4)

	# --- identity by OID -------------------------------------------------
	# Standard SNMPv2-MIB system group.
	if (oid ~ /^\.?1\.3\.6\.1\.2\.1\.1\.4\.0$/) { print oid, "STRING: \"noc@example.invalid\""; next }
	if (oid ~ /^\.?1\.3\.6\.1\.2\.1\.1\.5\.0$/) { print oid, "STRING: \"device-under-test\"";   next }
	if (oid ~ /^\.?1\.3\.6\.1\.2\.1\.1\.6\.0$/) { print oid, "STRING: \"lab\"";                 next }

	# Vendor identity columns, supplied by the caller as a comma-separated
	# list of OID prefixes in REDACT_OIDS. Anything matching is replaced by a
	# placeholder of the same type.
	if (REDACT_OIDS != "") {
		n = split(REDACT_OIDS, want, ",")
		for (i = 1; i <= n; i++) {
			p = want[i]
			gsub(/^[[:space:]]+|[[:space:]]+$/, "", p)
			if (p != "" && index(oid, p) == 1) {
				print oid, "STRING: \"REDACTED\""
				next
			}
		}
	}

	# --- identity in the table index -------------------------------------
	if (REDACT_INDEX_OIDS != "") {
		m = split(REDACT_INDEX_OIDS, ix, ",")
		for (i = 1; i <= m; i++) {
			q = ix[i]
			gsub(/^[[:space:]]+|[[:space:]]+$/, "", q)
			if (q == "" || index(oid, q) != 1) continue
			suffix = substr(oid, length(q) + 1)
			# The column number sits between the prefix and the index, so keep
			# the first element and rewrite everything past it.
			if (match(suffix, /^\.[0-9]+\./)) {
				col = substr(suffix, 1, RLENGTH - 1)
				idxpart = substr(suffix, RLENGTH)
				if (!(idxpart in seen)) {
					seen[idxpart] = ++nextidx
				}
				# Rewrite the OID and fall through. Printing here would skip the
				# value rules below, and these tables carry the same MAC in the
				# value as in the index — which is exactly how the first version
				# of this rule leaked both APs' addresses.
				oid = q col ".6.0.0.94.0.83." seen[idxpart]
				break
			}
		}
	}

	# --- identity by type ------------------------------------------------
	if (rest ~ /^IpAddress:/)  { print oid, "IpAddress: 198.51.100.1"; next }
	if (rest ~ /^Network Address:/) { print oid, "Network Address: 198.51.100.1"; next }

	# A Hex-STRING of exactly six octets is a MAC. Longer ones are opaque
	# vendor blobs and are left alone; they carry no address.
	if (rest ~ /^Hex-STRING:[[:space:]]*([0-9A-Fa-f]{2}[[:space:]]+){5}[0-9A-Fa-f]{2}[[:space:]]*$/) {
		print oid, "Hex-STRING: 00 00 5E 00 53 00"
		next
	}

	# Colon-separated MAC appearing inside a STRING value.
	if (rest ~ /([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}/) {
		gsub(/([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}/, "00:00:5E:00:53:00", rest)
		print oid, rest
		next
	}

	# A STRING that is nothing but a long run of digits is a serial number.
	# Anchored so it cannot match a version string such as "200.15.6.212" or
	# a counter rendered as text.
	if (rest ~ /^STRING:[[:space:]]*"?[0-9]{8,}"?[[:space:]]*$/) {
		print oid, "STRING: \"REDACTED-SERIAL\""
		next
	}

	# Deliberately `oid` and not `$0`: the index rule above may have rewritten
	# the OID, and printing the original line here would silently discard that
	# — which left a MAC-indexed column unredacted the first time round.
	print oid, rest
}
