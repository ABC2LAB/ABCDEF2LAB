package report

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestPrepareDiagnosticRequiresBoundPassiveConfirmation(t *testing.T) {
	sum := sha256.Sum256([]byte("{}"))
	base := DiagnosticReport{TaskID: 1, GeneratedAt: time.Now().UTC(), Run: &DiagnosticRun{VerificationVersion: "passive/v1"}, Entries: []DiagnosticEntry{{ID: "candidate-1", Kind: "candidate", Outcome: "confirmed", Verification: "passive_observation_v1", VulnClass: "information_disclosure", Status: "server_version_banner_twice", Evidence: "{}", Digest: hex.EncodeToString(sum[:])}}}
	if _, err := PrepareDiagnostic(base); err != nil {
		t.Fatal(err)
	}
	for _, altered := range []string{"no-run", "wrong-digest", "wrong-class", "review"} {
		t.Run(altered, func(t *testing.T) {
			in := base
			in.Entries = append([]DiagnosticEntry(nil), base.Entries...)
			switch altered {
			case "no-run":
				in.Run = nil
			case "wrong-digest":
				in.Entries[0].Digest = "wrong"
			case "wrong-class":
				in.Entries[0].VulnClass = "IDOR"
			case "review":
				in.Entries[0].Outcome = "review"
			}
			if _, err := PrepareDiagnostic(in); err == nil {
				t.Fatal("unbound passive confirmation accepted")
			}
		})
	}
}
