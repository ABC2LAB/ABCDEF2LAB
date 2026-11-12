package diagnosisprep

import (
	"github.com/Autumn-27/artex/diagnosisflow"
	"regexp"
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
	// Account는 대상 계정 조작(account_*)의 입력이다(C14 결정 1·2, docs/account-login-decision.md). 자격 값은 받지 않는다.
	Account *AccountRequest `json:"account,omitempty"`
}

// AccountRequest는 대상에 붙인 계정 하나를 가리킨다. Label은 화면·감사용 이름이고 사용자명 원문이 아니다.
// StatementConfirmed는 시험 계정 표시(account_mark_test) 때 승인자가 진술에 동의했다는 뜻이다.
type AccountRequest struct {
	Label              string `json:"label"`
	StatementConfirmed bool   `json:"statement_confirmed,omitempty"`
}

// ProductAccount는 계정 하나의 분류 상태다. Classification은 test 또는 real이고 기본은 real이다. test는 표시한 대상 판이
// 지금도 같은 승인 판일 때만이다. Reason은 real로 돌아간 사유(target_reapproved·manual_unmark)이고, 표시한 적이 없으면 비어 있다.
type ProductAccount struct {
	Label          string    `json:"label"`
	Classification string    `json:"classification"`
	Reason         string    `json:"reason,omitempty"`
	RegisteredBy   string    `json:"registered_by"`
	RegisteredAt   time.Time `json:"registered_at"`
	ClassifiedBy   string    `json:"classified_by,omitempty"`
	ClassifiedAt   time.Time `json:"classified_at,omitempty"`
	// MarkedRevision은 시험 계정으로 표시한 대상 판이다(표시한 적 없으면 0).
	MarkedRevision uint64 `json:"marked_revision,omitempty"`
}

// 대상 계정 상한·라벨 형식이다. 라벨은 소문자·숫자·밑줄·하이픈만 받아 사용자명·이메일 원문을 넣지 못하게 한다.
const MaxTargetAccounts = 16

var accountLabel = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ValidAccountLabel은 계정 라벨 형식 검사다.
func ValidAccountLabel(s string) bool { return accountLabel.MatchString(s) }

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
	Accounts   []ProductAccount        `json:"accounts,omitempty"`
	Account    *ProductAccount         `json:"account,omitempty"`
	Reason     string                  `json:"reason,omitempty"`
}

func validProductAccount(a ProductAccount) bool {
	if !ValidAccountLabel(a.Label) || !reference.MatchString(a.RegisteredBy) || a.RegisteredAt.IsZero() {
		return false
	}
	switch a.Classification {
	case "test":
		return a.Reason == "" && reference.MatchString(a.ClassifiedBy) && !a.ClassifiedAt.IsZero() && a.MarkedRevision > 0
	case "real":
		return a.Reason == "" || a.Reason == "target_reapproved" || a.Reason == "manual_unmark"
	}
	return false
}

func validProductRequest(r *ProductRequest) bool {
	if r == nil {
		return false
	}
	if r.Operation != "resolve" && r.TargetURL != "" {
		return false
	}
	if strings.HasPrefix(r.Operation, "account") {
		if !reference.MatchString(r.TargetID) || r.ProfileID != "" || r.Registration != nil || r.TargetURL != "" || r.JobID != "" || r.CommandKey != "" || r.ExpectedRevision != 0 || len(r.SelectedIDs) > 0 {
			return false
		}
		switch r.Operation {
		case "accounts":
			return r.Account == nil
		case "account_register", "account_unmark":
			return r.Account != nil && ValidAccountLabel(r.Account.Label) && !r.Account.StatementConfirmed
		case "account_mark_test":
			return r.Account != nil && ValidAccountLabel(r.Account.Label) && r.Account.StatementConfirmed
		}
		return false
	}
	if r.Account != nil {
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
