//go:build linux

package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

const checkpointFuzzLimit = 16 << 10

func checkpointFuzzValue(sequence uint64, suffix string) auditlog.Checkpoint {
	if len(suffix) > 48 {
		suffix = suffix[:48]
	}
	suffix = hex.EncodeToString([]byte(suffix))
	return auditlog.Checkpoint{
		Version: auditlog.Version, JournalID: "journal-fuzz-" + suffix,
		Scope: transmit.Scope{
			RunRef: "run-fuzz-" + suffix, TaskRef: "task-fuzz",
			CaseRef: "case-fuzz", ResetRef: "reset-fuzz", PolicyRef: "policy-fuzz",
		},
		KeyRef: "key-fuzz-" + suffix, Sequence: sequence, MAC: "mac-fuzz-" + suffix,
	}
}

func checkpointFuzzCanonical(t testing.TB, checkpoint auditlog.Checkpoint) []byte {
	t.Helper()
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return encoded
}

func checkpointFuzzDecode(t *testing.T, encoded []byte) (auditlog.Checkpoint, error) {
	t.Helper()
	wantInput := bytes.Clone(encoded)
	got, err := decodeCheckpoint(encoded)
	if !bytes.Equal(encoded, wantInput) {
		t.Fatal("decodeCheckpoint mutated input")
	}
	if err != nil {
		if err != errInput {
			t.Fatalf("parser returned non-contract error %v", err)
		}
		return got, err
	}
	compact := new(bytes.Buffer)
	if err := json.Compact(compact, encoded); err != nil {
		t.Fatalf("accepted input was not JSON: %v", err)
	}
	canonical, err := json.Marshal(got)
	if err != nil || !bytes.Equal(compact.Bytes(), canonical) {
		t.Fatal("accepted checkpoint was not canonical")
	}
	return got, nil
}

// FuzzCheckpointDecodeArbitrary keeps the parser boundary panic-free and
// deterministic for bounded bytes. Acceptance always means exact canonical
// re-serialization; syntax or canonicalization failures stay generic.
func FuzzCheckpointDecodeArbitrary(f *testing.F) {
	canonical := checkpointFuzzCanonical(f, checkpointFuzzValue(0, "seed"))
	zero := checkpointFuzzCanonical(f, auditlog.Checkpoint{})
	f.Add(canonical)
	f.Add(zero)
	f.Add([]byte("null"))
	f.Add([]byte("{\"Version\":1"))
	f.Fuzz(func(t *testing.T, encoded []byte) {
		if len(encoded) > checkpointFuzzLimit {
			encoded = encoded[:checkpointFuzzLimit]
		}
		// Parsing syntax alone must also accept the zero struct. Trust in a
		// checkpoint's version, scope and MAC is verified after this boundary.
		if got, err := checkpointFuzzDecode(t, bytes.Clone(zero)); err != nil || got != (auditlog.Checkpoint{}) {
			t.Fatal("zero checkpoint syntax control rejected")
		}
		first, firstErr := checkpointFuzzDecode(t, encoded)
		second, secondErr := checkpointFuzzDecode(t, encoded)
		if (firstErr == nil) != (secondErr == nil) || firstErr != secondErr || !reflect.DeepEqual(first, second) {
			t.Fatal("repeated parsing changed result")
		}
	})
}

// FuzzCheckpointCanonicalMutations starts from a valid complete checkpoint and
// changes one syntax/canonicalization rule at a time. The positive modes cover
// compact and pretty JSON; all other modes must be rejected by decodeCheckpoint.
func FuzzCheckpointCanonicalMutations(f *testing.F) {
	const modes = 23
	for mode := uint8(0); mode < modes; mode++ {
		f.Add(mode, uint64(mode), "seed")
	}
	f.Add(uint8(0), ^uint64(0), "\x00\xff\"\\")
	f.Fuzz(func(t *testing.T, mode uint8, sequence uint64, suffix string) {
		checkpoint := checkpointFuzzValue(sequence, suffix)
		canonical := checkpointFuzzCanonical(t, checkpoint)
		if got, err := checkpointFuzzDecode(t, canonical); err != nil || got != checkpoint {
			t.Fatal("generated canonical control rejected or changed")
		}
		valid := false
		switch mode % modes {
		case 0:
			valid = true
		case 1:
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, canonical, "", "  "); err != nil {
				t.Fatal(err)
			}
			canonical = pretty.Bytes()
			valid = true
		case 2:
			canonical = append(append([]byte(nil), canonical...), []byte(" {}")...)
		case 3:
			canonical = append(append(append([]byte(nil), canonical[:len(canonical)-1]...), []byte(`,"PrivateSentinel":"fixture"`)...), '}')
		case 4:
			canonical = bytes.Replace(canonical, []byte(`"Version":`), []byte(`"\u0056ersion":`), 1)
			var equivalent auditlog.Checkpoint
			if json.Unmarshal(canonical, &equivalent) != nil || equivalent != checkpoint {
				t.Fatal("escaped key did not preserve the semantic fixture")
			}
		case 5:
			old := []byte(`,"MAC":` + strconv.Quote(checkpoint.MAC))
			canonical = bytes.Replace(canonical, old, nil, 1)
		case 6:
			canonical = []byte("null")
		case 7:
			canonical = append(append([]byte(nil), canonical...), '{')
		case 8:
			old := []byte(`"Sequence":` + strconv.FormatUint(sequence, 10))
			canonical = bytes.Replace(canonical, old, []byte(`"Sequence":"bad"`), 1)
		case 9:
			old := []byte(`"Version":` + strconv.Itoa(auditlog.Version))
			canonical = bytes.Replace(canonical, old, []byte(`"Version":null`), 1)
		case 10:
			old := []byte(`"TaskRef":"task-fuzz"`)
			canonical = bytes.Replace(canonical, old, []byte(`"TaskRef":"task-fuzz","TaskRef":"duplicate"`), 1)
		case 11:
			old := []byte(`"MAC":"` + checkpoint.MAC + `"`)
			canonical = bytes.Replace(canonical, old, []byte(`"MAC":null`), 1)
		case 12:
			old := []byte(`"Sequence":` + strconv.FormatUint(sequence, 10))
			canonical = bytes.Replace(canonical, old, []byte(`"Sequence":-1`), 1)
		case 13:
			old := []byte(`"Sequence":` + strconv.FormatUint(sequence, 10))
			canonical = bytes.Replace(canonical, old, []byte(`"Sequence":1.5`), 1)
		case 14:
			canonical = bytes.Replace(canonical, []byte(`"Scope":{`), []byte(`"Scope":{"Unknown":1,`), 1)
		case 15:
			old := []byte(`"RunRef":` + strconv.Quote(checkpoint.Scope.RunRef))
			canonical = bytes.Replace(canonical, old, []byte(`"RunRef":null`), 1)
		case 16:
			canonical = bytes.Replace(canonical, []byte(`"RunRef":`), []byte(`"runref":`), 1)
		case 17:
			old := []byte(`"Version":` + strconv.Itoa(auditlog.Version))
			canonical = bytes.Replace(canonical, old, append(bytes.Clone(old), append([]byte{','}, old...)...), 1)
		case 18:
			old := []byte(`"Sequence":` + strconv.FormatUint(sequence, 10))
			canonical = bytes.Replace(canonical, old, []byte(`"Sequence":18446744073709551616`), 1)
		case 19:
			canonical = bytes.Replace(canonical, []byte(`"Version":`), []byte(`"version":`), 1)
		case 20:
			old := []byte(`"Version":` + strconv.Itoa(auditlog.Version) + `,"JournalID":` + strconv.Quote(checkpoint.JournalID))
			reordered := []byte(`"JournalID":` + strconv.Quote(checkpoint.JournalID) + `,"Version":` + strconv.Itoa(auditlog.Version))
			canonical = bytes.Replace(canonical, old, reordered, 1)
		case 21:
			canonical = bytes.Replace(canonical, []byte(`"TaskRef":"task-fuzz",`), nil, 1)
		case 22:
			canonical = bytes.Replace(canonical, []byte(`"TaskRef":"task-fuzz"`), []byte(`"TaskRef":[]`), 1)
		}
		// Only these two modes deliberately break JSON syntax. All remaining
		// negative controls must reach their intended type/canonical boundary.
		if mode%modes != 2 && mode%modes != 7 && !json.Valid(canonical) {
			t.Fatalf("mutation %d unexpectedly broke JSON syntax", mode%modes)
		}
		got, err := checkpointFuzzDecode(t, canonical)
		if valid {
			if err != nil {
				t.Fatalf("valid mode %d rejected: %v", mode%modes, err)
			}
			if got != checkpoint {
				t.Fatalf("valid mode changed checkpoint: got=%+v want=%+v", got, checkpoint)
			}
		} else if err != errInput {
			t.Fatalf("invalid mode %d returned %v", mode%modes, err)
		}
	})
}
