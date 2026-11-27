package e2proxy

import (
	"bytes"
)

// AddProtectedPasswords는 인증 준비 호스트만 등록하는 비밀번호다. 이전 정책(보류·mask_spans_v1)에서는 응답 보류·구간
// 가림·모델 출력 가림에 쓴다. model_visible_v1(제품)은 비밀번호를 모델에 주고 저장 기록에서도 가리지 않으므로(절대규칙 2,
// 2026-11-26 DLP 폐기) 등록할 곳이 없다: 값을 받기만 하고 남기지 않는다(그래서 짧은 비밀번호 계정도 결속된다).
func (e *Execution) AddProtectedPasswords(values ...[]byte) error {
	if e == nil {
		return ErrConfig
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(values) == 0 || len(values) > maxRedactValues {
		return ErrConfig
	}
	if e.cfg.PasswordResponse == PasswordResponseModelVisible {
		return nil
	}
	passwords := append([][]byte(nil), e.passwords...)

	contains := func(all [][]byte, value []byte) bool {
		for _, old := range all {
			if bytes.Equal(old, value) {
				return true
			}
		}
		return false
	}
	for _, value := range values {
		if !ValidProtectedPassword(value) {
			return ErrConfig
		}
		if !contains(passwords, value) {
			passwords = append(passwords, bytes.Clone(value))
		}
	}
	if len(passwords) > maxRedactValues || !validResponseLimits(e.cfg.Limits, append(append([][]byte(nil), e.secrets...), passwords...)) {
		return ErrConfig
	}
	if len(passwords) != len(e.passwords) {
		e.passwords = passwords
		e.secretsGen++
	}
	return nil
}
