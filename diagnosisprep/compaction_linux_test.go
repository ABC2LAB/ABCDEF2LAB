//go:build linux

package diagnosisprep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func compactionFixture(t *testing.T) (*Service, *fakeBackend, string) {
	t.Helper()
	s, b, root := newFixture(t)
	first := create(t, s, "compact-first")
	start(t, s, first, "compact-start")
	awaitExecutionReturned(t, s, first.ID)
	return s, b, root
}
func compactWithFacts(t *testing.T, s *Service, hook compactionHook) error {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshots, err := preparationSnapshots(s.jobs)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]receipt{}
	for id, r := range s.jobs {
		expected[id] = cloneReceipt(r)
	}
	return s.journal.compact(t.Context(), snapshots, func(raw []json.RawMessage, n int) error {
		got, err := replayPreparationReceipts(t.Context(), raw, n)
		if err != nil || !reflect.DeepEqual(got, expected) {
			return errStorage
		}
		return nil
	}, hook)
}
func TestCompactionPreservesUnknownFactsReservationsAndReports(t *testing.T) {
	for _, drained := range []bool{false, true} {
		t.Run(strconv.FormatBool(drained), func(t *testing.T) {
			s, b, root := compactionFixture(t)
			b.inspection.MaxRuns = 2
			b.run = func(_ context.Context, _ Execution, p Publish) error {
				return p(Update{State: Unknown, Phase: Closure, PreparationStarted: true, AuthorityState: AuthorityNotIssued, DrainComplete: drained, ModelUsage: &ModelUsage{Requests: 1, ReservedInputTokens: 30, ReservedOutputTokens: 20}})
			}
			unknown := create(t, s, "compact-unknown")
			start(t, s, unknown, "compact-unknown-start")
			awaitExecutionReturned(t, s, unknown.ID)
			next := create(t, s, "compact-next")
			expected := map[string]receipt{}
			for id, r := range s.jobs {
				expected[id] = cloneReceipt(r)
			}
			oldBytes := s.journal.stat.Size
			if s.Shutdown(t.Context()) != nil {
				t.Fatal("종료 실패")
			}
			for attempt := 0; attempt < 3; attempt++ {
				restored, err := New(t.Context(), root, b)
				if err != nil {
					t.Fatal("압축 재기동 실패", err)
				}
				if !reflect.DeepEqual(restored.jobs, expected) || restored.journal.snapshotCount != len(expected) || restored.journal.stat.Size >= oldBytes {
					t.Fatal("압축이 결과·개인 증거·예약·작업 이력을 바꿈")
				}
				want := "busy"
				if drained {
					want = "budget_exhausted"
				}
				if got := start(t, restored, next, "compact-next-start").Error; got != want {
					t.Fatalf("압축 뒤 실행 제한: %s", got)
				}
				auth := request(AuthorizeTask)
				auth.TaskID = "42"
				if got := call(t, restored, auth); got.Authorized == nil || !*got.Authorized {
					t.Fatal("압축이 과거 task 소유·보고서 참조를 지움")
				}
				lookup := request(Lookup)
				lookup.IdempotencyKey = "compact-unknown"
				if got := call(t, restored, lookup).Job; got == nil || got.ID != unknown.ID || got.ResultReady {
					t.Fatal("조회 키 손실 또는 Unknown 성공 승격")
				}
				if restored.Shutdown(t.Context()) != nil {
					t.Fatal("압축 마감 실패")
				}
			}
			if b.calls.Load() != 2 {
				t.Fatal("압축이 실행을 재생")
			}
		})
	}
}

func TestCompactionFaultBeforePublishKeepsOriginalBytes(t *testing.T) {
	for _, stage := range []string{"temporary_directory", "journal_write", "journal_sync", "head_write", "head_sync", "directory_sync", "candidate_ready", "validate_snapshot", "publish"} {
		t.Run(stage, func(t *testing.T) {
			s, _, root := compactionFixture(t)
			beforeData, _ := os.ReadFile(filepath.Join(root, journalName))
			beforeHead, _ := os.ReadFile(filepath.Join(root, headName))
			if compactWithFacts(t, s, func(got string) error {
				if got == stage {
					return errors.New("owned synthetic storage failure")
				}
				return nil
			}) == nil {
				t.Fatal("주입한 압축 실패를 무시")
			}
			data, _ := os.ReadFile(filepath.Join(root, journalName))
			head, _ := os.ReadFile(filepath.Join(root, headName))
			if string(data) != string(beforeData) || string(head) != string(beforeHead) || !s.journal.current() {
				t.Fatal("실패한 압축이 옛 chain/head를 변경")
			}
			if _, err := os.Lstat(filepath.Join(root, currentJournalDirectory)); !os.IsNotExist(err) {
				t.Fatal("검증·동기화 전 압축 후보 채택")
			}
			entries, _ := os.ReadDir(root)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".preparation-compact-") {
					t.Fatal("미채택 후보 정리 실패")
				}
			}
		})
	}
}

func TestCompactionInterruptedPublishRecoversVerifiedWholeGeneration(t *testing.T) {
	for _, stage := range []string{"parent_sync", "legacy_guard"} {
		t.Run(stage, func(t *testing.T) {
			s, b, root := compactionFixture(t)
			expected := map[string]receipt{}
			for id, r := range s.jobs {
				expected[id] = cloneReceipt(r)
			}
			beforeData, _ := os.ReadFile(filepath.Join(root, journalName))
			beforeHead, _ := os.ReadFile(filepath.Join(root, headName))
			if compactWithFacts(t, s, func(got string) error {
				if got == stage {
					return errors.New("owned synthetic interruption")
				}
				return nil
			}) == nil || !s.journal.poisoned {
				t.Fatal("교체 후 동기화 불명을 성공으로 처리")
			}
			data, _ := os.ReadFile(filepath.Join(root, journalName))
			head, _ := os.ReadFile(filepath.Join(root, headName))
			if string(data) != string(beforeData) || string(head) != string(beforeHead) {
				t.Fatal("중단된 교체가 옛 자료를 지움")
			}
			crash(t, s)
			restored, err := New(t.Context(), root, b)
			if err != nil {
				t.Fatal("완전히 검증한 새 세대 복원 실패", err)
			}
			defer restored.Shutdown(t.Context())
			if !reflect.DeepEqual(restored.jobs, expected) || b.calls.Load() != 1 {
				t.Fatal("교체 중단이 예약·결과를 잃거나 실행을 재생")
			}
		})
	}
}

func TestCompactionSecondGenerationAndCleanupFailure(t *testing.T) {
	s, b, root := compactionFixture(t)
	if compactWithFacts(t, s, nil) != nil {
		t.Fatal("첫 압축")
	}
	create(t, s, "after-compaction")
	expected := map[string]receipt{}
	for id, r := range s.jobs {
		expected[id] = cloneReceipt(r)
	}
	if compactWithFacts(t, s, func(stage string) error {
		if stage == "cleanup" {
			return errors.New("owned synthetic cleanup failure")
		}
		return nil
	}) != nil {
		t.Fatal("검증된 새 세대를 정리 경고 때문에 버림")
	}
	if s.Shutdown(t.Context()) != nil {
		t.Fatal("종료")
	}
	restored, err := New(t.Context(), root, b)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Shutdown(t.Context())
	if !reflect.DeepEqual(restored.jobs, expected) || b.calls.Load() != 1 {
		t.Fatal("세대 교환이 이력을 잃거나 실행 재생")
	}
}

func TestCompactionCandidateMustPassReceiptReplay(t *testing.T) {
	for _, kind := range []string{"wrong_source", "duplicate_job", "missing_job"} {
		t.Run(kind, func(t *testing.T) {
			s, _, root := compactionFixture(t)
			create(t, s, "second-draft")
			raw, err := preparationSnapshots(s.jobs)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "wrong_source":
				var r receipt
				_ = json.Unmarshal(raw[0], &r)
				r.Job.SourceDigest = strings.Repeat("f", 64)
				raw[0], _ = json.Marshal(r)
			case "duplicate_job":
				raw[1] = raw[0]
			case "missing_job":
				raw = raw[:1]
			}
			beforeData, _ := os.ReadFile(filepath.Join(root, journalName))
			beforeHead, _ := os.ReadFile(filepath.Join(root, headName))
			if s.journal.compact(t.Context(), raw, func(records []json.RawMessage, count int) error {
				got, e := replayPreparationReceipts(t.Context(), records, count)
				if e != nil || !reflect.DeepEqual(got, s.jobs) {
					return errStorage
				}
				return nil
			}, nil) == nil {
				t.Fatal("서명이 아닌 chain 검증만으로 잘못된 영수증 채택")
			}
			data, _ := os.ReadFile(filepath.Join(root, journalName))
			head, _ := os.ReadFile(filepath.Join(root, headName))
			if string(data) != string(beforeData) || string(head) != string(beforeHead) {
				t.Fatal("잘못된 압축 후보가 옛 저널을 변경")
			}
		})
	}
}

func TestCompactionLegacyReaderCannotResetBudget(t *testing.T) {
	s, b, root := compactionFixture(t)
	if compactWithFacts(t, s, nil) != nil {
		t.Fatal("압축")
	}
	newer := create(t, s, "newer-than-legacy")
	if s.Shutdown(t.Context()) != nil {
		t.Fatal("종료")
	}
	if err := os.Rename(filepath.Join(root, currentJournalDirectory), filepath.Join(root, "hidden-generation")); err != nil {
		t.Fatal(err)
	}
	// 새 디렉터리를 모르는 옛 reader의 평면 파일 분기를 그대로 밟는다.
	if legacy, _, err := openJournal(root); err == nil {
		legacy.close()
		t.Fatal("옛 reader가 오래된 회계·빈 저널로 복원")
	}
	if err := os.Rename(filepath.Join(root, "hidden-generation"), filepath.Join(root, currentJournalDirectory)); err != nil {
		t.Fatal(err)
	}
	restored, err := New(t.Context(), root, b)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Shutdown(t.Context())
	if restored.jobs[newer.ID].Job.ID != newer.ID || b.calls.Load() != 1 {
		t.Fatal("옛 reader 대조가 새 작업을 잃거나 재실행")
	}
}

func TestCompactionGenerationCorruptionIsNeverRecreated(t *testing.T) {
	for _, kind := range []string{"missing_head", "missing_data", "mode", "symlink", "hardlink", "partial", "changed_head"} {
		t.Run(kind, func(t *testing.T) {
			s, b, root := compactionFixture(t)
			if compactWithFacts(t, s, nil) != nil {
				t.Fatal("압축")
			}
			if s.Shutdown(t.Context()) != nil {
				t.Fatal("종료")
			}
			dir := filepath.Join(root, currentJournalDirectory)
			data := filepath.Join(dir, journalName)
			head := filepath.Join(dir, headName)
			var err error
			switch kind {
			case "missing_head":
				err = os.Remove(head)
			case "missing_data":
				err = os.Remove(data)
			case "mode":
				err = os.Chmod(data, 0644)
			case "symlink":
				other := filepath.Join(root, "other")
				raw, _ := os.ReadFile(data)
				if err = os.WriteFile(other, raw, 0600); err == nil {
					err = os.Remove(data)
				}
				if err == nil {
					err = os.Symlink(other, data)
				}
			case "hardlink":
				err = os.Link(data, filepath.Join(root, "other"))
			case "partial":
				raw, _ := os.ReadFile(data)
				err = os.WriteFile(data, raw[:len(raw)-1], 0600)
			case "changed_head":
				err = os.WriteFile(head, []byte(`{"sequence":1,"digest":"invalid"}`), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, err := New(t.Context(), root, b); err == nil {
				got.Shutdown(t.Context())
				t.Fatal("손상 압축 세대를 초기화·부분 성공으로 복원")
			}
			if b.calls.Load() != 1 {
				t.Fatal("손상된 저널에서 실행")
			}
		})
	}
}

// 실제 비밀·대상 자료가 없는 자체 생성 유효 이력으로 최대 부팅 자원을 측정한다.
func seedCompactionResourceJournal(t *testing.T, root string, jobs int) int64 {
	t.Helper()
	data, err := os.OpenFile(filepath.Join(root, journalName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	seq := uint64(0)
	prev := ""
	var total int64
	write := func(raw json.RawMessage) {
		t.Helper()
		rec := journalRecord{Sequence: seq + 1, Previous: prev, Payload: raw}
		rec.Digest = recordDigest(rec.Sequence, prev, raw)
		line, e := json.Marshal(rec)
		if e != nil {
			t.Fatal(e)
		}
		line = append(line, '\n')
		if n, e := data.Write(line); e != nil || n != len(line) {
			t.Fatal("fixture write")
		}
		seq, prev, total = rec.Sequence, rec.Digest, total+int64(len(line))
	}
	write(json.RawMessage(`{"format":"diagnosis-preparation-journal.v1"}`))
	inspection := fixtureInspection()
	inspection.MaxRuns = 100000
	for i := 0; i < 9; i++ {
		inspection.Scope.PathPrefixes = append(inspection.Scope.PathPrefixes, fmt.Sprintf("/scope%d/", i)+strings.Repeat("a", 160))
	}
	input := Input{TargetURL: "https://example.test/read", ProfileID: "local"}
	for i := 0; i < jobs; i++ {
		id := fmt.Sprintf("resource-%06d", i)
		r := receipt{Principal: "owner", CreateKey: id, Inspection: storeInspection(inspection), Job: Job{ID: id, Revision: 1, Input: input, State: Ready, Phase: DraftPhase, Scope: inspection.Scope, SourceDigest: sourceDigest(input, inspection.InstallationDigest), AuthorityState: AuthorityNotIssued}}
		raw, _ := json.Marshal(r)
		write(raw)
		r.StartKey = "start-" + id
		r.ExecutionEvidenceVersion = 1
		r.ExecutionBootID = executionBootID()
		r.Job.Revision++
		r.Job.State = Preparing
		r.Job.Phase = ScopeCheck
		r.Job.PreparationStarted = true
		raw, _ = json.Marshal(r)
		write(raw)
		r.ExecutionReturned = true
		r.Job.Revision++
		r.Job.State = Unknown
		r.Job.Phase = Closure
		r.Job.DrainComplete = true
		r.Job.ReasonCode = "model_usage_unknown"
		raw, _ = json.Marshal(r)
		write(raw)
	}
	if data.Sync() != nil || data.Close() != nil {
		t.Fatal("fixture sync")
	}
	raw, _ := json.Marshal(journalHead{seq, prev})
	if os.WriteFile(filepath.Join(root, headName), raw, 0600) != nil {
		t.Fatal("fixture head")
	}
	return total
}
func TestCompactionMaximumSupportedWorkload(t *testing.T) {
	if os.Getenv("ARTEX_PREPARATION_RESOURCE_TEST") != "1" {
		t.Skip("별도 자원 측정 opt-in")
	}
	root := privateDir(t)
	const supportedJobs = 8192
	beforeBytes := seedCompactionResourceJournal(t, root, supportedJobs)
	if beforeBytes > maxPreparationJournalBytes {
		t.Fatal("fixture exceeds supported journal bound")
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	start := time.Now()
	b := &fakeBackend{inspection: fixtureInspection()}
	s, err := New(ctx, root, b)
	if err != nil {
		t.Fatal("최대 지원 이력 복원 실패", err, "context", ctx.Err())
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	if len(s.jobs) != supportedJobs || s.journal.snapshotCount != supportedJobs || b.calls.Load() != 0 {
		t.Fatal("최대 지원 작업 손실·재실행")
	}
	var peakKiB uint64
	if f, err := os.Open("/proc/self/status"); err == nil {
		raw, _ := io.ReadAll(f)
		f.Close()
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "VmHWM:") {
				fmt.Sscanf(line, "VmHWM: %d kB", &peakKiB)
			}
		}
	}
	t.Logf("compaction_resource jobs=%d before_bytes=%d after_bytes=%d elapsed_ms=%d allocated_bytes=%d heap_bytes=%d peak_process_kib=%d", supportedJobs, beforeBytes, s.journal.stat.Size, elapsed.Milliseconds(), after.TotalAlloc-before.TotalAlloc, after.HeapAlloc, peakKiB)
	if s.Shutdown(t.Context()) != nil {
		t.Fatal("자원 시험 마감")
	}
}

func TestCompactionCandidateMustPreserveSourceAnchor(t *testing.T) {
	s, _, root := compactionFixture(t)
	hook := func(stage string) error {
		if stage != "candidate_ready" {
			return nil
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".preparation-compact-") {
				dir := filepath.Join(root, entry.Name())
				raw, err := os.ReadFile(filepath.Join(dir, journalName))
				if err != nil {
					return err
				}
				lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
				previous := ""
				var sequence uint64
				var output strings.Builder
				for index, line := range lines {
					var rec journalRecord
					if json.Unmarshal([]byte(line), &rec) != nil {
						return errStorage
					}
					if index == 0 {
						var header compactionHeader
						if json.Unmarshal(rec.Payload, &header) != nil {
							return errStorage
						}
						header.SourceDigest = strings.Repeat("e", 64)
						header.SourceSequence++
						rec.Payload, _ = json.Marshal(header)
					}
					rec.Previous = previous
					rec.Digest = recordDigest(rec.Sequence, previous, rec.Payload)
					encoded, _ := json.Marshal(rec)
					output.Write(encoded)
					output.WriteByte('\n')
					previous, sequence = rec.Digest, rec.Sequence
				}
				if os.WriteFile(filepath.Join(dir, journalName), []byte(output.String()), 0600) != nil {
					return errStorage
				}
				head, _ := json.Marshal(journalHead{sequence, previous})
				return os.WriteFile(filepath.Join(dir, headName), head, 0600)
			}
		}
		return errStorage
	}
	if compactWithFacts(t, s, hook) == nil {
		t.Fatal("새 저널의 옛 chain 연결 증거가 달라도 채택")
	}
	if _, err := os.Stat(filepath.Join(root, currentJournalDirectory)); !os.IsNotExist(err) {
		t.Fatal("다른 source anchor를 게시")
	}
}

func TestPreparationByteLimitDoesNotChangeOtherJournals(t *testing.T) {
	root := privateDir(t)
	size := seedCompactionResourceJournal(t, root, 2400)
	if size <= maxJournalBytes || size >= maxPreparationJournalBytes {
		t.Fatal("분리 상한 대조 fixture 범위")
	}
	if generic, _, err := openJournal(root); err == nil {
		generic.close()
		t.Fatal("대상·계정 저널의 기존 32MiB 상한 확대")
	}
	preparation, _, err := openPreparationJournal(root)
	if err != nil {
		t.Fatal("준비 저널만 확대한 바이트 상한 거절", err)
	}
	preparation.close()
	if err := os.Truncate(filepath.Join(root, journalName), maxPreparationJournalBytes+1); err != nil {
		t.Fatal(err)
	}
	var beforeRead, afterRead runtime.MemStats
	runtime.ReadMemStats(&beforeRead)
	if opened, _, err := openPreparationJournal(root); err == nil {
		opened.close()
		t.Fatal("준비 바이트 상한 초과를 읽음")
	}
	runtime.ReadMemStats(&afterRead)
	if afterRead.TotalAlloc-beforeRead.TotalAlloc > 4<<20 {
		t.Fatal("상한 초과 파일을 거부 전에 메모리로 읽음")
	}
}

func TestCompactionResourceFixtureAtSmallScale(t *testing.T) {
	root := privateDir(t)
	seedCompactionResourceJournal(t, root, 16)
	b := &fakeBackend{inspection: fixtureInspection()}
	s, err := New(t.Context(), root, b)
	if err != nil {
		t.Fatal("자원 fixture 의미 검증 실패", err)
	}
	defer s.Shutdown(t.Context())
	if len(s.jobs) != 16 || s.journal.snapshotCount != 16 || b.calls.Load() != 0 {
		t.Fatal("fixture 복원·압축 의미 불일치")
	}
}
func TestCompactionCancelledContextKeepsOriginal(t *testing.T) {
	s, _, root := compactionFixture(t)
	raw, err := preparationSnapshots(s.jobs)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(root, journalName))
	head, _ := os.ReadFile(filepath.Join(root, headName))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if s.journal.compact(ctx, raw, func([]json.RawMessage, int) error { return nil }, nil) == nil {
		t.Fatal("취소된 압축 채택")
	}
	after, _ := os.ReadFile(filepath.Join(root, journalName))
	afterHead, _ := os.ReadFile(filepath.Join(root, headName))
	if string(before) != string(after) || string(head) != string(afterHead) {
		t.Fatal("취소된 압축이 원본 변경")
	}
}

func seedMinimalPreparationJobs(t *testing.T, root string, count int) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(root, journalName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	sequence := uint64(0)
	digest := ""
	write := func(payload json.RawMessage) {
		t.Helper()
		rec := journalRecord{Sequence: sequence + 1, Previous: digest, Payload: payload}
		rec.Digest = recordDigest(rec.Sequence, digest, payload)
		raw, _ := json.Marshal(rec)
		raw = append(raw, '\n')
		if n, e := f.Write(raw); e != nil || n != len(raw) {
			t.Fatal("fixture write")
		}
		sequence, digest = rec.Sequence, rec.Digest
	}
	write(json.RawMessage(`{"format":"diagnosis-preparation-journal.v1"}`))
	input := Input{TargetURL: "https://example.test/read", ProfileID: "local"}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("bounded-%05d", i)
		r := receipt{Principal: "owner", CreateKey: id, Job: Job{ID: id, Revision: 1, Input: input, SourceDigest: sourceDigest(input, ""), State: AwaitingApproval, Phase: DraftPhase, AuthorityState: AuthorityNotIssued}}
		raw, _ := json.Marshal(r)
		write(raw)
	}
	if f.Sync() != nil || f.Close() != nil {
		t.Fatal("fixture sync")
	}
	raw, _ := json.Marshal(journalHead{sequence, digest})
	if os.WriteFile(filepath.Join(root, headName), raw, 0600) != nil {
		t.Fatal("fixture head")
	}
}
func TestPreparationWorkloadCapacityBoundaries(t *testing.T) {
	for _, count := range []int{8192, 8193} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			root := privateDir(t)
			seedMinimalPreparationJobs(t, root, count)
			before, _ := os.ReadFile(filepath.Join(root, journalName))
			head, _ := os.ReadFile(filepath.Join(root, headName))
			b := &fakeBackend{inspection: fixtureInspection()}
			s, err := New(t.Context(), root, b)
			if count == 8193 {
				if err == nil {
					s.Shutdown(t.Context())
					t.Fatal("작업 개수 코드 상한 초과 수용")
				}
				after, _ := os.ReadFile(filepath.Join(root, journalName))
				afterHead, _ := os.ReadFile(filepath.Join(root, headName))
				if string(after) != string(before) || string(afterHead) != string(head) {
					t.Fatal("지원 개수 초과가 기존 저널 변경")
				}
			} else {
				if err != nil || len(s.jobs) != 8192 {
					t.Fatal("지원한 작업 수 복원 실패", err)
				}
				defer s.Shutdown(t.Context())
				req := request(Create)
				req.IdempotencyKey = "capacity-next"
				req.Input = &Input{TargetURL: "https://example.test/read", ProfileID: "local"}
				if response := call(t, s, req); response.Error != "capacity" {
					t.Fatal("복원 뒤 새 작업이 전체 자원 상한 우회")
				}
			}
			if b.calls.Load() != 0 {
				t.Fatal("용량 검사로 backend 실행")
			}
		})
	}
}
