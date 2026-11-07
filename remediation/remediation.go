// Package remediation builds inert, version-bound remediation proposals.
// It never accepts raw evidence, invokes a model, reads a repository or sends requests.
package remediation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const GeneratorVersion = "remediation-v4"

// 자동 관측 32개와 사용자 보완 32개의 출처를 함께 보존한다. HTTP 입력 상한은 별도다.
const maxTechnologies = 64

var ErrInvalid = errors.New("invalid remediation input")
var idPattern = regexp.MustCompile(`^(finding|candidate|verdict)-[1-9][0-9]{0,18}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var versionPattern = regexp.MustCompile(`^[vV]?[0-9]{1,4}(\.[0-9A-Za-z]{1,12}){0,3}([+~-][A-Za-z0-9.-]{1,32})?$`)

// Technology carries categorical provenance, never an arbitrary source document.
type Technology struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Version       string `json:"version"`
	Source        string `json:"source"`
	Confidence    string `json:"confidence"`
	Component     string `json:"component"`
	ComponentRole string `json:"component_role"`
	EvidenceRef   string `json:"evidence_ref,omitempty"`
	ObservedAt    string `json:"observed_at,omitempty"`
}
type Input struct {
	Context         Context      `json:"context"`
	FindingID       string       `json:"finding_id"`
	ReportVersion   string       `json:"report_version"` // sha256:<actual report or source digest>, not evidence revision
	EvidenceVersion string       `json:"evidence_version"`
	EvidenceDigest  string       `json:"evidence_digest"`
	SourceDigest    string       `json:"source_digest"`
	VulnClass       string       `json:"vuln_class"`
	Status          string       `json:"status"` // verified outcome, not triage status
	EvidenceCount   int          `json:"evidence_count"`
	Technologies    []Technology `json:"technologies"`
}
type Bundle struct {
	Context              Context      `json:"context"`
	VulnClass            string       `json:"vuln_class"`
	MissingContextFields []string     `json:"missing_context_fields"`
	SchemaVersion        string       `json:"schema_version"`
	InputDigest          string       `json:"input_digest"`
	FindingID            string       `json:"finding_id"`
	ReportVersion        string       `json:"report_version"`
	EvidenceVersion      string       `json:"evidence_version"`
	EvidenceDigest       string       `json:"evidence_digest"`
	SourceDigest         string       `json:"source_digest"`
	RemediationVersion   string       `json:"remediation_version"`
	TechnologyDigest     string       `json:"technology_digest"`
	Technologies         []Technology `json:"technologies"`
	SecurityCondition    []string     `json:"security_condition"`
	ImmediateMitigation  []string     `json:"immediate_mitigation"`
	RootFix              []string     `json:"root_fix"`
	Configuration        []string     `json:"configuration"`
	CodeExample          []string     `json:"code_example"`
	Assumptions          []string     `json:"assumptions"`
	Verification         []string     `json:"verification"`
	ResidualRisk         []string     `json:"residual_risk"`
	AIPrompt             string       `json:"ai_prompt"`
}

var technologyKinds = map[string]bool{
	"language": true, "framework": true, "database": true, "orm": true,
	"server": true, "authentication": true, "deployment": true,
}

func member(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
func hash(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("%w: JSON 인코딩 실패", ErrInvalid)
	}
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:]), nil
}
func normalize(in Input) (Input, error) {
	if !idPattern.MatchString(in.FindingID) || !strings.HasPrefix(in.ReportVersion, "sha256:") || !digestPattern.MatchString(strings.TrimPrefix(in.ReportVersion, "sha256:")) || !digestPattern.MatchString(in.EvidenceDigest) || !digestPattern.MatchString(in.SourceDigest) || !regexp.MustCompile(`^(0|[1-9][0-9]{0,18})$`).MatchString(in.EvidenceVersion) || in.EvidenceCount < 0 || in.EvidenceCount > 100000 || len(in.Technologies) > maxTechnologies {
		return Input{}, ErrInvalid
	}
	if !member(in.Status, "confirmed", "review", "not_executed", "unknown", "negative") {
		return Input{}, ErrInvalid
	}
	if in.Status == "confirmed" && (!strings.HasPrefix(in.FindingID, "finding-") || in.EvidenceCount == 0 || in.EvidenceVersion == "0") {
		return Input{}, ErrInvalid
	}
	// 미지원 유형도 비신뢰 메타데이터로 보존하며 해결책 선택만 기본 분기를 사용한다.
	in.VulnClass = label(in.VulnClass)
	in.Context = NormalizeContext(in.Context)
	in.Technologies = append([]Technology{}, in.Technologies...)
	for i, t := range in.Technologies {
		if !technologyKinds[t.Kind] {
			return Input{}, ErrInvalid
		}
		t.Name = label(t.Name)
		t.Component = label(t.Component)
		t.ComponentRole = role(t.ComponentRole)
		t.EvidenceRef = reference(t.EvidenceRef)
		t.ObservedAt = observedAt(t.ObservedAt)
		if !member(t.Source, "user_provided", "observed", "inferred", "unknown") {
			t.Source = "unknown"
		}
		if !member(t.Confidence, "confirmed", "observed", "inferred", "unknown") {
			t.Confidence = "unknown"
		}
		if !versionPattern.MatchString(t.Version) {
			t.Version = "unknown"
		}
		if t.Name == "unknown" {
			t.Version = "unknown"
		}
		in.Technologies[i] = t
	}
	sort.Slice(in.Technologies, func(i, j int) bool {
		return technologyKey(in.Technologies[i]) < technologyKey(in.Technologies[j])
	})
	return in, nil
}
func technologyKey(t Technology) string {
	return strings.Join([]string{t.Kind, t.Name, t.Version, t.Source, t.Confidence, t.Component, t.ComponentRole, t.EvidenceRef, t.ObservedAt}, "\x00")
}

func Build(input Input) (Bundle, error) {
	in, err := normalize(input)
	if err != nil {
		return Bundle{}, err
	}
	inputDigest, err := hash(in)
	if err != nil {
		return Bundle{}, err
	}
	technologyDigest, err := hash(in.Technologies)
	if err != nil {
		return Bundle{}, err
	}
	b := Bundle{Context: in.Context, VulnClass: in.VulnClass, MissingContextFields: missingContextFields(in.Context), SchemaVersion: "3", InputDigest: inputDigest, FindingID: in.FindingID, ReportVersion: in.ReportVersion, EvidenceVersion: in.EvidenceVersion, EvidenceDigest: in.EvidenceDigest, SourceDigest: in.SourceDigest, RemediationVersion: GeneratorVersion, TechnologyDigest: technologyDigest, Technologies: in.Technologies}
	b.SecurityCondition = []string{"관측 결과와 현재 저장소를 대조하여 아래 보안 조건의 위반 여부를 먼저 검증한다."}
	b.ImmediateMitigation = []string{"영향 기능의 접근 범위를 승인된 사용자로 제한하고 감사한다. 임시 완화는 근본 수정이나 해결 완료가 아니다."}
	b.Configuration = []string{"실제 배포 설정과 호환성을 확인한다. 설정만으로 코드 결함이 해결되었다고 판단하지 않는다."}
	b.Assumptions = []string{"이 보고서는 아직 적용하지 않은 수정 작업 지시서다. 수신 AI가 실제 파일·함수·배포 관계와 작업 권한을 확인한 뒤 허용된 수정·시험·리뷰를 수행한다.", "언어로 프레임워크를 추정하지 않는다. 버전이 미확인이면 저장소 의존성 잠금 파일과 실행 환경을 확인한다.", "설명용 예시는 고객 코드 패치가 아니다. 조회한 공식 문서나 RAG 출처는 없으며 출처·수정 버전을 주장하지 않는다."}
	b.Verification = []string{"승인된 로컬 합성 환경에서 정상 권한·정상 입력의 기존 동작을 유지하는 시험을 실행한다.", "같은 조건에서 취약 동작이 거부되는 회귀 시험을 추가한다. 목표별 실제 실행 결과와 미실행 항목을 구분한다."}
	b.ResidualRisk = []string{"제공된 비민감 위치·전제·관측 요약과 보호된 원문 증거를 구분한다. 누락된 근거는 승인된 비민감 사본에서 확인하며 내부 증거 ID만으로 내용을 추정하지 않는다.", "본 생성기는 실제 재현·저장소 수정·시험·배포를 수행하지 않는다."}
	switch in.VulnClass {
	case "idor":
		b.SecurityCondition = append(b.SecurityCondition, "모든 객체 접근에 서버 측 주체·행위·객체·테넌트 인가가 필요하다.")
		b.RootFix = []string{"인증된 주체의 객체 권한을 데이터 조회·변경 전에 확인하고 기본 거부한다. 사용자 제공 객체 ID만 신뢰하지 않는다."}
		b.CodeExample = []string{"기술 중립 의사코드(예시): principal = authenticated_identity(); object = load_scoped(principal, object_id); require_permission(principal, action, object); return permitted_fields(object)."}
		b.Verification = append(b.Verification, "소유자 정상 접근, 다른 사용자·테넌트·미인증 접근 거부를 각각 시험한다.")
	case "sql_injection":
		b.SecurityCondition = append(b.SecurityCondition, "신뢰하지 않는 값이 SQL 구문 구조로 해석되지 않아야 한다.")
		b.RootFix = []string{"데이터 값은 실제 드라이버의 바인딩 API로 전달한다. 동적 식별자는 허용 목록으로 제한한다. 문자열 이스케이프만을 근본 수정으로 사용하지 않는다."}
		b.CodeExample = []string{"기술 중립 의사코드(예시): prepare(fixed_query).bind(untrusted_value).execute(); 식별자는 바인딩과 별도로 허용 목록 검증."}
	case "xss":
		b.SecurityCondition = append(b.SecurityCondition, "신뢰하지 않는 값이 활성 스크립트나 HTML 구조로 해석되지 않아야 한다.")
		b.RootFix = []string{"출력 문맥별 인코딩·안전한 텍스트 삽입 API를 사용하고 필요한 HTML만 검토된 sanitizer로 정제한다."}
		b.Configuration = []string{"CSP를 호환성 시험 후 방어 심층화로 설정한다. CSP만으로 unsafe 출력 경로의 근본 수정을 대체하지 않는다."}
	case "ssrf":
		b.SecurityCondition = append(b.SecurityCondition, "서버의 외부 요청이 승인된 목적지·프로토콜에 한정되어야 한다.")
		b.RootFix = []string{"실제 전송 경로에서 목적지 허용 목록과 DNS·IP·리다이렉트 재검증을 강제하고 내부·메타데이터 접근을 차단한다."}
		b.Configuration = []string{"승인된 목적지만 허용하는 egress 정책을 배포 환경에 맞춰 검토한다. 프록시 우회 및 IPv6 경로도 시험한다."}
	case "security_headers":
		b.SecurityCondition = append(b.SecurityCondition, "해당 응답 문맥에 필요한 보안 헤더 정책이 일관되게 적용되어야 한다.")
		b.RootFix = []string{"관측된 누락·오설정 헤더와 응답 경로를 먼저 확인한 뒤 앱 또는 실제 프록시의 단일 관리 위치에서 정책을 적용한다."}
		b.Configuration = []string{"헤더 종류·필요 값·HTTPS 조건·정상 기능 영향을 확인한 후 설정한다. 특정 서버·설정 파일·헤더 값은 확인 없이 단정하지 않는다."}
	case "session_cookie":
		b.SecurityCondition = append(b.SecurityCondition, "세션 쿠키가 전송·스크립트 접근·사이트 간 사용 경계에 맞춰 보호되어야 한다.")
		b.RootFix = []string{"실제 세션 발급 경로에서 Secure·HttpOnly·SameSite 필요성을 확인하고 설정하며 세션 재발급과 만료 정책을 검토한다."}
		b.Configuration = []string{"HTTPS 종료 지점과 교차 사이트 로그인 흐름을 확인한다. SameSite 값은 실제 흐름에 맞춰 선택하고 무조건 고정하지 않는다."}
	default:
		b.RootFix = []string{"이 유형에는 전용 해결책 템플릿이 없다. 기록된 원래 유형·영향 위치·관측·수정 목표를 보존하여 관련 처리 경로와 위반된 보안 조건을 확인한다. 원인과 권한이 확인되면 실제 프로젝트 기술로 최소 코드·설정을 수정하고 시험한다."}
	}
	if len(b.CodeExample) == 0 {
		b.CodeExample = []string{"실제 API·프레임워크·버전 확인 전에는 실행 가능한 코드를 생성하지 않는다. 위 원칙을 실제 경로에 맞춰 적용한다."}
	}
	if in.EvidenceVersion == "0" {
		b.ResidualRisk = append(b.ResidualRisk, "증거 버전 0: 구조화 증거 revision이 아직 발급되지 않은 기존 기록이다. 현재 source/report digest에만 결속된 미확정 안내이며 실제 재현 근거는 별도 확인해야 한다.")
	}
	if in.Status != "confirmed" {
		b.ResidualRisk = append(b.ResidualRisk, "확정 취약점이 아니다. 후보·미실행·불확실·음성 결과를 결함으로 단정하지 말고 허용된 확인·시험을 먼저 수행한다. 문제와 원인이 확인될 때만 해당 수정으로 이어간다.")
	}
	b.AIPrompt, err = buildPrompt(in, b)
	if err != nil {
		return Bundle{}, err
	}
	return b, nil
}

// IsStale fails closed, including mutated saved actions or prompt content.
func IsStale(saved Bundle, input Input) bool {
	fresh, err := Build(input)
	if err != nil {
		return true
	}
	savedHash, savedErr := hash(saved)
	freshHash, freshErr := hash(fresh)
	return savedErr != nil || freshErr != nil || savedHash != freshHash
}
