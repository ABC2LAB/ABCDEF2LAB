package e2proxy

import (
	"reflect"
	"slices"
)

// EqualCallResults는 관측·기술 slice를 포함한 모든 신뢰 필드를 정확히 대조한다.
func EqualCallResults(left, right CallResult) bool {
	return reflect.DeepEqual(left, right)
}

// CloneCallResult는 보고서 투영이 추가된 뒤에도 보관된 실행 증거와 반환값의 별칭을 끊는다.
func CloneCallResult(in CallResult) CallResult {
	out := in
	out.Observations = slices.Clone(in.Observations)
	for i := range out.Observations {
		out.Observations[i].Technologies = slices.Clone(in.Observations[i].Technologies)
	}
	return out
}
