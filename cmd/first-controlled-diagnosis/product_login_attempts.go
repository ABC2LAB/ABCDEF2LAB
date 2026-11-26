package main

// 모델 직접 로그인 회차의 로그인 시도 판정 기본값이다(docs/contracts/model-login-attempts.md). 제품 발급이 E② 정책의 인증
// 칸에 싣는다.
const (
	// productLoginConsecutiveFailures는 같은 아이디로 연달아 실패한 로그인 상한이다(회차당, 탐색·진단 공통, 사용자 결정
	// 2026-11-26, CLAUDE.md 절대규칙 1·3).
	productLoginConsecutiveFailures = 3
	// productLoginContrastProbes는 로그인 시도마다 보내는 프로그램 대조 요청 URL 수 상한이다(계약 §4).
	productLoginContrastProbes = 3
	// productLoginTokenMinBytes는 응답 값을 세션 토큰 후보로 볼 최소 길이다(계약 §3).
	productLoginTokenMinBytes = 16
)
