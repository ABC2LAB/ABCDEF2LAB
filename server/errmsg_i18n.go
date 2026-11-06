package server

import "github.com/Autumn-27/artex/config"

// trMsg 는 메시지를 그대로 돌려준다. 사람이 보는 글은 한국어 하나이고 번역 카탈로그를 두지 않는다.
// 호출부를 지우는 일은 별도 PR로 한다(docs/engine-language-migration.md 8단계).
func trMsg(ko string) string { return ko }

// logT returns an operator-facing console log template unchanged (config.T).
func logT(ko string) string { return config.T(ko) }

// loadOutputLanguage resolves the user-facing output language from the source of
// truth: the runtime setting (settings table) over config.Language() (ARTEX_LANG >
// config file > ko). Call it only to (re)fill the cache; it does a DB query and a
// config file read.
func (s *Server) loadOutputLanguage() string {
	if s.m != nil && s.m.pg != nil {
		if v, ok, _ := s.m.pg.GetSetting(settingOutputLanguage); ok {
			if nv := config.NormalizeLanguage(v); nv != "" {
				return nv
			}
		}
	}
	return config.Language()
}

// outputLanguage returns the cached output language, resolving it on first use.
func (s *Server) outputLanguage() string {
	if v, ok := s.outputLang.Load().(string); ok && v != "" {
		return v
	}
	v := s.loadOutputLanguage()
	s.outputLang.Store(v)
	return v
}
