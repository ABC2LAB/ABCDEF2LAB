package diagnosisprep

import (
	"github.com/Autumn-27/artex/diagnosisflow"
	"strings"
	"time"
)

// ProductControl is private IPC, never an execution grant or a public v2 command.
// Only the installed product wrapper implements it; generic coordinators reject it.
const ProductControl Command = "product_control"

type ProductRequest struct {
	JobID            string              `json:"job_id,omitempty"`
	CommandKey       string              `json:"command_key,omitempty"`
	ExpectedRevision uint64              `json:"expected_revision,omitempty"`
	SelectedIDs      []string            `json:"selected_ids,omitempty"`
	TargetURL        string              `json:"target_url,omitempty"`
	Operation        string              `json:"operation"`
	TargetID         string              `json:"target_id,omitempty"`
	ProfileID        string              `json:"profile_id,omitempty"`
	Registration     *TargetRegistration `json:"registration,omitempty"`
}
type TargetRegistration struct {
	ReversibleWrites bool     `json:"reversible_writes,omitempty"`
	URL              string   `json:"url"`
	PathPrefixes     []string `json:"path_prefixes"`
	Methods          []string `json:"methods"`
	ReadonlyBasis    string   `json:"readonly_basis"`
	Requests         int64    `json:"requests"`
	ValidHours       int64    `json:"valid_hours"`
}
type ProductTarget struct {
	ID             string    `json:"id"`
	Origin         string    `json:"origin"`
	State          string    `json:"state"`
	Revision       uint64    `json:"revision"`
	PathPrefixes   []string  `json:"path_prefixes,omitempty"`
	Methods        []string  `json:"methods,omitempty"`
	Requests       int64     `json:"requests"`
	ChallengeName  string    `json:"challenge_name,omitempty"`
	ChallengeValue string    `json:"challenge_value,omitempty"`
	Addresses      []string  `json:"addresses,omitempty"`
	Port           int       `json:"port"`
	TLSSPKISHA256  string    `json:"tls_spki_sha256,omitempty"`
	ApprovedAt     time.Time `json:"approved_at,omitempty"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	Reason         string    `json:"reason,omitempty"`
}
type ProductCredential struct {
	ProfileID  string `json:"profile_id"`
	Model      string `json:"model"`
	Configured bool   `json:"configured"`
}
type ProductResponse struct {
	Workflow   *diagnosisflow.Snapshot `json:"workflow,omitempty"`
	Targets    []ProductTarget         `json:"targets,omitempty"`
	Target     *ProductTarget          `json:"target,omitempty"`
	Credential *ProductCredential      `json:"credential,omitempty"`
	Reason     string                  `json:"reason,omitempty"`
}

func validProductRequest(r *ProductRequest) bool {
	if r == nil {
		return false
	}
	if r.Operation != "resolve" && r.TargetURL != "" {
		return false
	}
	if strings.HasPrefix(r.Operation, "workflow_") {
		return reference.MatchString(r.JobID) && r.TargetID == "" && r.ProfileID == "" && r.Registration == nil && ((r.Operation == "workflow_status" && r.CommandKey == "" && r.ExpectedRevision == 0 && len(r.SelectedIDs) == 0) || ((r.Operation == "workflow_selection" || r.Operation == "workflow_pause" || r.Operation == "workflow_resume") && reference.MatchString(r.CommandKey) && r.ExpectedRevision > 0 && len(r.SelectedIDs) <= 64))
	}
	if r.JobID != "" || r.CommandKey != "" || r.ExpectedRevision != 0 || len(r.SelectedIDs) > 0 {
		return false
	}
	switch r.Operation {
	case "resolve":
		_, e := CanonicalInput(Input{TargetURL: r.TargetURL, ProfileID: "target"})
		return e == nil && r.TargetID == "" && r.ProfileID == "" && r.Registration == nil
	case "targets":
		return r.TargetID == "" && r.ProfileID == "" && r.Registration == nil
	case "register":
		s := r.Registration
		if r.TargetID != "" || r.ProfileID != "" || s == nil || !validText(s.ReadonlyBasis, 1024) || len(s.ReadonlyBasis) < 20 || s.Requests < 1 || s.Requests > 100 || s.ValidHours < 1 || s.ValidHours > 24 || len(s.PathPrefixes) < 1 || len(s.PathPrefixes) > 16 || len(s.Methods) < 1 || len(s.Methods) > 5 {
			return false
		}
		if _, e := CanonicalInput(Input{TargetURL: s.URL, ProfileID: "target"}); e != nil {
			return false
		}
		for _, p := range s.PathPrefixes {
			if len(p) < 1 || len(p) > 512 {
				return false
			}
		}
		for _, m := range s.Methods {
			if m != "GET" && m != "HEAD" && !(s.ReversibleWrites && (m == "POST" || m == "PUT" || m == "PATCH")) {
				return false
			}
		}
		return true
	case "approve", "revoke", "target":
		return reference.MatchString(r.TargetID) && r.ProfileID == "" && r.Registration == nil
	case "credentials":
		return reference.MatchString(r.ProfileID) && r.TargetID == "" && r.Registration == nil
	}
	return false
}
