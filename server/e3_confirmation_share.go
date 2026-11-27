package server

import "time"

// E③ 확정 단계 몫(2026-11-18 사용자 결정 ②, docs/contracts/e3-product-confirmation-interface.md §7.1).
//
// 확정 단계는 모델 단계가 끝난 뒤 같은 실행 안에서 돈다. 모델 단계가 실행 상한을 다 써도 확정 단계가 돌 수 있게, 실행을
// 시작할 때 확정 단계 몫을 떼어 둔다. 몫이 0이면(확정 단계 꺼짐) 모델 단계 예산은 제품 상한 그대로다.
const (
	// E3ConfirmationShareDivisor: 확정 몫은 요청·시간 모두 실행 상한의 1/4을 넘지 않는다. 넘치는 종류는 시험하지 않고
	// UNDECIDABLE(예산)로 남는다.
	E3ConfirmationShareDivisor = 4
	// E3ConfirmationLoginRequestsPerAccount: 계정 하나의 확정 단계 직전 다시 로그인 요청 상한(사용자 결정 2026-11-20 (가)).
	// 학습 로그인 재사용(모델 로그인 계약 §6-2)은 로그인 화면 GET(CSRF·로그인 전 쿠키) 1 + 로그인 POST 1이다.
	E3ConfirmationLoginRequestsPerAccount = 2
	// E3ConfirmationLoginContrastRequests: 학습 로그인 성공을 검증하는 세션 없는 대조 GET(회차당 1, 계약 §4 프로그램 대조).
	E3ConfirmationLoginContrastRequests = 1
	// E3ConfirmationReloginRequests: 확정 단계 직전 다시 로그인 몫(소유 계정·평가 계정 + 대조 1). 모델 단계가 길어 대상
	// 세션이 만료돼도 확정 단계가 새 세션으로 돌게 한다.
	E3ConfirmationReloginRequests = 2*E3ConfirmationLoginRequestsPerAccount + E3ConfirmationLoginContrastRequests
	// E3ConfirmationIdentityRequests: 신원 확인 요청(소유 계정·평가 계정 각 1건).
	E3ConfirmationIdentityRequests = 2
	// E3ConfirmationFixedRequests: 시험할 종류가 하나라도 있을 때만 드는 고정 몫(다시 로그인 + 신원 확인).
	E3ConfirmationFixedRequests = E3ConfirmationReloginRequests + E3ConfirmationIdentityRequests
	// E3ConfirmationRequestsPerType: 종류 하나당 생성 1 + 소유 조회 1 + 교차 조회 1 + 교차 거부 뒤 평가 계정 신원 재확인 1
	// (거부 응답일 때만 씀, 사용자 결정 2026-11-20 S21 (나)).
	E3ConfirmationRequestsPerType = 4
	// E3ConfirmationRequestTimeoutDefault는 확정 단계 요청 하나의 전용 시간 상한 기본값이다. E② 요청 시간 상한(제품 60초)과
	// 따로 둔다. VAmPI 파일럿에서 확정 요청의 실제 시간을 잰 뒤 확정한다(사용자 결정 2026-11-18 (나)·(다)).
	E3ConfirmationRequestTimeoutDefault = 10 * time.Second
)

// E3ConfirmationShare는 실행 하나의 확정 단계 몫이다. Admitted 종류만 시험하고, BudgetSkipped 종류는 요청을 보내지
// 않고 UNDECIDABLE(예산)로 남긴다.
type E3ConfirmationShare struct {
	Requests      uint64
	Timeout       time.Duration
	Admitted      int
	BudgetSkipped int
}

// ComputeE3ConfirmationShare는 시험할 종류 수와 실행 상한(요청·시간)에서 몫을 정한다. 요청 몫 = 고정 7(다시 로그인 5 + 신원 2) + 종류×4,
// 시간 몫 = 요청 몫 × 확정 요청 시간 상한이고, 둘 다 실행 상한의 1/4 안에 드는 만큼만 종류를 받는다. 종류가 없거나
// 하나도 들지 않으면 몫은 0이다(모델 단계 예산을 이유 없이 줄이지 않는다).
func ComputeE3ConfirmationShare(types int, runRequests uint64, run, perRequest time.Duration) E3ConfirmationShare {
	if types <= 0 {
		return E3ConfirmationShare{}
	}
	if perRequest <= 0 || run <= 0 || runRequests == 0 {
		return E3ConfirmationShare{BudgetSkipped: types}
	}
	requestCap := runRequests / E3ConfirmationShareDivisor
	timeCap := run / E3ConfirmationShareDivisor
	admitted := 0
	for k := 1; k <= types; k++ {
		requests := uint64(E3ConfirmationFixedRequests + E3ConfirmationRequestsPerType*k)
		if requests > requestCap || time.Duration(requests) > timeCap/perRequest {
			break
		}
		admitted = k
	}
	if admitted == 0 {
		return E3ConfirmationShare{BudgetSkipped: types}
	}
	requests := uint64(E3ConfirmationFixedRequests + E3ConfirmationRequestsPerType*admitted)
	return E3ConfirmationShare{Requests: requests, Timeout: time.Duration(requests) * perRequest, Admitted: admitted, BudgetSkipped: types - admitted}
}
