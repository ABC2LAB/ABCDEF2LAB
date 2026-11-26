package main

import (
	"encoding/json"
	"sync"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/dlp"
	"github.com/Autumn-27/artex/e2proxy"
)

// explorationPasswords는 이 회차에 결속한 시험 계정 비밀번호다. 탐색 활동 기록을 E② 저장 기록 가림과 같은 값·방식으로
// 정확값 가림하는 데만 쓴다(2026-11-24 사용자 결정, #875 발견 3). E② 인증 준비가 읽는 것과 같은 실행 자료(메모리의 자격
// 사본)에서 회차마다 만들고, 디스크·로그에 쓰지 않으며, 회차가 끝나면(clearMaterials) 지운다. 모델 입력은 가리지 않는다
// (11/23 규칙: 대상이 돌려준 내용은 모델에 갈 수 있다).
type explorationPasswords struct {
	mu     sync.Mutex
	values [][]byte
	closed bool
}

func (*explorationPasswords) String() string { return "explorationPasswords[protected]" }

// redact는 저장 기록에 거는 가림이다. 회차가 끝나 목록을 지운 뒤 들어온 기록은 무엇이 들었는지 모르므로 통째로 가린다.
func (p *explorationPasswords) redact(b []byte) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return []byte(dlp.Redacted)
	}
	return e2proxy.RedactRecordValues(b, p.values)
}

func (p *explorationPasswords) clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, v := range p.values {
		clear(v)
	}
	p.values, p.closed = nil, true
}

// explorationPasswords는 이 실행의 결속 계정마다 실행 자료의 자격 사본에서 비밀번호를 꺼낸다. E②가 받지 않는 값(짧음 등)은
// E②도 등록하지 않고 그 계정으로 로그인하지 않으므로 넣지 않는다. 결속 계정의 자격 사본이 없거나 읽을 수 없으면 가림 목록을
// 만들지 못한 것이라 nil·오류다(탐색을 가림 없이 열지 않는다). 한 실행에서 한 번 만들고 clearMaterials가 지운다.
func (l *urlLoadedProfile) explorationPasswords() (*explorationPasswords, error) {
	if l.runPasswords != nil {
		return l.runPasswords, nil
	}
	out := &explorationPasswords{}
	if l.policy.Auth != nil {
		for _, account := range l.policy.Auth.Accounts {
			raw, ok := l.materials[account.Credential.File]
			var cred struct {
				Password string `json:"password"`
			}
			if !ok || json.Unmarshal(raw, &cred) != nil || cred.Password == "" {
				out.clear()
				return nil, errPreparation
			}
			value := []byte(cred.Password)
			cred.Password = ""
			if !e2proxy.ValidProtectedPassword(value) {
				clear(value)
				continue
			}
			out.values = append(out.values, value)
		}
	}
	l.runPasswords = out
	return out, nil
}

// explorationCredentials는 이 실행의 결속 계정마다 실행 자료의 자격 사본에서 라벨·아이디·비밀번호를 꺼내 탐색 에이전트 지침에
// 넣을 값으로 돌려준다(사용자 결정 2026-11-26: 탐색에서도 모델이 브라우저로 직접 로그인한다, CLAUDE.md 절대규칙 1·2). 진단 단계
// (E②)와 같은 자격 사본을 쓴다. 결속 계정의 자격 사본이 없거나 읽을 수 없으면 오류다(어느 계정을 넣었는지 모르는 채로 열지
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
