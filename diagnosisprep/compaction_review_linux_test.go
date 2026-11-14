//go:build linux

package diagnosisprep

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func copyPreparationJobs(jobs map[string]receipt) map[string]receipt {
	out := make(map[string]receipt, len(jobs))
	for id, r := range jobs {
		out[id] = cloneReceipt(r)
	}
	return out
}

// Rewrite only owned synthetic fixtures and rebuild a valid chain/head, so receipt
// equivalence and format checks are exercised independently of digest corruption.
func rewriteCompactionFixture(t *testing.T, dir string, edit func(int, json.RawMessage) json.RawMessage) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, journalName))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var seq uint64
	prev := ""
	for i, line := range bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'}) {
		var rec journalRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatal(err)
		}
		rec.Payload = edit(i, rec.Payload)
		rec.Previous = prev
		rec.Digest = recordDigest(rec.Sequence, prev, rec.Payload)
		raw, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(raw)
		out.WriteByte('\n')
		seq, prev = rec.Sequence, rec.Digest
	}
	if err := os.WriteFile(filepath.Join(dir, journalName), out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	head, err := json.Marshal(journalHead{seq, prev})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, headName), head, 0600); err != nil {
		t.Fatal(err)
	}
}

func compactionCandidateDirectory(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".preparation-compact-") {
			return filepath.Join(root, e.Name())
		}
	}
	t.Fatal("압축 후보 없음")
	return ""
}

func TestStartupCompactionIOFailureUsesVerifiedOriginal(t *testing.T) {
	stages := []string{"temporary_directory", "candidate_directory_open", "journal_write", "journal_sync", "head_write", "head_sync", "directory_sync", "candidate_ready", "validate_snapshot", "candidate_recheck", "publish"}
	for _, generation := range []string{"flat", "snapshot"} {
		for _, stage := range stages {
			t.Run(generation+"/"+stage, func(t *testing.T) {
				s, b, root := compactionFixture(t)
				b.run = func(ctx context.Context, e Execution, p Publish) error {
					task := strconv.FormatInt(100+b.calls.Load(), 10)
					return completeRun(ctx, e, func(u Update) error {
						if u.TaskID != "" {
							u.TaskID = task
						}
						return p(u)
					})
				}
				if generation == "snapshot" {
					if err := compactWithFacts(t, s, nil); err != nil {
						t.Fatal(err)
					}
					next := create(t, s, "second-generation")
					if got := start(t, s, next, "second-generation-start"); got.Error != "" {
						t.Fatal(got.Error)
					}
					awaitExecutionReturned(t, s, next.ID)
				}
				expected := copyPreparationJobs(s.jobs)
				originalDir, originalCount := s.journal.root.path, s.journal.snapshotCount
				beforeData, err := os.ReadFile(filepath.Join(originalDir, journalName))
				if err != nil {
					t.Fatal(err)
				}
				beforeHead, err := os.ReadFile(filepath.Join(originalDir, headName))
				if err != nil {
					t.Fatal(err)
				}
				calls := b.calls.Load()
				if err := s.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
				reached := false
				restored, err := newServiceForBootWithCompactionHook(t.Context(), root, b, executionBootID(), func(at string) error {
					if at == stage {
						reached = true
						return errors.New("owned startup I/O failure")
					}
					return nil
				})
				if restored != nil {
					defer restored.Shutdown(t.Context())
				}
				if err != nil || !reached {
					t.Fatal("게시 전 I/O 실패가 검증된 옛 저널의 기동을 막음", err, reached)
				}
				if !reflect.DeepEqual(restored.jobs, expected) || b.calls.Load() != calls || restored.journal.poisoned || !restored.journal.current() || restored.journal.root.path != originalDir || restored.journal.snapshotCount != originalCount {
					t.Fatal("압축 보류가 원래 작업·회계·저널을 바꿈")
				}
				data, err := os.ReadFile(filepath.Join(originalDir, journalName))
				if err != nil {
					t.Fatal(err)
				}
				head, err := os.ReadFile(filepath.Join(originalDir, headName))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(data, beforeData) || !bytes.Equal(head, beforeHead) {
					t.Fatal("기동 보류가 옛 저널 바이트 변경")
				}
				entries, err := os.ReadDir(root)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".preparation-compact-") {
						t.Fatal("기동 보류 뒤 압축 후보 잔류")
					}
				}
				next := create(t, restored, "continue-original")
				if got := start(t, restored, next, "continue-original-start"); got.Error != "" {
					t.Fatal("검증된 옛 저널로 실행 재개 실패", got.Error)
				}
				awaitExecutionReturned(t, restored, next.ID)
				final := copyPreparationJobs(restored.jobs)
				if err := restored.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
				reopened, err := New(t.Context(), root, b)
				if err != nil {
					t.Fatal("보류 뒤 회계 복원 실패", err)
				}
				defer reopened.Shutdown(t.Context())
				if !reflect.DeepEqual(reopened.jobs, final) || b.calls.Load() != calls+1 {
					t.Fatal("보류 뒤 작업 유실 또는 실행 재생")
				}
			})
		}
	}
}

func TestStartupCompactionRejectsChangedValidAccounting(t *testing.T) {
	for _, kind := range []string{"model_usage", "aggregate_budget", "execution_returned"} {
		t.Run(kind, func(t *testing.T) {
			s, b, root := compactionFixture(t)
			expected := copyPreparationJobs(s.jobs)
			if err := s.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			changedValidCandidate := false
			restored, err := newServiceForBootWithCompactionHook(t.Context(), root, b, executionBootID(), func(stage string) error {
				if stage != "candidate_ready" {
					return nil
				}
				dir := compactionCandidateDirectory(t, root)
				var candidate []json.RawMessage
				rewriteCompactionFixture(t, dir, func(index int, raw json.RawMessage) json.RawMessage {
					if index == 0 {
						return raw
					}
					var r receipt
					if err := json.Unmarshal(raw, &r); err != nil {
						t.Fatal(err)
					}
					switch kind {
					case "model_usage":
						r.Job.ModelUsage = &ModelUsage{Requests: 1, ReservedInputTokens: 30, ReservedOutputTokens: 20}
					case "aggregate_budget":
						r.Inspection.AggregateBudget.Requests++
					case "execution_returned":
						r.ExecutionReturned = false
					}
					encoded, err := json.Marshal(r)
					if err != nil {
						t.Fatal(err)
					}
					candidate = append(candidate, encoded)
					return encoded
				})
				got, err := replayPreparationReceipts(t.Context(), candidate, len(candidate))
				if err != nil || reflect.DeepEqual(got, expected) {
					t.Fatal("회계만 다른 유효 후보 대조 실패", err)
				}
				changedValidCandidate = true
				return nil
			})
			if restored != nil {
				defer restored.Shutdown(t.Context())
			}
			if err == nil || !changedValidCandidate {
				t.Fatal("회계 값만 바뀐 유효 압축 후보로 기동 허용")
			}
			if _, err := os.Stat(filepath.Join(root, currentJournalDirectory)); !os.IsNotExist(err) {
				t.Fatal("회계가 다른 후보 게시")
			}
			normal, err := New(t.Context(), root, b)
			if err != nil {
				t.Fatal("원본 저널 손상", err)
			}
			defer normal.Shutdown(t.Context())
			if !reflect.DeepEqual(normal.jobs, expected) || b.calls.Load() != 1 {
				t.Fatal("후보 거절이 원래 회계를 변경")
			}
		})
	}
}

func TestStartupCompactionPublishedFailureRemainsClosed(t *testing.T) {
	for _, stage := range []string{"parent_sync", "legacy_guard"} {
		t.Run(stage, func(t *testing.T) {
			s, b, root := compactionFixture(t)
			expected := copyPreparationJobs(s.jobs)
			if err := s.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			restored, err := newServiceForBootWithCompactionHook(t.Context(), root, b, executionBootID(), func(at string) error {
				if at == stage {
					return errors.New("owned publication failure")
				}
				return nil
			})
			if restored != nil {
				defer restored.Shutdown(t.Context())
			}
			if err == nil {
				t.Fatal("게시 뒤 동기화 불명으로 기동 허용")
			}
			normal, err := New(t.Context(), root, b)
			if err != nil {
				t.Fatal(err)
			}
			defer normal.Shutdown(t.Context())
			head, err := os.ReadFile(filepath.Join(root, headName))
			if err != nil {
				t.Fatal(err)
			}
			if string(head) != legacyCompactionGuard {
				t.Fatal("재기동이 옛 reader 가드 복구 누락")
			}
			if !reflect.DeepEqual(normal.jobs, expected) || b.calls.Load() != 1 {
				t.Fatal("게시 뒤 복구가 회계·실행을 변경")
			}
		})
	}
}

func TestPreparationSnapshotRejectsDuplicateJob(t *testing.T) {
	s, _, _ := compactionFixture(t)
	raw, err := preparationSnapshots(s.jobs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replayPreparationReceipts(t.Context(), []json.RawMessage{raw[0]}, 1); err != nil {
		t.Fatal("유효 스냅샷 대조", err)
	}
	if _, err := replayPreparationReceipts(t.Context(), []json.RawMessage{raw[0], raw[0]}, 2); err == nil {
		t.Fatal("스냅샷의 같은 작업 ID 중복 허용")
	}
}

func TestStartupCompactionRejectsSnapshotCountMismatch(t *testing.T) {
	s, b, root := newFixture(t)
	create(t, s, "count-first")
	create(t, s, "count-second")
	// 같은 작업의 개정 기록을 만들어 실제 기동 압축 분기를 밟는다.
	snapshots, err := preparationSnapshots(s.jobs)
	if err != nil {
		t.Fatal(err)
	}
	var first receipt
	if err := json.Unmarshal(snapshots[0], &first); err != nil {
		t.Fatal(err)
	}
	next := first.Job
	if got := start(t, s, next, "count-start"); got.Error != "" {
		t.Fatal(got.Error)
	}
	awaitExecutionReturned(t, s, next.ID)
	// 마지막 기록은 하나가 terminal이라 count를 줄일 때 그 기록을 첫 snapshot에 둔다.
	if err := s.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	reached := false
	restored, err := newServiceForBootWithCompactionHook(t.Context(), root, b, executionBootID(), func(stage string) error {
		if stage != "candidate_ready" {
			return nil
		}
		dir := compactionCandidateDirectory(t, root)
		rewriteCompactionFixture(t, dir, func(index int, raw json.RawMessage) json.RawMessage {
			if index != 0 {
				return raw
			}
			var h compactionHeader
			if err := json.Unmarshal(raw, &h); err != nil {
				t.Fatal(err)
			}
			h.SnapshotCount--
			encoded, err := json.Marshal(h)
			if err != nil {
				t.Fatal(err)
			}
			reached = true
			return encoded
		})
		return nil
	})
	if restored != nil {
		defer restored.Shutdown(t.Context())
	}
	if err == nil || !reached {
		t.Fatal("압축 후보 스냅샷 개수 불일치 허용")
	}
}

func TestPreparationSnapshotRejectsShortSourceSequence(t *testing.T) {
	s, b, root := compactionFixture(t)
	if err := compactWithFacts(t, s, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	rewriteCompactionFixture(t, filepath.Join(root, currentJournalDirectory), func(index int, raw json.RawMessage) json.RawMessage {
		if index != 0 {
			return raw
		}
		var h compactionHeader
		if err := json.Unmarshal(raw, &h); err != nil {
			t.Fatal(err)
		}
		h.SourceSequence = uint64(h.SnapshotCount)
		encoded, err := json.Marshal(h)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	})
	restored, err := New(t.Context(), root, b)
	if restored != nil {
		defer restored.Shutdown(t.Context())
	}
	if err == nil {
		t.Fatal("원본 sequence가 스냅샷보다 짧아도 복원")
	}
}

func TestPreparationGenerationRequiresMatchingFormat(t *testing.T) {
	for _, kind := range []string{"flat_snapshot", "generation_flat"} {
		t.Run(kind, func(t *testing.T) {
			s, _, root := newFixture(t)
			create(t, s, "format-create")
			dir := root
			if kind == "generation_flat" {
				if err := compactWithFacts(t, s, nil); err != nil {
					t.Fatal(err)
				}
				dir = filepath.Join(root, currentJournalDirectory)
			}
			if err := s.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			rewriteCompactionFixture(t, dir, func(index int, raw json.RawMessage) json.RawMessage {
				if index != 0 {
					return raw
				}
				if kind == "generation_flat" {
					return json.RawMessage(`{"format":"diagnosis-preparation-journal.v1"}`)
				}
				encoded, err := json.Marshal(compactionHeader{Format: compactionFormat, SnapshotCount: 1, SourceSequence: 2, SourceDigest: strings.Repeat("a", 64)})
				if err != nil {
					t.Fatal(err)
				}
				return encoded
			})
			opened, _, err := openPreparationJournal(root)
			if opened != nil {
				defer opened.close()
			}
			if err == nil {
				t.Fatal("파일 위치와 스냅샷 형식 불일치 허용")
			}
		})
	}
}

func TestStartupCompactionCurrentJournalChangedBlocks(t *testing.T) {
	s, b, root := compactionFixture(t)
	if err := s.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	replaced := false
	restored, err := newServiceForBootWithCompactionHook(t.Context(), root, b, executionBootID(), func(stage string) error {
		if stage != "candidate_ready" {
			return nil
		}
		path := filepath.Join(root, journalName)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		temp := filepath.Join(root, "owned-journal-replacement")
		if err := os.WriteFile(temp, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(temp, path); err != nil {
			t.Fatal(err)
		}
		replaced = true
		return nil
	})
	if restored != nil {
		defer restored.Shutdown(t.Context())
	}
	if !replaced {
		t.Fatal("현재 저널 교체 hook 미도달")
	}
	if err == nil {
		t.Fatal("게시 전 현재 저널 교체 뒤 기동 허용")
	}
}

func TestStartupCompactionCancelledBlocks(t *testing.T) {
	s, b, root := compactionFixture(t)
	if err := s.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reached := false
	restored, err := newServiceForBootWithCompactionHook(ctx, root, b, executionBootID(), func(stage string) error {
		if stage == "journal_write" {
			reached = true
			cancel()
		}
		return nil
	})
	if restored != nil {
		defer restored.Shutdown(t.Context())
	}
	if !reached {
		t.Fatal("취소 hook 미도달")
	}
	if err == nil {
		t.Fatal("기동 압축 중 취소 뒤 기동 허용")
	}
}

func TestStartupCompactionCandidateReadIOUsesOriginal(t *testing.T) {
	for _, kind := range []string{"open_head", "recheck_directory"} {
		t.Run(kind, func(t *testing.T) {
			s, b, root := compactionFixture(t)
			expected := copyPreparationJobs(s.jobs)
			beforeData, err := os.ReadFile(filepath.Join(root, journalName))
			if err != nil {
				t.Fatal(err)
			}
			beforeHead, err := os.ReadFile(filepath.Join(root, headName))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			reached := false
			restored, err := newServiceForBootWithCompactionHook(t.Context(), root, b, executionBootID(), func(stage string) error {
				wanted := "candidate_recheck"
				if kind == "open_head" {
					wanted = "candidate_ready"
				}
				if stage != wanted {
					return nil
				}
				dir := compactionCandidateDirectory(t, root)
				from := filepath.Join(dir, headName)
				if kind == "recheck_directory" {
					from = dir
				}
				if err := os.Rename(from, filepath.Join(root, "owned-unavailable-candidate")); err != nil {
					t.Fatal(err)
				}
				reached = true
				return nil
			})
			if restored != nil {
				defer restored.Shutdown(t.Context())
			}
			if err != nil || !reached {
				t.Fatal("후보 읽기 I/O 실패로 검증된 원본 기동 거절", err)
			}
			if !reflect.DeepEqual(restored.jobs, expected) || !restored.journal.current() || restored.journal.snapshotCount != 0 || b.calls.Load() != 1 {
				t.Fatal("후보 읽기 실패 뒤 원본 회계·세대 변경")
			}
			data, err := os.ReadFile(filepath.Join(root, journalName))
			if err != nil {
				t.Fatal(err)
			}
			head, err := os.ReadFile(filepath.Join(root, headName))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, beforeData) || !bytes.Equal(head, beforeHead) {
				t.Fatal("후보 읽기 실패 뒤 원본 바이트 변경")
			}
		})
	}
}

func TestStartupCompactionCandidateMetadataChangeBlocks(t *testing.T) {
	s, b, root := compactionFixture(t)
	if err := s.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	reached := false
	restored, err := newServiceForBootWithCompactionHook(t.Context(), root, b, executionBootID(), func(stage string) error {
		if stage != "candidate_recheck" {
			return nil
		}
		dir := compactionCandidateDirectory(t, root)
		if err := os.Rename(filepath.Join(dir, headName), filepath.Join(root, "owned-changed-head")); err != nil {
			t.Fatal(err)
		}
		reached = true
		return nil
	})
	if restored != nil {
		defer restored.Shutdown(t.Context())
	}
	if !reached || err == nil {
		t.Fatal("후보 파일 metadata 변경을 I/O 보류로 처리")
	}
}

func TestProtectedReadIOIsDistinctFromInodeChange(t *testing.T) {
	s, _, root := compactionFixture(t)
	j := s.journal
	err := j.root.checkMatch("owned-unavailable-file", j.file, j.stat)
	if !errors.Is(err, errStorageIO) {
		t.Fatal("보호 파일 경로 열기 실패를 내용 불일치로 분류", err)
	}
	path := filepath.Join(root, journalName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(root, "owned-inode-replacement")
	if err := os.WriteFile(replacement, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	err = j.root.checkMatch(journalName, j.file, j.stat)
	if err == nil || errors.Is(err, errStorageIO) {
		t.Fatal("원본 inode 변경을 일시 I/O로 처리", err)
	}
}
