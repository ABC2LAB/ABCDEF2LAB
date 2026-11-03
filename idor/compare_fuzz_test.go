package idor

import (
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/oracle"
)

func cloneCompareCapture(in CrossAccessCapture) CrossAccessCapture {
	out := in
	out.Canaries = append([]Marker(nil), in.Canaries...)
	out.RequesterInputs = append([]string(nil), in.RequesterInputs...)
	return out
}

func compareFuzzCanaries(payload string) (Marker, Marker) {
	if len(payload) > 128 {
		payload = payload[:128]
	}
	encoded := hex.EncodeToString([]byte(payload))
	return Marker{Field: "email", Value: "email-canary-" + encoded}, Marker{Field: "phone", Value: "phone-canary-" + encoded}
}

func compareFuzzBody(payload string, first, second Marker, includeFirst, includeSecond bool) string {
	if len(payload) > 128 {
		payload = payload[:128]
	}
	noise := strings.ReplaceAll(payload, first.Value, "noise-one")
	noise = strings.ReplaceAll(noise, second.Value, "noise-two")
	body := `{"data":"` + noise + `"}`
	if includeFirst {
		body = strings.TrimSuffix(body, `"}`) + `","email":"` + first.Value + `"}`
	}
	if includeSecond {
		body = strings.TrimSuffix(body, `"}`) + `","phone":"` + second.Value + `"}`
	}
	return body
}

func compareFuzzCapture(flags uint16, payload string) CrossAccessCapture {
	first, second := compareFuzzCanaries(payload)
	ownerFirst, ownerSecond := flags&1 != 0, flags&2 != 0
	crossFirst, crossSecond := flags&4 != 0, flags&8 != 0
	if flags&0x800 != 0 {
		first.Field, second.Field = "metadata-a", "metadata-b"
	}
	canaries := []Marker{first, second, {Field: "id", Value: "42", Trivial: true}, {Field: "empty"}}
	if flags&0x8000 != 0 {
		canaries = append(canaries, first)
	}
	ownerRef, crossRef := "basket:42", "basket:42"
	if flags&0x1000 != 0 {
		crossRef = "basket:99"
	}
	if flags&0x2000 != 0 {
		ownerRef = ""
	}
	if flags&0x400 != 0 {
		crossRef = ""
	}
	cross := ResponseView{ResourceRef: crossRef, Normal: flags&0x20 == 0, TemplateClassified: flags&0x40 == 0, ErrorTemplate: flags&0x80 != 0, CommonTemplate: flags&0x100 != 0}
	owner := ResponseView{ResourceRef: ownerRef, Normal: flags&0x10 == 0, TemplateClassified: true}
	owner.Body = compareFuzzBody(payload, first, second, ownerFirst, ownerSecond)
	cross.Body = compareFuzzBody(payload, first, second, crossFirst, crossSecond)
	inputs := []string{"basket", "42"}
	if flags&0x200 != 0 {
		inputs = append(inputs, first.Value)
	}
	return CrossAccessCapture{Canaries: canaries, Owner: owner, Cross: cross, RequesterInputs: inputs, RequesterInputsComplete: flags&0x4000 == 0}
}

// FuzzCompareEvidence exercises each independent comparison signal. The clean
// all-YES control is checked on every callback before flags weaken known axes.
func FuzzCompareEvidence(f *testing.F) {
	for _, flags := range []uint16{0, 1, 2, 4, 8, 0x10, 0x20, 0x40, 0x80, 0x100, 0x200, 0x400, 0x1000, 0x2000, 0x4000, 0x8000, 0xffff} {
		f.Add(flags, "fixture")
	}
	f.Add(uint16(0), "42")
	f.Add(uint16(0xc0), "unclassified-error")
	f.Add(uint16(0xa0), "classified-error-nonnormal")
	f.Add(uint16(0x80f), "metadata")
	f.Fuzz(func(t *testing.T, flags uint16, payload string) {
		clean := compareFuzzCapture(0x0f, payload)
		wantClean := oracle.ComparisonFacts{SameResource: oracle.StateYes, OwnerNormalData: oracle.StateYes, CrossNormalData: oracle.StateYes, NontrivialDataMatch: oracle.StateYes, ReflectionExcluded: oracle.StateYes, TemplateExcluded: oracle.StateYes}
		if got := Compare(clean); got != wantClean {
			t.Fatalf("positive control = %+v, want %+v", got, wantClean)
		}
		candidate := compareFuzzCapture(flags, payload)
		ownerPresent, crossPresent := flags&3 != 0, flags&0xc != 0
		want := oracle.ComparisonFacts{SameResource: oracle.StateYes, OwnerNormalData: oracle.StateNo, CrossNormalData: oracle.StateYes, NontrivialDataMatch: oracle.StateNo, ReflectionExcluded: oracle.StateYes, TemplateExcluded: oracle.StateYes}
		if flags&(0x400|0x2000) != 0 {
			want.SameResource = oracle.StateUnknown
		} else if flags&0x1000 != 0 {
			want.SameResource = oracle.StateNo
		}
		if flags&0x10 != 0 {
			want.OwnerNormalData = oracle.StateUnknown
		} else if ownerPresent {
			want.OwnerNormalData = oracle.StateYes
		}
		// A classified error beats Normal; an unclassified error flag alone
		// carries no such proof. These are independent capture observations.
		if flags&0x40 == 0 && flags&0x80 != 0 {
			want.CrossNormalData = oracle.StateNo
		} else if flags&0x20 != 0 {
			want.CrossNormalData = oracle.StateUnknown
		}
		if crossPresent {
			want.NontrivialDataMatch = oracle.StateYes
		}
		if flags&0x4000 != 0 {
			want.ReflectionExcluded = oracle.StateUnknown
		} else if flags&0x200 != 0 {
			want.ReflectionExcluded = oracle.StateNo
		}
		if flags&0x40 != 0 {
			want.TemplateExcluded = oracle.StateUnknown
		} else if flags&(0x80|0x100) != 0 {
			want.TemplateExcluded = oracle.StateNo
		}
		before := cloneCompareCapture(candidate)
		if got := Compare(candidate); got != want {
			t.Fatalf("flags=%x got=%+v want=%+v", flags, got, want)
		}
		if !reflect.DeepEqual(candidate, before) {
			t.Fatal("Compare mutated input")
		}
	})
}

// FuzzCompareMetamorphic checks marker/input ordering, duplicate markers, field
// metadata and the conservative no-eligible-marker behavior.
func FuzzCompareMetamorphic(f *testing.F) {
	f.Add(uint8(0), "noise")
	f.Add(uint8(1), "independent")
	f.Add(uint8(2), strings.Repeat("x", 64))
	f.Add(uint8(4), "42")
	f.Add(uint8(8), "empty-markers")
	f.Fuzz(func(t *testing.T, mode uint8, payload string) {
		capture := compareFuzzCapture(0x0f, payload)
		capture.RequesterInputs = []string{"unrelated", "42"}
		before := cloneCompareCapture(capture)
		baseline := Compare(capture)
		if baseline != (oracle.ComparisonFacts{
			SameResource: oracle.StateYes, OwnerNormalData: oracle.StateYes,
			CrossNormalData: oracle.StateYes, NontrivialDataMatch: oracle.StateYes,
			ReflectionExcluded: oracle.StateYes, TemplateExcluded: oracle.StateYes,
		}) {
			t.Fatal("metamorphic positive control did not prove all six signals")
		}
		permuted := cloneCompareCapture(capture)
		permuted.Canaries[0], permuted.Canaries[1] = permuted.Canaries[1], permuted.Canaries[0]
		permuted.RequesterInputs[0], permuted.RequesterInputs[1] = permuted.RequesterInputs[1], permuted.RequesterInputs[0]
		if mode&1 != 0 {
			permuted.Canaries = append(permuted.Canaries, permuted.Canaries[0])
		}
		if mode&2 != 0 {
			permuted.Canaries[0].Field = "renamed-field"
		}
		permutedBefore := cloneCompareCapture(permuted)
		if got := Compare(permuted); got != baseline {
			t.Fatalf("permutation changed facts: base=%+v got=%+v", baseline, got)
		}
		if !reflect.DeepEqual(permuted, permutedBefore) || !reflect.DeepEqual(capture, before) {
			t.Fatal("Compare mutated input")
		}
		if mode&(4|8) != 0 {
			noEligible := cloneCompareCapture(capture)
			for i := range noEligible.Canaries {
				noEligible.Canaries[i].Trivial = true
				noEligible.Canaries[i].Value = "42"
				if mode&8 != 0 {
					noEligible.Canaries[i].Trivial = false
					noEligible.Canaries[i].Value = ""
				}
			}
			noEligibleBefore := cloneCompareCapture(noEligible)
			facts := Compare(noEligible)
			if !reflect.DeepEqual(noEligible, noEligibleBefore) {
				t.Fatal("Compare mutated ineligible markers")
			}
			if facts.OwnerNormalData != oracle.StateUnknown || facts.NontrivialDataMatch != oracle.StateUnknown || facts.ReflectionExcluded != oracle.StateYes {
				t.Fatalf("no-eligible facts = %+v", facts)
			}
		}
	})
}

func TestComparePartialReflectionCannotConfirm(t *testing.T) {
	const canary = "canary-7f3a9c@lab.local"
	capture := baseCapture()
	capture.RequesterInputs = []string{"q=pre+" + canary + "+-suf"}

	facts := Compare(capture)
	if facts.ReflectionExcluded != oracle.StateNo {
		t.Fatalf("partial query reflection = %q, want no", facts.ReflectionExcluded)
	}
	now := time.Now().UTC()
	verdict := oracle.Evaluate(denyCase(facts, now), now)
	if verdict.Value == oracle.Confirmed {
		t.Fatalf("partial query reflection was confirmed: %+v", verdict)
	}
}
