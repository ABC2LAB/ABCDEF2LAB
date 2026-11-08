package kgfixture

import (
	"strings"
	"testing"

	"github.com/Autumn-27/artex/idor"
)

// matchingPairing is what a correctly prepared E² snapshot says about validRegistry.
func matchingPairing() Pairing {
	return Pairing{
		Labels:   []string{"account-b", "account-a"}, // order does not matter
		ResetRef: "reset-1",
		Resources: map[string][]idor.Marker{
			"note:1001": {{Field: "id", Value: "1001", Trivial: true}, {Field: "body", Value: sentinel}},
			"note:1002": nil, // no view on the E² side, no canary in the registry
		},
	}
}

func TestCheckPairingAcceptsTheSamePreparation(t *testing.T) {
	s := mustParse(t, encode(t, validRegistry()))
	if err := s.CheckPairing(matchingPairing()); err != nil {
		t.Fatalf("CheckPairing = %v", err)
	}
	p := matchingPairing()
	p.Resources["note:1002"] = []idor.Marker{} // an empty view is the same as none
	if err := s.CheckPairing(p); err != nil {
		t.Fatalf("CheckPairing with an empty view = %v", err)
	}
}

func TestCheckPairingRejectsEachMismatch(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Pairing)
		want Code
	}{
		{"label missing", func(p *Pairing) { p.Labels = []string{"account-a"} }, CodePairingLabels},
		{"label extra", func(p *Pairing) { p.Labels = append(p.Labels, "account-c") }, CodePairingLabels},
		{"label repeated", func(p *Pairing) { p.Labels = []string{"account-a", "account-a"} }, CodePairingLabels},
		{"label case", func(p *Pairing) { p.Labels = []string{"account-a", "Account-b"} }, CodePairingLabels},
		{"resource missing", func(p *Pairing) { delete(p.Resources, "note:1002") }, CodePairingResources},
		{"resource extra", func(p *Pairing) { p.Resources["note:1003"] = nil }, CodePairingResources},
		{"resource ref variant", func(p *Pairing) {
			p.Resources["Note:1001"] = p.Resources["note:1001"]
			delete(p.Resources, "note:1001")
		}, CodePairingResources},
		{"reset generation", func(p *Pairing) { p.ResetRef = "reset-2" }, CodePairingReset},
		{"reset empty", func(p *Pairing) { p.ResetRef = "" }, CodePairingReset},
		{"marker missing", func(p *Pairing) { p.Resources["note:1001"] = p.Resources["note:1001"][:1] }, CodePairingMarkers},
		{"marker extra", func(p *Pairing) {
			p.Resources["note:1001"] = append(p.Resources["note:1001"], idor.Marker{Field: "title", Value: "x"})
		}, CodePairingMarkers},
		{"marker value", func(p *Pairing) {
			p.Resources["note:1001"] = []idor.Marker{{Field: "id", Value: "1001", Trivial: true}, {Field: "body", Value: "other"}}
		}, CodePairingMarkers},
		{"marker trivial flipped", func(p *Pairing) {
			p.Resources["note:1001"] = []idor.Marker{{Field: "id", Value: "1001"}, {Field: "body", Value: sentinel}}
		}, CodePairingMarkers},
		{"marker repeated", func(p *Pairing) {
			p.Resources["note:1001"] = []idor.Marker{{Field: "body", Value: sentinel}, {Field: "body", Value: sentinel}}
		}, CodePairingMarkers},
		{"view missing for a resource with canaries", func(p *Pairing) { p.Resources["note:1001"] = nil }, CodePairingMarkers},
		{"view markers on a resource without canaries", func(p *Pairing) {
			p.Resources["note:1002"] = []idor.Marker{{Field: "body", Value: "x"}}
		}, CodePairingMarkers},
	}
	s := mustParse(t, encode(t, validRegistry()))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := matchingPairing()
			c.edit(&p)
			err := s.CheckPairing(p)
			if codeOf(err) != c.want {
				t.Fatalf("CheckPairing = %v, want %s", err, c.want)
			}
			if err.Error() != "kgfixture: "+string(c.want) || strings.Contains(err.Error(), "SENTINEL") {
				t.Fatalf("error text %q carries more than the code", err.Error())
			}
		})
	}
}

func TestCheckPairingAfterInvalidate(t *testing.T) {
	s := mustParse(t, encode(t, validRegistry()))
	s.Invalidate()
	if codeOf(s.CheckPairing(matchingPairing())) != CodeSnapshotInvalid {
		t.Fatal("pairing accepted on an invalidated snapshot")
	}
}

func TestCheckPairingDoesNotChangeTheCallerInput(t *testing.T) {
	s := mustParse(t, encode(t, validRegistry()))
	p := matchingPairing()
	labels := append([]string(nil), p.Labels...)
	if err := s.CheckPairing(p); err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Labels, ",") != strings.Join(labels, ",") {
		t.Fatalf("labels reordered in place: %v", p.Labels)
	}
	if got := s.Principals(); got[0] != "account-a" || got[1] != "account-b" {
		t.Fatalf("registry principals reordered: %v", got)
	}
}
