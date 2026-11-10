package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const pathSentinel = "SENTINELVALUE"

// 가림 규칙의 합성 벡터는 testdata에 두어 다른 언어의 소비자도 같은 값으로 시험할 수 있게 한다.
func TestMaskPath(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "path-mask-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Profile string `json:"profile"`
		Vectors []struct {
			Name           string `json:"name"`
			Path           string `json:"path"`
			PathPattern    string `json:"path_pattern"`
			MaskedSegments int    `json:"masked_segments"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if file.Profile != pathMaskProfile || len(file.Vectors) < 20 {
		t.Fatal(file.Profile, len(file.Vectors))
	}
	for _, tc := range file.Vectors {
		t.Run(tc.Name, func(t *testing.T) {
			pattern, masked := maskPath(tc.Path)
			if pattern != tc.PathPattern || masked != tc.MaskedSegments {
				t.Fatalf("got %q %d want %q %d", pattern, masked, tc.PathPattern, tc.MaskedSegments)
			}
		})
	}
}

func TestMaskPathBoundsSegments(t *testing.T) {
	fits := strings.Repeat("/a", maxPathSegments) + "/"
	if pattern, masked := maskPath(fits); pattern != fits || masked != 0 {
		t.Fatal(pattern, masked)
	}
	tooLong := strings.Repeat("/a", maxPathSegments+1)
	if pattern, masked := maskPath(tooLong); pattern != maskedLongPath || masked != maxPathSegments+1 {
		t.Fatal(pattern, masked)
	}
}

func newPathEngine(t *testing.T) (*engineMeter, func(string, string) (int, string), string) {
	t.Helper()
	upstream, _ := recordingUpstream(t)
	m, front, dir := newTestEngine(t, upstream)
	if code := m.enablePathLog(); code != "" {
		t.Fatal(code)
	}
	do := func(method, target string) (int, string) {
		req, err := http.NewRequest(method, front.URL+target, strings.NewReader(pathSentinel))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Cookie", "a="+pathSentinel)
		return send(t, req)
	}
	return m, do, filepath.Join(dir, enginePathLogName)
}

func TestEnginePathLogRecordsMaskedForwardedRequests(t *testing.T) {
	m, do, path := newPathEngine(t)
	do(http.MethodGet, "/api/Users/123?token="+pathSentinel)
	do(http.MethodPost, "/users/"+pathSentinel+"@example.test")
	do(http.MethodGet, "/a%2Fb/ok")
	m.seal()
	if status, _ := do(http.MethodGet, "/after/seal"); status != http.StatusServiceUnavailable {
		t.Fatal(status)
	}
	want := []pathRow{{1, "GET", "/api/Users/{id}", 1, pathMaskProfile}, {2, "POST", "/users/{id}", 1, pathMaskProfile}, {3, "GET", "/{id}/ok", 1, pathMaskProfile}}
	if got := readRows[pathRow](t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	body, err := os.ReadFile(path)
	if err != nil || bytes.Contains(body, []byte(pathSentinel)) {
		t.Fatal(err, string(body))
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal(err, info.Mode())
	}
}

func TestEnginePathLogIsOffByDefault(t *testing.T) {
	upstream, _ := recordingUpstream(t)
	_, front, dir := newTestEngine(t, upstream)
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/one", nil)
	send(t, req)
	if _, err := os.Stat(filepath.Join(dir, enginePathLogName)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestEnginePathLogRefusesExistingFile(t *testing.T) {
	m, _, _ := newPathEngine(t)
	if code := m.enablePathLog(); code != logExists {
		t.Fatal(code)
	}
}

func TestEnginePathLogFailureStopsForwarding(t *testing.T) {
	m, do, _ := newPathEngine(t)
	_ = m.paths.file.Close()
	do(http.MethodGet, "/one")
	if status, _ := do(http.MethodGet, "/two"); status != http.StatusServiceUnavailable {
		t.Fatalf("request after a lost path row was forwarded: %d", status)
	}
}

func TestEnginePathLogCloseReportsFailure(t *testing.T) {
	upstream, _ := recordingUpstream(t)
	path, dir := privateTestDir(t)
	m, code := newEngineMeter(dir, targetURL(t, upstream), time.Now, 0)
	if code != "" {
		t.Fatal(code)
	}
	if code := m.enablePathLog(); code != "" {
		t.Fatal(code)
	}
	_ = m.paths.file.Close()
	if err := m.close(); err == nil {
		t.Fatal("a path log that cannot be closed must fail the run")
	}
	if _, err := os.Stat(filepath.Join(path, enginePathLogName)); err != nil {
		t.Fatal(err)
	}
}

func TestPathLogFlagIsEngineOnly(t *testing.T) {
	path, _ := privateTestDir(t)
	for _, args := range [][]string{
		{"-role", "observer", "-listen", "127.0.0.1:0", "-upstream", "http://t:1", "-log-dir", path, "-allow-path", "/x", "-path-log"},
		{"-role", "probe", "-path-log"},
	} {
		var stderr bytes.Buffer
		if code := run(args, strings.NewReader(""), io.Discard, &stderr); code != 2 || !strings.HasPrefix(stderr.String(), invalidInput) {
			t.Fatal(args, code, stderr.String())
		}
	}
}

func TestRunEngineRejectsLeftoverPathLog(t *testing.T) {
	path, dir := privateTestDir(t)
	file, code := createPrivateFile(dir, enginePathLogName)
	if code != "" {
		t.Fatal(code)
	}
	_ = file.Close()
	var stderr bytes.Buffer
	args := []string{"-role", "engine", "-listen", "127.0.0.1:0", "-upstream", "http://t:1", "-log-dir", path, "-path-log"}
	if exit := run(args, strings.NewReader(""), io.Discard, &stderr); exit == 0 || !strings.HasPrefix(stderr.String(), logExists) {
		t.Fatal(exit, stderr.String())
	}
	// 실패한 시작은 요청 기록을 남기지 않아서, 남은 경로 기록만 치우면 다시 시작할 수 있다.
	if _, err := os.Stat(filepath.Join(path, engineLogName)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestPathLogSyncFailureMarksSealIncomplete(t *testing.T) {
	m, do, logPath := newPathEngine(t)
	do(http.MethodGet, "/one")
	_ = m.paths.file.Close()
	m.seal()
	if marker := readMarker[sealedMarker](t, filepath.Join(filepath.Dir(logPath), sealedName)); marker.LogComplete {
		t.Fatalf("%+v", marker)
	}
}
