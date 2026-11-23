package e2proxy

import (
	"bytes"
)

// AddProtectedPasswords는 인증 준비 호스트만 등록하는 비밀번호다. 출력 가림·세션 유출 검사는 항상 유지한다.
// 명시적으로 결속한 mask_spans_v1에서만 응답 구간 가림을 쓰며 일반 보호 값의 보류 동작은 바꾸지 않는다.
func (e *Execution) AddProtectedPasswords(values ...[]byte) error {
	if e == nil {
		return ErrConfig
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(values) == 0 || len(values) > maxRedactValues {
		return ErrConfig
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
