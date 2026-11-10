package main

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

func TestProductTargetApprovalExpiryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		hours                     int64
		certificateLife, lifetime time.Duration
	}{
		{"valid_hours", 1, 24 * time.Hour, time.Hour},
		{"certificate_not_after", 4, 20 * time.Minute, 20 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := targetTestRegistry(t)
			clock := time.Now().UTC().Truncate(time.Second)
			approvedAt := clock
			r.now = func() time.Time { return clock }
			v, err := r.register("ARTEX", func() diagnosisprep.TargetRegistration {
				spec := targetTestSpec("expiry.example.test")
				spec.ValidHours = tc.hours
				return spec
			}())
			if err != nil {
				t.Fatal(err)
			}
			dns, probes := 0, 0
			r.lookupTXT = func(context.Context, string) ([]string, error) { dns++; return []string{v.ChallengeValue}, nil }
			r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
				dns++
				return []netip.Addr{netip.MustParseAddr("10.200.1.10")}, nil
			}
			r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
				probes++
				return strings.Repeat("a", 64), approvedAt.Add(tc.certificateLife), nil
			}
			approved, err := r.approve(t.Context(), "ARTEX", v.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !approved.ExpiresAt.Equal(approvedAt.Add(tc.lifetime)) {
				t.Fatalf("승인 만료가 요청 시간·인증서 만료 중 빠른 값이 아님: %v", approved.ExpiresAt)
			}
			rec, err := r.lookup("ARTEX", "https://expiry.example.test/read", "")
			if err != nil {
				t.Fatal(err)
			}
			// 실제 실행 감시가 부르는 current()도 다른 실행 조건이 유효한 상태에서 대조한다.
			loaded := &urlLoadedProfile{productTarget: &rec, productRegistry: r, profile: urlPreparationProfile{ValidFrom: approvedAt.Add(-time.Hour), ValidUntil: approvedAt.Add(48 * time.Hour)}}
			if err := loaded.current(); err != nil {
				t.Fatal("정상 실행 감시 대조 실패", err)
			}
			clock = approved.ExpiresAt.Add(-time.Nanosecond)
			if _, err := r.lookup("ARTEX", "https://expiry.example.test/read", ""); err != nil || r.current(rec) != nil {
				t.Fatal("만료 직전 정상 승인 거부", err)
			}
			for _, elapsed := range []time.Duration{0, time.Nanosecond} {
				clock = approved.ExpiresAt.Add(elapsed)
				if _, err := r.lookup("ARTEX", "https://expiry.example.test/read", ""); err == nil || err.Error() != "target_not_approved" {
					t.Fatalf("만료 승인으로 새 실행 자료 조회: %v", err)
				}
				if err := r.current(rec); !errors.Is(err, errPreparation) {
					t.Fatalf("기존 회차의 만료 승인을 현재로 인정: %v", err)
				}
				if err := loaded.current(); !errors.Is(err, errPreparation) {
					t.Fatal("실행 감시가 만료된 대상을 현재 권위로 인정", err)
				}
				beforeDNS, beforeTLS := dns, probes
				if err := r.recheck(t.Context(), rec); err == nil || err.Error() != "target_approval_changed" || dns != beforeDNS || probes != beforeTLS {
					t.Fatalf("만료 후 시작 사전 검사에서 통신: %v DNS=%d TLS=%d", err, dns-beforeDNS, probes-beforeTLS)
				}
			}
			historic, err := r.lookup("ARTEX", "https://expiry.example.test/read", approved.ID)
			if err != nil || productTargetDigest(historic) != productTargetDigest(rec) {
				t.Fatal("만료가 과거 보고서의 승인 결속을 바꿈", err)
			}
		})
	}
}

func TestProductTargetLiteralIPRequiresDNSOwnership(t *testing.T) {
	r, _ := targetTestRegistry(t)
	for _, host := range []string{"8.8.8.8", "[2606:4700:4700::1111]"} {
		if _, err := r.register("ARTEX", targetTestSpec(host)); err == nil || err.Error() != "dns_ownership_required" {
			t.Fatalf("IP literal이 DNS 소유 증명 등록을 통과함: %s %v", host, err)
		}
		if len(r.records) != 0 || len(r.approvals) != 0 {
			t.Fatal("거부한 IP 등록이 승인 저장소를 바꿈")
		}
	}
	if _, err := r.register("ARTEX", targetTestSpec("owned.example.test")); err != nil {
		t.Fatal("정상 DNS 호스트 등록 실패", err)
	}
}

func TestProductTargetProtectedCheckerRequired(t *testing.T) {
	r, _ := targetTestRegistry(t)
	v, err := r.register("ARTEX", targetTestSpec("owned.example.test"))
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
	r.endpointAllowed = nil
	if _, err := r.approve(t.Context(), "ARTEX", v.ID); err == nil || err.Error() != "target_address_denied" || probes != 0 {
		t.Fatalf("보호 서비스 검사 누락 상태에서 승인·TLS 접촉: %v probes=%d", err, probes)
	}
	r.endpointAllowed = func(string, netip.Addr) bool { return true }
	if _, err := r.approve(t.Context(), "ARTEX", v.ID); err != nil || probes != 1 {
		t.Fatal("정상 검사기 결속 대조 실패", err)
	}
	rec, err := r.lookup("ARTEX", "https://owned.example.test/read", "")
	if err != nil {
		t.Fatal(err)
	}
	r.endpointAllowed = nil
	before := probes
	if err := r.recheck(t.Context(), rec); err == nil || err.Error() != "target_address_denied" || probes != before {
		t.Fatalf("시작 검사에서 누락된 검사기를 허용: %v probes=%d", err, probes-before)
	}
}

func TestProductTargetValidityRangeBeforePersistence(t *testing.T) {
	r, _ := targetTestRegistry(t)
	for _, hours := range []int64{0, 25} {
		spec := targetTestSpec("owned.example.test")
		spec.ValidHours = hours
		if _, err := r.register("ARTEX", spec); err == nil || err.Error() != "readonly_scope_required" {
			t.Fatalf("승인 시간 상한·하한 미집행: %d %v", hours, err)
		}
		if len(r.records) != 0 {
			t.Fatal("잘못된 승인 시간이 pending 기록을 남김")
		}
	}
	spec := targetTestSpec("owned.example.test")
	spec.ValidHours = 24
	if _, err := r.register("ARTEX", spec); err != nil {
		t.Fatal("24시간 정상 경계 거부", err)
	}
}
