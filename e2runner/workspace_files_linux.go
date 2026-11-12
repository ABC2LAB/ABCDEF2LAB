package e2runner

import (
	"bytes"
	"errors"

	"github.com/Autumn-27/artex/e2sandbox"
)

// 호스트 파일 도구(Read·Write·Edit·LS)가 쓰는 intent 보관소 연산이다(docs/workspace-decision.md §2-4, C29). 보관소 안의
// 상대 경로만 다루고 대상에 요청을 보내지 않는다. 모델에 돌려줄 내용의 가림(RedactOutput)은 호출자가 한다.

func (r *Runner) endWorkspaceCall(intentID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.workspaceBusy[intentID]--; r.workspaceBusy[intentID] <= 0 {
		delete(r.workspaceBusy, intentID)
	}
}

// workspaceOpen은 파일 도구를 받을 수 있는지 본다. 보관소를 쓰는 실행이고, intent가 이 실행에 있고, 멈추거나 막히지
// 않았어야 한다.
func (r *Runner) workspaceOpen(intentID int64) error {
	if r == nil || r.runnerState == nil || intentID <= 0 || !r.cfg.PersistWorkspace {
		return ErrConfig
	}
	if !r.cfg.Execution.HasIntent(intentID) {
		return ErrUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || r.draining || r.intentFencedLocked(intentID) {
		return ErrUnavailable
	}
	return nil
}

func workspaceErr(err error) error {
	switch {
	case errors.Is(err, e2sandbox.ErrWorkspaceNotFound), errors.Is(err, e2sandbox.ErrWorkspaceInvalid):
		return ErrWorkspaceFile
	case errors.Is(err, e2sandbox.ErrWorkspaceTooLarge):
		return ErrWorkspaceFull
	}
	return ErrUnavailable
}

// WorkspaceFile은 intent 보관소의 파일 하나를 읽는다. 보관소는 마지막으로 정상 마감한 호출 뒤의 상태다.
func (r *Runner) WorkspaceFile(intentID int64, name string) ([]byte, error) {
	if err := r.workspaceOpen(intentID); err != nil {
		return nil, err
	}
	limit := e2sandbox.WorkspaceLimit(e2sandbox.Config{})
	r.workspaceMu.Lock()
	defer r.workspaceMu.Unlock()
	archive, err := r.loadWorkspaceLocked(intentID, limit)
	if err != nil {
		return nil, ErrUnavailable
	}
	data, err := e2sandbox.ReadWorkspaceFile(archive, name, limit)
	if err != nil {
		return nil, workspaceErr(err)
	}
	return data, nil
}

// WorkspaceList는 intent 보관소의 디렉터리 하나(""는 맨 위) 바로 아래 항목을 이름 순으로 돌려준다.
func (r *Runner) WorkspaceList(intentID int64, dir string) ([]e2sandbox.WorkspaceEntry, error) {
	if err := r.workspaceOpen(intentID); err != nil {
		return nil, err
	}
	limit := e2sandbox.WorkspaceLimit(e2sandbox.Config{})
	r.workspaceMu.Lock()
	defer r.workspaceMu.Unlock()
	archive, err := r.loadWorkspaceLocked(intentID, limit)
	if err != nil {
		return nil, ErrUnavailable
	}
	entries, err := e2sandbox.ListWorkspace(archive, dir, limit)
	if err != nil {
		return nil, workspaceErr(err)
	}
	return entries, nil
}

// WriteWorkspaceFile은 intent 보관소에 파일 하나를 쓰거나 바꾼다. 같은 intent의 호출이 보관소를 쓰는 중이면
// ErrWorkspaceBusy다. 다음 호출이 그 파일을 /work에서 본다.
func (r *Runner) WriteWorkspaceFile(intentID int64, name string, content []byte) error {
	return r.changeWorkspaceFile(intentID, name, func(archive []byte) ([]byte, error) { return content, nil })
}

// EditWorkspaceFile은 보관소 파일 하나에서 old를 new로 바꾸고 바꾼 수를 돌려준다. all이 아니면 old가 정확히 한 번
// 있어야 한다.
func (r *Runner) EditWorkspaceFile(intentID int64, name, old, new string, all bool) (int, error) {
	if old == "" || old == new {
		return 0, ErrWorkspaceEdit
	}
	replaced := 0
	err := r.changeWorkspaceFile(intentID, name, func(archive []byte) ([]byte, error) {
		current, err := e2sandbox.ReadWorkspaceFile(archive, name, e2sandbox.WorkspaceLimit(e2sandbox.Config{}))
		if err != nil {
			return nil, workspaceErr(err)
		}
		n := bytes.Count(current, []byte(old))
		if n == 0 || n > 1 && !all {
			return nil, ErrWorkspaceEdit
		}
		// 결과 크기를 만들기 전에 넘침 없이 계산한다. 상한 밖이면 큰 결과를 호스트 메모리에 만들지 않는다.
		limit := e2sandbox.WorkspaceLimit(e2sandbox.Config{})
		if uint64(len(current)) > limit {
			return nil, ErrWorkspaceFull
		}
		if len(new) > len(old) && uint64(len(new)-len(old)) > (limit-uint64(len(current)))/uint64(n) {
			return nil, ErrWorkspaceFull
		}
		replaced = n
		return bytes.ReplaceAll(current, []byte(old), []byte(new)), nil
	})
	if err != nil {
		return 0, err
	}
	return replaced, nil
}

func (r *Runner) changeWorkspaceFile(intentID int64, name string, next func([]byte) ([]byte, error)) error {
	if err := r.workspaceOpen(intentID); err != nil {
		return err
	}
	limit := e2sandbox.WorkspaceLimit(e2sandbox.Config{})
	// 보관소 잠금을 먼저 잡고 진행 중 호출을 본다. 호출은 진행 표시를 올린 뒤 보관소를 읽으므로, 표시가 없으면 이
	// 변경이 끝난 뒤에 읽는다.
	r.workspaceMu.Lock()
	defer r.workspaceMu.Unlock()
	r.mu.Lock()
	busy := r.workspaceBusy[intentID] > 0
	r.mu.Unlock()
	if busy {
		return ErrWorkspaceBusy
	}
	archive, err := r.loadWorkspaceLocked(intentID, limit)
	if err != nil {
		return ErrUnavailable
	}
	if _, listErr := e2sandbox.ListWorkspace(archive, name, limit); listErr == nil {
		return ErrWorkspaceFile // 디렉터리에는 쓰지 않는다.
	}
	content, err := next(archive)
	if err != nil {
		return err
	}
	updated, err := e2sandbox.AppendWorkspaceFile(archive, name, content, limit)
	if err != nil {
		return workspaceErr(err)
	}
	if r.storeWorkspaceLocked(intentID, updated) != "saved" {
		return ErrUnavailable
	}
	return nil
}

// WorkspaceEnabled는 이 러너가 intent 보관소를 쓰는지 본다(파일 도구를 붙일지).
func (r *Runner) WorkspaceEnabled() bool {
	return r != nil && r.runnerState != nil && r.cfg.PersistWorkspace
}
