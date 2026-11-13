package main

import "github.com/Autumn-27/artex/server"

// httpHostConfig는 일반 HTTP launch의 실행 host 설정이다. Plan당 상한도 여기서 실행 Runtime으로 넘어간다.
func (c launchConfig) httpHostConfig() (server.FirstDiagnosisHTTPHostConfig, bool) {
	limits, ok := c.plannerLimits()
	if c.HTTP == nil || !ok {
		return server.FirstDiagnosisHTTPHostConfig{}, false
	}
	return server.FirstDiagnosisHTTPHostConfig{PolicyFile: c.HTTP.PolicyFile, AuthorityFile: c.HTTP.AuthorityFile, AuthorityRoot: c.HTTP.AuthorityRoot, PlannerLimits: limits, ExternalTestAccounts: c.HTTP.ExternalTestAccounts}, true
}

func (c launchConfig) modelOptions() server.FirstDiagnosisModelOptions {
	// Plan당 상한은 parseLaunchConfig가 이미 검사했다. 범위 밖 값은 기본값으로 바뀌지 않고 준비 서버 생성이 거부한다.
	limits, _ := c.plannerLimits()
	return server.FirstDiagnosisModelOptions{Mode: c.ModelExecutionMode, LocalManifestPath: c.LocalLLMManifestPath, OpenRouterManifestPath: c.OpenRouterManifestPath, PlannerLimits: limits}
}
