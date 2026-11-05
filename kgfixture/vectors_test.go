package kgfixture

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// The shared vectors pin the canonical bytes across Go and Python
// (scripts/eval/tests/test_kg_fixture_vectors.py reads the same file).
type vectorFile struct {
	Format string `json:"format"`
	Cases  []struct {
		Name     string `json:"name"`
		Registry string `json:"registry"`
		Expect   struct {
			Digest        string `json:"digest"`
			PolicyVersion string `json:"policy_version"`
			SameDigestAs  string `json:"same_digest_as"`
			Error         string `json:"error"`
			Layer         string `json:"layer"`
		} `json:"expect"`
	} `json:"cases"`
}

func TestSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors vectorFile
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if vectors.Format != "abc2lab.kg-fixture-vectors/v1" || len(vectors.Cases) == 0 {
		t.Fatalf("vector file format %q cases %d", vectors.Format, len(vectors.Cases))
	}
	digests := map[string]string{}
	accepted, rejected := 0, 0
	for _, c := range vectors.Cases {
		snapshot, err := Parse([]byte(c.Registry))
		if c.Expect.Error != "" {
			rejected++
			var coded *Error
			if !errors.As(err, &coded) || string(coded.Code) != c.Expect.Error {
				t.Errorf("%s: err=%v, want %s", c.Name, err, c.Expect.Error)
			}
			if snapshot != nil {
				t.Errorf("%s: snapshot returned with an error", c.Name)
			}
			continue
		}
		accepted++
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		digest := snapshot.Digest()
		got := hex.EncodeToString(digest[:])
		if got != c.Expect.Digest || snapshot.PolicyVersion() != c.Expect.PolicyVersion {
			t.Errorf("%s: digest %s version %s", c.Name, got, snapshot.PolicyVersion())
		}
		if c.Expect.SameDigestAs != "" && digests[c.Expect.SameDigestAs] != got {
			t.Errorf("%s: digest differs from %s", c.Name, c.Expect.SameDigestAs)
		}
		digests[c.Name] = got
	}
	if accepted == 0 || rejected == 0 {
		t.Fatalf("accepted %d rejected %d", accepted, rejected)
	}
}
