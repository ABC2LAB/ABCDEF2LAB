package e2host

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/Autumn-27/artex/e2checkpoint"
	"github.com/Autumn-27/artex/e2proxy"
)

const maxSnapshot = 256 << 20

// persistHead는 DB ACK와 별도 Host 볼륨의 현재 head를 맞춘다. 두 저장소 중
// 하나라도 과거로 복원되면 다음 기동에서 불일치로 중지한다.
func (r *run) persistHead(ctx context.Context) error {
	if r == nil || r.directory == "" {
		return ErrUnavailable
	}
	head, err := r.owner.store.Head(ctx, e2checkpoint.Query{Epoch: r.owner.epoch, Binding: r.binding})
	if err != nil {
		return ErrUnavailable
	}
	data, err := json.Marshal(head)
	if err != nil {
		return ErrUnavailable
	}
	return writePrivateFile(r.directory, "head", data)
}

// writePrivateFile은 <name>.json을 같은 디렉터리의 임시 파일로 쓰고 Sync한 뒤 원자적으로 바꾼다.
func writePrivateFile(directory, name string, data []byte) error {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ErrUnavailable
	}
	tmp := filepath.Join(directory, name+"."+hex.EncodeToString(nonce[:])+".tmp")
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrUnavailable
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	if os.Rename(tmp, filepath.Join(directory, name+".json")) != nil {
		return ErrUnavailable
	}
	dir, err := os.Open(directory)
	if err != nil {
		return ErrUnavailable
	}
	err = dir.Sync()
	_ = dir.Close()
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func readSnapshot(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() <= 0 || info.Size() > maxSnapshot {
		return nil, ErrUnavailable
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSnapshot+1))
	if err != nil || len(data) != int(info.Size()) {
		return nil, ErrUnavailable
	}
	return data, nil
}

// completeAudit는 DB의 현재 head와 Host 볼륨의 복사본 및 전체 MAC 체인을
// 대조하고, 송신 의도마다 결과가 게시됐는지 확인한다. HTTP 결과 불명도
// 결과 사건이 인증됐다면 마감할 수 있지만, 결과 사건 자체가 없으면 막는다.
func (r *run) completeAudit(ctx context.Context, phase e2checkpoint.Phase) error {
	events, head, err := r.verifiedEvents(ctx)
	if err != nil || head.Phase != phase || !allAttemptsFinished(events) {
		return ErrUnavailable
	}
	return nil
}

// verifiedEvents는 Host 전용 파일의 한 스냅샷을 현재 epoch/head와 대조한 뒤
// 같은 바이트에서 MAC 체인을 검증한다. 호출자는 run 잠금을 보유한다.
func (r *run) verifiedEvents(ctx context.Context) ([]e2proxy.Event, e2checkpoint.Head, error) {
	snapshot, current, err := r.verifiedSnapshot(ctx)
	if err != nil {
		return nil, e2checkpoint.Head{}, err
	}
	defer clear(snapshot)
	events, _, err := e2proxy.VerifyJournalEvents(ctx, bytes.NewReader(snapshot), r.owner.key, current.Checkpoint)
	if err != nil {
		return nil, e2checkpoint.Head{}, ErrUnavailable
	}
	return events, current, nil
}

// verifiedSnapshot은 Host 볼륨의 head 복사본·저널 스냅샷을 현재 epoch/head(VerifyCurrent)와 대조한 바이트를
// 돌려준다. 호출자가 다 쓴 뒤 지운다.
func (r *run) verifiedSnapshot(ctx context.Context) ([]byte, e2checkpoint.Head, error) {
	mirror, err := readSnapshot(filepath.Join(r.directory, "head.json"))
	if err != nil {
		return nil, e2checkpoint.Head{}, ErrUnavailable
	}
	var expected e2checkpoint.Head
	if json.Unmarshal(mirror, &expected) != nil || expected.Epoch != r.owner.epoch ||
		expected.Binding != r.binding {
		return nil, e2checkpoint.Head{}, ErrUnavailable
	}
	snapshot, err := readSnapshot(filepath.Join(r.directory, "events.jsonl"))
	if err != nil {
		return nil, e2checkpoint.Head{}, ErrUnavailable
	}
	_, current, err := r.owner.store.VerifyCurrent(ctx,
		e2checkpoint.Query{Epoch: r.owner.epoch, Binding: r.binding}, bytes.NewReader(snapshot))
	if err != nil || current != expected {
		clear(snapshot)
		return nil, e2checkpoint.Head{}, ErrUnavailable
	}
	return snapshot, current, nil
}

// allAttemptsFinished는 실제 송신에 앞서는 intent ACK와 마지막 result를
// 같은 시도에 결속한다. 전송 전 거부에는 intent 없이 result만 남을 수 있다.
func allAttemptsFinished(events []e2proxy.Event) bool {
	pending, valid := unfinishedAttempts(events)
	return valid && len(pending) == 0
}

// unfinishedAttempts validates pairing without guessing whether an unpaired
// intent was sent. Malformed pairings are not eligible for recovery.
func unfinishedAttempts(events []e2proxy.Event) ([]string, bool) {
	pending := make(map[string]e2proxy.Event)
	finished := make(map[string]bool)
	for _, event := range events {
		switch event.Stage {
		case "intent":
			if _, exists := pending[event.AttemptID]; exists || finished[event.AttemptID] {
				return nil, false
			}
			pending[event.AttemptID] = event
		case "result":
			if finished[event.AttemptID] {
				return nil, false
			}
			intent, exists := pending[event.AttemptID]
			if exists {
				if intent.Program != event.Program || intent.IntentID != event.IntentID ||
					intent.Scope != event.Scope || intent.CallRef != event.CallRef ||
					intent.CommandRef != event.CommandRef || intent.PolicyRevision != event.PolicyRevision ||
					intent.RuleRef != event.RuleRef || intent.Sequence != event.Sequence || intent.Method != event.Method ||
					!sameE3IntentBinding(intent.E3Audit, event.E3Audit) {
					return nil, false
				}
				delete(pending, event.AttemptID)
			} else if event.Decision == "allowed" || !event.NotSent || event.ForwardAttempted ||
				event.RequestWritten || event.ResponseReceived {
				return nil, false
			}
			finished[event.AttemptID] = true
		}
	}
	refs := make([]string, 0, len(pending))
	for ref := range pending {
		refs = append(refs, ref)
	}
	slices.Sort(refs)
	return refs, true
}

func sameE3IntentBinding(intent, result *e2proxy.E3Audit) bool {
	if intent == nil || result == nil {
		return intent == nil && result == nil
	}
	return intent.Role == result.Role && intent.AccountLabel == result.AccountLabel &&
		intent.AccountBindingRef == result.AccountBindingRef && intent.E3 == result.E3 &&
		intent.ResourceType == result.ResourceType && intent.CreateRef == result.CreateRef
}

// CheckRestoredHeads는 과거 저널과 현재 head를 검사한다. 미봉인 회차를
// 이 읽기 전용 검사에서 자동 마감하지 않는다.
func (h *Host) CheckRestoredHeads(ctx context.Context) error {
	if h == nil {
		return ErrUnavailable
	}
	h.scanMu.Lock()
	defer h.scanMu.Unlock()
	return h.checkRestoredHeads(ctx, false, "", nil)
}

// RecoverDisconnectedHeads는 소켓 단절 뒤 또는 독점 Host 프로세스 잠금을
// 새로 잡은 기동 경로에서만 호출한다. 열려 있는 실행은 절대 복구하지 않는다.
// 송신 의도에 결과가 없거나 저장소 ACK가 불명이면 마감을 거부한다.
func (h *Host) RecoverDisconnectedHeads(ctx context.Context) error {
	if h == nil {
		return ErrUnavailable
	}
	h.scanMu.Lock()
	defer h.scanMu.Unlock()
	return h.checkRestoredHeads(ctx, true, "", nil)
}

// DB와 Host 볼륨을 함께 같은 과거 시점으로 되돌린 경우는 독립적인
// 외부 단조 anchor 없이는 탐지할 수 없으므로 운영 복구 대상이다.
func (h *Host) checkRestoredHeads(ctx context.Context, recoverDisconnected bool, recoveryTarget string, status *RecoveryStatus) error {
	if h == nil || h.db == nil || ctx == nil || ctx.Err() != nil {
		return ErrUnavailable
	}
	h.mu.Lock()
	active := len(h.active)
	h.mu.Unlock()
	if active != 0 {
		return ErrUnavailable
	}
	rows, err := h.db.QueryContext(ctx, `SELECT run_ref FROM e2_http_audit_heads WHERE store_ref=$1`, h.epoch.StoreRef)
	if err != nil {
		return ErrUnavailable
	}
	stored := make(map[string]bool)
	targetSeen := recoveryTarget == ""
	for rows.Next() {
		var runRef string
		if rows.Scan(&runRef) != nil || runRef == "" || stored[runRef] {
			_ = rows.Close()
			return ErrUnavailable
		}
		stored[runRef] = true
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if rowErr != nil {
		return ErrUnavailable
	}
	entries, err := os.ReadDir(h.root)
	if err != nil {
		return ErrUnavailable
	}
	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) != sha256.Size*2 {
			return ErrUnavailable
		}
		directory := filepath.Join(h.root, entry.Name())
		mirror, err := readSnapshot(filepath.Join(directory, "head.json"))
		if err != nil {
			return ErrUnavailable
		}
		var expected e2checkpoint.Head
		if json.Unmarshal(mirror, &expected) != nil || expected.Epoch != h.epoch || expected.Binding.KeyRef != h.keyRef {
			return ErrUnavailable
		}
		if expected.Phase != e2checkpoint.Sealed && expected.Phase != e2checkpoint.Interrupted &&
			expected.Phase != e2checkpoint.Open {
			return ErrUnavailable
		}
		digest := sha256.Sum256([]byte(expected.Binding.Scope.RunRef))
		if entry.Name() != hex.EncodeToString(digest[:]) {
			return ErrUnavailable
		}
		if !stored[expected.Binding.Scope.RunRef] {
			return ErrUnavailable
		}
		if expected.Binding.Scope.RunRef == recoveryTarget {
			targetSeen = true
		}
		delete(stored, expected.Binding.Scope.RunRef)
		rctx, cancel := context.WithCancel(context.Background())
		r := &run{owner: h, scope: expected.Binding.Scope, binding: expected.Binding, directory: directory, ctx: rctx, cancel: cancel}
		h.mu.Lock()
		if h.active[r.scope.RunRef] != nil {
			h.mu.Unlock()
			cancel()
			return ErrUnavailable
		}
		h.active[r.scope.RunRef] = r
		h.mu.Unlock()
		actual, headErr := h.store.Head(ctx, e2checkpoint.Query{Epoch: h.epoch, Binding: r.binding})
		matched := headErr == nil && actual == expected
		acceptedOpen := false
		if matched && expected.Phase == e2checkpoint.Open {
			record, exists, recordErr := r.readRecoveryRecord()
			if recordErr != nil {
				err = ErrUnavailable
			} else if exists {
				var events []e2proxy.Event
				events, _, err = r.verifiedEvents(ctx)
				if err == nil {
					err = h.verifyRecoveryRecord(record, expected, events)
				}
				acceptedOpen = err == nil
				if acceptedOpen && status != nil && recoveryTarget == r.scope.RunRef {
					status.Head, status.Pending, status.Recovered = expected, append([]string(nil), record.Pending...), true
				}
			} else if recoverDisconnected && r.completeAudit(ctx, e2checkpoint.Open) == nil {
				h.mu.Lock()
				r.interruptOK = true
				h.mu.Unlock()
				var snapshot []byte
				snapshot, err = readSnapshot(filepath.Join(directory, "events.jsonl"))
				if err == nil {
					actual, err = h.store.Publish(ctx, e2checkpoint.Request{Epoch: h.epoch,
						Binding: r.binding, Kind: e2checkpoint.Interrupt, ExpectedRevision: expected.Revision,
						Previous: expected.Checkpoint, Candidate: expected.Checkpoint}, bytes.NewReader(snapshot))
					clear(snapshot)
				}
				if err == nil {
					err = r.persistHead(ctx)
				}
				if err == nil {
					expected = actual
					// 끊긴 실행이 내준 근거도 interrupted head와 대조해 따로 남긴다. 정상 봉인이 아니므로 화면은
					// 포함돼도 "봉인 미확인"이다. 남기지 못해도 마감은 그대로 진행한다(기록 없음 = 미확인).
					if issued, readErr := readIssued(directory); readErr == nil {
						_, _ = r.phaseChecks(ctx, e2checkpoint.Interrupted, issued)
					}
				}
			} else if recoveryTarget == r.scope.RunRef {
				var events []e2proxy.Event
				events, _, err = r.verifiedEvents(ctx)
				if err == nil {
					var pending []string
					var valid bool
					pending, valid = unfinishedAttempts(events)
					if !valid || len(pending) == 0 {
						err = ErrUnavailable
					} else if status != nil {
						status.Head, status.Pending, status.PendingStateChange =
							expected, pending, pendingStateChange(events, pending)
					}
				}
				acceptedOpen = err == nil
			} else if recoveryTarget != "" {
				// A targeted operator inspection may pass another unresolved open
				// run, but it must still validate that peer's current head and
				// journal. Normal Host admission remains blocked until every open
				// run has a valid recovery record.
				var events []e2proxy.Event
				events, _, err = r.verifiedEvents(ctx)
				if err == nil {
					var pending []string
					var valid bool
					pending, valid = unfinishedAttempts(events)
					if !valid || len(pending) == 0 {
						err = ErrUnavailable
					}
				}
				acceptedOpen = err == nil
			} else {
				err = ErrUnavailable
			}
		} else if matched {
			err = r.completeAudit(ctx, expected.Phase)
		}
		h.remove(r)
		if !matched || err != nil ||
			(actual.Phase != e2checkpoint.Sealed && actual.Phase != e2checkpoint.Interrupted && !acceptedOpen) || actual != expected {
			return ErrUnavailable
		}
	}
	if len(stored) != 0 || !targetSeen {
		return ErrUnavailable
	}
	return nil
}
