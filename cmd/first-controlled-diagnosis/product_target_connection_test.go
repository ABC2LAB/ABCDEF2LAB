package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/server"
)

func connectionTestProfile(r *productTargetRegistry, rec productTargetRecord) *urlLoadedProfile {
	return &urlLoadedProfile{productRegistry: r, productTarget: &rec, policy: server.E2HTTPFilePolicy{Rules: []server.E2HTTPFileRule{{Origin: rec.Target.Origin, ConnectIP: rec.SelectedIP, TLSSPKISHA256: rec.Target.TLSSPKISHA256, PathPrefix: "/read", Methods: []string{"GET"}}}}}
}

func TestC35DNSChecksEveryUniqueAddress(t *testing.T) {
	r, _ := targetTestRegistry(t)
	ips := []netip.Addr{}
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) { return ips, nil }
	for i := 1; i <= 32; i++ {
		ip := netip.MustParseAddr(fmt.Sprintf("10.200.1.%d", i))
		ips = append(ips, ip, ip)
	}
	got, err := r.addresses(t.Context(), "owned.example.test")
	if err != nil || len(got) != 32 {
		t.Fatal("중복 제거 뒤 32개 경계 거부")
	}
	ips = append(ips, netip.MustParseAddr("10.200.1.33"))
	if _, err = r.addresses(t.Context(), "owned.example.test"); err == nil {
		t.Fatal("33개 주소 수용")
	}
	for _, bad := range []string{"169.254.169.254", "127.0.0.1", "100.100.100.200", "fd00:ec2::254", "10.200.2.10"} {
		ips = []netip.Addr{netip.MustParseAddr("10.200.1.10"), netip.MustParseAddr(bad)}
		if _, err = r.addresses(t.Context(), "owned.example.test"); err == nil {
			t.Fatal("주소 하나라도 정책 밖인 집합 수용")
		}
	}
}

func TestC35ConnectionIsImmutableAndJobBound(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	approved, err := r.lookup("ARTEX", target.Origin+"/read", "")
	if err != nil {
		t.Fatal(err)
	}
	before := productTargetCopy(approved)
	ip, pin := "10.200.1.20", strings.Repeat("b", 64)
	reads := 0
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		reads++
		return []netip.Addr{netip.MustParseAddr(ip)}, nil
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		return pin, time.Now().Add(time.Hour), nil
	}
	first := connectionTestProfile(r, approved)
	if err = first.prepareTargetConnection(t.Context(), nil, "job-a"); err != nil {
		t.Fatal("새 검증 증거 결속 실패", err)
	}
	if first.policy.Rules[0].ConnectIP != ip || first.policy.Rules[0].TLSSPKISHA256 != pin || first.productConnection.jobID != "job-a" {
		t.Fatal("실행 정책에 검증한 주소·SPKI 미결속")
	}
	if !reflect.DeepEqual(before, r.records[target.ID]) || r.current(approved) != nil {
		t.Fatal("시작 검사가 승인 기록·revision을 변경")
	}
	ip, pin = "10.200.1.30", strings.Repeat("c", 64)
	second := connectionTestProfile(r, approved)
	if err = second.prepareTargetConnection(t.Context(), nil, "job-b"); err != nil {
		t.Fatal(err)
	}
	if first.policy.Rules[0].ConnectIP != "10.200.1.20" || first.policy.Rules[0].TLSSPKISHA256 != strings.Repeat("b", 64) || second.policy.Rules[0].ConnectIP != ip {
		t.Fatal("이전 실행에 새 연결 증거 혼입")
	}
	if first.policy.Rules[0].Origin != approved.Target.Origin || first.policy.Rules[0].PathPrefix != "/read" || !reflect.DeepEqual(first.policy.Rules[0].Methods, []string{"GET"}) {
		t.Fatal("연결 갱신이 origin·경로·메서드를 확대")
	}
	beforeReads := reads
	if first.prepareTargetConnection(t.Context(), nil, "job-a") == nil || reads != beforeReads {
		t.Fatal("시작된 회차를 새 DNS 증거로 다시 결속")
	}
	backend := &urlPreparationRuntime{}
	if _, err = backend.issue(first, "job-b", server.FirstDiagnosisPreparedTask{}); err == nil {
		t.Fatal("다른 job의 검증 증거로 권위 발급")
	}
	if _, err = backend.issue(connectionTestProfile(r, approved), "job-a", server.FirstDiagnosisPreparedTask{}); err == nil {
		t.Fatal("연결 검증 없이 권위 발급")
	}
}

func TestC35FailedConnectionCannotAcquireAuthority(t *testing.T) {
	for _, cause := range []string{"txt", "dns", "metadata", "protected_endpoint", "protected_second_address", "untrusted_tls", "invalid_spki", "expired_certificate", "revoked", "revoked_during_dns"} {
		t.Run(cause, func(t *testing.T) {
			r, _, target := accountTestTarget(t, true)
			rec, err := r.lookup("ARTEX", target.Origin+"/read", "")
			if err != nil {
				t.Fatal(err)
			}
			loaded := connectionTestProfile(r, rec)
			before := connectionTestProfile(r, rec).policy
			probes := 0
			r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("10.200.1.20")}, nil
			}
			r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
				probes++
				return strings.Repeat("b", 64), time.Now().Add(time.Hour), nil
			}
			switch cause {
			case "txt":
				r.lookupTXT = func(context.Context, string) ([]string, error) { return nil, nil }
			case "dns":
				r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
					return nil, errors.New("synthetic-only-dns-error")
				}
			case "metadata":
				r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr("10.200.1.20"), netip.MustParseAddr("169.254.169.254")}, nil
				}
			case "protected_endpoint":
				r.endpointAllowed = func(string, netip.Addr) bool { return false }
			case "protected_second_address":
				r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr("10.200.1.20"), netip.MustParseAddr("10.200.1.21")}, nil
				}
				r.endpointAllowed = func(_ string, ip netip.Addr) bool { return ip.String() != "10.200.1.21" }
			case "untrusted_tls":
				r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
					probes++
					return "", time.Time{}, errors.New("synthetic-only-tls-error")
				}
			case "invalid_spki":
				r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
					probes++
					return "", time.Now().Add(time.Hour), nil
				}
			case "expired_certificate":
				r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
					probes++
					return strings.Repeat("b", 64), time.Now().Add(-time.Second), nil
				}
			case "revoked_during_dns":
				r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
					_, e := r.revoke("ARTEX", target.ID)
					return []netip.Addr{netip.MustParseAddr("10.200.1.20")}, e
				}
			case "revoked":
				if _, err = r.revoke("ARTEX", target.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err = loaded.prepareTargetConnection(t.Context(), nil, "job-owned"); err == nil || loaded.productConnection != nil {
				t.Fatal("미승인·미검증 연결을 실행에 결속")
			}
			if !reflect.DeepEqual(loaded.policy, before) {
				t.Fatal("연결 검증 실패가 정책을 변경")
			}
			if cause == "txt" || cause == "dns" || cause == "metadata" || cause == "protected_endpoint" || cause == "protected_second_address" || cause == "revoked" || cause == "revoked_during_dns" {
				if probes != 0 {
					t.Fatal("거부 후 TLS 접촉")
				}
			}
			if strings.Contains(targetConnectionFailure(err), "synthetic-only") {
				t.Fatal("오류 원문 노출")
			}
		})
	}
}

func TestC35ApprovalPeriodIsIndependentOfCertificate(t *testing.T) {
	r, _ := targetTestRegistry(t)
	for _, hours := range []int64{0, 721} {
		spec := targetTestSpec("owned.example.test")
		spec.ValidHours = hours
		if _, err := r.register("ARTEX", spec); err == nil {
			t.Fatal("승인 기간 코드 상한 밖 수용")
		}
	}
	spec := targetTestSpec("owned.example.test")
	spec.ValidHours = 720
	target, err := r.register("ARTEX", spec)
	if err != nil {
		t.Fatal(err)
	}
	r.lookupTXT = func(context.Context, string) ([]string, error) { return []string{target.ChallengeValue}, nil }
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.10")}, nil
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		return strings.Repeat("a", 64), time.Now().Add(time.Hour), nil
	}
	approved, err := r.approve(t.Context(), "ARTEX", target.ID)
	if err != nil || approved.ExpiresAt.Sub(approved.ApprovedAt) != 720*time.Hour {
		t.Fatal("승인 기간을 인증서 교체 주기로 제한")
	}
	// 형식·필드 범위는 그대로이며 연결 검사 없이 이 pending에 권위를 만들지 않는다.
	if _, err = r.register("ARTEX", diagnosisprep.TargetRegistration{URL: target.Origin}); err == nil {
		t.Fatal("범위·진술 없이 등록")
	}
}

func TestC35AuditFailureCannotFallBackToAnotherAddress(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			r, _, target := accountTestTarget(t, true)
			rec, err := r.lookup("ARTEX", target.Origin+"/read", "")
			if err != nil {
				t.Fatal(err)
			}
			r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("10.200.1.20"), netip.MustParseAddr("10.200.1.21")}, nil
			}
			probes := 0
			r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
				probes++
				return strings.Repeat("b", 64), time.Now().Add(time.Hour), nil
			}
			contacts := &failingContactAppend{productContactJournal: r.contacts, failAt: failAt}
			r.contacts = contacts
			l := connectionTestProfile(r, rec)
			if l.prepareTargetConnection(t.Context(), nil, "audit-failed") == nil || l.productConnection != nil || probes != failAt-1 || contacts.appends != failAt {
				t.Fatal("감사 실패 뒤 다른 IP로 재시도·권위 발급", probes, contacts.appends)
			}
		})
	}
}

type c35RevokeOnContact struct {
	productContactJournal
	revoke func() error
}

func (c *c35RevokeOnContact) Append(raw json.RawMessage) error {
	if err := c.productContactJournal.Append(raw); err != nil {
		return err
	}
	return c.revoke()
}

func TestC35RevocationAfterAuditPreventsTLSContact(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	rec, err := r.lookup("ARTEX", target.Origin+"/read", "")
	if err != nil {
		t.Fatal(err)
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		t.Fatal("감사 저장 중 철회된 대상에 TLS 접촉")
		return "", time.Time{}, nil
	}
	r.contacts = &c35RevokeOnContact{productContactJournal: r.contacts, revoke: func() error { _, err := r.revoke("ARTEX", target.ID); return err }}
	l := connectionTestProfile(r, rec)
	if l.prepareTargetConnection(t.Context(), nil, "revoked-at-audit") == nil || l.productConnection != nil {
		t.Fatal("접촉 전 철회 검사 누락")
	}
}
