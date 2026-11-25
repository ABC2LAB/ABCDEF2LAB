package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Autumn-27/artex/server"
)

func TestURLBoundTaskStoreKeepsOnlyUnfinishedRuns(t *testing.T) {
	if _, err := newURLBoundTaskStore(""); err == nil {
		t.Fatal("상태 디렉터리 없이 결속 기록을 만듦")
	}
	stateDir := t.TempDir()
	store, err := newURLBoundTaskStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	read := func() []urlBoundTask {
		t.Helper()
		store.mu.Lock()
		defer store.mu.Unlock()
		list, err := store.readLocked()
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	var got [][]server.FirstDiagnosisBoundTask
	release := func(previous []server.FirstDiagnosisBoundTask) error {
		got = append(got, previous)
		return nil
	}
	first := urlBoundTask{JobID: "job-1", TaskID: 1, ExplorationID: 11}
	second := urlBoundTask{JobID: "job-2", TaskID: 2, ExplorationID: 12}
	if store.bind(urlBoundTask{JobID: "job-0"}, release) == nil || store.bind(first, nil) == nil {
		t.Fatal("잘못된 결속을 기록함")
	}
	if err := store.bind(first, release); err != nil {
		t.Fatal(err)
	}
	// 첫 회차가 돌아오지 못하고(프로세스 종료) 다음 회차가 결속하면, 첫 회차의 작업만 정리 대상으로 넘긴다.
	if err := store.bind(second, release); err != nil {
		t.Fatal(err)
	}
	want := [][]server.FirstDiagnosisBoundTask{{}, {{TaskID: 1, ExplorationID: 11}}}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(read(), []urlBoundTask{second}) {
		t.Fatalf("결속 정리 대상 %+v, 남은 기록 %+v", got, read())
	}
	info, err := os.Stat(filepath.Join(stateDir, urlBoundTasksDir, urlBoundTasksFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("결속 기록 권한 %v err=%v", info, err)
	}
	// 정리가 실패하면 기록을 바꾸지 않는다(다음 회차가 다시 정리한다).
	failed := errors.New("release failed")
	third := urlBoundTask{JobID: "job-3", TaskID: 3, ExplorationID: 13}
	if err := store.bind(third, func([]server.FirstDiagnosisBoundTask) error { return failed }); !errors.Is(err, failed) || !reflect.DeepEqual(read(), []urlBoundTask{second}) {
		t.Fatalf("정리 실패 뒤 기록 %+v err=%v", read(), err)
	}
	// 회차가 돌아오면 자기 결속을 지운다. 다른 회차 기록은 남긴다.
	store.unbind("job-other")
	if !reflect.DeepEqual(read(), []urlBoundTask{second}) {
		t.Fatalf("다른 회차 결속을 지움 %+v", read())
	}
	store.unbind(second.JobID)
	if len(read()) != 0 {
		t.Fatalf("돌아온 회차 결속이 남음 %+v", read())
	}
	// 깨진 기록은 어느 작업인지 알 수 없으므로 정리 대상 없이 비운다.
	if err := os.WriteFile(store.path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := store.bind(third, release); err != nil || !reflect.DeepEqual(got, [][]server.FirstDiagnosisBoundTask{{}}) || !reflect.DeepEqual(read(), []urlBoundTask{third}) {
		t.Fatalf("깨진 기록 뒤 정리 대상 %+v 기록 %+v err=%v", got, read(), err)
	}
}
