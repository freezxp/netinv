package maps

import (
	"sort"
	"strings"
)

// Link suggestions derived from what operators wrote on their ports.
//
// LLDP is the right source for adjacency and stays the first one, but plenty of
// fleets do not run it — the ASR 900 in the pilot answers no LLDP at all, and
// on many estates it is disabled on purpose. What those estates do have is a
// convention older than LLDP: ifAlias and ifDescr naming the far end, "to
// CORE-SW-01 Gi0/1", "uplink AL-GW". That is a topology description, written by
// someone who knew, and it was going unused.
//
// These are suggestions, never facts. A description can be stale, aspirational,
// or copied from the port next to it, so nothing here writes a map — it offers
// a candidate with the text that produced it, and a human decides.

// DeviceRef is the identity a description might mention.
type DeviceRef struct {
	ID      string
	Name    string
	SysName string
}

// IfaceRef is one interface and the text written on it.
type IfaceRef struct {
	DeviceID string
	IfIndex  int
	Name     string
	Alias    string
	Descr    string
}

// minKeyLen is the shortest device name that may be matched inside free text.
//
// Two characters is not a name, it is a coincidence waiting to happen: a site
// coded "YY" would match "yy" inside any word, and an estate with a device
// called "AA" would suggest links from every port whose description happens to
// contain it. Short names are still matched when the whole description *is* the
// name, which is the case where the operator clearly meant it.
const minKeyLen = 3

// normalise lowercases and reduces anything that is not alphanumeric to a
// single space, so "to CORE-SW-01(Gi0/1)" and "TO core sw 01 gi0 1" tokenise
// the same way. Punctuation carries no meaning in these descriptions and
// varies by whoever typed them.
func normalise(s string) []string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

// deviceKeys are the token sequences that identify a device in free text: its
// name, its sysName, and the first label of a sysName that looks like an FQDN
// (people write "es-30-akhmw", not the whole domain).
func deviceKeys(d DeviceRef) [][]string {
	seen := map[string]bool{}
	var out [][]string
	add := func(s string) {
		toks := normalise(s)
		if len(toks) == 0 {
			return
		}
		k := strings.Join(toks, " ")
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, toks)
	}
	add(d.Name)
	add(d.SysName)
	if host, _, ok := strings.Cut(d.SysName, "."); ok {
		add(host)
	}
	return out
}

// containsSeq reports whether needle appears as a run of consecutive tokens in
// haystack. Token-level rather than substring matching is what keeps "AL" from
// matching inside "ALPHA" while still matching "to AL gw".
func containsSeq(haystack, needle []string) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		ok := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// matchDevice finds which device a description names, if exactly one does.
//
// Ambiguity is resolved by preferring the longest match — "core-sw-01" beats
// "core-sw" when both are device names — and abandoned when two different
// devices match equally well, because guessing between them would produce a
// confident link to the wrong box.
func matchDevice(text []string, devices []DeviceRef, exclude string) (string, bool) {
	bestID, bestLen, tied := "", 0, false
	for _, d := range devices {
		if d.ID == exclude {
			continue
		}
		for _, key := range deviceKeys(d) {
			joined := strings.Join(key, "")
			// Short keys only count when the description is nothing but the
			// name, where the intent is unmistakable.
			if len(joined) < minKeyLen && !(len(text) == len(key) && containsSeq(text, key)) {
				continue
			}
			if !containsSeq(text, key) {
				continue
			}
			switch {
			case len(joined) > bestLen:
				bestID, bestLen, tied = d.ID, len(joined), false
			case len(joined) == bestLen && d.ID != bestID:
				tied = true
			}
		}
	}
	if bestID == "" || tied {
		return "", false
	}
	return bestID, true
}

// SuggestFromDescriptions derives candidate links from interface descriptions.
//
// A description on A naming B is one-sided evidence. When B also has a port
// naming A, the two are merged into a single suggestion carrying both ifIndexes
// — that pair is worth far more than two halves, because it binds both ends and
// agrees with itself.
func SuggestFromDescriptions(devices []DeviceRef, ifaces []IfaceRef) []Suggestion {
	byID := map[string]DeviceRef{}
	for _, d := range devices {
		byID[d.ID] = d
	}

	type side struct {
		ifIndex  int
		ifName   string
		evidence string
	}
	// claims[a][b] = how A's port describes B
	claims := map[string]map[string]side{}

	for _, i := range ifaces {
		text := strings.TrimSpace(i.Alias + " " + i.Descr)
		toks := normalise(text)
		if len(toks) == 0 {
			continue
		}
		peer, ok := matchDevice(toks, devices, i.DeviceID)
		if !ok {
			continue
		}
		if _, seen := claims[i.DeviceID]; !seen {
			claims[i.DeviceID] = map[string]side{}
		}
		// First port wins for a given peer: a device with several ports naming
		// the same neighbour is a LAG or a parallel link, and offering one
		// suggestion per member would bury the operator in near-duplicates.
		if _, dup := claims[i.DeviceID][peer]; !dup {
			claims[i.DeviceID][peer] = side{
				ifIndex: i.IfIndex, ifName: i.Name,
				evidence: strings.TrimSpace(text),
			}
		}
	}

	out := []Suggestion{}
	done := map[[2]string]bool{}
	for a, peers := range claims {
		for b, aSide := range peers {
			x, y, _ := pairKey(a, b)
			if done[[2]string{x, y}] {
				continue
			}
			done[[2]string{x, y}] = true

			s := Suggestion{
				ADeviceID: a, ADevice: byID[a].Name,
				AIfIndex: aSide.ifIndex, AIfName: aSide.ifName,
				BDeviceID: b, BDevice: byID[b].Name,
				Source: "description", Evidence: byID[a].Name + " " +
					aSide.ifName + ": " + aSide.evidence,
				Confidence: "one-end",
			}
			if bSide, mutual := claims[b][a]; mutual {
				s.BIfIndex, s.BIfName = bSide.ifIndex, bSide.ifName
				s.Confidence = "both-ends"
				s.Evidence += "  |  " + byID[b].Name + " " + bSide.ifName +
					": " + bSide.evidence
			}
			out = append(out, s)
		}
	}

	// Deterministic order: both ends confirmed first, since those are the ones
	// worth accepting without checking, then by name so the list does not
	// reshuffle between loads.
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Confidence == "both-ends") != (out[j].Confidence == "both-ends") {
			return out[i].Confidence == "both-ends"
		}
		if out[i].ADevice != out[j].ADevice {
			return out[i].ADevice < out[j].ADevice
		}
		return out[i].BDevice < out[j].BDevice
	})
	return out
}
