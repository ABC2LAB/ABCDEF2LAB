package report

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// 확정 단계(E③) 제품 판정 구역. 제품 판정은 당시 정책 동결본과 대조하는 진위 확인을 준비 서비스 IPC로 해야 해서, 보고서
// 생성 쪽이 직접 읽지 않고 제품 관리 중계가 판정 목록 API와 같은 확인을 거친 목록을 넘긴다(2026-11-23 사용자 결정, #875 발견 1).
// 진위 확인을 통과하고 근거가 봉인된 CONFIRMED만 요약의 확정 수에 센다. 진위 확인 실패 행은 조용히 빼지 않고 건수로 보인다.

// MaxDiagnosticProductVerdicts는 구역에 담는 판정 행 상한이다. 한 작업의 제품 판정 수는 선언의 자원 종류 수 이하라
// 판정 목록 API의 상한(e3contract.MaxDeclarationTypes)과 같게 둔다.
const MaxDiagnosticProductVerdicts = 16

// maxProductVerdictReasonCodes·maxProductResourceTypeBytes는 판정 한 행의 사유 코드 수와 자원 종류 이름 길이 상한이다.
const (
	maxProductVerdictReasonCodes = 16
	maxProductResourceTypeBytes  = 128
)

var (
	productVerdictIDPattern  = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	productReasonCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// ProductVerdictEntry는 확정 단계 판정 한 행이다. Verified가 false면 진위 확인에 실패한 행이라 값·검사를 담지 않는다.
type ProductVerdictEntry struct {
	VerdictID     string
	Verified      bool
	ResourceType  string
	OracleValue   string // confirmed | suspected | undecidable
	ReasonCodes   []string
	EvidenceCalls int
	Seal          string // confirmed | unconfirmed | "" (근거 호출 없음)
	ObservedAt    time.Time
}

// ProductVerdicts는 보고서의 확정 단계 판정 구역이다. Provided가 false면(제품 관리 중계를 거치지 않은 보고서) 구역을
// 그리지 않는다. Unavailable은 중계가 판정 목록을 읽지 못한 경우다. Total은 중계가 읽은 전체 행 수(크기 상한으로 잘리기 전)다.
// Confirmed·Suspected·Undecidable·Failed·Unsealed는 PrepareDiagnostic이 Items에서 센다.
type ProductVerdicts struct {
	Provided, Unavailable                               bool
	Total                                               int
	Items                                               []ProductVerdictEntry
	Confirmed, Suspected, Undecidable, Failed, Unsealed int
}

// Truncated는 크기 상한으로 일부 행만 담겼는지다.
func (p ProductVerdicts) Truncated() bool { return len(p.Items) < p.Total }

// productVerdictReasonText는 판정 사유 코드의 화면 문구다(web/src/workbench/live/verdictReasons.ts와 같은 뜻). 표에 없는
// 코드는 뜻을 지어내지 않고 코드를 그대로 보인다.
var productVerdictReasonText = map[string]string{
	"policy_deny_resolved":                  "정책상 교차 계정 접근 금지",
	"policy_allows":                         "정책이 접근을 허용",
	"sharing_or_delegation_allows":          "공유 · 위임으로 허용",
	"expected_access_policy_missing":        "접근 정책 없음",
	"expected_access_policy_invalid":        "접근 정책이 올바르지 않음",
	"expected_access_unknown":               "허용 여부를 알 수 없음",
	"sharing_or_delegation_unknown":         "공유 · 위임 여부를 알 수 없음",
	"subjects_distinct_verified":            "두 계정이 서로 다른 사용자임을 확인",
	"subject_not_verified":                  "계정 신원을 확인하지 못함",
	"subjects_not_distinct":                 "두 계정이 같은 사용자",
	"data_nontrivial_match":                 "다른 계정이 소유자 데이터를 읽음",
	"data_no_match":                         "소유자 데이터가 보이지 않음",
	"data_reflection_present":               "요청 값이 그대로 되돌아온 응답일 수 있음",
	"data_template_present":                 "누구에게나 같은 틀 응답일 수 있음",
	"data_unverifiable":                     "데이터를 확인할 수 없음",
	"product_data_encoding_unsupported":     "응답 인코딩을 읽을 수 없음",
	"product_data_partial_response":         "응답을 끝까지 받지 못함",
	"product_data_check_failed":             "데이터 검사 실패",
	"product_owner_marker_unobserved":       "소유 계정 조회에서 표식이 보이지 않음",
	"product_identity_unverified":           "계정 신원 확인 실패",
	"product_subjects_identical":            "두 계정이 같은 사용자",
	"product_create_approval_denied":        "시험 객체 생성 승인 거절",
	"product_create_approval_timeout":       "시험 객체 생성 승인 시간 초과",
	"product_confirmation_budget_exhausted": "확정 단계 요청 몫 소진",
	"product_request_result_unknown":        "요청 결과 불명 · 다시 보내지 않음",
	"product_request_denied":                "전송 통제가 요청을 막음",
	"product_create_failed":                 "시험 객체 생성 실패",
	"product_request_timeout":               "요청 시간 초과",
	"product_write_record_unavailable":      "쓰기 기록을 남기지 못해 보내지 않음",
	"product_create_request_unavailable":    "생성 요청을 만들 수 없어 보내지 않음",
	"product_data_check_contradiction":      "거부 응답인데 표식이 보임(모순)",
	"product_session_expired":               "교차 계정 세션 만료 가능성 · 판정 안 함",
	"product_cross_denial_unverified":       "거부 뒤 교차 계정 신원을 다시 확인하지 못함",
}

// ReasonText는 사유 코드들을 문구로 이어 붙인다.
func (e ProductVerdictEntry) ReasonText() string {
	out := make([]string, 0, len(e.ReasonCodes))
	for _, c := range e.ReasonCodes {
		if text, ok := productVerdictReasonText[c]; ok {
			out = append(out, text)
		} else {
			out = append(out, c)
		}
	}
	return strings.Join(out, " · ")
}

// ValueText는 판정 값의 문구다. 근거가 봉인되지 않은 CONFIRMED는 확정 수에 세지 않는다는 것을 같이 적는다.
func (e ProductVerdictEntry) ValueText() string {
	switch e.OracleValue {
	case "confirmed":
		if e.Seal != "confirmed" {
			return "취약 확정(근거 봉인 미확인 · 확정 수에 안 셈)"
		}
		return "취약 확정"
	case "suspected":
		return "취약 의심"
	default:
		return "판정 불가"
	}
}

// SealText는 근거 봉인 상태 문구다.
func (e ProductVerdictEntry) SealText() string {
	switch e.Seal {
	case "confirmed":
		return "봉인 확인됨"
	case "unconfirmed":
		return "봉인 미확인"
	default:
		return "근거 호출 없음"
	}
}

// prepareProductVerdicts는 구역 입력을 검사하고 건수를 센 뒤, 진위 확인을 통과하고 근거가 봉인된 CONFIRMED를 요약의 확정
// 수·심각도·유형 집계에 더한다. 입력 슬라이스는 바꾸지 않는다. 모양이 어긋나면 보고서 전체를 막는다(ErrExportBlocked).
func prepareProductVerdicts(in *DiagnosticReport) error {
	p := &in.Product
	p.Confirmed, p.Suspected, p.Undecidable, p.Failed, p.Unsealed = 0, 0, 0, 0, 0
	if !p.Provided {
		if p.Unavailable || p.Total != 0 || len(p.Items) != 0 {
			return ErrExportBlocked
		}
		return nil
	}
	if p.Unavailable && (p.Total != 0 || len(p.Items) != 0) {
		return ErrExportBlocked
	}
	if p.Total < len(p.Items) || p.Total > MaxDiagnosticProductVerdicts {
		return ErrExportBlocked
	}
	items := make([]ProductVerdictEntry, len(p.Items))
	copy(items, p.Items)
	p.Items = items
	seen := map[string]bool{}
	for i := range p.Items {
		e := &p.Items[i]
		// 진위 확인 실패 행은 판정 번호를 읽지 못했을 수 있다(빈 번호). 확인된 행은 번호가 있어야 한다.
		if (e.VerdictID != "" || e.Verified) && (!productVerdictIDPattern.MatchString(e.VerdictID) || seen[e.VerdictID]) {
			return ErrExportBlocked
		}
		if e.VerdictID != "" {
			seen[e.VerdictID] = true
		}
		if !e.Verified {
			// 진위 확인 실패 행은 값·검사를 숨긴 채 온다. 값이 남아 있으면 숨김이 깨진 것이다.
			if e.ResourceType != "" || e.OracleValue != "" || len(e.ReasonCodes) != 0 || e.EvidenceCalls != 0 || e.Seal != "" || !e.ObservedAt.IsZero() {
				return ErrExportBlocked
			}
			p.Failed++
			continue
		}
		if e.ResourceType == "" || len(e.ResourceType) > maxProductResourceTypeBytes || !utf8.ValidString(e.ResourceType) || strings.ContainsRune(e.ResourceType, 0) {
			return ErrExportBlocked
		}
		if e.EvidenceCalls < 0 || len(e.ReasonCodes) > maxProductVerdictReasonCodes || e.ObservedAt.IsZero() {
			return ErrExportBlocked
		}
		codes := make([]string, len(e.ReasonCodes))
		copy(codes, e.ReasonCodes)
		e.ReasonCodes = codes
		for _, c := range codes {
			if !productReasonCodePattern.MatchString(c) {
				return ErrExportBlocked
			}
		}
		if e.Seal != "" && e.Seal != "confirmed" && e.Seal != "unconfirmed" {
			return ErrExportBlocked
		}
		switch e.OracleValue {
		case "confirmed":
			if e.Seal != "confirmed" {
				p.Unsealed++
				break
			}
			p.Confirmed++
			in.Confirmed++
			// 확정 단계 판정은 심각도를 매기지 않는다. 확정 수와 심각도 집계의 합이 어긋나지 않게 '미평가'로 센다.
			in.BySeverity["미평가"]++
			if in.Shared {
				in.ByType["[REDACTED]"]++
			} else {
				in.ByType["접근통제(IDOR) · 확정 단계"]++
			}
		case "suspected":
			p.Suspected++
		case "undecidable":
			p.Undecidable++
		default:
			return ErrExportBlocked
		}
		if in.Shared {
			// 공유 사본은 자유문장을 가리는 기존 규칙을 따른다. 자원 종류 이름은 운영자가 쓴 선언 이름이라 가린다.
			e.ResourceType = "[REDACTED]"
		} else {
			e.ResourceType = DiagnosticText(e.ResourceType)
		}
	}
	return nil
}
