package e2proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// journalCheckingRecorder는 실제 저널과 같은 행 규칙(validJournalEvent)으로 사건을 받는다.
type journalCheckingRecorder struct{ coreRecorder }

func (r *journalCheckingRecorder) Record(ctx context.Context, e Event) error {
	if !validJournalEvent(e, JournalVersion) {
		return errors.New("invalid journal event")
	}
	return r.coreRecorder.Record(ctx, e)
}

// 대상이 HTTP 범위 밖 상태 코드(600~999)로 답해도 그 요청만 결과 불명으로 끝나고, 감사 실패로 새 송신이 멈추지 않는다
// (요청 하나의 실패는 그 요청만 실패). 다음 요청은 그대로 나간다.
func TestNonstandardStatusIsUnknownNotAuditFailure(t *testing.T) {
	code := 999
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte("owned"))
	}))
	defer target.Close()
	rec := &journalCheckingRecorder{}
	e, c := coreOpen(t, coreConfig(t, target.URL, rec, 4))
	resp, r, err := c.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err == nil || resp != nil || errors.Is(err, ErrAudit) {
		t.Fatalf("nonstandard status: resp=%v err=%v", resp != nil, err)
	}
	if r.Reason != Unknown || !r.ForwardAttempted || !r.RequestWritten || r.ResponseReceived || r.StatusCode != 0 || !r.AuditComplete {
		t.Fatalf("nonstandard status receipt: %+v", r)
	}
	if view := e.Snapshot(); view.State != "active" || view.Counts.Unknown != 1 {
		t.Fatalf("nonstandard status stopped the execution: %+v", view)
	}
	code = http.StatusOK
	resp, r, err = c.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err != nil || resp == nil || r.StatusCode != http.StatusOK || r.Reason != Complete {
		t.Fatalf("next request after a nonstandard status: %+v err=%v", r, err)
	}
	resp.Body.Close()
}
