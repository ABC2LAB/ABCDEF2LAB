package server

import "testing"

// TestReporterSeedLabelsLocalized 는 reporter 에이전트의 표시 전용 라벨이 한국어로 유지되는지
// 고정한다. 이 두 문자열은 agentDTO 로 UI 에만 렌더되고 프롬프트나 에이전트 선택에는 들어가지 않는다.
// 본문(agent.ReporterDefaultPrompt)과 트리거 문구(reporterToolCallMessage)는 모델 입력이라 영어이고
// 이 시험 대상이 아니다. 한국어 판정은 assertKoreanError 를 재사용한다.
func TestReporterSeedLabelsLocalized(t *testing.T) {
	assertKoreanError(t, "reporter_name", reporterAgentName)
	assertKoreanError(t, "reporter_description", reporterAgentDescription)
}
