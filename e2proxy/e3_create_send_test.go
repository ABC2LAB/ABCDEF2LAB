package e2proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/oracle"
)

func e3CreateFixture(t *testing.T, target string, spec e3contract.CreateSpec, objectID string) (*Execution, E3CreateInput, *coreRecorder) {
	t.Helper()
	recorder := &coreRecorder{}
	cfg := coreConfig(t, target, recorder, 3)
	cfg.AuditVersion = JournalVersionV3
	cfg.E3Verifier = e3IdentityVerifier{}
	cfg.E3ConfirmationRequests = 3
	cfg.E3WriteConsent = true
	cfg.E3Resettable = true
	cfg.Rules = append(cfg.Rules,
		Rule{Ref: "create-worker", Origin: target, ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: "/allowed", PathScopeRef: "create-worker-path", Methods: []string{"POST"}},
		Rule{Ref: "create-program", Purpose: PurposeE3Create, Origin: target, ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: spec.Path, PathScopeRef: "create-program-path", Methods: []string{spec.Method}},
		Rule{Ref: "read-worker", Origin: target, ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: "/allowed", PathScopeRef: "read-worker-path", Methods: []string{"GET"}},
		Rule{Ref: "read-program", Purpose: PurposeE3Read, Origin: target, ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: "/allowed/books/{id}", PathScopeRef: "read-program-path", Methods: []string{"GET"}})
	e, err := NewExecution(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := e3contract.NewMarker(bytes.NewReader(bytes.Repeat([]byte{2}, 128)))
	if err != nil {
		t.Fatal(err)
	}
	value := marker.Reveal()
	if err := e.RegisterProgramMarker(value); err != nil {
		t.Fatal(err)
	}
	clear(value)
	body, err := e3contract.AssembleBody(spec, marker, objectID)
	if err != nil {
		t.Fatal(err)
	}
	in := E3CreateInput{Binding: e3contract.RunBinding{JobRef: "job-1", RunRef: "run", TaskRef: "task",
		E2PolicyRef: "policy", GenerationRef: "generation-1", InstanceRef: "instance-1",
		Policy: e3contract.PolicySource{Kind: e3contract.PolicyProductDeclaration, Ref: "declaration-1",
			DigestSHA256: strings.Repeat("a", 64), TargetApprovalRevision: "approval-1"}},
		ResourceType: "book", AccountLabel: "owner-a", Spec: spec, Marker: marker, Body: body, ObjectID: objectID,
		Accounts:  &e3IdentityAccounts{ref: "acct-1", header: http.Header{"Authorization": {"Bearer fixture-session"}}, current: true},
		DigestKey: bytes.Repeat([]byte{7}, 32), Timeout: time.Second}
	return e, in, recorder
}

func TestE3CreateSendsOneMarkedRequestAndKeepsIDPrivate(t *testing.T) {
	for _, tc := range []struct {
		name, idPath, idField, objectID, response string
	}{
		{name: "response-id", idPath: "id", response: `{"id":"book-a"}`},
		{name: "request-id", idField: "title", objectID: "e3id-0123456789abcdef0123456789abcdef", response: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil || r.Method != "POST" || r.URL.Path != "/allowed/books" ||
					r.Header.Get("Authorization") != "Bearer fixture-session" ||
					!bytes.Contains(body, []byte("marker")) || r.Header.Get(AttemptHeader) == "" {
					t.Error("생성 요청의 방법·경로·계정·본문이 다름")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()
			spec := e3contract.CreateSpec{Path: "/allowed/books", Method: "POST", Kind: e3contract.BodyJSON,
				BodyTemplate: `{}`, MarkerField: "marker", IDPath: tc.idPath, IDField: tc.idField}
			e, in, recorder := e3CreateFixture(t, server.URL, spec, tc.objectID)
			event, err := e.SendE3Create(t.Context(), in)
			if err != nil || requests.Load() != 1 || !event.AuditComplete || !event.ResponseComplete ||
				event.E3Audit == nil || event.E3Audit.Normal != oracle.StateYes || event.E3Audit.ObjectIDDigest == "" {
				t.Fatalf("생성 결과 누락: err=%v requests=%d event=%+v", err, requests.Load(), event.Receipt)
			}
			ref := e3contract.CallRef{CallRef: event.CallRef, AttemptID: event.AttemptID, Sequence: event.Sequence}
			if e.e3Created[ref] == "" {
				t.Fatal("조회용 객체 ID가 실행 메모리에 남지 않음")
			}
			for _, row := range recorder.events {
				if !validJournalRecordEvent(row, JournalVersionV3) {
					t.Fatalf("잘못된 v3 생성 사건: %s", row.Stage)
				}
				raw, err := json.Marshal(row)
				if err != nil || bytes.Contains(raw, []byte("fixture-session")) ||
					bytes.Contains(raw, in.Marker.Reveal()) || bytes.Contains(raw, []byte(e.e3Created[ref])) {
					t.Fatal("생성 사건에 세션·표식·객체 ID 원문이 남음")
				}
			}
		})
	}
}

func TestE3CreateRequiresWriteConsentAndReset(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	spec := e3contract.CreateSpec{Path: "/allowed/books", Method: "POST", Kind: e3contract.BodyJSON,
		BodyTemplate: `{}`, MarkerField: "marker", IDPath: "id"}
	for _, change := range []func(*Execution){
		func(e *Execution) { e.cfg.E3WriteConsent = false },
		func(e *Execution) { e.cfg.E3Resettable = false },
		func(e *Execution) { e.cfg.E3ConfirmationRequests = 0 },
	} {
		e, in, rec := e3CreateFixture(t, server.URL, spec, "")
		change(e)
		if _, err := e.SendE3Create(t.Context(), in); err == nil || len(rec.events) != 0 {
			t.Fatal("동의·리셋·확정 몫 없이 생성 허용")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("거부한 생성이 대상에 도착")
	}
}

func TestE3CreateUnknownResultDoesNotRetryOrIssueID(t *testing.T) {
	var writes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	spec := e3contract.CreateSpec{Path: "/allowed/books", Method: http.MethodPost, Kind: e3contract.BodyJSON,
		BodyTemplate: `{}`, MarkerField: "marker", IDPath: "id"}
	e, in, _ := e3CreateFixture(t, server.URL, spec, "")
	event, _ := e.SendE3Create(t.Context(), in)
	if writes.Load() != 1 || event.Stage != "result" || event.ResponseComplete || event.E3Audit != nil && event.E3Audit.ObjectIDDigest != "" ||
		len(e.e3Created) != 0 {
		t.Fatalf("결과 불명 생성을 재시도하거나 ID를 발급: writes=%d", writes.Load())
	}
}
