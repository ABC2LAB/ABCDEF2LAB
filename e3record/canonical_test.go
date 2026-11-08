package e3record

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

type canonicalVectorInput struct {
	Kind  string          `json:"kind"`
	Value json.RawMessage `json:"value"`
	Count int             `json:"count"`
}

type canonicalVectorExpected struct {
	UTF8   *string `json:"utf8"`
	Repeat *struct {
		Prefix string `json:"prefix"`
		Text   string `json:"text"`
		Count  int    `json:"count"`
		Suffix string `json:"suffix"`
	} `json:"utf8_repeat"`
	ByteLength int    `json:"byte_length"`
	SHA256     string `json:"sha256"`
}

type canonicalVector struct {
	Name     string                   `json:"name"`
	Input    canonicalVectorInput     `json:"input"`
	Expected *canonicalVectorExpected `json:"expected"`
	Error    string                   `json:"error"`
}

func loadCanonicalVectors(t *testing.T) []canonicalVector {
	t.Helper()
	raw, err := os.ReadFile("testdata/canonical-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var suite struct {
		Version int               `json:"format_version"`
		Vectors []canonicalVector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &suite); err != nil {
		t.Fatal(err)
	}
	if suite.Version != 1 || len(suite.Vectors) == 0 {
		t.Fatal("missing v1 shared canonical vectors")
	}
	return suite.Vectors
}

func (v canonicalVectorInput) typedValue(t *testing.T) any {
	t.Helper()
	if v.Kind == "json" {
		decoder := json.NewDecoder(bytes.NewReader(v.Value))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	var text string
	if err := json.Unmarshal(v.Value, &text); err != nil {
		t.Fatal(err)
	}
	switch v.Kind {
	case "uint64":
		value, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return value
	case "float64":
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			t.Fatal(err)
		}
		return value
	case "utf8_hex":
		value, err := hex.DecodeString(text)
		if err != nil {
			t.Fatal(err)
		}
		return string(value)
	case "repeat_string":
		return strings.Repeat(text, v.Count)
	case "time":
		value, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			t.Fatal(err)
		}
		return canonicalTime(value)
	default:
		t.Fatalf("unknown canonical vector input kind %q", v.Kind)
		return nil
	}
}

func (v canonicalVectorExpected) expectedBytes(t *testing.T) []byte {
	t.Helper()
	var data []byte
	switch {
	case v.UTF8 != nil && v.Repeat == nil:
		data = []byte(*v.UTF8)
	case v.UTF8 == nil && v.Repeat != nil:
		data = []byte(v.Repeat.Prefix + strings.Repeat(v.Repeat.Text, v.Repeat.Count) + v.Repeat.Suffix)
	default:
		t.Fatal("vector must specify exactly one expected byte representation")
	}
	digest := sha256.Sum256(data)
	if len(data) != v.ByteLength || hex.EncodeToString(digest[:]) != v.SHA256 {
		t.Fatal("shared vector expected byte length or SHA-256 mismatch")
	}
	return data
}

func TestCanonicalSharedPythonVectors(t *testing.T) {
	for _, vector := range loadCanonicalVectors(t) {
		t.Run(vector.Name, func(t *testing.T) {
			got, err := canonical(vector.Input.typedValue(t))
			if vector.Error != "" {
				if vector.Expected != nil || vector.Error != ErrInvalid.Error() {
					t.Fatal("invalid error vector declaration")
				}
				if !errors.Is(err, ErrInvalid) || got != nil {
					t.Fatalf("expected %s; got %v (%d bytes)", vector.Error, err, len(got))
				}
				return
			}
			if vector.Expected == nil {
				t.Fatal("vector lacks expected bytes")
			}
			want := vector.Expected.expectedBytes(t)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("canonical mismatch: got %d bytes, want %d; error %v", len(got), len(want), err)
			}
		})
	}
}

func TestPrepareMatchesSharedVerdictVector(t *testing.T) {
	in, source := fixture()
	prepared, err := Prepare(t.Context(), in, sourceReader(source))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, valid := prepared.Snapshot()
	if !valid {
		t.Fatal("missing prepared record")
	}
	for _, vector := range loadCanonicalVectors(t) {
		if vector.Name == "confirmed-verdict-record" {
			want := vector.Expected.expectedBytes(t)
			if !bytes.Equal(snapshot.Canonical, want) || snapshot.PayloadDigest != sha256.Sum256(want) {
				t.Fatal("prepared payload differs from shared verdict-record bytes")
			}
			return
		}
	}
	t.Fatal("missing shared confirmed-verdict-record vector")
}
