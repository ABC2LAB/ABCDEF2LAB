package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/Autumn-27/artex/taskmode"
)

func markerReaderFixture(t *testing.T) (controlledTaskMarkerReaderOptions, *os.File) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	anchor, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = anchor.Close() })
	return controlledTaskMarkerReaderOptions{
		RootPath: root, RootAnchor: anchor, MaxEntries: 64,
		CheckScope: func(context.Context, int64) error { return nil },
	}, anchor
}

func writeTaskMarker(t *testing.T, root, task string, intent int64, body string) string {
	t.Helper()
	name := "controlled-" + task + ".task.marker"
	if intent > 0 {
		name = "controlled-" + task + "-" + formatMarkerID(intent) + ".marker"
	}
	if body == "" {
		if intent > 0 {
			body = `{"task_id":"` + task + `","intent_id":` + formatMarkerID(intent) + `,"controlled":true}`
		} else {
			body = `{"task_id":"` + task + `","controlled":true}`
		}
	}
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func formatMarkerID(id int64) string {
	return strconv.FormatInt(id, 10)
}

func readFixture(t *testing.T, options controlledTaskMarkerReaderOptions, id int64) (taskmode.Marker, error) {
	t.Helper()
	return readTaskModeMarkers(context.Background(), options, id, defaultTaskMarkerOps())
}

func TestTaskMarkerReaderMatrix(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*testing.T, string)
		state taskmode.MarkerState
		err   bool
	}{
		{"task and intent", func(t *testing.T, root string) {
			writeTaskMarker(t, root, "7", 0, "")
			writeTaskMarker(t, root, "7", 3, "")
		}, taskmode.MarkerPresent, false},
		{"task only", func(t *testing.T, root string) { writeTaskMarker(t, root, "7", 0, "") }, taskmode.MarkerPresent, false},
		{"empty", func(*testing.T, string) {}, taskmode.MarkerClear, false},
		{"intent only", func(t *testing.T, root string) { writeTaskMarker(t, root, "7", 3, "") }, taskmode.MarkerUnknown, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options, _ := markerReaderFixture(t)
			tc.setup(t, options.RootPath)
			got, err := readFixture(t, options, 7)
			if tc.err {
				if err == nil || got != (taskmode.Marker{}) {
					t.Fatalf("want fail-closed error, got %#v %v", got, err)
				}
				return
			}
			if err != nil || got.TaskID != 7 || got.State != tc.state {
				t.Fatalf("got %#v %v", got, err)
			}
		})
	}
}

func TestTaskMarkerReaderRejectsReservedNamespaceCorruption(t *testing.T) {
	options, _ := markerReaderFixture(t)
	writeTaskMarker(t, options.RootPath, "7", 0, "")
	if err := os.WriteFile(filepath.Join(options.RootPath, "controlled-8-01.marker"), []byte(`{"task_id":"8","intent_id":1,"controlled":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readFixture(t, options, 7)
	if err == nil || got != (taskmode.Marker{}) {
		t.Fatalf("corrupt namespace accepted: %#v %v", got, err)
	}
}

func TestTaskMarkerReaderRejectsUnsafeFiles(t *testing.T) {
	tests := []struct {
		name string
		make func(*testing.T, string)
	}{
		{"symlink", func(t *testing.T, root string) {
			if err := os.Symlink("elsewhere", filepath.Join(root, "controlled-7.task.marker")); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory", func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, "controlled-7.task.marker"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"wrong mode", func(t *testing.T, root string) {
			p := writeTaskMarker(t, root, "7", 0, "")
			if err := os.Chmod(p, 0o400); err != nil {
				t.Fatal(err)
			}
		}},
		{"malformed json", func(t *testing.T, root string) {
			writeTaskMarker(t, root, "7", 0, `{"task_id":"7","controlled":false}`)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			options, _ := markerReaderFixture(t)
			tc.make(t, options.RootPath)
			got, err := readFixture(t, options, 7)
			if err == nil || got != (taskmode.Marker{}) {
				t.Fatalf("unsafe marker accepted: %#v %v", got, err)
			}
		})
	}
}

func TestTaskMarkerReaderRootAndScopeFailures(t *testing.T) {
	options, _ := markerReaderFixture(t)
	writeTaskMarker(t, options.RootPath, "7", 0, "")
	oldRoot := options.RootPath + ".old"
	if err := os.Rename(options.RootPath, oldRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(oldRoot) })
	if err := os.Mkdir(options.RootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := readFixture(t, options, 7)
	if err == nil || got != (taskmode.Marker{}) {
		t.Fatalf("replacement root accepted: %#v %v", got, err)
	}

	options, _ = markerReaderFixture(t)
	options.CheckScope = func(context.Context, int64) error { return errors.New("secret scope detail") }
	got, err = readFixture(t, options, 7)
	if !errors.Is(err, errTaskMarkerReaderScope) || got != (taskmode.Marker{}) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("scope detail escaped: %#v %v", got, err)
	}
}

func TestTaskMarkerReaderMalformedPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
		file string
	}{
		{"syntax", `{"task_id":"7"`, "controlled-7.task.marker"},
		{"unknown field", `{"task_id":"7","controlled":true,"extra":1}`, "controlled-7.task.marker"},
		{"duplicate key", `{"task_id":"7","task_id":"7","controlled":true}`, "controlled-7.task.marker"},
		{"trailing", `{"task_id":"7","controlled":true} trailing`, "controlled-7.task.marker"},
		{"wrong task", `{"task_id":"8","controlled":true}`, "controlled-7.task.marker"},
		{"wrong intent", `{"task_id":"7","intent_id":2,"controlled":true}`, "controlled-7-1.marker"},
		{"controlled false", `{"task_id":"7","controlled":false}`, "controlled-7.task.marker"},
		{"controlled missing", `{"task_id":"7"}`, "controlled-7.task.marker"},
		{"intent missing", `{"task_id":"7","controlled":true}`, "controlled-7-1.marker"},
		{"task with intent", `{"task_id":"7","intent_id":1,"controlled":true}`, "controlled-7.task.marker"},
		{"wrong other task", `{"task_id":"9","controlled":true}`, "controlled-8.task.marker"},
		{"oversize", strings.Repeat("x", int(maxTaskMarkerBytes)+1), "controlled-7.task.marker"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			options, _ := markerReaderFixture(t)
			if err := os.WriteFile(filepath.Join(options.RootPath, tc.file), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readFixture(t, options, 7)
			if err == nil || got != (taskmode.Marker{}) {
				t.Fatalf("malformed payload accepted: %#v %v", got, err)
			}
		})
	}
}

func TestTaskMarkerReaderReservedNamesAndOtherTask(t *testing.T) {
	options, _ := markerReaderFixture(t)
	writeTaskMarker(t, options.RootPath, "7", 0, "")
	writeTaskMarker(t, options.RootPath, "8", 1, "")
	if err := os.WriteFile(filepath.Join(options.RootPath, "notes.txt"), []byte("not a marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readFixture(t, options, 7)
	if err != nil || got.State != taskmode.MarkerPresent || got.TaskID != 7 {
		t.Fatalf("other task/nonmarker changed result: %#v %v", got, err)
	}
}

func TestTaskMarkerReaderRootValidationAndOptions(t *testing.T) {
	options, anchor := markerReaderFixture(t)
	if err := os.Chmod(options.RootPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := readFixture(t, options, 7); err == nil || got != (taskmode.Marker{}) {
		t.Fatalf("wrong root mode accepted: %#v %v", got, err)
	}
	if err := os.Chmod(options.RootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := anchor.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := readFixture(t, options, 7); err == nil || got != (taskmode.Marker{}) {
		t.Fatalf("closed anchor accepted: %#v %v", got, err)
	}
	for _, bad := range []controlledTaskMarkerReaderOptions{
		{RootPath: options.RootPath, RootAnchor: anchor, MaxEntries: 1},
		{RootPath: options.RootPath, CheckScope: options.CheckScope, MaxEntries: 1},
		{RootPath: options.RootPath, RootAnchor: anchor, CheckScope: options.CheckScope},
		{RootPath: options.RootPath, RootAnchor: anchor, CheckScope: options.CheckScope, MaxEntries: -1},
	} {
		if _, err := newControlledTaskMarkerReader(bad); !errors.Is(err, errTaskMarkerReaderInput) {
			t.Fatalf("invalid options accepted: %v", err)
		}
	}
}

func TestTaskMarkerReaderScopeBoundaries(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		t.Run(formatMarkerID(int64(failAt)), func(t *testing.T) {
			options, _ := markerReaderFixture(t)
			writeTaskMarker(t, options.RootPath, "7", 0, "")
			calls := 0
			options.CheckScope = func(context.Context, int64) error {
				calls++
				if calls == failAt {
					return errors.New("scope detail")
				}
				return nil
			}
			got, err := readFixture(t, options, 7)
			if err == nil || got != (taskmode.Marker{}) || !errors.Is(err, errTaskMarkerReaderScope) || calls != failAt {
				t.Fatalf("scope failure not fail-closed: %#v %v", got, err)
			}
		})
	}
}

func TestTaskMarkerReaderRejectsAmbiguousListing(t *testing.T) {
	options, _ := markerReaderFixture(t)
	writeTaskMarker(t, options.RootPath, "7", 0, "")
	base := defaultTaskMarkerOps()
	for _, tc := range []struct {
		name string
		list []string
	}{
		{"duplicate", []string{"controlled-7.task.marker", "controlled-7.task.marker"}},
		{"overlimit", []string{"controlled-7.task.marker", "controlled-7.task.marker", "extra"}},
		{"malformed reserved", []string{"controlled-7.task.marker", "controlled-7-01.marker"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := base
			if tc.name == "overlimit" {
				options.MaxEntries = 2
				ops.names = func(context.Context, *os.File, int) ([]string, error) {
					return []string{"controlled-7.task.marker", "extra", "third"}, nil
				}
			} else {
				ops.names = func(context.Context, *os.File, int) ([]string, error) { return tc.list, nil }
			}
			got, err := readTaskModeMarkers(context.Background(), options, 7, ops)
			if err == nil || got != (taskmode.Marker{}) {
				t.Fatalf("ambiguous listing accepted: %#v %v", got, err)
			}
			options.MaxEntries = 64
		})
	}
}

func TestTaskMarkerReaderFaultInjectionIsFailClosed(t *testing.T) {
	options, _ := markerReaderFixture(t)
	writeTaskMarker(t, options.RootPath, "7", 0, "")
	base := defaultTaskMarkerOps()
	tests := []struct {
		name   string
		mutate func(*taskMarkerReadOps)
	}{
		{"listing", func(ops *taskMarkerReadOps) {
			ops.names = func(context.Context, *os.File, int) ([]string, error) {
				return []string{"controlled-7.task.marker"}, errors.New("list secret")
			}
		}},
		{"read", func(ops *taskMarkerReadOps) {
			ops.read = func(*os.File, string, int64) ([]byte, error) { return nil, errors.New("read secret") }
		}},
		{"stat", func(ops *taskMarkerReadOps) {
			ops.stat = func(*os.File, string) (os.FileInfo, error) { return nil, errors.New("stat secret") }
		}},
		{"close", func(ops *taskMarkerReadOps) {
			ops.close = func(file *os.File) error {
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				return errors.New("close secret")
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ops := base
			tc.mutate(&ops)
			got, err := readTaskModeMarkers(context.Background(), options, 7, ops)
			if err == nil || got != (taskmode.Marker{}) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("fault escaped: %#v %v", got, err)
			}
		})
	}
}

func TestTaskMarkerReaderCancellationAndConcurrency(t *testing.T) {
	options, anchor := markerReaderFixture(t)
	writeTaskMarker(t, options.RootPath, "7", 0, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := readTaskModeMarkers(ctx, options, 7, defaultTaskMarkerOps())
	if err == nil || got != (taskmode.Marker{}) {
		t.Fatalf("cancellation accepted: %#v %v", got, err)
	}
	if _, err := anchor.Stat(); err != nil {
		t.Fatalf("reader closed borrowed anchor: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := readFixture(t, options, 7)
			if err != nil || got.State != taskmode.MarkerPresent {
				t.Errorf("concurrent read: %#v %v", got, err)
			}
		}()
	}
	wg.Wait()
}

func TestTaskMarkerReaderInputValidation(t *testing.T) {
	options, _ := markerReaderFixture(t)
	for _, bad := range []controlledTaskMarkerReaderOptions{
		{}, {RootPath: "relative", RootAnchor: options.RootAnchor, CheckScope: options.CheckScope, MaxEntries: 1},
		{RootPath: options.RootPath, RootAnchor: options.RootAnchor, CheckScope: options.CheckScope, MaxEntries: maxTaskMarkerEntries + 1},
	} {
		if _, err := newControlledTaskMarkerReader(bad); !errors.Is(err, errTaskMarkerReaderInput) {
			t.Fatalf("bad options accepted: %v", err)
		}
	}
	if _, err := readTaskModeMarkers(context.Background(), options, 0, defaultTaskMarkerOps()); !errors.Is(err, errTaskMarkerReaderInput) {
		t.Fatalf("bad task accepted: %v", err)
	}
	if _, err := readTaskModeMarkers(nil, options, 7, defaultTaskMarkerOps()); !errors.Is(err, errTaskMarkerReaderInput) {
		t.Fatalf("nil context accepted: %v", err)
	}
	if _, err := readTaskModeMarkers(context.Background(), options, -1, defaultTaskMarkerOps()); !errors.Is(err, errTaskMarkerReaderInput) {
		t.Fatalf("negative task accepted: %v", err)
	}
}

func TestTaskMarkerReaderConstructorRepeatedReads(t *testing.T) {
	options, anchor := markerReaderFixture(t)
	reader, err := newControlledTaskMarkerReader(options)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader(context.Background(), 7)
	if err != nil || got.State != taskmode.MarkerClear {
		t.Fatalf("initial clear: %#v %v", got, err)
	}
	writeTaskMarker(t, options.RootPath, "7", 0, "")
	got, err = reader(context.Background(), 7)
	if err != nil || got.State != taskmode.MarkerPresent {
		t.Fatalf("present after marker: %#v %v", got, err)
	}
	if _, err := anchor.Stat(); err != nil {
		t.Fatalf("borrowed anchor closed: %v", err)
	}
}

func TestTaskMarkerReaderRejectsFIFO(t *testing.T) {
	options, _ := markerReaderFixture(t)
	path := filepath.Join(options.RootPath, "controlled-7.task.marker")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readFixture(t, options, 7)
	if err == nil || got != (taskmode.Marker{}) {
		t.Fatalf("fifo accepted: %#v %v", got, err)
	}
}

func TestTaskMarkerReaderRejectsInvalidNames(t *testing.T) {
	for _, name := range []string{
		"controlled-07.task.marker", "controlled-0.task.marker", "controlled-x.task.marker",
		"controlled-9223372036854775808.task.marker", "controlled-7-9223372036854775808.marker",
		"controlled-7-0.marker", "controlled-7--1.marker", "controlled-7-1-extra.marker",
		"Controlled-7.task.marker", "controlled-7.task.MARKER", "unknown.marker", "controlled-notes",
	} {
		t.Run(name, func(t *testing.T) {
			options, _ := markerReaderFixture(t)
			writeTaskMarker(t, options.RootPath, "7", 0, "")
			if err := os.WriteFile(filepath.Join(options.RootPath, name), []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readFixture(t, options, 7)
			if err == nil || got != (taskmode.Marker{}) {
				t.Fatalf("invalid name accepted: %#v %v", got, err)
			}
		})
	}
}

func TestTaskMarkerReaderCompleteBoundedInventory(t *testing.T) {
	options, _ := markerReaderFixture(t)
	writeTaskMarker(t, options.RootPath, "8", 0, "")
	for i := 0; i < 65; i++ {
		if err := os.WriteFile(filepath.Join(options.RootPath, "note-"+strconv.Itoa(i)), []byte("unread synthetic data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	options.MaxEntries = 66
	ops := defaultTaskMarkerOps()
	read := ops.read
	reads := 0
	ops.read = func(dir *os.File, name string, max int64) ([]byte, error) {
		reads++
		if name != "controlled-8.task.marker" {
			t.Fatalf("opened nonmarker body: %q", name)
		}
		return read(dir, name, max)
	}
	got, err := readTaskModeMarkers(context.Background(), options, 7, ops)
	if err != nil || got != (taskmode.Marker{TaskID: 7, State: taskmode.MarkerClear}) || reads != 1 {
		t.Fatalf("complete inventory: %#v %v reads=%d", got, err, reads)
	}
	options.MaxEntries--
	if got, err := readFixture(t, options, 7); err == nil || got != (taskmode.Marker{}) {
		t.Fatalf("actual inventory exceeded bound: %#v %v", got, err)
	}
}

func TestTaskMarkerReaderDoesNotReturnPartialPresent(t *testing.T) {
	options, _ := markerReaderFixture(t)
	writeTaskMarker(t, options.RootPath, "7", 0, "")
	writeTaskMarker(t, options.RootPath, "7", 1, `{"task_id":"7","intent_id":1,"controlled":false}`)
	if got, err := readFixture(t, options, 7); err == nil || got != (taskmode.Marker{}) {
		t.Fatalf("partial present escaped: %#v %v", got, err)
	}
}

func TestTaskMarkerReaderDetectsFileChanges(t *testing.T) {
	for _, change := range []string{"delete", "replace", "modify", "final replace", "new intent", "rename intent"} {
		t.Run(change, func(t *testing.T) {
			options, _ := markerReaderFixture(t)
			path := writeTaskMarker(t, options.RootPath, "7", 0, "")
			// Establish the same fixture succeeds before injecting one observed change.
			if got, err := readFixture(t, options, 7); err != nil || got.State != taskmode.MarkerPresent {
				t.Fatalf("control: %#v %v", got, err)
			}
			ops := defaultTaskMarkerOps()
			read, names := ops.read, ops.names
			mutate := func() {
				switch change {
				case "delete":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				case "replace", "final replace":
					replacement := filepath.Join(options.RootPath, "replacement")
					if err := os.WriteFile(replacement, []byte(`{"task_id":"7","controlled":true}`), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(replacement, path); err != nil {
						t.Fatal(err)
					}
				case "modify":
					if err := os.WriteFile(path, []byte(`{"task_id":"7","intent_id":0,"controlled":true}`), 0o600); err != nil {
						t.Fatal(err)
					}
				case "new intent":
					writeTaskMarker(t, options.RootPath, "7", 2, "")
				case "rename intent":
					if err := os.Rename(path, filepath.Join(options.RootPath, "controlled-7-2.marker")); err != nil {
						t.Fatal(err)
					}
				}
			}
			if change == "final replace" || change == "new intent" || change == "rename intent" {
				calls := 0
				ops.names = func(ctx context.Context, dir *os.File, max int) ([]string, error) {
					calls++
					if calls == 2 {
						mutate()
					}
					return names(ctx, dir, max)
				}
			} else {
				ops.read = func(dir *os.File, name string, max int64) ([]byte, error) {
					body, err := read(dir, name, max)
					if err != nil {
						t.Fatal(err)
					}
					mutate()
					return body, nil
				}
			}
			got, err := readTaskModeMarkers(context.Background(), options, 7, ops)
			if err == nil || got != (taskmode.Marker{}) {
				t.Fatalf("observed file change accepted: %#v %v", got, err)
			}
		})
	}
}

func TestTaskMarkerReaderDetectsRootChanges(t *testing.T) {
	for _, when := range []string{"before", "during"} {
		for _, change := range []string{"missing", "empty replacement", "symlink"} {
			t.Run(when+"/"+change, func(t *testing.T) {
				options, _ := markerReaderFixture(t)
				if got, err := readFixture(t, options, 7); err != nil || got.State != taskmode.MarkerClear {
					t.Fatalf("control: %#v %v", got, err)
				}
				mutate := func() {
					old := filepath.Join(t.TempDir(), "old")
					if err := os.Rename(options.RootPath, old); err != nil {
						t.Fatal(err)
					}
					switch change {
					case "empty replacement":
						if err := os.Mkdir(options.RootPath, 0o700); err != nil {
							t.Fatal(err)
						}
					case "symlink":
						if err := os.Symlink(old, options.RootPath); err != nil {
							t.Fatal(err)
						}
					}
				}
				ops := defaultTaskMarkerOps()
				if when == "before" {
					mutate()
				} else {
					names, calls := ops.names, 0
					ops.names = func(ctx context.Context, dir *os.File, max int) ([]string, error) {
						result, err := names(ctx, dir, max)
						calls++
						if calls == 1 {
							mutate()
						}
						return result, err
					}
				}
				got, err := readTaskModeMarkers(context.Background(), options, 7, ops)
				if err == nil || got != (taskmode.Marker{}) {
					t.Fatalf("observed root change accepted: %#v %v", got, err)
				}
			})
		}
	}
}

func TestTaskMarkerReaderCancellationDuringRead(t *testing.T) {
	for _, stage := range []string{"list", "read", "scope"} {
		t.Run(stage, func(t *testing.T) {
			options, _ := markerReaderFixture(t)
			writeTaskMarker(t, options.RootPath, "7", 0, "")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ops := defaultTaskMarkerOps()
			switch stage {
			case "list":
				names := ops.names
				ops.names = func(ctx context.Context, dir *os.File, max int) ([]string, error) {
					result, err := names(ctx, dir, max)
					cancel()
					return result, err
				}
			case "read":
				read := ops.read
				ops.read = func(dir *os.File, name string, max int64) ([]byte, error) {
					result, err := read(dir, name, max)
					cancel()
					return result, err
				}
			case "scope":
				options.CheckScope = func(context.Context, int64) error { cancel(); return nil }
			}
			got, err := readTaskModeMarkers(ctx, options, 7, ops)
			if !errors.Is(err, context.Canceled) || got != (taskmode.Marker{}) {
				t.Fatalf("cancellation lost: %#v %v", got, err)
			}
		})
	}
}
