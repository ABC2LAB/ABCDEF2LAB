package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

type releaseEndpoint struct {
	requests []diagnosisprep.Request
	reply    func(diagnosisprep.Request) (diagnosisprep.Response, error)
}

func (e *releaseEndpoint) Call(_ context.Context, r diagnosisprep.Request) (diagnosisprep.Response, error) {
	e.requests = append(e.requests, r)
	return e.reply(r)
}

func releaseCall(t *testing.T, e *releaseEndpoint, method, body, contentType, owner string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, productReleasePath, strings.NewReader(body))
	if body == "" {
		r = httptest.NewRequest(method, productReleasePath, nil)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	productReleaseHTTP(w, r, owner, e)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestProductReleaseRequiresOperator(t *testing.T) {
	e := &releaseEndpoint{reply: func(diagnosisprep.Request) (diagnosisprep.Response, error) {
		t.Fatal("운영자가 아닌데 준비 서비스에 보냄")
		return diagnosisprep.Response{}, nil
	}}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		code, out := releaseCall(t, e, method, `{"job_id":"prep-a","expected_revision":3,"reason":"x"}`, "application/json", "viewer")
		if code != 403 || out["error"] != "operator_required" {
			t.Fatalf("%s: 운영자 아님이 거절되지 않음: %d %v", method, code, out)
		}
	}
}

func TestProductReleaseStatusAndRefusals(t *testing.T) {
	now := time.Now().UTC()
	e := &releaseEndpoint{reply: func(r diagnosisprep.Request) (diagnosisprep.Response, error) {
		out := diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: r.RequestID}
		switch r.Command {
		case diagnosisprep.ReleaseStatus:
			out.Release = &diagnosisprep.ReleaseView{QuietSeconds: 1200, Now: now, Refusal: diagnosisprep.ReleaseNotStuck}
		case diagnosisprep.Release:
			out.Error = "release_" + diagnosisprep.ReleaseNotQuiet
		}
		return out, nil
	}}
	code, out := releaseCall(t, e, http.MethodGet, "", "", "ARTEX")
	release, _ := out["release"].(map[string]any)
	if code != 200 || release["refusal"] != diagnosisprep.ReleaseNotStuck || e.requests[0].Command != diagnosisprep.ReleaseStatus || e.requests[0].Principal != "ARTEX" {
		t.Fatalf("상태 조회 실패: %d %v", code, out)
	}
	code, out = releaseCall(t, e, http.MethodPost, `{"job_id":"prep-a","expected_revision":3,"reason":"  응답 없음  "}`, "application/json", "ARTEX")
	sent := e.requests[1]
	if code != 409 || out["error"] != "release_not_quiet" || sent.Command != diagnosisprep.Release || sent.JobID != "prep-a" || sent.ExpectedRevision != 3 || sent.Release.Reason != "응답 없음" {
		t.Fatalf("거절 전달 실패: %d %v %#v", code, out, sent)
	}
	if !strings.HasPrefix(sent.RequestID, "product-release-") || sent.RequestID == e.requests[0].RequestID {
		t.Fatalf("감사 기록용 요청 ID가 요청마다 다르지 않음: %q", sent.RequestID)
	}
}

func TestProductReleaseRejectsMalformedAndUnavailable(t *testing.T) {
	e := &releaseEndpoint{reply: func(diagnosisprep.Request) (diagnosisprep.Response, error) {
		return diagnosisprep.Response{}, errors.New("끊김")
	}}
	for _, tc := range []struct{ body, contentType string }{
		{`{"job_id":"prep-a","expected_revision":3,"reason":""}`, "application/json"},
		{`{"job_id":"prep-a","expected_revision":3,"reason":"x","extra":1}`, "application/json"},
		{`{"job_id":"prep-a","expected_revision":0,"reason":"x"}`, "application/json"},
		{`{"job_id":"prep-a","expected_revision":3,"reason":"x"}`, "text/plain"},
		{`{"job_id":"prep-a","expected_revision":3,"reason":"` + strings.Repeat("가", 200) + `"}`, "application/json"},
	} {
		if code, _ := releaseCall(t, e, http.MethodPost, tc.body, tc.contentType, "ARTEX"); code != 400 {
			t.Fatalf("잘못된 요청을 받음(%d): %s", code, tc.body)
		}
	}
	if len(e.requests) != 0 {
		t.Fatal("잘못된 요청을 준비 서비스에 보냄")
	}
	if code, out := releaseCall(t, e, http.MethodPost, `{"job_id":"prep-a","expected_revision":3,"reason":"x"}`, "application/json", "ARTEX"); code != 503 || out["error"] != "preparation_service_unavailable" {
		t.Fatalf("준비 서비스 끊김이 503이 아님: %d %v", code, out)
	}
	if code, _ := releaseCall(t, e, http.MethodDelete, "", "", "ARTEX"); code != 405 {
		t.Fatalf("허용 안 된 메서드: %d", code)
	}
}
