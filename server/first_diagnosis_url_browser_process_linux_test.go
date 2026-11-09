//go:build linux

package server

import "testing"

// 실제 브라우저 수용은 공통 서버 fixture 위에 UI 상호작용만 추가한다.
func TestFirstDiagnosisURLBrowserIndependentProcessesOwnedPostgres(t *testing.T) {
	runURLProcessAcceptance(t, true)
}
