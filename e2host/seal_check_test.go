package e2host

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2checkpoint"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/transmit"
)

// 근거 체크포인트와 봉인 포함 확인(E1, 2026-11-20 결정): Verified는 검증에 쓴 현재 head 체크포인트를 같이 돌려주고,
// 봉인 때 Host가 내준 체크포인트마다 VerifyJournalV3Extension(근거 → 봉인 head)으로 포함 여부를 확인해 남긴다.

// recordE3Call은 같은 결속으로 호출 하나(intent·result)를 기록하고 그 참조를 돌려준다.
func recordE3Call(t *testing.T, session *Session, scope transmit.Scope, call string, index uint64) e3contract.CallRef {
	t.Helper()
	_, intent, result := hostE3Events(scope)
	attempt := sha256.Sum256([]byte(call))
	intent.CallRef, result.CallRef = call, call
	intent.AttemptID, result.AttemptID = hex.EncodeToString(attempt[:16]), hex.EncodeToString(attempt[:16])
	intent.Index, result.Index = index, index+1
	if err := session.Record(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	if err := session.Record(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	return e3contract.CallRef{CallRef: call, AttemptID: result.AttemptID, Sequence: result.Sequence}
}

func currentHead(t *testing.T, host *Host, session *Session) e2checkpoint.Head {
	t.Helper()
	head, err := host.store.Head(t.Context(), e2checkpoint.Query{Epoch: host.epoch, Binding: session.run.binding})
	if err != nil {
		t.Fatal(err)
	}
	return head
}

// storedHead는 신뢰 저장소(DB)의 마감 head를 직접 읽는다. 봉인 뒤에는 Host가 실행을 놓아 저장소 조회 권한이 없다.
func storedHead(t *testing.T, pool *sql.DB, runRef string) (e2checkpoint.Phase, e2proxy.JournalCheckpoint) {
	t.Helper()
	var phase string
	var raw []byte
	if err := pool.QueryRowContext(t.Context(), `SELECT phase,checkpoint FROM e2_http_audit_heads WHERE run_ref=$1`, runRef).Scan(&phase, &raw); err != nil {
		t.Fatal(err)
	}
	var checkpoint e2proxy.JournalCheckpoint
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		t.Fatal(err)
	}
	return e2checkpoint.Phase(phase), checkpoint
}

// readSealRecord는 Host 볼륨의 마감 확인 기록을 읽는다.
func readSealRecord(t *testing.T, directory string) (string, SealResult) {
	t.Helper()
	raw, err := readSnapshot(filepath.Join(directory, sealChecksFile+".json"))
	if err != nil {
		t.Fatal("Host 볼륨 기록", err)
	}
	var recorded struct {
		Version string `json:"version"`
		SealResult
	}
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatal(err)
	}
	return recorded.Version, recorded.SealResult
}

func TestHostVerifiedAtCheckpointIsIncludedAfterSeal(t *testing.T) {
	host, pool, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	binding, _, _ := hostE3Events(scope)
	first := recordE3Call(t, session, scope, "call-first", 1)
	v1, err := session.VerifiedAt(t.Context(), binding, first)
	if err != nil || v1.Event.CallRef != "call-first" {
		t.Fatal("첫 근거 검증", err)
	}
	// 조건 1: 결과 사건과 같은 인증 스냅샷의 현재 head이고, 실행 범위가 그대로다.
	if head := currentHead(t, host, session); v1.Checkpoint != head.Checkpoint || v1.Checkpoint.Sequence != 2 ||
		v1.Checkpoint.Scope != scope || v1.Checkpoint.Version != e2proxy.JournalVersionV3 {
		t.Fatalf("근거 체크포인트가 검증 시점 head가 아님: %+v ≠ %+v", v1.Checkpoint, head.Checkpoint)
	}
	// head가 그대로면 같은 체크포인트이고, 확인 목록에 한 번만 남는다.
	if again, err := session.VerifiedAt(t.Context(), binding, first); err != nil || again.Checkpoint != v1.Checkpoint {
		t.Fatal("같은 head 재검증", err)
	}
	second := recordE3Call(t, session, scope, "call-second", 3)
	v2, err := session.VerifiedAt(t.Context(), binding, second)
	if err != nil || v2.Checkpoint == v1.Checkpoint || v2.Checkpoint.Sequence != 4 {
		t.Fatal("두 번째 근거 체크포인트", err, v2.Checkpoint)
	}
	// 앞 호출을 head가 바뀐 뒤 다시 물으면 새 head가 근거다.
	if later, err := session.VerifiedAt(t.Context(), binding, first); err != nil || later.Checkpoint != v2.Checkpoint {
		t.Fatal("바뀐 head에서 앞 호출 재검증", err)
	}
	result, err := session.SealChecked(t.Context())
	if err != nil || result == nil {
		t.Fatal("봉인", err)
	}
	// 조건 2: 기준은 신뢰 저장소가 인증한 최종 sealed head다.
	if phase, head := storedHead(t, pool, scope.RunRef); phase != e2checkpoint.Sealed || result.Phase != e2checkpoint.Sealed || result.Head != head {
		t.Fatalf("봉인 head: %s %+v %+v", phase, head, result)
	}
	want := []SealCheck{{Sequence: 2, MAC: v1.Checkpoint.MAC, Included: true}, {Sequence: 4, MAC: v2.Checkpoint.MAC, Included: true}}
	if len(result.Checks) != len(want) || result.Checks[0] != want[0] || result.Checks[1] != want[1] {
		t.Fatalf("포함 결과: %+v", result.Checks)
	}
	version, recorded := readSealRecord(t, session.run.directory)
	if version != sealChecksVersion || recorded.Phase != e2checkpoint.Sealed || recorded.Head != result.Head ||
		len(recorded.Checks) != 2 || recorded.Checks[0] != want[0] || recorded.Checks[1] != want[1] {
		t.Fatalf("Host 볼륨 기록 내용: %+v", recorded)
	}
	issued, err := readIssued(session.run.directory)
	if err != nil || len(issued) != 2 || issued[0] != v1.Checkpoint || issued[1] != v2.Checkpoint {
		t.Fatal("근거 목록 기록", err, issued)
	}
	// 봉인 결과 파일이 있어도 다음 기동 검사는 그대로 통과한다.
	if err := host.CheckRestoredHeads(t.Context()); err != nil {
		t.Fatal("봉인 결과 파일 뒤 기동 검사", err)
	}
}

func TestHostObservedAtOrdinaryResultIsIncludedAfterSeal(t *testing.T) {
	host, _, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	intent, result := hostSentEvents(scope)
	if err := session.Record(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	if err := session.Record(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	ref := ObservationRef{CallRef: result.CallRef, AttemptID: result.AttemptID, Sequence: result.Sequence}
	verified, err := session.ObservedAt(t.Context(), ref)
	if err != nil || !reflect.DeepEqual(verified.Event, result) || verified.Checkpoint.Sequence != 2 || verified.Checkpoint.Scope != scope {
		t.Fatal("ordinary result not attested", err)
	}
	wrong := ref
	wrong.CallRef = "missing"
	if _, err := session.ObservedAt(t.Context(), wrong); !errors.Is(err, ErrEventMissing) {
		t.Fatal("missing ordinary result accepted", err)
	}
	want := ref
	want.Sequence++
	if _, err := session.ObservedAt(t.Context(), want); !errors.Is(err, ErrEventMissing) {
		t.Fatal("wrong sequence accepted", err)
	}
	seal, err := session.SealChecked(t.Context())
	if err != nil || seal == nil || len(seal.Checks) != 1 || seal.Checks[0] != (SealCheck{Sequence: verified.Checkpoint.Sequence, MAC: verified.Checkpoint.MAC, Included: true}) {
		t.Fatal("ordinary checkpoint not sealed", err, seal)
	}
}

func TestHostObservedAtRejectsE3Result(t *testing.T) {
	host, _, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	_, intent, result := hostE3Events(scope)
	if err := session.Record(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	if err := session.Record(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	_, err = session.ObservedAt(t.Context(), ObservationRef{CallRef: result.CallRef, AttemptID: result.AttemptID, Sequence: result.Sequence})
	if !errors.Is(err, ErrBindingMismatch) {
		t.Fatal("E3 result used as ordinary evidence", err)
	}
}

// 내준 근거가 봉인 저널의 앞부분이 아니면(되감기·다른 분기) 포함 실패다. 실제 저널은 Host만 쓰므로 목록에 위조
// 체크포인트를 넣어 같은 판정 경로를 고정한다.
func TestHostSealCheckRejectsCheckpointOutsideSealedChain(t *testing.T) {
	host, _, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	binding, _, _ := hostE3Events(scope)
	ref := recordE3Call(t, session, scope, "call-first", 1)
	v, err := session.VerifiedAt(t.Context(), binding, ref)
	if err != nil {
		t.Fatal(err)
	}
	branch := v.Checkpoint
	branch.MAC = strings.Repeat("e", 64)
	other := v.Checkpoint
	other.JournalID = "journal-other"
	ahead := v.Checkpoint
	ahead.Sequence = 99
	session.run.issued = append(session.run.issued, branch, other, ahead)
	result, err := session.SealChecked(t.Context())
	if err != nil || result == nil || len(result.Checks) != 4 {
		t.Fatal("봉인", err, result)
	}
	for i, wantIncluded := range []bool{true, false, false, false} {
		if result.Checks[i].Included != wantIncluded {
			t.Fatalf("체크포인트 %d 포함=%v: %+v", i, result.Checks[i].Included, result.Checks)
		}
	}
}

// 확인 목록 상한을 넘는 새 근거는 내주지 않는다. 이미 내준 head는 계속 근거로 쓸 수 있다.
func TestHostVerifiedAtStopsAtSealCheckLimit(t *testing.T) {
	host, _, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	binding, _, _ := hostE3Events(scope)
	first := recordE3Call(t, session, scope, "call-first", 1)
	v, err := session.VerifiedAt(t.Context(), binding, first)
	if err != nil {
		t.Fatal(err)
	}
	for len(session.run.issued) < maxSealChecks {
		filler := v.Checkpoint
		filler.Sequence = uint64(1000 + len(session.run.issued))
		session.run.issued = append(session.run.issued, filler)
	}
	if again, err := session.VerifiedAt(t.Context(), binding, first); err != nil || again.Checkpoint != v.Checkpoint {
		t.Fatal("이미 내준 head가 상한에 막힘", err)
	}
	second := recordE3Call(t, session, scope, "call-second", 3)
	if _, err := session.VerifiedAt(t.Context(), binding, second); !errors.Is(err, ErrUnavailable) {
		t.Fatal("상한을 넘는 새 근거를 내줌", err)
	}
	if len(session.run.issued) != maxSealChecks {
		t.Fatal("상한을 넘어 목록이 늘어남", len(session.run.issued))
	}
}

// 봉인은 성공했지만 포함 확인 결과를 남기지 못하면 결과 없이(봉인 미확인) 봉인 성공을 돌려준다. 다음 회차는 막지
// 않는다.
func TestHostSealSucceedsWhenCheckRecordFails(t *testing.T) {
	host, pool, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	binding, _, _ := hostE3Events(scope)
	ref := recordE3Call(t, session, scope, "call-first", 1)
	if _, err := session.VerifiedAt(t.Context(), binding, ref); err != nil {
		t.Fatal(err)
	}
	// 같은 이름의 디렉터리가 있으면 원자 교체가 실패한다.
	if err := os.Mkdir(filepath.Join(session.run.directory, sealChecksFile+".json"), 0700); err != nil {
		t.Fatal(err)
	}
	result, err := session.SealChecked(t.Context())
	if err != nil || result != nil {
		t.Fatal("기록 실패를 봉인 실패로 바꾸거나 결과를 냄", err, result)
	}
	if phase, _ := storedHead(t, pool, scope.RunRef); phase != e2checkpoint.Sealed {
		t.Fatal("봉인되지 않음", phase)
	}
	next := scope
	next.RunRef = "run-host-next"
	if s, err := host.Open(t.Context(), next, "revision-host"); err != nil {
		t.Fatal("다음 회차가 막힘", err)
	} else {
		_ = s.Abort()
	}
}

// 소켓을 지나도 근거 체크포인트와 봉인 포함 결과가 그대로 온다.
func TestClientVerifiedAtAndSealCheckedOverSocket(t *testing.T) {
	host, _, scope := fixture(t)
	path := filepath.Join(t.TempDir(), "audit.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.AcceptUnix()
		if err == nil {
			host.serveConn(ctx, conn, uint32(os.Geteuid()))
		}
	}()
	client, err := OpenClient(ctx, path, scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Abort()
	binding, intent, result := hostE3Events(scope)
	if client.Record(ctx, intent) != nil || client.Record(ctx, result) != nil {
		t.Fatal("기록")
	}
	ref := e3contract.CallRef{CallRef: result.CallRef, AttemptID: result.AttemptID, Sequence: result.Sequence}
	missing := ref
	missing.CallRef = "missing-call"
	if _, err := client.VerifiedAt(ctx, binding, missing); !errors.Is(err, ErrEventMissing) {
		t.Fatal("없는 사건", err)
	}
	v, err := client.VerifiedAt(ctx, binding, ref)
	if err != nil || v.Event.Index != result.Index || v.Checkpoint.Version != e2proxy.JournalVersionV3 || v.Checkpoint.Sequence != 2 ||
		v.Checkpoint.Scope != scope || !validMAC(v.Checkpoint.MAC) {
		t.Fatal("소켓 근거 체크포인트", err, v.Checkpoint)
	}
	sealed, err := client.SealChecked(ctx)
	if err != nil || sealed == nil || sealed.Phase != e2checkpoint.Sealed || sealed.Head.Sequence != 2 || len(sealed.Checks) != 1 ||
		sealed.Checks[0] != (SealCheck{Sequence: 2, MAC: v.Checkpoint.MAC, Included: true}) {
		t.Fatal("소켓 봉인 포함 결과", err, sealed)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("봉인 뒤 Host 연결이 닫히지 않음")
	}
}

// 클라이언트는 응답 모양이 계약 밖이면 ACK 불명으로 보고 연결을 막는다.
func TestClientRejectsMalformedCheckpointReplies(t *testing.T) {
	scope := transmit.Scope{TaskRef: "task-test", RunRef: "run-test", CaseRef: "case-test", ResetRef: "reset-test", PolicyRef: "policy-test"}
	_, _, result := hostE3Events(scope)
	good := e2proxy.JournalCheckpoint{Version: e2proxy.JournalVersionV3, JournalID: "journal-test", Sequence: 2, MAC: strings.Repeat("a", 64), Scope: scope}
	event := result
	for name, tc := range map[string]struct {
		operation string
		answer    reply
	}{
		"verified_without_checkpoint": {"verified", reply{OK: true, Event: &event}},
		"verified_upper_mac": {"verified", reply{OK: true, Event: &event, Checkpoint: &e2proxy.JournalCheckpoint{Version: good.Version, JournalID: good.JournalID,
			Sequence: 2, MAC: strings.Repeat("A", 64), Scope: scope}}},
		"verified_header_checkpoint": {"verified", reply{OK: true, Event: &event, Checkpoint: &e2proxy.JournalCheckpoint{Version: good.Version,
			JournalID: good.JournalID, MAC: good.MAC, Scope: scope}}},
		"verified_v2_checkpoint": {"verified", reply{OK: true, Event: &event, Checkpoint: &e2proxy.JournalCheckpoint{Version: "e2-http-v2",
			JournalID: good.JournalID, Sequence: 2, MAC: good.MAC, Scope: scope}}},
		"missing_with_checkpoint":  {"verified", reply{Missing: true, Checkpoint: &good}},
		"mismatch_with_checkpoint": {"verified", reply{Mismatch: true, Checkpoint: &good}},
		"record_with_checkpoint":   {"record", reply{OK: true, Checkpoint: &good}},
		"record_with_seal":         {"record", reply{OK: true, Seal: &SealResult{Phase: e2checkpoint.Sealed, Head: good, Checks: []SealCheck{}}}},
		"seal_bad_head":            {"seal", reply{OK: true, Seal: &SealResult{Phase: e2checkpoint.Sealed, Head: e2proxy.JournalCheckpoint{}, Checks: []SealCheck{}}}},
		// 봉인 응답은 정상 봉인만 말한다. interrupted는 Host 볼륨 기록에만 남는다.
		"seal_interrupted_phase":   {"seal", reply{OK: true, Seal: &SealResult{Phase: e2checkpoint.Interrupted, Head: good, Checks: []SealCheck{}}}},
		"seal_no_phase":            {"seal", reply{OK: true, Seal: &SealResult{Head: good, Checks: []SealCheck{}}}},
		"seal_nil_checks":          {"seal", reply{OK: true, Seal: &SealResult{Head: good}}},
		"seal_zero_sequence":       {"seal", reply{OK: true, Seal: &SealResult{Phase: e2checkpoint.Sealed, Head: good, Checks: []SealCheck{{MAC: good.MAC, Included: true}}}}},
		"seal_included_after_head": {"seal", reply{OK: true, Seal: &SealResult{Phase: e2checkpoint.Sealed, Head: good, Checks: []SealCheck{{Sequence: 3, MAC: good.MAC, Included: true}}}}},
		"seal_bad_mac":             {"seal", reply{OK: true, Seal: &SealResult{Phase: e2checkpoint.Sealed, Head: good, Checks: []SealCheck{{Sequence: 1, MAC: "x", Included: true}}}}},
		"seal_too_many":            {"seal", reply{OK: true, Seal: &SealResult{Phase: e2checkpoint.Sealed, Head: good, Checks: tooManyChecks(good.MAC)}}},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				reader := bufio.NewReader(conn)
				var request message
				if decodeLine(reader, &request) != nil || json.NewEncoder(conn).Encode(reply{OK: true}) != nil {
					return
				}
				if decodeLine(reader, &request) != nil || request.Operation != tc.operation {
					return
				}
				_ = json.NewEncoder(conn).Encode(tc.answer)
				_ = decodeLine(reader, &request)
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			client, err := OpenClient(ctx, path, scope, "revision-test")
			if err != nil {
				t.Fatal(err)
			}
			defer client.Abort()
			binding, _, _ := hostE3Events(scope)
			ref := e3contract.CallRef{CallRef: result.CallRef, AttemptID: result.AttemptID, Sequence: result.Sequence}
			switch tc.operation {
			case "verified":
				if _, err := client.VerifiedAt(ctx, binding, ref); !errors.Is(err, ErrUnavailable) {
					t.Fatal("계약 밖 응답을 받음", err)
				}
				if err := client.Record(ctx, hostEvent(scope)); !errors.Is(err, ErrUnavailable) {
					t.Fatal("계약 밖 응답 뒤 기록 허용", err)
				}
			case "record":
				if err := client.Record(ctx, hostEvent(scope)); !errors.Is(err, ErrUnavailable) {
					t.Fatal("계약 밖 응답을 받음", err)
				}
			case "seal":
				if got, err := client.SealChecked(ctx); !errors.Is(err, ErrUnavailable) || got != nil {
					t.Fatal("계약 밖 봉인 응답을 받음", err, got)
				}
			}
		})
	}
}

// 봉인 ACK는 왔지만 결과가 없으면(Host가 확인을 남기지 못함) 봉인 성공·결과 없음이다.
func TestClientSealWithoutChecksIsSealedUnconfirmed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		var request message
		if decodeLine(reader, &request) != nil || json.NewEncoder(conn).Encode(reply{OK: true}) != nil {
			return
		}
		if decodeLine(reader, &request) == nil && request.Operation == "seal" {
			_ = json.NewEncoder(conn).Encode(reply{OK: true})
		}
	}()
	scope := transmit.Scope{TaskRef: "task-test", RunRef: "run-test", CaseRef: "case-test", ResetRef: "reset-test", PolicyRef: "policy-test"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := OpenClient(ctx, path, scope, "revision-test")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := client.SealChecked(ctx); err != nil || got != nil {
		t.Fatal("결과 없는 봉인 ACK", err, got)
	}
}

func tooManyChecks(mac string) []SealCheck {
	out := make([]SealCheck, maxSealChecks+1)
	for i := range out {
		out[i] = SealCheck{Sequence: 1, MAC: mac, Included: true}
	}
	return out
}

// 조건 1: epoch가 바뀌면 근거를 내주지 않는다.
func TestHostVerifiedAtRequiresCurrentEpoch(t *testing.T) {
	host, pool, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	binding, _, _ := hostE3Events(scope)
	ref := recordE3Call(t, session, scope, "call-first", 1)
	if _, err := pool.Exec(`UPDATE e2_http_audit_epochs SET generation=generation+1 WHERE store_ref=$1`, host.epoch.StoreRef); err != nil {
		t.Fatal(err)
	}
	if v, err := session.VerifiedAt(t.Context(), binding, ref); !errors.Is(err, ErrUnavailable) || v.Checkpoint != (e2proxy.JournalCheckpoint{}) {
		t.Fatal("현재 epoch가 아닌데 근거를 내줌", err)
	}
	if len(session.run.issued) != 0 {
		t.Fatal("내주지 않은 근거가 목록에 남음")
	}
}

// 근거 목록을 Host 볼륨에 남기지 못하면 근거를 내주지 않는다(끊김 마감 때 대조할 수 없는 근거를 주지 않음).
func TestHostVerifiedAtRequiresPersistedEvidenceList(t *testing.T) {
	host, _, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	binding, _, _ := hostE3Events(scope)
	ref := recordE3Call(t, session, scope, "call-first", 1)
	if err := os.Mkdir(filepath.Join(session.run.directory, issuedFile+".json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := session.VerifiedAt(t.Context(), binding, ref); !errors.Is(err, ErrUnavailable) {
		t.Fatal("목록을 남기지 못했는데 근거를 내줌", err)
	}
	if len(session.run.issued) != 0 {
		t.Fatal("남기지 못한 근거가 메모리 목록에 남음")
	}
}

// 조건 4: 연결이 끊겨 Host가 interrupted로 마감한 저널도 내준 근거를 대조하되, 기록의 phase는 interrupted다(정상
// 봉인과 섞지 않음). 같은 Host가 복구할 때와 Host가 다시 뜬 뒤 복구할 때(메모리 목록 없음) 모두 Host 볼륨 목록을
// 쓴다.
func TestHostInterruptedRunRecordsChecksSeparately(t *testing.T) {
	t.Run("same_host_disconnect", func(t *testing.T) {
		host, pool, scope := fixture(t)
		path := filepath.Join(t.TempDir(), "audit.sock")
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			conn, err := listener.AcceptUnix()
			if err == nil {
				host.serveConn(ctx, conn, uint32(os.Geteuid()))
			}
		}()
		client, err := OpenClient(ctx, path, scope, "revision-host")
		if err != nil {
			t.Fatal(err)
		}
		binding, intent, result := hostE3Events(scope)
		if client.Record(ctx, intent) != nil || client.Record(ctx, result) != nil {
			t.Fatal("기록")
		}
		v, err := client.VerifiedAt(ctx, binding, e3contract.CallRef{CallRef: result.CallRef, AttemptID: result.AttemptID, Sequence: result.Sequence})
		if err != nil {
			t.Fatal(err)
		}
		client.Abort()
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("연결 종료 뒤 Host 복구가 끝나지 않음")
		}
		phase, head := storedHead(t, pool, scope.RunRef)
		digest := sha256.Sum256([]byte(scope.RunRef))
		version, recorded := readSealRecord(t, filepath.Join(host.root, hex.EncodeToString(digest[:])))
		if phase != e2checkpoint.Interrupted || version != sealChecksVersion || recorded.Phase != e2checkpoint.Interrupted ||
			recorded.Head != head || len(recorded.Checks) != 1 ||
			recorded.Checks[0] != (SealCheck{Sequence: v.Checkpoint.Sequence, MAC: v.Checkpoint.MAC, Included: true}) {
			t.Fatalf("interrupted 기록: %s %+v", phase, recorded)
		}
	})
	t.Run("restarted_host", func(t *testing.T) {
		host, pool, scope := fixture(t)
		session, err := host.Open(t.Context(), scope, "revision-host")
		if err != nil {
			t.Fatal(err)
		}
		binding, _, _ := hostE3Events(scope)
		ref := recordE3Call(t, session, scope, "call-first", 1)
		v, err := session.VerifiedAt(t.Context(), binding, ref)
		if err != nil {
			t.Fatal(err)
		}
		directory := session.run.directory
		// 같은 저널에 없는 근거도 목록에 있으면 포함 실패로 남는다.
		forged := v.Checkpoint
		forged.MAC = strings.Repeat("f", 64)
		data, err := json.Marshal(issuedRecord{Version: issuedVersion, Checkpoints: []e2proxy.JournalCheckpoint{v.Checkpoint, forged}})
		if err != nil || writePrivateFile(directory, issuedFile, data) != nil {
			t.Fatal("목록 위조")
		}
		_ = session.Abort()
		restarted, err := New(pool, host.epoch, host.keyRef, host.key, host.root)
		if err != nil {
			t.Fatal(err)
		}
		if err := restarted.RecoverDisconnectedHeads(t.Context()); err != nil {
			t.Fatal("복구", err)
		}
		phase, head := storedHead(t, pool, scope.RunRef)
		_, recorded := readSealRecord(t, directory)
		if phase != e2checkpoint.Interrupted || recorded.Phase != e2checkpoint.Interrupted || recorded.Head != head || len(recorded.Checks) != 2 ||
			!recorded.Checks[0].Included || recorded.Checks[1].Included {
			t.Fatalf("재기동 뒤 interrupted 기록: %s %+v", phase, recorded)
		}
		if err := restarted.CheckRestoredHeads(t.Context()); err != nil {
			t.Fatal("기록 뒤 기동 검사", err)
		}
	})
}
