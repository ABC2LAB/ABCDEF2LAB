package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/Autumn-27/artex/server"
)

// 재시작 정리(#912 러너 재현, 2026-11-25 사용자 지시, #927 이동찬 P1).
//
// 준비 서비스가 점검 도중 죽으면 그 회차의 작업이 감독자 마감(drainTask) 전 상태(paused=false)로 DB에 남고, 새 준비 서버는
// 그런 작업을 다른 실행으로 보고 시작을 거부한다. 이 실행기는 회차가 작업을 결속할 때 그 결속을 보호 상태 디렉터리에 남기고
// Execute가 돌아오면 지운다. 그래서 기록이 남는 것은 Execute가 돌아오지 못한 회차(프로세스 종료)뿐이고, 다음 회차가 작업을
// 결속하기 전에 그 기록의 작업만 마감 상태로 돌린다(server.ReleaseInterruptedFirstDiagnosisTasks). 기록에 없는 작업은
// 건드리지 않는다.

const (
	// urlBoundTasksDir는 결속 기록을 두는 상태 디렉터리 아래 하위 디렉터리(0700)다.
	urlBoundTasksDir = "tasks"
	// urlBoundTasksFile은 결속 기록 파일(0600)이다.
	urlBoundTasksFile = "bound.json"
	// 남길 수 있는 결속 기록 수 상한. 준비 서비스는 한 번에 회차 하나만 시작하므로 보통 0~1개다.
	maxURLBoundTasks = 16
)

type urlBoundTask struct {
	JobID         string `json:"job_id"`
	TaskID        int64  `json:"task_id"`
	ExplorationID int64  `json:"exploration_id"`
}

type urlBoundTaskStore struct {
	mu   sync.Mutex
	path string
}

func newURLBoundTaskStore(stateDir string) (*urlBoundTaskStore, error) {
	if stateDir == "" {
		return nil, errPreparation
	}
	dir := filepath.Join(stateDir, urlBoundTasksDir)
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	if checkDirectory(dir, true) != nil {
		return nil, errPreparation
	}
	return &urlBoundTaskStore{path: filepath.Join(dir, urlBoundTasksFile)}, nil
}

// readLocked는 기록을 읽는다. 형식이 깨진 기록은 어느 작업인지 알 수 없으므로 정리 대상 없이 비운다(로그를 남긴다).
// 그 경우 남은 작업이 있으면 준비 서버가 전처럼 시작을 거부한다. 파일을 읽지 못하면 오류다.
func (s *urlBoundTaskStore) readLocked() ([]urlBoundTask, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []urlBoundTask
	if json.Unmarshal(raw, &out) != nil || len(out) > maxURLBoundTasks {
		log.Print("재시작 정리: 작업 결속 기록 형식이 맞지 않아 비움(남은 작업은 정리하지 않음)")
		return nil, nil
	}
	valid := out[:0]
	for _, e := range out {
		if e.JobID == "" || e.TaskID <= 0 || e.ExplorationID <= 0 {
			log.Print("재시작 정리: 작업 결속 기록 한 줄의 형식이 맞지 않아 뺌")
			continue
		}
		valid = append(valid, e)
	}
	return valid, nil
}

func (s *urlBoundTaskStore) writeLocked(list []urlBoundTask) error {
	if list == nil {
		list = []urlBoundTask{}
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// bind는 다른 회차가 남긴 결속을 정리한 뒤(release) 이 회차의 결속만 남긴다. 정리나 기록에 실패하면 기록을 바꾸지 않는다.
func (s *urlBoundTaskStore) bind(current urlBoundTask, release func([]server.FirstDiagnosisBoundTask) error) error {
	if current.JobID == "" || current.TaskID <= 0 || current.ExplorationID <= 0 || release == nil {
		return errPreparation
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.readLocked()
	if err != nil {
		return err
	}
	previous := make([]server.FirstDiagnosisBoundTask, 0, len(list))
	for _, e := range list {
		if e.JobID != current.JobID && e.TaskID != current.TaskID {
			previous = append(previous, server.FirstDiagnosisBoundTask{TaskID: e.TaskID, ExplorationID: e.ExplorationID})
		}
	}
	if err := release(previous); err != nil {
		return err
	}
	return s.writeLocked([]urlBoundTask{current})
}

// unbind는 이 회차의 결속을 지운다. Execute가 돌아오면 부른다(그 회차는 자기 마감 경로를 거쳤다).
func (s *urlBoundTaskStore) unbind(jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.readLocked()
	if err != nil {
		log.Print("재시작 정리: 작업 결속 기록을 읽지 못해 지우지 못함")
		return
	}
	kept := list[:0]
	for _, e := range list {
		if e.JobID != jobID {
			kept = append(kept, e)
		}
	}
	if err := s.writeLocked(kept); err != nil {
		log.Print("재시작 정리: 작업 결속 기록을 지우지 못함")
	}
}
