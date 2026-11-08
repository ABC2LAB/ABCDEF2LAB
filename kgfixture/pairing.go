package kgfixture

import (
	"sort"

	"github.com/Autumn-27/artex/idor"
)

// Pairing is what the E² private snapshot says about the same preparation
// (contract §9). The server fills it from the snapshot it is registering:
// Labels from sessions[].label, ResetRef from reset_ref, and Resources from
// resources[].ref → resources[].view.Markers (no view = no markers). It holds no
// session secret, URL or approval: those stay with E².
type Pairing struct {
	Labels    []string
	ResetRef  string
	Resources map[string][]idor.Marker
}

// Pairing mismatch codes. Like the loader codes they are the whole error text,
// so a mismatch never echoes a label, ref or canary value.
const (
	CodePairingLabels    Code = "pairing_labels"
	CodePairingResources Code = "pairing_resources"
	CodePairingReset     Code = "pairing_reset"
	CodePairingMarkers   Code = "pairing_markers"
)

// CheckPairing reports whether this registry and an E² private snapshot describe
// one preparation: the same account labels, the same full resource refs, the same
// reset generation, and for every resource exactly the same canary markers as the
// E² view (contract §9). Sets are compared exactly; a repeat on either side is a
// mismatch. The server runs it when it freezes the registry at snapshot
// registration and executes nothing on any mismatch (fail-closed). Ownership is
// not compared: E² resources[].accounts lists who may read, not who owns.
func (s *Snapshot) CheckPairing(p Pairing) error {
	if s.invalid.Load() {
		return reject(CodeSnapshotInvalid)
	}
	if !sameStrings(s.principals, p.Labels) {
		return reject(CodePairingLabels)
	}
	if !sameStrings(s.ResourceRefs(), keys(p.Resources)) {
		return reject(CodePairingResources)
	}
	if p.ResetRef != s.resetGeneration {
		return reject(CodePairingReset)
	}
	for ref, r := range s.resources {
		if !sameMarkers(r.canaries, p.Resources[ref]) {
			return reject(CodePairingMarkers)
		}
	}
	return nil
}

func keys(m map[string][]idor.Marker) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// sameStrings is exact multiset equality of sorted copies. The registry side has
// no repeats (the loader rejects them), so a repeat on the other side cannot match.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// sameMarkers is exact multiset equality over (Field, Value, Trivial): the view
// must carry the registered markers and nothing else, with the same
// classification. With equal lengths, no count going negative means every count
// ends at zero.
func sameMarkers(a, b []idor.Marker) bool {
	if len(a) != len(b) {
		return false
	}
	count := map[idor.Marker]int{}
	for _, m := range a {
		count[m]++
	}
	for _, m := range b {
		count[m]--
		if count[m] < 0 {
			return false
		}
	}
	return true
}
