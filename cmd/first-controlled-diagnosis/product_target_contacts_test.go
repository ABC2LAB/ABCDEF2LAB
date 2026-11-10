package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

func TestProductTargetTLSContactsDurableAndRedacted(t *testing.T) {
	r, root := targetTestRegistry(t)
	v, err := r.register("ARTEX", targetTestSpec("audit.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	r.lookupTXT = func(context.Context, string) ([]string, error) { return []string{v.ChallengeValue}, nil }
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.10")}, nil
	}
	var failure error
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		return strings.Repeat("a", 64), time.Now().Add(time.Hour), failure
	}
	if _, err = r.approve(t.Context(), "ARTEX", v.ID); err != nil {
		t.Fatal(err)
	}
	rec, err := r.lookup("ARTEX", "https://audit.example.test/read", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.recheck(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	failure = errors.New("synthetic-peer-private-detail")
	if err = r.recheck(t.Context(), rec); err == nil || err.Error() != "target_tls_changed" {
		t.Fatal("TLS 실패가 시작을 막지 않음", err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	journal, records, err := diagnosisprep.OpenProtectedJournal(filepath.Join(root, "tls-contacts"))
	if err != nil {
		t.Fatal("TLS 사전 접촉 감사 복원 실패", err)
	}
	defer journal.Close()
	if len(records) != 6 {
		t.Fatalf("승인·시작 성공·시작 실패의 시도/결과가 보존되지 않음: %d", len(records))
	}
	for i, raw := range records {
		var event productTLSContact
		if err = json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		stage := "start"
		if i < 2 {
			stage = "approve"
		}
		if event.Format != "abc2lab.product-tls-contact/v1" || event.TargetID != v.ID || event.Stage != stage || event.Host != "audit.example.test" || event.IP != "10.200.1.10" || event.Port != 443 || event.At.IsZero() {
			t.Fatalf("TLS 접촉의 대상·단계 결속 실패: %+v", event)
		}
		if i%2 == 0 {
			if event.Phase != "attempt" {
				t.Fatal("접촉 전 감사 없음")
			}
		} else {
			var before productTLSContact
			_ = json.Unmarshal(records[i-1], &before)
			result := "verified"
			if i == 5 {
				result = "unverified"
			}
			if event.Phase != "completed" || event.ID != before.ID || event.Result != result {
				t.Fatal("접촉 시도/결과 결속 실패")
			}
		}
		if strings.Contains(string(raw), failure.Error()) {
			t.Fatal("비공개 TLS 오류 원문이 감사에 포함됨")
		}
	}
}

func TestProductTargetTLSAuditFailurePreventsContact(t *testing.T) {
	r, _ := targetTestRegistry(t)
	v, err := r.register("ARTEX", targetTestSpec("audit.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		t.Fatal("감사 불가 상태에서 TLS 접촉")
		return "", time.Time{}, nil
	}
	if err = r.contacts.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = r.approve(t.Context(), "ARTEX", v.ID); !errors.Is(err, errPreparation) {
		t.Fatal("감사 실패가 승인을 차단하지 않음", err)
	}
	if r.records[v.ID].Target.State != "pending" {
		t.Fatal("감사 실패로 승인 상태가 바뀜")
	}
}

func TestProductTargetCertificateExpiresDuringApproval(t *testing.T) {
	r, _ := targetTestRegistry(t)
	clock := time.Now().UTC()
	expires := clock.Add(time.Second)
	r.now = func() time.Time { return clock }
	v, err := r.register("ARTEX", targetTestSpec("expiry.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	r.lookupTXT = func(context.Context, string) ([]string, error) { return []string{v.ChallengeValue}, nil }
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.10")}, nil
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		clock = expires
		return strings.Repeat("a", 64), expires, nil
	}
	if _, err = r.approve(t.Context(), "ARTEX", v.ID); err == nil || err.Error() != "target_approval_expired" {
		t.Fatal("TLS 확인 중 만료된 인증서로 승인", err)
	}
	if r.check() != nil || r.records[v.ID].Target.State != "pending" || len(r.approvals) != 0 {
		t.Fatal("만료된 승인 시도가 기존 저널을 손상시킴")
	}
}

// descriptor 검사는 정상이지만 디스크 쓰기가 실패하는 저장 경계를 주입한다.
type failingContactAppend struct {
	productContactJournal
	failAt, appends int
}

func (f *failingContactAppend) Append(raw json.RawMessage) error {
	f.appends++
	if f.appends == f.failAt {
		return errors.New("owned synthetic disk full")
	}
	return f.productContactJournal.Append(raw)
}
func TestProductTargetTLSContactAppendFailurePreventsApproval(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("append_%d", failAt), func(t *testing.T) {
			r, _ := targetTestRegistry(t)
			v, err := r.register("ARTEX", targetTestSpec("append.example.test"))
			if err != nil {
				t.Fatal(err)
			}
			r.lookupTXT = func(context.Context, string) ([]string, error) { return []string{v.ChallengeValue}, nil }
			r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("10.200.1.10")}, nil
			}
			probes := 0
			r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
				probes++
				return strings.Repeat("a", 64), time.Now().Add(time.Hour), nil
			}
			failing := &failingContactAppend{productContactJournal: r.contacts, failAt: failAt}
			r.contacts = failing
			if r.check() != nil {
				t.Fatal("positive descriptor check")
			}
			if _, err = r.approve(t.Context(), "ARTEX", v.ID); err == nil {
				t.Fatal("failed audit append allowed approval")
			}
			if probes != failAt-1 || failing.appends != failAt || r.records[v.ID].Target.State != "pending" || len(r.approvals) != 0 {
				t.Fatal("audit failure boundary not enforced", probes, failing.appends)
			}
		})
	}
}
