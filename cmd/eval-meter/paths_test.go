package main

import (
	"bytes"
	"encoding/hex"
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

const (
	pathSentinel = "SENTINELVALUE"
	shortAccount = "jimsmithexample"
	shortToken   = "XyZaBc9XyZaBc9XyZ"
)

var testPathKey = bytes.Repeat([]byte{7}, pathKeyBytes)

func writePathKey(t *testing.T, key []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "path.key")
	if err := os.WriteFile(path, key, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadPathKey(t *testing.T) {
	good := writePathKey(t, testPathKey, 0o600)
	if key, code := readPathKey(good); code != "" || !bytes.Equal(key, testPathKey) {
		t.Fatal(code)
	}
	link := filepath.Join(t.TempDir(), "link.key")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"readable by others": writePathKey(t, testPathKey, 0o644),
		"short":              writePathKey(t, testPathKey[:pathKeyBytes-1], 0o600),
		"long":               writePathKey(t, append(append([]byte{}, testPathKey...), 1), 0o600),
		"symlink":            link,
		"missing":            filepath.Join(t.TempDir(), "none"),
		"directory":          t.TempDir(),
	} {
		if _, code := readPathKey(path); code != pathKeyRequired {
			t.Fatal(name, code)
		}
	}
}

func TestEnablePathLogRejectsWrongKeyLength(t *testing.T) {
	upstream, _ := recordingUpstream(t)
	m, _, dir := newTestEngine(t, upstream)
	if code := m.enablePathLog(testPathKey[:8]); code != invalidInput {
		t.Fatal(code)
	}
	if _, err := os.Stat(filepath.Join(dir, enginePathLogName)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// 가림 규칙의 합성 벡터는 testdata에 두어 다른 언어의 소비자도 같은 값으로 시험할 수 있게 한다.
func TestMaskPath(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "path-mask-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Profile string `json:"profile"`
		KeyHex  string `json:"key_hex"`
		KeyRef  string `json:"key_ref"`
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
	key, err := hex.DecodeString(file.KeyHex)
	if file.Profile != pathMaskProfile || len(file.Vectors) < 20 || err != nil || len(key) != pathKeyBytes {
		t.Fatal(file.Profile, len(file.Vectors), err)
	}
	if got := pathKeyRef(key); got != file.KeyRef || got == pathKeyRef(testPathKey) {
		t.Fatal(got)
	}
	for _, tc := range file.Vectors {
		t.Run(tc.Name, func(t *testing.T) {
			pattern, masked := maskPath(tc.Path, key)
			if pattern != tc.PathPattern || masked != tc.MaskedSegments {
				t.Fatalf("got %q %d want %q %d", pattern, masked, tc.PathPattern, tc.MaskedSegments)
			}
		})
	}
}

func TestMaskPathBoundsSegments(t *testing.T) {
	fits := strings.Repeat("/1", maxPathSegments) + "/"
	if pattern, masked := maskPath(fits, testPathKey); pattern != strings.Repeat("/{id}", maxPathSegments)+"/" || masked != maxPathSegments {
		t.Fatal(pattern, masked)
	}
	tooLong := strings.Repeat("/a", maxPathSegments+1)
	if pattern, masked := maskPath(tooLong, testPathKey); pattern != maskedLongPath || masked != maxPathSegments+1 {
		t.Fatal(pattern, masked)
	}
}

// startPathEngine은 경로 기록을 켠 계측기 앞단 주소와 경로 기록 파일 위치를 준다.
func startPathEngine(t *testing.T) (*engineMeter, string, string) {
	t.Helper()
	upstream, _ := recordingUpstream(t)
	m, front, dir := newTestEngine(t, upstream)
	if code := m.enablePathLog(testPathKey); code != "" {
		t.Fatal(code)
	}
	return m, front.URL, filepath.Join(dir, enginePathLogName)
}

func newPathEngine(t *testing.T) (*engineMeter, func(string, string) (int, string), string) {
	t.Helper()
	m, base, path := startPathEngine(t)
	do := func(method, target string) (int, string) {
		req, err := http.NewRequest(method, base+target, strings.NewReader(pathSentinel))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Cookie", "a="+pathSentinel)
		return send(t, req)
	}
	return m, do, path
}

func TestEnginePathLogRecordsMaskedForwardedRequests(t *testing.T) {
	m, do, path := newPathEngine(t)
	do(http.MethodGet, "/api/Users/123?token="+pathSentinel)
	do(http.MethodPost, "/users/"+pathSentinel+"@example.test")
	do(http.MethodGet, "/a%2Fb/ok")
	do(http.MethodGet, "/profile/"+shortAccount)
	do(http.MethodGet, "/reset/"+shortToken)
	m.seal()
	if status, _ := do(http.MethodGet, "/after/seal"); status != http.StatusServiceUnavailable {
		t.Fatal(status)
	}
	h := func(segment string) string { return pseudonym(segment, testPathKey) }
	ref := pathKeyRef(testPathKey)
	want := []pathRow{
		{1, "GET", "/" + h("api") + "/" + h("Users") + "/{id}", 1, pathMaskProfile, ref, false, true},
		{2, "POST", "/" + h("users") + "/{id}", 1, pathMaskProfile, ref, false, true},
		{3, "GET", "/{id}/" + h("ok"), 1, pathMaskProfile, ref, false, true},
		{4, "GET", "/" + h("profile") + "/" + h(shortAccount), 0, pathMaskProfile, ref, false, true},
		{5, "GET", "/" + h("reset") + "/" + h(shortToken), 0, pathMaskProfile, ref, false, true},
	}
	if got := readRows[pathRow](t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 규칙으로 값이라고 볼 수 없는 짧은 계정명·토큰도 원문으로 남지 않는다.
	for _, raw := range []string{pathSentinel, shortAccount, shortToken, "/profile", "/reset", "/Users"} {
		if bytes.Contains(body, []byte(raw)) {
			t.Fatal(raw, string(body))
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal(err, info.Mode())
	}
}

// 인증 헤더는 빈 값이 아닌 줄이 있는지만 남는다. 값·쿠키 이름은 기록 파일 어디에도 없다.
func TestEnginePathLogRecordsAuthHeaderPresenceOnly(t *testing.T) {
	m, base, path := startPathEngine(t)
	cases := []struct {
		name          string
		header        http.Header
		authorization bool
		cookie        bool
	}{
		{"none", http.Header{}, false, false},
		{"authorization", http.Header{"Authorization": {"Bearer " + pathSentinel}}, true, false},
		{"cookie", http.Header{"Cookie": {"sid=" + pathSentinel}}, false, true},
		{"both", http.Header{"Authorization": {"Basic " + pathSentinel}, "Cookie": {"sid=" + pathSentinel}}, true, true},
		{"empty values", http.Header{"Authorization": {""}, "Cookie": {"   "}}, false, false},
		// 첫 줄이 비어 있어도 뒤 줄에 값이 있으면 자격이 실린 요청이다.
		{"later line", http.Header{"Cookie": {"", "sid=" + pathSentinel}}, false, true},
	}
	for _, tc := range cases {
		req, err := http.NewRequest(http.MethodGet, base+"/item", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = tc.header
		if status, _ := send(t, req); status == http.StatusServiceUnavailable {
			t.Fatal(tc.name, status)
		}
	}
	m.seal()
	rows := readRows[pathRow](t, path)
	if len(rows) != len(cases) {
		t.Fatalf("%+v", rows)
	}
	for i, tc := range cases {
		if rows[i].HasAuthorization != tc.authorization || rows[i].HasCookie != tc.cookie {
			t.Errorf("%s: %+v", tc.name, rows[i])
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{pathSentinel, "sid", "Bearer", "Basic"} {
		if bytes.Contains(body, []byte(raw)) {
			t.Fatal(raw, string(body))
		}
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
	if code := m.enablePathLog(testPathKey); code != logExists {
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
	if code := m.enablePathLog(testPathKey); code != "" {
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
	key := writePathKey(t, testPathKey, 0o600)
	for _, args := range [][]string{
		{"-role", "observer", "-listen", "127.0.0.1:0", "-upstream", "http://t:1", "-log-dir", path, "-allow-path", "/x", "-path-log", "-path-key-file", key},
		{"-role", "probe", "-path-log"},
		{"-role", "engine", "-listen", "127.0.0.1:0", "-upstream", "http://t:1", "-log-dir", path, "-path-log"},
		{"-role", "engine", "-listen", "127.0.0.1:0", "-upstream", "http://t:1", "-log-dir", path, "-path-key-file", key},
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
	args := []string{"-role", "engine", "-listen", "127.0.0.1:0", "-upstream", "http://t:1", "-log-dir", path, "-path-log",
		"-path-key-file", writePathKey(t, testPathKey, 0o600)}
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
