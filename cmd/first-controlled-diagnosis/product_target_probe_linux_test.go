//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

func c35PoolIPs() []netip.Addr {
	ips := make([]netip.Addr, 32)
	for i := range ips {
		ips[i] = netip.MustParseAddr(fmt.Sprintf("10.200.1.%d", 10+i))
	}
	return ips
}

func TestC35Slow32AddressPool(t *testing.T) {
	for _, tc := range []struct {
		name, stage       string
		plain, allHealthy bool
	}{
		{name: "start-last", stage: "start"},
		{name: "approve-last", stage: "approve"},
		{name: "start-all", stage: "start", allHealthy: true},
		{name: "http-start-last", stage: "start", plain: true},
		{name: "http-approve-all", stage: "approve", plain: true, allHealthy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := targetTestRegistry(t)
			if tc.plain {
				r.policy.AllowedPorts = []int{80}
			}
			spec := targetTestSpec("slow-pool.example.test")
			if tc.plain {
				spec.URL = "http://slow-pool.example.test/read"
			}
			target, err := r.register("ARTEX", spec)
			if err != nil {
				t.Fatal(err)
			}
			r.lookupTXT = func(context.Context, string) ([]string, error) { return []string{target.ChallengeValue}, nil }
			if tc.stage == "start" {
				r.lookupIP = func(context.Context, string) ([]netip.Addr, error) { return c35PoolIPs()[:1], nil }
				r.reach = func(context.Context, string, int) error { return nil }
				r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
					return strings.Repeat("a", 64), time.Now().Add(time.Hour), nil
				}
				target, err = r.approve(t.Context(), "ARTEX", target.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			ips := c35PoolIPs()
			r.lookupIP = func(context.Context, string) ([]netip.Addr, error) { return ips, nil }
			contacts := &c35RecordingContacts{productContactJournal: r.contacts}
			r.contacts = contacts
			var active, started, finished atomic.Int64
			probe := func(ctx context.Context, ip string) error {
				started.Add(1)
				active.Add(1)
				defer func() { active.Add(-1); finished.Add(1) }()
				if !tc.allHealthy && ip != ips[len(ips)-1].String() {
					<-ctx.Done()
					return ctx.Err()
				}
				timer := time.NewTimer(500 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-timer.C:
					return nil
				}
			}
			r.probe = func(ctx context.Context, _ string, ip string, _ int) (string, time.Time, error) {
				err := probe(ctx, ip)
				return strings.Repeat("b", 64), time.Now().Add(time.Hour), err
			}
			r.reach = func(ctx context.Context, ip string, _ int) error { return probe(ctx, ip) }
			ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
			defer cancel()
			selected := ""
			if tc.stage == "approve" {
				var approved diagnosisprep.ProductTarget
				approved, err = r.approve(ctx, "ARTEX", target.ID)
				if err == nil {
					selected = r.records[approved.ID].SelectedIP
				}
			} else {
				rec, e := r.lookup("ARTEX", spec.URL, "")
				if e != nil {
					t.Fatal(e)
				}
				loaded := connectionTestProfile(r, rec)
				err = loaded.prepareTargetConnection(ctx, nil, "job-slow-pool")
				if loaded.productConnection != nil {
					selected = loaded.productConnection.IP
				}
			}
			if err != nil || selected == "" || ctx.Err() != nil {
				t.Fatal("느린 정상 주소가 있는 32주소 풀 거절", err)
			}
			if !tc.allHealthy && (selected != ips[len(ips)-1].String() || started.Load() != 32) {
				t.Fatal("마지막 정상 주소 미검사", selected, started.Load())
			}
			if active.Load() != 0 || finished.Load() != started.Load() {
				t.Fatal("선택 뒤 미종료 접촉", active.Load(), finished.Load(), started.Load())
			}
			attempts, completed := map[string]productTLSContact{}, map[string]productTLSContact{}
			for _, event := range contacts.events {
				if event.Phase == "attempt" {
					attempts[event.ID] = event
				} else {
					completed[event.ID] = event
				}
			}
			if len(attempts) != int(started.Load()) || len(completed) != len(attempts) {
				t.Fatal("시작한 접촉의 감사 마감 유실", len(attempts), len(completed), started.Load())
			}
			for id, before := range attempts {
				after, ok := completed[id]
				if !ok || before.IP != after.IP || before.JobID != after.JobID {
					t.Fatal("접촉 시도/결과 결속 유실")
				}
			}
		})
	}
}

type c35PoolJournal struct {
	productContactJournal
	fail         func(productTLSContact) bool
	after        func(productTLSContact)
	events       []productTLSContact
	broken       bool
	afterFailure int
}

func (j *c35PoolJournal) Append(raw json.RawMessage) error {
	if j.broken {
		j.afterFailure++
		return errors.New("owned failed journal retried")
	}
	var event productTLSContact
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	if j.fail != nil && j.fail(event) {
		j.broken = true
		return errors.New("owned synthetic disk failure")
	}
	if err := j.productContactJournal.Append(raw); err != nil {
		j.broken = true
		return err
	}
	j.events = append(j.events, event)
	if j.after != nil {
		j.after(event)
	}
	return nil
}

func c35StartPool(t *testing.T, ips []netip.Addr) (*productTargetRegistry, productTargetRecord, *urlLoadedProfile) {
	t.Helper()
	r, _, target := accountTestTarget(t, true)
	rec, err := r.lookup("ARTEX", target.Origin+"/read", "")
	if err != nil {
		t.Fatal(err)
	}
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) { return ips, nil }
	return r, rec, connectionTestProfile(r, rec)
}

func TestC35PoolWinnerWaitsForPeers(t *testing.T) {
	for _, kind := range []string{"audit-failure", "revoked", "certificate-expired"} {
		t.Run(kind, func(t *testing.T) {
			ips := c35PoolIPs()[:2]
			r, rec, loaded := c35StartPool(t, ips)
			now := time.Now().UTC()
			var clock atomic.Int64
			clock.Store(now.UnixNano())
			r.now = func() time.Time { return time.Unix(0, clock.Load()) }
			secondStarted := make(chan struct{})
			var active, finished atomic.Int64
			var revokeFailed atomic.Bool
			journal := &c35PoolJournal{productContactJournal: r.contacts}
			if kind == "audit-failure" {
				journal.fail = func(e productTLSContact) bool { return e.Phase == "completed" && e.IP == ips[1].String() }
			}
			r.contacts = journal
			r.probe = func(ctx context.Context, _ string, ip string, _ int) (string, time.Time, error) {
				active.Add(1)
				defer func() { active.Add(-1); finished.Add(1) }()
				if ip == ips[0].String() {
					select {
					case <-ctx.Done():
						return "", time.Time{}, ctx.Err()
					case <-secondStarted:
						return strings.Repeat("b", 64), now.Add(time.Second), nil
					}
				}
				close(secondStarted)
				<-ctx.Done()
				if kind == "revoked" {
					if _, err := r.revoke("ARTEX", rec.Target.ID); err != nil {
						revokeFailed.Store(true)
					}
				}
				if kind == "certificate-expired" {
					clock.Store(now.Add(time.Second).UnixNano())
				}
				return "", time.Time{}, ctx.Err()
			}
			ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
			defer cancel()
			err := loaded.prepareTargetConnection(ctx, nil, "job-pool-drain")
			if err == nil || loaded.productConnection != nil || active.Load() != 0 || finished.Load() != 2 || revokeFailed.Load() || ctx.Err() != nil {
				t.Fatal("선택 뒤 늦은 실패 무시 또는 미종료 접촉", err, active.Load(), finished.Load())
			}
			if kind == "audit-failure" && (!errors.Is(err, errPreparation) || journal.afterFailure != 0) {
				t.Fatal("마감 감사 실패를 선택 성공으로 가리거나 실패 저널 재시도", err)
			}
			if kind == "revoked" && err.Error() != "target_approval_changed" {
				t.Fatal("마감 중 철회 누락", err)
			}
			if kind == "certificate-expired" && !errors.Is(err, errTargetCertificateExpired) {
				t.Fatal("마감 중 만료된 인증서 채택", err)
			}
		})
	}
}

func TestC35PoolAuditFailureStopsAdmission(t *testing.T) {
	ips := c35PoolIPs()
	r, _, loaded := c35StartPool(t, ips)
	var attempted sync.Map
	journal := &c35PoolJournal{productContactJournal: r.contacts}
	attemptCount := 0
	journal.fail = func(e productTLSContact) bool {
		if e.Phase == "attempt" {
			attemptCount++
			return attemptCount == 3
		}
		return false
	}
	journal.after = func(e productTLSContact) {
		if e.Phase == "attempt" {
			attempted.Store(e.IP, true)
		}
	}
	r.contacts = journal
	var active, started, finished atomic.Int64
	var missingAudit atomic.Bool
	r.probe = func(ctx context.Context, _ string, ip string, _ int) (string, time.Time, error) {
		if _, ok := attempted.Load(ip); !ok {
			missingAudit.Store(true)
		}
		active.Add(1)
		started.Add(1)
		defer func() { active.Add(-1); finished.Add(1) }()
		<-ctx.Done()
		return "", time.Time{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	err := loaded.prepareTargetConnection(ctx, nil, "job-pool-audit")
	if !errors.Is(err, errPreparation) || loaded.productConnection != nil || started.Load() != 2 || active.Load() != 0 || finished.Load() != 2 || missingAudit.Load() {
		t.Fatal("감사 실패 뒤 admission 또는 접촉 전 감사/작업 종료 누락", err, started.Load(), active.Load())
	}
	if attemptCount != 3 || journal.afterFailure != 0 || len(journal.events) != 2 {
		t.Fatal("실패 저널 재시도 또는 미시도 주소 감사", attemptCount, journal.afterFailure, len(journal.events))
	}
}

func TestC35PoolDeadlineJoinsAllProbes(t *testing.T) {
	r, _, loaded := c35StartPool(t, c35PoolIPs())
	contacts := &c35RecordingContacts{productContactJournal: r.contacts}
	r.contacts = contacts
	var active, started, finished atomic.Int64
	r.probe = func(ctx context.Context, _ string, _ string, _ int) (string, time.Time, error) {
		active.Add(1)
		started.Add(1)
		defer func() { active.Add(-1); finished.Add(1) }()
		<-ctx.Done()
		return "", time.Time{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	err := loaded.prepareTargetConnection(ctx, nil, "job-pool-deadline")
	if !errors.Is(err, context.DeadlineExceeded) || loaded.productConnection != nil || started.Load() < 2 || started.Load() >= 32 || active.Load() != 0 || finished.Load() != started.Load() {
		t.Fatal("전체 기한/병렬 접촉 마감 위반", err, started.Load(), active.Load())
	}
	attempts, completed := map[string]bool{}, map[string]bool{}
	deadline, _ := ctx.Deadline()
	lastAttempt := ""
	for _, e := range contacts.events {
		if e.Phase == "attempt" {
			if !e.At.Before(deadline) {
				t.Fatal("전체 기한 뒤 새 주소의 접촉 시도 기록")
			}
			attempts[e.ID] = true
			lastAttempt = e.ID
		} else {
			if e.Result != "unverified" {
				t.Fatal("취소된 접촉 검증 성공 기록")
			}
			completed[e.ID] = true
		}
	}
	// A synchronous attempt append can cross the deadline before admission.
	// That final attempt stays incomplete; admitted workers still all finish.
	if len(attempts) < int(started.Load()) || len(completed) < int(started.Load()) || len(attempts)-len(completed) > 1 {
		t.Fatal("취소 접촉 마감 유실", len(attempts), len(completed), started.Load())
	}
	for id := range attempts {
		if !completed[id] && id != lastAttempt {
			t.Fatal("마지막 admission 전 시도 이외의 접촉 미마감")
		}
	}
	for id := range completed {
		if !attempts[id] {
			t.Fatal("시도 없는 취소 완료 기록")
		}
	}
}

func TestC35PoolCancellationAfterAuditPreventsContact(t *testing.T) {
	r, _, loaded := c35StartPool(t, c35PoolIPs())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	journal := &c35PoolJournal{productContactJournal: r.contacts, after: func(e productTLSContact) {
		if e.Phase == "attempt" {
			cancel()
		}
	}}
	r.contacts = journal
	var probes atomic.Int64
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		probes.Add(1)
		return strings.Repeat("b", 64), time.Now().Add(time.Hour), nil
	}
	err := loaded.prepareTargetConnection(ctx, nil, "job-pool-cancelled-at-audit")
	if !errors.Is(err, context.Canceled) || loaded.productConnection != nil || probes.Load() != 0 || len(journal.events) != 1 || journal.events[0].Phase != "attempt" {
		t.Fatal("감사 도중 취소 뒤 접촉 또는 가짜 완료", err, probes.Load(), len(journal.events))
	}
}

func TestC35PoolSelectionCancelsPendingProbe(t *testing.T) {
	ips := c35PoolIPs()[:2]
	r, _, loaded := c35StartPool(t, ips)
	secondStarted := make(chan struct{})
	peerResult := make(chan error, 1)
	r.probe = func(ctx context.Context, _ string, ip string, _ int) (string, time.Time, error) {
		if ip == ips[0].String() {
			select {
			case <-ctx.Done():
				return "", time.Time{}, ctx.Err()
			case <-secondStarted:
				return strings.Repeat("b", 64), time.Now().Add(time.Hour), nil
			}
		}
		close(secondStarted)
		<-ctx.Done()
		peerResult <- ctx.Err()
		return "", time.Time{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	if err := loaded.prepareTargetConnection(ctx, nil, "job-pool-select"); err != nil || loaded.productConnection == nil || loaded.productConnection.IP != ips[0].String() {
		t.Fatal("정상 선택 또는 마감 실패", err)
	}
	select {
	case err := <-peerResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("선택 뒤 대기 접촉을 취소하지 않고 자체 기한까지 기다림", err)
		}
	default:
		t.Fatal("선택 뒤 대기 접촉 미종료")
	}
}
