package diagnosisprep

import (
	"github.com/Autumn-27/artex/diagnosisflow"
	"regexp"
	"strings"
	"time"
)

// MaxTargetRequests는 대상 하나·회차 하나에 승인할 수 있는 E② 요청 수의 코드 상한이다. 설치 정책(target_policy.max_requests)·
// 대상 등록(requests)·워크플로 프로파일이 같은 값을 쓴다. 기본값이 아니라 설치 조직이 고를 수 있는 최대치다(C32). 100이던
// 상한을 2026-11-14에 올렸다: 느린 모델·넓은 대상에서 진단이 승인 요청 수에 먼저 막혀 성능을 깎았다.
const MaxTargetRequests = 1000

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
	// Account는 대상 계정 조작(account_*)의 입력이다(C14 결정 1·2, docs/account-login-decision.md).
	Account *AccountRequest `json:"account,omitempty"`
	// Login은 대상 로그인 방식 설정(target_login_set)이다(C14 결정 3). 자격 값은 없다.
	Login *TargetLogin `json:"login,omitempty"`
	// Credential은 계정 자격(account_credential_set)이다. 준비 프로세스가 보호 파일에 쓰고 저널에는 참조·해시만 남긴다.
	// 응답·로그·모델 입력에는 나가지 않는다.
	Credential *AccountCredential `json:"credential,omitempty"`
}

// TargetLogin은 대상 앱의 로그인 방식이다. 앱의 경로·필드 이름은 코드가 아니라 이 설정으로만 받는다(하드코딩 금지).
// 경로는 승인된 경로 접두 안이어야 한다. 세션 위치는 Set-Cookie 이름 목록(Cookies) 또는 JSON 응답 필드 경로(JSONField)와
// 그 값을 실을 요청 헤더(Header·Prefix) 중 하나다.
type TargetLogin struct {
	Method        string   `json:"method"`
	Path          string   `json:"path"`
	Format        string   `json:"format"`
	UsernameField string   `json:"username_field"`
	PasswordField string   `json:"password_field"`
	Cookies       []string `json:"cookies,omitempty"`
	JSONField     string   `json:"json_field,omitempty"`
	Header        string   `json:"header,omitempty"`
	Prefix        string   `json:"prefix,omitempty"`
}

// AccountCredential은 계정 하나의 로그인 자격이다.
type AccountCredential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

var (
	loginField  = regexp.MustCompile(`^[A-Za-z0-9_.\-\[\]]{1,64}$`)
	cookieName  = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}$")
	headerName  = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	jsonSegment = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,64}$`)
)

// ValidTargetLogin은 로그인 방식의 형식 검사다. 경로가 승인 경로 접두 안인지는 대상 기록을 가진 쪽이 따로 본다.
func ValidTargetLogin(l TargetLogin) bool {
	if l.Method != "POST" || (l.Format != "form" && l.Format != "json") || !loginField.MatchString(l.UsernameField) || !loginField.MatchString(l.PasswordField) ||
		l.UsernameField == l.PasswordField || len(l.Path) < 1 || len(l.Path) > 512 || !strings.HasPrefix(l.Path, "/") || strings.HasPrefix(l.Path, "//") || strings.ContainsAny(l.Path, "?#%;\\ ") {
		return false
	}
	cookies := len(l.Cookies) > 0
	field := l.JSONField != "" || l.Header != "" || l.Prefix != ""
	if cookies == field {
		return false
	}
	if cookies {
		if len(l.Cookies) > 8 {
			return false
		}
		seen := map[string]bool{}
		for _, c := range l.Cookies {
			if !cookieName.MatchString(c) || seen[c] {
				return false
			}
			seen[c] = true
		}
		return true
	}
	segments := strings.Split(l.JSONField, ".")
	// 접두는 선택이다(값만 싣는 헤더도 있다). 있으면 제어 문자 없는 32바이트 이하다.
	if len(segments) > 8 || !headerName.MatchString(l.Header) || l.Prefix != "" && !validText(l.Prefix, 32) {
		return false
	}
	switch strings.ToLower(l.Header) {
	case "host", "cookie", "content-length", "transfer-encoding", "connection", "proxy-authorization":
		return false
	}
	for _, s := range segments {
		if !jsonSegment.MatchString(s) {
			return false
		}
	}
	return true
}

// ValidAccountCredential은 자격 값의 형식 검사다(길이·제어 문자). 값 자체는 검사 뒤에도 남기지 않는다.
func ValidAccountCredential(c AccountCredential) bool {
	return len(c.Username) >= 1 && len(c.Username) <= 256 && len(c.Password) >= 1 && len(c.Password) <= 512 &&
		!strings.ContainsAny(c.Username, "\x00\r\n") && !strings.ContainsAny(c.Password, "\x00\r\n")
}

// AccountRequest는 대상에 붙인 계정 하나를 가리킨다. Label은 화면·감사용 이름이고 사용자명 원문이 아니다.
// StatementConfirmed는 시험 계정 표시(account_mark_test) 때 승인자가 진술에 동의했다는 뜻이다.
type AccountRequest struct {
	Label              string `json:"label"`
	StatementConfirmed bool   `json:"statement_confirmed,omitempty"`
}

// ProductAccount는 계정 하나의 분류 상태다. Classification은 test 또는 real이고 기본은 real이다. test는 표시한 대상 판이
// 지금도 같은 승인 판이고 승인이 만료되지 않았을 때만이다. Reason은 real로 돌아간 사유다: target_reapproved(대상 판이 바뀜 —
// 재승인·철회 포함), target_expired(승인 만료), manual_unmark(사람이 뗌). 표시한 적이 없으면 비어 있다.
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
	// CredentialConfigured는 보호 파일에 자격이 있다는 뜻이다. 값·해시는 내보내지 않는다.
	CredentialConfigured bool `json:"credential_configured,omitempty"`
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
	Login      *TargetLogin            `json:"login,omitempty"`
	// AccountStatement는 accounts 목록과 함께 주는 시험 계정 표시 진술 문장이다. 화면은 이 문장을 그대로 보여 주고 동의를
	// 받는다(저널에는 이 문장의 SHA-256만 남는다).
	AccountStatement string `json:"account_statement,omitempty"`
	// ExternalConsentStatement는 accounts 목록과 함께 주는 외부 API 시험 계정 전송 동의 진술 문장이다(초안 입력
	// external_test_accounts). 화면은 이 문장을 그대로 보여 주고 동의를 받는다. 실행이 시작되면 저널에 이 문장의 SHA-256이
	// 남는다(#552, #561 리뷰).
	ExternalConsentStatement string `json:"external_consent_statement,omitempty"`
	Reason                   string `json:"reason,omitempty"`
}

func validProductAccount(a ProductAccount) bool {
	if !ValidAccountLabel(a.Label) || !reference.MatchString(a.RegisteredBy) || a.RegisteredAt.IsZero() {
		return false
	}
	switch a.Classification {
	case "test":
		return a.Reason == "" && reference.MatchString(a.ClassifiedBy) && !a.ClassifiedAt.IsZero() && a.MarkedRevision > 0
	case "real":
		return a.Reason == "" || a.Reason == "target_reapproved" || a.Reason == "target_expired" || a.Reason == "manual_unmark"
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
			return r.Account == nil && r.Login == nil && r.Credential == nil
		case "account_register", "account_unmark":
			return r.Account != nil && ValidAccountLabel(r.Account.Label) && !r.Account.StatementConfirmed && r.Login == nil && r.Credential == nil
		case "account_mark_test":
			return r.Account != nil && ValidAccountLabel(r.Account.Label) && r.Account.StatementConfirmed && r.Login == nil && r.Credential == nil
		case "account_credential_set":
			return r.Account != nil && ValidAccountLabel(r.Account.Label) && !r.Account.StatementConfirmed && r.Login == nil && r.Credential != nil && ValidAccountCredential(*r.Credential)
		}
		return false
	}
	if strings.HasPrefix(r.Operation, "target_login") {
		if !reference.MatchString(r.TargetID) || r.ProfileID != "" || r.Registration != nil || r.TargetURL != "" || r.JobID != "" || r.CommandKey != "" || r.ExpectedRevision != 0 || len(r.SelectedIDs) > 0 || r.Account != nil || r.Credential != nil {
			return false
		}
		switch r.Operation {
		case "target_login":
			return r.Login == nil
		case "target_login_set":
			return r.Login != nil && ValidTargetLogin(*r.Login)
		}
		return false
	}
	if r.Account != nil || r.Login != nil || r.Credential != nil {
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
		if r.TargetID != "" || r.ProfileID != "" || s == nil || !validText(s.ReadonlyBasis, 1024) || len(s.ReadonlyBasis) < 20 || s.Requests < 1 || s.Requests > MaxTargetRequests || s.ValidHours < 1 || s.ValidHours > 24 || len(s.PathPrefixes) < 1 || len(s.PathPrefixes) > 16 || len(s.Methods) < 1 || len(s.Methods) > 5 {
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
