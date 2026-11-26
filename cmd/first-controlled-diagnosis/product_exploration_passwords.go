package main

import (
	"encoding/json"

	"github.com/Autumn-27/artex/agent"
)

// explorationCredentials는 이 실행의 결속 계정마다 실행 자료의 자격 사본에서 라벨·아이디·비밀번호를 꺼내 탐색 에이전트 지침에
// 넣을 값으로 돌려준다(사용자 결정 2026-11-26: 탐색에서도 모델이 브라우저로 직접 로그인한다, CLAUDE.md 절대규칙 1·2). 진단 단계
// (E②)와 같은 자격 사본을 쓴다. 기록에서 비밀번호를 가리지 않으므로(절대규칙 2, 2026-11-26 DLP 폐기) 짧은 비밀번호 계정도 넣는다. 결속 계정의 자격 사본이 없거나 읽을 수 없으면 오류다(어느 계정을 넣었는지 모르는 채로 열지
// 않는다). 결속 계정이 없으면 빈 목록이다.
func (l *urlLoadedProfile) explorationCredentials() ([]agent.E2HTTPAccountCredential, error) {
	out := []agent.E2HTTPAccountCredential{}
	if l.policy.Auth == nil {
		return out, nil
	}
	for _, account := range l.policy.Auth.Accounts {
		raw, ok := l.materials[account.Credential.File]
		var cred struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !ok || json.Unmarshal(raw, &cred) != nil || cred.Username == "" || cred.Password == "" {
			return nil, errPreparation
		}
		out = append(out, agent.E2HTTPAccountCredential{Label: account.Label, Username: cred.Username, Password: cred.Password})
	}
	return out, nil
}
