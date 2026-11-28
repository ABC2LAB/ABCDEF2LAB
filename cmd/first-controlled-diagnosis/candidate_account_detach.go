package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/Autumn-27/artex/e2proxy"
)

// detachAccounts는 익명으로 도는 후속 검증 실행(익명 후보의 선택 검증 #950 · 저장 후보 검증 active/v1)에서 결속 계정을 뗀다.
// 9칸-1부터 자격만 있어도 대상 계정을 결속하므로, 계정이 있는 대상의 익명 후보도 결속된 채로 읽힌다. 자격 사본을 지우고
// (메모리 값을 0으로 · 발급 목록에서 뺌) 정책 인증 칸 · 실행 계정 · 외부 계정 동의를 끄고 시험 계정 헤더 모드를 푼다. 돌려준
// 이름은 뗀 자격 사본이다(발급 뒤 권한 디렉터리에 없는지 anonymousIssueClean이 본다). 떼고도 그 대상의 자격 사본이 발급
// 목록에 남으면 오류다(닫힌 쪽: 시작하지 않는다).
func (l *urlLoadedProfile) detachAccounts() ([]string, error) {
	var names []string
	if l.policy.Auth != nil {
		for _, account := range l.policy.Auth.Accounts {
			names = append(names, account.Credential.File)
			if raw := l.materials[account.Credential.File]; raw != nil {
				clear(raw)
			}
			delete(l.materials, account.Credential.File)
		}
	}
	l.policy.Auth = nil
	l.productAccounts = nil
	l.policy.HeaderMode = e2proxy.HeaderPreserve
	l.externalTestAccounts = false
	if l.productTarget != nil {
		prefix := bindingCredentialTargetPrefix(l.productTarget.Target.ID)
		for name := range l.materials {
			if strings.HasPrefix(name, prefix) {
				return names, errPreparation
			}
		}
	}
	return names, nil
}

// anonymousIssueClean은 계정을 뗀 실행의 발급 결과에 자격이 따라가지 않았는지 본다: 외부 계정 동의가 꺼졌고, 뗀 자격 사본이
// 권한 디렉터리에 없다. 하나라도 아니면 거짓이다(시작하지 않는다).
func anonymousIssueClean(launch launchConfig, detached []string) bool {
	if launch.HTTP == nil || launch.HTTP.AuthorityRoot == "" || launch.HTTP.ExternalTestAccounts {
		return false
	}
	for _, name := range detached {
		if name == "" || filepath.Base(name) != name {
			return false
		}
		if _, err := os.Lstat(filepath.Join(launch.HTTP.AuthorityRoot, name)); !os.IsNotExist(err) {
			return false
		}
	}
	return true
}
