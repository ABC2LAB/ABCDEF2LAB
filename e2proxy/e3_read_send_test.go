package e2proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/oracle"
)

type e3ReadVerifier struct {
	event Event
	err   error
}

func (v e3ReadVerifier) Verified(_ context.Context, _ e3contract.RunBinding, _ e3contract.CallRef) (Event, error) {
	return v.event, v.err
}

type e3ReadAccounts struct {
	owner *e3IdentityAccounts
	cross *e3IdentityAccounts
}

func (a *e3ReadAccounts) selected(label string) *e3IdentityAccounts {
	switch label {
	case "owner-a":
		return a.owner
	case "cross-b":
		return a.cross
	default:
		return nil
	}
}

func (a *e3ReadAccounts) AccountBindingRef(runRef, label string) (string, bool) {
	account := a.selected(label)
	if account == nil || runRef != "run" {
		return "", false
	}
	account.mu.RLock()
	defer account.mu.RUnlock()
	return account.ref, account.current
}

func (a *e3ReadAccounts) BoundSession(runRef, label, ref string) (http.Header, error) {
	account := a.selected(label)
	if account == nil || runRef != "run" {
		return nil, ErrDenied
	}
	account.mu.RLock()
	defer account.mu.RUnlock()
	if !account.current || account.ref != ref {
		return nil, ErrDenied
	}
	return account.header.Clone(), nil
}

func e3ReadFixture(t *testing.T, serveRead func(http.ResponseWriter, *http.Request)) (*Execution, E3ReadInput, *coreRecorder, *atomic.Int64) {
	t.Helper()
	reads := new(atomic.Int64)
	var marker string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"book-a"}`)
		case http.MethodGet:
			reads.Add(1)
			if r.URL.Path != "/allowed/books/book-a" || r.Header.Get(AttemptHeader) == "" ||
				strings.Contains(r.URL.String(), marker) || r.Header.Get("Authorization") == "" {
				t.Error("조회 요청의 경로·시도 참조·세션이 잘못됨")
			}
			serveRead(w, r)
		default:
			t.Errorf("예상하지 않은 메서드: %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)
	spec := e3contract.CreateSpec{Path: "/allowed/books", Method: http.MethodPost, Kind: e3contract.BodyJSON,
		BodyTemplate: `{}`, MarkerField: "marker", IDPath: "id"}
	e, create, recorder := e3CreateFixture(t, server.URL, spec, "")
	marker = string(create.Marker.Reveal())
	created, err := e.SendE3Create(t.Context(), create)
	if err != nil || !created.ResponseComplete || created.E3Audit == nil || created.E3Audit.ObjectIDDigest == "" {
		t.Fatalf("생성 fixture 실패: %v", err)
	}
	e.cfg.E3Verifier = e3ReadVerifier{event: created}
	accounts := &e3ReadAccounts{owner: create.Accounts.(*e3IdentityAccounts),
		cross: &e3IdentityAccounts{ref: "acct-2", header: http.Header{"Authorization": {"Bearer fixture-cross-session"}}, current: true}}
	in := E3ReadInput{Binding: create.Binding, ResourceType: create.ResourceType, Role: e3contract.RoleOwner,
		AccountLabel: "owner-a", Spec: e3contract.ReadSpec{PathTemplate: "/allowed/books/{id}", DataPath: "data"},
		MarkerField: "marker", CreateRef: e3contract.CallRef{CallRef: created.CallRef, AttemptID: created.AttemptID,
			Sequence: created.Sequence}, ObjectID: "book-a", Marker: create.Marker, Accounts: accounts,
		DigestKey: create.DigestKey, Timeout: create.Timeout}
	return e, in, recorder, reads
}

func TestE3ReadSendsOwnerAndCrossWithCurrentSession(t *testing.T) {
	var marker string
	e, in, recorder, reads := e3ReadFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-session" &&
			r.Header.Get("Authorization") != "Bearer fixture-cross-session" {
			t.Error("현재 계정 세션이 적용되지 않음")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"marker":`+string(mustJSON(t, marker))+`}}`)
	})
	marker = string(in.Marker.Reveal())
	owner, err := e.SendE3Read(t.Context(), in)
	if err != nil || owner.E3Audit == nil || owner.E3Audit.Marker.Presence != e3contract.MarkerPresent ||
		owner.E3Audit.Normal != oracle.StateYes || !owner.ResponseComplete {
		t.Fatalf("소유자 조회 관찰 실패: err=%v, event=%+v", err, owner.Receipt)
	}
	in.Role, in.AccountLabel = e3contract.RoleCross, "cross-b"
	cross, err := e.SendE3Read(t.Context(), in)
	if err != nil || cross.E3Audit == nil || cross.E3Audit.Marker.Presence != e3contract.MarkerPresent ||
		cross.E3Audit.AccountBindingRef != "acct-2" || reads.Load() != 2 {
		t.Fatalf("교차 계정 조회 관찰 실패: err=%v reads=%d", err, reads.Load())
	}
	for _, row := range recorder.events {
		if !validJournalRecordEvent(row, JournalVersionV3) {
			t.Fatalf("잘못된 v3 사건: %s", row.Stage)
		}
		raw, err := json.Marshal(row)
		if err != nil || bytes.Contains(raw, []byte(marker)) || bytes.Contains(raw, []byte("fixture-session")) ||
			bytes.Contains(raw, []byte("fixture-cross-session")) || bytes.Contains(raw, []byte("book-a")) {
			t.Fatal("조회 감사에 표식·세션·객체 ID 원문이 남음")
		}
	}
}

func TestE3ReadRejectsUnverifiedCreateBeforeSending(t *testing.T) {
	e, in, _, reads := e3ReadFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("조회가 나가면 안 됨") })
	created := e.cfg.E3Verifier.(e3ReadVerifier).event
	for _, change := range []func(){
		func() { e.cfg.E3Verifier = e3ReadVerifier{err: errors.New("stale head")} },
		func() {
			created.E3Audit.ObjectIDDigest = strings.Repeat("0", 64)
			e.cfg.E3Verifier = e3ReadVerifier{event: created}
		},
	} {
		change()
		if _, err := e.SendE3Read(t.Context(), in); !errors.Is(err, e3contract.ErrJournalIntegrity) {
			t.Fatalf("검증 안 된 생성 사건을 허용: %v", err)
		}
	}
	if reads.Load() != 0 {
		t.Fatal("Host 검증 실패 뒤 조회 송신")
	}
}

func TestE3ReadRejectsWrongIDAndSessionBeforeSending(t *testing.T) {
	e, in, _, reads := e3ReadFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("조회가 나가면 안 됨") })
	in.ObjectID = "other-id"
	if _, err := e.SendE3Read(t.Context(), in); !errors.Is(err, ErrDenied) {
		t.Fatalf("다른 객체 ID를 허용: %v", err)
	}
	in.ObjectID = "book-a"
	in.Accounts.(*e3ReadAccounts).owner.replace("new-session-ref")
	if _, err := e.SendE3Read(t.Context(), in); !errors.Is(err, ErrDenied) {
		t.Fatalf("다른 세션 참조를 허용: %v", err)
	}
	if reads.Load() != 0 {
		t.Fatal("거부한 조회가 송신됨")
	}
}

func TestE3ReadDoesNotTurnMissingOrUnsupportedMarkerIntoEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, response string
		want                        e3contract.MarkerStatus
	}{
		{name: "absent", contentType: "application/json", response: `{"data":{"marker":"other"}}`,
			want: e3contract.MarkerComplete},
		{name: "unsupported", contentType: "text/plain", response: `marker`,
			want: e3contract.MarkerUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, in, _, reads := e3ReadFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = io.WriteString(w, tc.response)
			})
			event, err := e.SendE3Read(t.Context(), in)
			if err != nil || reads.Load() != 1 || event.E3Audit == nil ||
				event.E3Audit.Marker.Status != tc.want || event.E3Audit.Marker.Presence == e3contract.MarkerPresent {
				t.Fatalf("조회 표식 반례 실패: err=%v reads=%d", err, reads.Load())
			}
		})
	}
}

func TestE3ReadSharesConfirmationRequestCap(t *testing.T) {
	e, in, _, reads := e3ReadFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("예산 초과 조회가 나가면 안 됨") })
	e.cfg.E3ConfirmationRequests = 1 // 생성이 첫 몫을 썼다.
	event, err := e.SendE3Read(t.Context(), in)
	if err != nil || reads.Load() != 0 || event.E3Audit == nil || event.ForwardAttempted ||
		event.E3Audit.NotSentReason != e3contract.NotSentBudgetExhausted {
		t.Fatalf("확정 요청 몫 초과를 보내거나 기록하지 않음: err=%v reads=%d", err, reads.Load())
	}
}

func TestE3ReadHTTPDenialCannotBecomeNormalFromBody(t *testing.T) {
	e, in, _, reads := e3ReadFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"status":"owner","data":{"marker":"other"}}`)
	})
	in.Spec.StatusPath, in.Spec.NormalValue = "status", "owner"
	event, err := e.SendE3Read(t.Context(), in)
	if err != nil || reads.Load() != 1 || event.E3Audit == nil || event.E3Audit.Normal != oracle.StateNo {
		t.Fatalf("HTTP 거부를 본문 상태로 정상 처리: err=%v reads=%d", err, reads.Load())
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
