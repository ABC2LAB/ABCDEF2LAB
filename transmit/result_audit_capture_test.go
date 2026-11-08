package transmit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/transmit"
)

// 결과 기록이 실패한 응답은 감사되지 않았으므로 호출자에게 내용을 넘기지 않는다.
func TestHTTPTransmitterResultAuditFailureDropsCapture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("private-body")) }))
	defer server.Close()
	tx, err := transmit.NewHTTP(context.Background(), transportConfig(server.URL, &recorder{failAt: "result"}))
	if err != nil {
		t.Fatal(err)
	}
	capture, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if ok || receipt.Reason != transmit.AuditUnavailable {
		t.Fatalf("ok=%v reason=%q", ok, receipt.Reason)
	}
	if !reflect.DeepEqual(capture, idor.Capture{}) {
		t.Fatalf("unaudited capture returned: %+v", capture)
	}
}
