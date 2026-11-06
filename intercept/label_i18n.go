package intercept

// 가로채기 판정 액션 라벨(allow/deny/ask). 판정 메시지에 쓰인다. enum 키와 소스 마커("[모델]" 표시는
// 아직 "[模型]" 저장값을 쓴다)는 안정 식별자라 번역하지 않는다.
var judgeActionLabels = map[string]string{"allow": "허용", "deny": "차단", "ask": "확인 요청"}
