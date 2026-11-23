//go:build linux

package llmpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

// 자동 넘김 모드에서는 호출마다 실제로 응답한 제공자가 진단 기록에 남는다(tag의 digest). 고정 모드와 실패한 호출은 승인
// tag 그대로다.
func TestOpenRouterDiagnosticRecordsReturnedProvider(t *testing.T) {
	ref := func(slug string) string {
		sum := sha256.Sum256([]byte(slug))
		return hex.EncodeToString(sum[:])
	}
	second := strings.Replace(openRouterReply, `"provider":"Fixture"`, `"provider":"Second"`, 1)
	for name, tc := range map[string]struct {
		routing string
		replies []string
		want    []string
	}{
		"fallback": {fallbackRoutingJSON, []string{openRouterReply, second}, []string{ref("fixture/endpoint"), ref("second/fp8")}},
		"pinned":   {pinnedRoutingJSON, []string{openRouterReply, openRouterReply}, []string{ref("fixture/endpoint"), ref("fixture/endpoint")}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, log := routerDiagnosticFixture(t)
			p := samplingPolicy(t, routingManifest(t, 4, tc.routing))
			i := 0
			c := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) {
				reply := tc.replies[i]
				i++
				return openRouterResponse(reply), nil
			}))
			for range tc.replies {
				resp, err := c.Do(openRouterRequest().WithContext(ctx))
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
			}
			var finished []string
			for _, e := range routerDiagnosticEvents(t, log) {
				switch e.Event {
				case "model_request_started":
					// 보내기 전에는 누가 응답할지 모르므로 승인 tag(목록의 첫 항목)다.
					if e.ProviderRef != ref("fixture/endpoint") {
						t.Fatal("시작 사건의 provider_ref가 승인 tag가 아님")
					}
				case "model_request_finished":
					finished = append(finished, e.ProviderRef)
				}
			}
			if len(finished) != len(tc.want) {
				t.Fatalf("끝 사건 %d개", len(finished))
			}
			for n, got := range finished {
				if got != tc.want[n] {
					t.Fatalf("호출 %d의 provider_ref가 실제 제공자가 아님", n)
				}
			}
		})
	}
}
