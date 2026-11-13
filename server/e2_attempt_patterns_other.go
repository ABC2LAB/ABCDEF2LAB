//go:build !linux

package server

import "github.com/Autumn-27/artex/e2proxy"

// 제품 실행은 linux에서만 돈다. 다른 운영체제에서는 표시용 패턴을 남기지 않는다.
func productAttemptPatterns(string, func() func(string) bool) e2proxy.AttemptPathObserver { return nil }
