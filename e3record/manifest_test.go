package e3record

import (
	"github.com/Autumn-27/artex/oracle"
	"testing"
)

func TestManifestRequiresIndependentPersistedContext(t *testing.T) {
	in, source := fixture()
	prepared, err := Prepare(t.Context(), in, sourceReader(source))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := prepared.Snapshot()
	if _, err := DecodeBoundManifest(snapshot.Manifest, source, oracle.Confirmed); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Source){
		func(s *Source) { s.Expected.Scope.RunRef = "foreign-run" },
		func(s *Source) { s.Expected.Scope.CaseRef = "foreign-case" },
		func(s *Source) { s.Expected.Owner.Handle = "foreign-session" },
		func(s *Source) { s.Expected.Evaluated.Label = "foreign-account" },
		func(s *Source) { s.Completed = false },
		func(s *Source) { s.FinalCheckpointDigest[0] ^= 1 },
	} {
		other := source
		change(&other)
		if _, err := DecodeBoundManifest(snapshot.Manifest, other, oracle.Confirmed); err == nil {
			t.Fatal("manifest self-comparison accepted foreign authority")
		}
	}
	if _, err := DecodeBoundManifest(snapshot.Manifest, source, oracle.Allowed); err == nil {
		t.Fatal("wrong verdict accepted")
	}
}
