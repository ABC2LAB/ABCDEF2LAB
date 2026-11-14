//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

func TestC35ApprovalPeriodReachesProductRequest(t *testing.T) {
	for _, hours := range []int64{24, 25, 720, 721} {
		t.Run(strconv.FormatInt(hours, 10), func(t *testing.T) {
			r, _ := targetTestRegistry(t)
			spec := targetTestSpec("owned.example.test")
			spec.ValidHours = hours
			req := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "c35-request", Principal: "ARTEX", Command: diagnosisprep.ProductControl, Product: &diagnosisprep.ProductRequest{Operation: "register", Registration: &spec}}
			endpoint := productPreparationEndpoint{product: &productRuntime{targets: r}}
			response, err := endpoint.Call(t.Context(), req)
			if hours == 721 {
				if !errors.Is(err, diagnosisprep.ErrProtocol) || len(r.records) != 0 {
					t.Fatal("721시간 요청이 제품 계약을 통과")
				}
				return
			}
			if err != nil || response.Error != "" || response.Product == nil || response.Product.Target == nil {
				t.Fatal("720시간 이하 제품 요청 거절", err, response.Error)
			}
			if err := diagnosisprep.ValidateResponse(req, response); err != nil {
				t.Fatal("제품 등록 응답 계약 불일치", err)
			}
			rec := r.records[response.Product.Target.ID]
			if rec.ValidHours != hours || rec.Target.State != "pending" {
				t.Fatal("등록 기간 유실 또는 승인 없는 권위 생성")
			}
		})
	}
}

func TestC35StalledAddressDoesNotConsumePoolBudget(t *testing.T) {
	for _, count := range []int{2, 4} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			r, _, target := accountTestTarget(t, true)
			rec, err := r.lookup("ARTEX", target.Origin+"/read", "")
			if err != nil {
				t.Fatal(err)
			}
			addresses := make([]netip.Addr, count)
			for i := range addresses {
				addresses[i] = netip.MustParseAddr(fmt.Sprintf("10.200.1.%d", 10+i))
			}
			r.lookupIP = func(context.Context, string) ([]netip.Addr, error) { return addresses, nil }
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			overall, _ := ctx.Deadline()
			var contacted []string
			r.probe = func(probeCtx context.Context, _ string, ip string, _ int) (string, time.Time, error) {
				contacted = append(contacted, ip)
				if ip != addresses[len(addresses)-1].String() {
					deadline, ok := probeCtx.Deadline()
					if !ok || !deadline.Before(overall) {
						t.Fatal("주소별 기한이 전체 기한과 분리되지 않음")
					}
					<-probeCtx.Done()
					return "", time.Time{}, probeCtx.Err()
				}
				if probeCtx.Err() != nil {
					return "", time.Time{}, probeCtx.Err()
				}
				return strings.Repeat("b", 64), time.Now().Add(time.Hour), nil
			}
			loaded := connectionTestProfile(r, rec)
			if err := loaded.prepareTargetConnection(ctx, nil, "job-stalled-pool"); err != nil {
				t.Fatal("앞 주소 지연이 정상 주소의 시작을 차단", err)
			}
			if loaded.productConnection == nil || loaded.productConnection.IP != addresses[len(addresses)-1].String() || len(contacted) != count || ctx.Err() != nil {
				t.Fatal("기한 안의 정상 주소 미선택", contacted)
			}
		})
	}
}

func TestC35RevocationDuringTLSProbeBlocksConnection(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	rec, err := r.lookup("ARTEX", target.Origin+"/read", "")
	if err != nil {
		t.Fatal(err)
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		if _, err := r.revoke("ARTEX", target.ID); err != nil {
			t.Fatal(err)
		}
		return strings.Repeat("b", 64), time.Now().Add(time.Hour), nil
	}
	loaded := connectionTestProfile(r, rec)
	if err := loaded.prepareTargetConnection(t.Context(), nil, "job-revoked-in-tls"); err == nil || loaded.productConnection != nil {
		t.Fatal("TLS 접촉 중 철회된 대상으로 연결 증거 발급")
	}
}

func TestC35RevokedTargetSkipsDNS(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	rec, err := r.lookup("ARTEX", target.Origin+"/read", "")
	if err != nil {
		t.Fatal(err)
	}
	lookups := 0
	r.lookupTXT = func(context.Context, string) ([]string, error) {
		lookups++
		return []string{target.ChallengeValue}, nil
	}
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		lookups++
		return []netip.Addr{netip.MustParseAddr("10.200.1.10")}, nil
	}
	if _, err := r.revoke("ARTEX", target.ID); err != nil {
		t.Fatal(err)
	}
	loaded := connectionTestProfile(r, rec)
	if err := loaded.prepareTargetConnection(t.Context(), nil, "job-revoked"); err == nil || lookups != 0 {
		t.Fatal("철회 뒤 DNS 재접촉", lookups)
	}
}

type c35RecordingContacts struct {
	productContactJournal
	events []productTLSContact
}

func (c *c35RecordingContacts) Append(raw json.RawMessage) error {
	if err := c.productContactJournal.Append(raw); err != nil {
		return err
	}
	var event productTLSContact
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	c.events = append(c.events, event)
	return nil
}

func TestC35ExpiredContextDoesNotAuditUnattemptedAddresses(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	rec, err := r.lookup("ARTEX", target.Origin+"/read", "")
	if err != nil {
		t.Fatal(err)
	}
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.10"), netip.MustParseAddr("10.200.1.20")}, nil
	}
	contacts := &c35RecordingContacts{productContactJournal: r.contacts}
	r.contacts = contacts
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	probes := 0
	r.probe = func(probeCtx context.Context, _ string, _ string, _ int) (string, time.Time, error) {
		probes++
		cancel()
		return "", time.Time{}, probeCtx.Err()
	}
	loaded := connectionTestProfile(r, rec)
	err = loaded.prepareTargetConnection(ctx, nil, "job-cancelled-probe")
	if err == nil || loaded.productConnection != nil || probes != 1 || len(contacts.events) != 2 {
		t.Fatal("기한 종료 뒤 미시도 주소에 접촉/감사 추가", probes, len(contacts.events))
	}
	for _, event := range contacts.events {
		if event.IP != "10.200.1.10" {
			t.Fatal("미시도 주소의 감사 기록", event.IP)
		}
	}
}

func TestC35TLSHandshakeHasIndividualDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		<-release
	}()
	defer func() { close(release); listener.Close(); <-done }()
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	r := &productTargetRegistry{}
	addr := listener.Addr().(*net.TCPAddr)
	if _, _, err := r.probeTLS(ctx, "owned-stalled.example", addr.IP.String(), addr.Port); err == nil {
		t.Fatal("미완료 TLS 핸드셰이크 성공 처리")
	}
	if ctx.Err() != nil {
		t.Fatal("한 주소의 핸드셰이크가 전체 기한을 소진")
	}
}

func TestC35ConnectionFailureReasonDoesNotInventDNSMismatch(t *testing.T) {
	for _, err := range []error{errPreparation, errors.New("owned private audit detail"), errors.New("owned invalid SPKI detail")} {
		if got := targetConnectionFailure(err); got != "target_connection_unavailable" {
			t.Fatal("검증/감사 실패를 DNS 불일치로 오기", got)
		}
	}
	for _, reason := range []string{"target_connection_timeout", "target_dns_mismatch", "target_dns_unavailable", "target_tls_unverified", "target_approval_changed"} {
		if got := targetConnectionFailure(errors.New(reason)); got != reason {
			t.Fatal("검증한 고정 사유 유실", reason, got)
		}
	}
}
