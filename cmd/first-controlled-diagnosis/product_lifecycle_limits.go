package main

// C34 결정(#566): 기본값은 유지하고 설치 조직이 고를 수 있는 코드 상한만 확대한다.
const (
	productMaxInstallHours  = 8760
	productMaxDelegatedRuns = 100000
	productMaxActiveTargets = 1024
)

// 설치 입력에서 생략했을 때의 기본값이다(2026-11-23 사용자 결정). 설치 수명은 30일, 위임 실행은 외부 모델 설치도 점검을
// 여러 번 돌릴 수 있게 100회다. 설치 입력 valid_for_hours·max_runs로 위 상한 안에서 바꾼다.
const (
	productDefaultInstallHours  = 30 * 24
	productDefaultDelegatedRuns = 100
)
