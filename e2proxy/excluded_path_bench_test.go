package e2proxy

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/internal/pathscope"
)

func excludedBenchPatterns(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("/read/area%02d/*/private", i)
	}
	return out
}

// 요청당 판정 시간: 제외 경로 0개·5개·32개(상한)에서 제외에 걸리지 않는 요청의 E② 판정(규칙 찾기·헤더·제외 검사·경계·
// 결과 기록)을 잰다. 경계가 마지막에 막아 네트워크 송신 없이 판정만 잰다(#627 동찬 조건 4).
func BenchmarkAdmissionDecisionWithExclusions(b *testing.B) {
	for _, n := range []int{0, 5, pathscope.MaxExcluded} {
		b.Run(fmt.Sprintf("excluded=%d", n), func(b *testing.B) {
			cfg := coreConfig(b, "https://owned.test", &coreRecorder{}, 1000)
			cfg.Rules[0].PathPrefix = "/read"
			cfg.ExcludedPaths = excludedBenchPatterns(n)
			cfg.RequestBoundary = &denyingBoundary{}
			cfg.Limits.MaxDenials = 1 << 40
			_, c := coreOpen(b, cfg)
			req := coreRequest(b, "GET", "https://owned.test/read/area07/x/public/"+strings.Repeat("seg/", 8)+"item")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := c.Do(b.Context(), req); err == nil {
					b.Fatal("sent")
				}
			}
		})
	}
}

// 제외 검사만(한 번 푼 경로 + 패턴 대조)의 시간이다.
func BenchmarkExclusionCheckOnly(b *testing.B) {
	req := coreRequest(b, "GET", "https://owned.test/read/area07/x/public/"+strings.Repeat("seg/", 8)+"item")
	for _, n := range []int{0, 5, pathscope.MaxExcluded} {
		patterns := excludedBenchPatterns(n)
		b.Run(fmt.Sprintf("excluded=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				p, ok := requestPath(req.URL)
				if !ok || pathscope.ExcludedAny(p, patterns) {
					b.Fatal("excluded")
				}
			}
		})
	}
}
