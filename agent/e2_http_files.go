package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/e2sandbox"
	actool "github.com/Autumn-27/norma/tool"
)

// E2HTTPWorkspace는 intent 보관소 파일 연산이다(docs/workspace-decision.md §2-4, C29). 러너가 보관소를 쓰는 실행일 때만
// 진단 Worker에 파일 도구(Read·Write·Edit·LS)를 붙인다. 파일 도구는 대상에 요청을 보내지 않는다.
type E2HTTPWorkspace interface {
	WorkspaceEnabled() bool
	WorkspaceFile(intentID int64, name string) ([]byte, error)
	WorkspaceList(intentID int64, dir string) ([]e2sandbox.WorkspaceEntry, error)
	WriteWorkspaceFile(intentID int64, name string, content []byte) error
	EditWorkspaceFile(intentID int64, name, old, new string, all bool) (int, error)
}

// 파일 도구 출력 상한. 원본 Read와 같은 줄 기본값·줄 길이를 쓰고, 한 번에 돌려주는 글 전체도 묶는다.
const (
	e2HTTPReadDefaultLines = 2000
	e2HTTPReadMaxLineBytes = 2000
	e2HTTPReadMaxBytes     = 256 << 10
	// Bash 작업 디렉터리. 모델이 이 아래 절대 경로를 주면 보관소 기준 상대 경로로 바꾼다.
	e2HTTPWorkspaceRoot = "/work"
)

// e2HTTPWorkspacePath는 모델이 준 경로를 보관소 기준 상대 경로로 바꾼다. /work 밖 절대 경로, "..", 정규화되지 않은
// 경로는 거부한다. dir이면 빈 경로(맨 위)를 받는다.
func e2HTTPWorkspacePath(p string, dir bool) (string, bool) {
	switch {
	case p == e2HTTPWorkspaceRoot || p == e2HTTPWorkspaceRoot+"/":
		p = ""
	case strings.HasPrefix(p, e2HTTPWorkspaceRoot+"/"):
		p = strings.TrimPrefix(p, e2HTTPWorkspaceRoot+"/")
	case strings.HasPrefix(p, "/"):
		return "", false
	}
	p = strings.TrimPrefix(p, "./")
	if p == "" || p == "." {
		return "", dir
	}
	if path.Clean(p) != p || strings.ContainsRune(p, 0) {
		return "", false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", false
		}
	}
	return p, true
}

// e2HTTPWorkspaceError는 러너 오류를 모델에 주는 고정 문구로 바꾼다.
func e2HTTPWorkspaceError(err error) actool.Result {
	switch {
	case errors.Is(err, e2runner.ErrWorkspaceBusy):
		return actool.Errorf("workspace_busy: a Bash call for this intent is still running; retry after it returns")
	case errors.Is(err, e2runner.ErrWorkspaceFile):
		return actool.Errorf("workspace_path_unavailable: no such file or directory in the workspace, or the path is outside it")
	case errors.Is(err, e2runner.ErrWorkspaceFull):
		return actool.Errorf("workspace_full: the change would exceed the workspace limit")
	case errors.Is(err, e2runner.ErrWorkspaceEdit):
		return actool.Errorf("edit_target_not_unique: old_string must occur exactly once (or set replace_all) and differ from new_string")
	}
	return actool.Errorf("workspace_unavailable")
}

func newE2HTTPFileTools(ws E2HTTPWorkspace, intentID int64, redact func([]byte) []byte, state *e2HTTPToolState) map[string]actool.CoreTool {
	if ws == nil || !ws.WorkspaceEnabled() || redact == nil || state == nil || intentID <= 0 {
		return nil
	}
	halted := func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.halted
	}
	sequential := func(json.RawMessage) bool { return false }
	return map[string]actool.CoreTool{
		"Read": actool.Build(actool.Spec{
			Name:        "Read",
			Description: "Reads a file from this intent's workspace (the Bash working directory) and returns it with line numbers. It never contacts the target.",
			Prompt:      "file_path is relative to the workspace or under /work. Use offset/limit for large files. Content is credential-redacted.",
			Schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"file_path": map[string]any{"type": "string", "minLength": 1},
				"offset":    map[string]any{"type": "integer", "minimum": 1},
				"limit":     map[string]any{"type": "integer", "minimum": 1},
			}, "required": []any{"file_path"}},
			Concurrent: sequential,
			Run: func(_ context.Context, input json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
				var in struct {
					FilePath string `json:"file_path"`
					Offset   int    `json:"offset"`
					Limit    int    `json:"limit"`
				}
				if json.Unmarshal(input, &in) != nil || in.Offset < 0 || in.Limit < 0 {
					return actool.Errorf("read_input_invalid"), nil
				}
				name, ok := e2HTTPWorkspacePath(in.FilePath, false)
				if !ok {
					return e2HTTPWorkspaceError(e2runner.ErrWorkspaceFile), nil
				}
				if halted() {
					return actool.Errorf("workspace_unavailable"), nil
				}
				data, err := ws.WorkspaceFile(intentID, name)
				if err != nil {
					return e2HTTPWorkspaceError(err), nil
				}
				return actool.Text(e2HTTPNumberedLines(redact(data), in.Offset, in.Limit)), nil
			},
		}),
		"LS": actool.Build(actool.Spec{
			Name:        "LS",
			Description: "Lists the files and subdirectories of a directory in this intent's workspace (non-recursive). It never contacts the target.",
			Prompt:      "path is relative to the workspace or under /work; defaults to the workspace root.",
			Schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"path": map[string]any{"type": "string"},
			}},
			Concurrent: sequential,
			Run: func(_ context.Context, input json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
				var in struct {
					Path string `json:"path"`
				}
				if json.Unmarshal(input, &in) != nil {
					return actool.Errorf("ls_input_invalid"), nil
				}
				dir, ok := e2HTTPWorkspacePath(in.Path, true)
				if !ok {
					return e2HTTPWorkspaceError(e2runner.ErrWorkspaceFile), nil
				}
				if halted() {
					return actool.Errorf("workspace_unavailable"), nil
				}
				entries, err := ws.WorkspaceList(intentID, dir)
				if err != nil {
					return e2HTTPWorkspaceError(err), nil
				}
				var b strings.Builder
				for _, e := range entries {
					if e.Dir {
						fmt.Fprintf(&b, "%s/\n", e.Name)
					} else {
						fmt.Fprintf(&b, "%s\t%d bytes\n", e.Name, e.Size)
					}
				}
				if b.Len() == 0 {
					b.WriteString("(empty)\n")
				}
				return actool.Text(string(redact([]byte(b.String())))), nil
			},
		}),
		"Write": actool.Build(actool.Spec{
			Name:        "Write",
			Description: "Writes a whole file in this intent's workspace, creating parent directories. Your next Bash call sees it under /work. It never contacts the target.",
			Prompt:      "Provide the complete file contents; Write replaces the whole file. Prefer Edit for small changes.",
			Schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"file_path": map[string]any{"type": "string", "minLength": 1},
				"content":   map[string]any{"type": "string"},
			}, "required": []any{"file_path", "content"}},
			Concurrent: sequential,
			Run: func(_ context.Context, input json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
				var in struct {
					FilePath string  `json:"file_path"`
					Content  *string `json:"content"`
				}
				if json.Unmarshal(input, &in) != nil || in.Content == nil {
					return actool.Errorf("write_input_invalid"), nil
				}
				name, ok := e2HTTPWorkspacePath(in.FilePath, false)
				if !ok {
					return e2HTTPWorkspaceError(e2runner.ErrWorkspaceFile), nil
				}
				if halted() {
					return actool.Errorf("workspace_unavailable"), nil
				}
				if err := ws.WriteWorkspaceFile(intentID, name, []byte(*in.Content)); err != nil {
					return e2HTTPWorkspaceError(err), nil
				}
				return actool.Text(fmt.Sprintf("wrote %s (%d bytes)", name, len(*in.Content))), nil
			},
		}),
		"Edit": actool.Build(actool.Spec{
			Name:        "Edit",
			Description: "Replaces an exact string in a file of this intent's workspace. old_string must occur exactly once unless replace_all is set. It never contacts the target.",
			Prompt:      "Read the file first. Text shown by Read is credential-redacted; old_string must match the stored file exactly.",
			Schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"file_path":   map[string]any{"type": "string", "minLength": 1},
				"old_string":  map[string]any{"type": "string", "minLength": 1},
				"new_string":  map[string]any{"type": "string"},
				"replace_all": map[string]any{"type": "boolean"},
			}, "required": []any{"file_path", "old_string", "new_string"}},
			Concurrent: sequential,
			Run: func(_ context.Context, input json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
				var in struct {
					FilePath   string  `json:"file_path"`
					OldString  string  `json:"old_string"`
					NewString  *string `json:"new_string"`
					ReplaceAll bool    `json:"replace_all"`
				}
				if json.Unmarshal(input, &in) != nil || in.NewString == nil {
					return actool.Errorf("edit_input_invalid"), nil
				}
				name, ok := e2HTTPWorkspacePath(in.FilePath, false)
				if !ok {
					return e2HTTPWorkspaceError(e2runner.ErrWorkspaceFile), nil
				}
				if halted() {
					return actool.Errorf("workspace_unavailable"), nil
				}
				n, err := ws.EditWorkspaceFile(intentID, name, in.OldString, *in.NewString, in.ReplaceAll)
				if err != nil {
					return e2HTTPWorkspaceError(err), nil
				}
				return actool.Text(fmt.Sprintf("edited %s (%d replacement(s))", name, n)), nil
			},
		}),
	}
}

// e2HTTPNumberedLines는 원본 Read처럼 1부터 센 줄 번호를 붙인다. 긴 줄과 전체 길이는 상한에서 자르고 그 사실을 적는다.
func e2HTTPNumberedLines(data []byte, offset, limit int) string {
	if offset < 1 {
		offset = 1
	}
	if limit < 1 {
		limit = e2HTTPReadDefaultLines
	}
	lines := strings.Split(strings.ToValidUTF8(string(data), "�"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var b strings.Builder
	for i := offset - 1; i < len(lines) && i < offset-1+limit; i++ {
		line := lines[i]
		if len(line) > e2HTTPReadMaxLineBytes {
			line = strings.ToValidUTF8(line[:e2HTTPReadMaxLineBytes], "") + " [line truncated]"
		}
		entry := fmt.Sprintf("%6d\t%s\n", i+1, line)
		if b.Len()+len(entry) > e2HTTPReadMaxBytes {
			b.WriteString("[output truncated; use offset/limit]\n")
			return b.String()
		}
		b.WriteString(entry)
	}
	if b.Len() == 0 {
		b.WriteString("(no lines in range)\n")
	}
	return b.String()
}
