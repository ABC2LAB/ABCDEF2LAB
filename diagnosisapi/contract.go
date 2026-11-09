// Package diagnosisapi는 인증된 사용자 요청과 신뢰 실행 계약 사이의 경계다.
// 운영자가 결속한 승인만 전달하며 Planner·Worker·DB 내부 타입을 알지 않는다.
package diagnosisapi

import (
	"context"
	"net/http"
	"time"

	"github.com/Autumn-27/artex/diagnosis"
)

const Version = "diagnosis-api.v1"
const BasePath = "/api/diagnoses/v1"
const maxBodyBytes = 16 << 10

type Budget struct {
	Requests        int64 `json:"requests"`
	Tokens          int64 `json:"tokens"`
	CostUSDMicros   int64 `json:"cost_usd_micros"`
	DurationSeconds int64 `json:"duration_seconds"`
}
type Configuration struct {
	TargetRef       string `json:"target_ref"`
	ScopeRef        string `json:"scope_ref"`
	ModelProfileRef string `json:"model_profile_ref"`
	ApprovalMode    string `json:"approval_mode"`
	Budget          Budget `json:"budget"`
}
type Profile struct {
	ID            string        `json:"id"`
	Label         string        `json:"label"`
	Configuration Configuration `json:"configuration"`
}
type Draft struct {
	ID            string              `json:"id"`
	Revision      uint64              `json:"revision"`
	Configuration Configuration       `json:"configuration"`
	SourceDigest  string              `json:"source_digest"`
	State         string              `json:"state"`
	TaskID        string              `json:"task_id,omitempty"`
	Snapshot      *diagnosis.Snapshot `json:"snapshot,omitempty"`
}

// Binding은 공개 HTTP 입력이 아니다. 운영자 registry만 생성한다.
// 동일 사용자·초안 세대·설정 digest·실제 준비 task와 단회 권위를 모두 결속해야 한다.
type Binding struct {
	Principal      string
	DraftID        string
	Revision       uint64
	SourceDigest   string
	TaskID         string
	PreparationRef string
	ExpiresAt      time.Time
}

// Registry 구현은 기존 보호 설정·정책·예산·격리·권위의 실제 검사를 재사용한다.
// 승인 자료가 없으면 Approval은 nil을 반환하며 초안은 awaiting_approval에 머문다.
type Registry interface {
	Profiles(context.Context, string) ([]Profile, error)
	Approval(context.Context, string, Draft) (*Binding, error)
}

// Authenticate는 기존 Bearer JWT 검증 후 안정된 사용자 ID를 반환해야 한다.
// 쿠키/query-token fallback이나 임의 헤더 값을 사용자 ID로 신뢰하면 안 된다.
type Authenticate func(*http.Request) (string, error)
type Options struct {
	Authenticate Authenticate
	Registry     Registry
	Gateway      diagnosis.Gateway
	Now          func() time.Time
	MaxDrafts    int
	CallTimeout  time.Duration
}

// ConfigurationDigest는 운영자 승인 자료와 공개 초안이 같은 정본 설정을 가리키게 한다.
func ConfigurationDigest(c Configuration) (string, error) {
	if !validConfiguration(c) {
		return "", errInput
	}
	return digest(c), nil
}
