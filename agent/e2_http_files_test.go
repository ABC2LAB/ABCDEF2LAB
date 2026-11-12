package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"runtime"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/e2sandbox"
	actool "github.com/Autumn-27/norma/tool"
)

// 실제 러너가 진단 Worker의 보관소 연산 경계를 구현한다.
var _ E2HTTPWorkspace = (*e2runner.Runner)(nil)

type fakeWorkspace struct {
	enabled bool
	files   map[string][]byte
	busy    bool
	calls   int
	lastDir string
}

func (w *fakeWorkspace) WorkspaceEnabled() bool { return w.enabled }
func (w *fakeWorkspace) WorkspaceFile(_ int64, name string) ([]byte, error) {
	w.calls++
	data, ok := w.files[name]
	if !ok {
		return nil, e2runner.ErrWorkspaceFile
	}
	return data, nil
}
func (w *fakeWorkspace) WorkspaceList(_ int64, dir string) ([]e2sandbox.WorkspaceEntry, error) {
	w.calls++
	w.lastDir = dir
	return []e2sandbox.WorkspaceEntry{{Name: "dump-SECRET.txt", Size: 3}, {Name: "out", Dir: true}}, nil
}
func (w *fakeWorkspace) WriteWorkspaceFile(_ int64, name string, content []byte) error {
	w.calls++
	if w.busy {
		return e2runner.ErrWorkspaceBusy
	}
	w.files[name] = append([]byte(nil), content...)
	return nil
}
func (w *fakeWorkspace) EditWorkspaceFile(_ int64, name, old, new string, all bool) (int, error) {
	w.calls++
	n := bytes.Count(w.files[name], []byte(old))
	if n != 1 && !all {
		return 0, e2runner.ErrWorkspaceEdit
	}
	w.files[name] = bytes.ReplaceAll(w.files[name], []byte(old), []byte(new))
	return n, nil
}

func fileToolCall(t *testing.T, tool actool.CoreTool, input map[string]any) actool.Result {
	t.Helper()
	raw, _ := json.Marshal(input)
	out, err := tool.Call(context.Background(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func redactSecret(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("SECRET"), []byte("[REDACTED]"))
}

// 경로는 보관소 기준 상대 경로나 /work 아래만 받는다.
func TestE2HTTPWorkspacePath(t *testing.T) {
	for _, tc := range []struct {
		in   string
		dir  bool
		want string
		ok   bool
	}{
		{"notes.txt", false, "notes.txt", true},
		{"/work/a/b.txt", false, "a/b.txt", true},
		{"./a.txt", false, "a.txt", true},
		{"/work", true, "", true},
		{"", true, "", true},
		{"", false, "", false},
		{"/work", false, "", false},
		{"/etc/passwd", false, "", false},
		{"../x", false, "", false},
		{"a/../b", false, "", false},
		{"a//b", false, "", false},
		{"/work/../etc", false, "", false},
	} {
		got, ok := e2HTTPWorkspacePath(tc.in, tc.dir)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("%q dir=%v: %q %v", tc.in, tc.dir, got, ok)
		}
	}
}

// Read·LS가 모델에 돌려주는 내용과 이름은 Bash 출력과 같은 가림을 거친다. 쓰기·고치기는 보관소로만 간다.
func TestE2HTTPFileToolsRedactAndStayInWorkspace(t *testing.T) {
	ws := &fakeWorkspace{enabled: true, files: map[string][]byte{"dump.txt": []byte("token SECRET\nline two\nline three\n")}}
	state := &e2HTTPToolState{}
	tools := newE2HTTPFileTools(ws, 22, redactSecret, state)
	if len(tools) != 4 {
		t.Fatalf("tools: %d", len(tools))
	}
	read := fileToolCall(t, tools["Read"], map[string]any{"file_path": "/work/dump.txt", "offset": 1, "limit": 2})
	text := read.Flatten()
	if read.IsError || strings.Contains(text, "SECRET") || !strings.Contains(text, "     1\ttoken [REDACTED]") || strings.Contains(text, "line three") {
		t.Fatalf("read: %q", text)
	}
	ls := fileToolCall(t, tools["LS"], map[string]any{"path": "/work"})
	if ls.IsError || strings.Contains(ls.Flatten(), "SECRET") || !strings.Contains(ls.Flatten(), "out/") || ws.lastDir != "" {
		t.Fatalf("ls: %q dir=%q", ls.Flatten(), ws.lastDir)
	}
	if w := fileToolCall(t, tools["Write"], map[string]any{"file_path": "plan.txt", "content": "a b a"}); w.IsError || string(ws.files["plan.txt"]) != "a b a" {
		t.Fatalf("write: %q", w.Flatten())
	}
	if e := fileToolCall(t, tools["Edit"], map[string]any{"file_path": "plan.txt", "old_string": "a", "new_string": "c"}); !e.IsError || !strings.HasPrefix(e.Flatten(), "edit_target_not_unique") {
		t.Fatalf("ambiguous edit: %q", e.Flatten())
	}
	if e := fileToolCall(t, tools["Edit"], map[string]any{"file_path": "plan.txt", "old_string": "b", "new_string": "c"}); e.IsError || string(ws.files["plan.txt"]) != "a c a" {
		t.Fatalf("edit: %q", e.Flatten())
	}
	before := ws.calls
	for name, input := range map[string]map[string]any{
		"Read":  {"file_path": "/etc/passwd"},
		"Write": {"file_path": "../escape", "content": "x"},
		"Edit":  {"file_path": "/tmp/x", "old_string": "a", "new_string": "b"},
		"LS":    {"path": "/"},
	} {
		if out := fileToolCall(t, tools[name], input); !out.IsError || !strings.HasPrefix(out.Flatten(), "workspace_path_unavailable") {
			t.Fatalf("%s outside workspace: %q", name, out.Flatten())
		}
	}
	if ws.calls != before {
		t.Fatal("outside path reached the workspace")
	}
	ws.busy = true
	if out := fileToolCall(t, tools["Write"], map[string]any{"file_path": "plan.txt", "content": "x"}); !strings.HasPrefix(out.Flatten(), "workspace_busy") {
		t.Fatalf("busy: %q", out.Flatten())
	}
	// 실행이 멈춘 intent에는 보관소를 열지 않는다.
	state.halted = true
	before = ws.calls
	for name, input := range map[string]map[string]any{"Read": {"file_path": "dump.txt"}, "Write": {"file_path": "x", "content": "y"}} {
		if out := fileToolCall(t, tools[name], input); !out.IsError {
			t.Fatalf("%s after halt", name)
		}
	}
	if ws.calls != before {
		t.Fatal("halted intent reached the workspace")
	}
	if newE2HTTPFileTools(&fakeWorkspace{}, 22, redactSecret, &e2HTTPToolState{}) != nil {
		t.Fatal("file tools without a persisted workspace")
	}
}

// 진단 Worker 프로필은 보관소 파일 도구를 bound 판으로만 채운다. 기본 도구 목록의 호스트 Read 등은 섞이지 않는다.
func TestDiagnosisWorkerProfileUsesWorkspaceFileTools(t *testing.T) {
	ws := &fakeWorkspace{enabled: true, files: map[string][]byte{}}
	bindings := map[string]actool.CoreTool{"Bash": e2WorkerFixtureTool("Bash", nil)}
	for name, tool := range newE2HTTPFileTools(ws, 22, redactSecret, &e2HTTPToolState{}) {
		bindings[name] = tool
	}
	base := defaultToolsExcept("MultiEdit", "Glob", "Grep")
	tools, err := diagnosisWorkerProfile.selectTools(base, bindings, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]actool.CoreTool{}
	for _, tool := range tools {
		names[tool.Name()] = tool
	}
	for _, name := range []string{"Read", "Write", "Edit", "LS"} {
		if names[name] != bindings[name] {
			t.Fatalf("%s not the workspace tool", name)
		}
	}
	without, err := diagnosisWorkerProfile.selectTools(base, map[string]actool.CoreTool{"Bash": e2WorkerFixtureTool("Bash", nil)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range without {
		if tool.Name() == "Read" || tool.Name() == "Write" || tool.Name() == "Edit" || tool.Name() == "LS" {
			t.Fatalf("host %s exposed without a workspace", tool.Name())
		}
	}
}

type workspaceBoundRunner struct {
	E2HTTPRunner
	*fakeWorkspace
}

// 실제 Worker 조립: 보관소를 쓰는 러너면 모델 요청에 보관소 파일 도구 4개가 실리고, 아니면 실리지 않는다.
func TestE2HTTPWorkerAttachesWorkspaceFileToolsOnlyWithWorkspace(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		_, _, binding := e2WorkerFixture(t)
		binding.Runner = workspaceBoundRunner{E2HTTPRunner: binding.Runner, fakeWorkspace: &fakeWorkspace{enabled: enabled, files: map[string][]byte{}}}
		provider := &e2SettlementProvider{}
		worker := NewWorker(provider, "synthetic", t.TempDir(), nil, 10000, 2)
		worker.SetNonStreaming(func() bool { return true })
		ctx := WithE2HTTPExecution(context.Background(), binding)
		_, _, _ = worker.Execute(ctx, "worker", 7, nil, nil, &db.Node{ID: 22, Kind: db.KindIntent, Payload: json.RawMessage("{}")}, nil, nil, nil, nil)
		if len(provider.requests) == 0 {
			t.Fatal("model not called")
		}
		got := map[string]bool{}
		for _, tool := range provider.requests[0].Tools {
			got[tool.Name] = true
		}
		for _, name := range []string{"Read", "Write", "Edit", "LS"} {
			if got[name] != enabled {
				t.Fatalf("workspace=%v %s exposed=%v", enabled, name, got[name])
			}
		}
	}
}

// e2HTTPNumberedLinesBySplit는 줄 배열을 만들던 이전 구현이다. 출력이 같은지 대조하는 기준으로만 쓴다.
func e2HTTPNumberedLinesBySplit(data []byte, offset, limit int) string {
	if offset < 1 {
		offset = 1
	}
	if limit < 1 {
		limit = e2HTTPReadDefaultLines
	}
	lines := strings.Split(strings.ToValidUTF8(string(data), "\uFFFD"), "\n")
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

// 줄 경계를 따라가는 Read 출력은 이전 구현과 글자 하나까지 같다(빈 줄·끝 개행·잘못된 UTF-8 묶음·상한에 걸친 글자·
// 긴 줄·범위 밖·출력 상한).
func TestE2HTTPNumberedLinesMatchesSplitOutput(t *testing.T) {
	long := strings.Repeat("a", e2HTTPReadMaxLineBytes-1) + "한글" + strings.Repeat("b", 10)
	invalidRun := strings.Repeat("\xff", e2HTTPReadMaxLineBytes+500)
	mixed := strings.Repeat("\xffok", e2HTTPReadMaxLineBytes)
	many := strings.Repeat("0123456789abcdef\n", e2HTTPReadMaxBytes/8)
	exact := strings.Repeat("a", e2HTTPReadMaxLineBytes)
	// 줄마다 "%6d\t" 7바이트와 개행 1바이트가 붙는다. 첫 줄 257바이트 + 256바이트 1023줄이면 출력 상한보다 정확히 1바이트 많다.
	overByOne := strings.Repeat("x", 249) + "\n" + strings.Repeat(strings.Repeat("y", 248)+"\n", 1023)
	fits := strings.Repeat(strings.Repeat("z", 248)+"\n", e2HTTPReadMaxBytes/256)
	inputs := []string{overByOne, fits, fits + "more\n", exact, exact + "a", exact[1:] + "é", exact[2:] + "é", exact[1:] + "\xff\xff", "", "\n", "a", "a\n", "a\n\nb", "a\nb\n\n", "x\xff\xfey\n\xc3\n", "valid \uFFFD and \xff\n", long, long + "\nnext\n", invalidRun + "\nz", mixed, strings.Repeat("\xe2\x82", 1500) + "end", many}
	for i, input := range inputs {
		for _, rng := range [][2]int{{0, 0}, {1, 1}, {1, 2}, {2, 1}, {3, 5}, {1, 1 << 30}, {1 << 30, 1}, {math.MaxInt, math.MaxInt}, {2, math.MaxInt}} {
			got, want := e2HTTPNumberedLines([]byte(input), rng[0], rng[1]), e2HTTPNumberedLinesBySplit([]byte(input), rng[0], min(rng[1], 1<<30))
			if got != want {
				t.Fatalf("input %d range %v:\ngot  %q\nwant %q", i, rng, truncateForLog(got), truncateForLog(want))
			}
		}
	}
}

func truncateForLog(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// 작은 범위를 읽을 때 메모리는 파일 줄 수가 아니라 출력 크기를 따른다. 1MiB가 전부 개행이면 이전 구현은 줄마다 string
// 헤더를 만들어 이 시험의 상한을 크게 넘었다.
func TestE2HTTPReadAllocationFollowsOutputNotLineCount(t *testing.T) {
	newlines := bytes.Repeat([]byte{'\n'}, 1<<20)
	allocated := func(run func()) uint64 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		run()
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}
	for _, offset := range []int{1, len(newlines) / 2} {
		if n := allocated(func() { _ = e2HTTPNumberedLines(newlines, offset, 1) }); n > 64<<10 {
			t.Fatalf("offset %d: numbering allocated %d bytes", offset, n)
		}
	}
	// 도구 호출 전체: 내용 전체 가림(여기서는 보호 값이 없을 때처럼 복사)은 유지하고, 그 밖에 줄 수에 비례해 늘지 않는다.
	ws := &fakeWorkspace{enabled: true, files: map[string][]byte{"lines.txt": newlines}}
	tools := newE2HTTPFileTools(ws, 22, bytes.Clone, &e2HTTPToolState{})
	raw, _ := json.Marshal(map[string]any{"file_path": "lines.txt", "offset": 1, "limit": 1})
	var out actool.Result
	n := allocated(func() { out, _ = tools["Read"].Call(context.Background(), raw, nil) })
	if out.IsError || out.Flatten() != "     1\t\n" || n > 4<<20 {
		t.Fatalf("read allocated %d bytes: %q", n, out.Flatten())
	}
}
