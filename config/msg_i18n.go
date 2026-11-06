package config

// trCfg 와 T 는 운영자 메시지를 그대로 돌려준다. 사람이 보는 글은 한국어 하나이고 번역 카탈로그를 두지 않는다.
// 호출부를 지우는 일은 별도 PR로 한다(docs/engine-language-migration.md 8단계).
func trCfg(ko string) string { return ko }

// T returns an operator-facing console log template unchanged.
func T(ko string) string { return ko }
