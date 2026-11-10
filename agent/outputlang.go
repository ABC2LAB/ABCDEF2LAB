package agent

import "context"

// 표시 언어는 한국어로 고정한다. 모델이 읽는 지시문 자체는 영어다.
const defaultOutputLang = "ko"

// 기존 호출부와의 호환 목록이며 런타임 언어 선택에는 사용하지 않는다.
var SupportedOutputLangs = []string{"ko"}

// 과거 훅이 다른 코드를 반환해도 표시 언어를 바꾸지 않는다.
// 훅과 호출부 제거는 API·웹 변경과 분리해 진행한다.
func resolveOutputLang(context.Context) string { return defaultOutputLang }
