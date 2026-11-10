package main

import (
	"bytes"
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

func TestMaskPath(t *testing.T) {
	for name, tc := range map[string]struct {
		path    string
		pattern string
		masked  int
	}{
		"plain":             {"/api/Users", "/api/Users", 0},
		"root":              {"/", "/", 0},
		"trailing slash":    {"/api/Users/", "/api/Users/", 0},
		"digits":            {"/api/Users/123", "/api/Users/{id}", 1},
		"digits and letter": {"/v2/items", "/v2/items", 0},
		"uuid":              {"/o/123e4567-e89b-12d3-a456-426614174000", "/o/{id}", 1},
		"hex":               {"/o/0123456789abcdef", "/o/{id}", 1},
		"short hex":         {"/o/abc123", "/o/abc123", 0},
		"encoded digits":    {"/reset/abcdefghij0123456789", "/reset/{id}", 1},
		"encoded mixed":     {"/reset/AbCdEfGhIjKlMnOpQrSt", "/reset/{id}", 1},
		"endpoint name":     {"/authentication-details", "/authentication-details", 0},
		"upper name":        {"/AUTHENTICATION-DETAILS", "/AUTHENTICATION-DETAILS", 0},
		"short mixed":       {"/getUser", "/getUser", 0},
		"email":             {"/users/a@example.test", "/users/{id}", 1},
		"encoded email":     {"/users/a%40example.test", "/users/{id}", 1},
		"base64 padding":    {"/t/abc%3D", "/t/{id}", 1},
		"decoded plain":     {"/a%2Db", "/a-b", 0},
		"encoded slash":     {"/a%2Fb", "/{id}", 1},
		"bad escape":        {"/a%ZZ/b", "/{id}/b", 1},
		"long":              {"/" + strings.Repeat("g", maxSegmentLength+1), "/{id}", 1},
		"exact long":        {"/" + strings.Repeat("g", maxSegmentLength), "/" + strings.Repeat("g", maxSegmentLength), 0},
		"not a path":        {"*", "{id}", 1},
		"two values":        {"/a/1/b/2", "/a/{id}/b/{id}", 2},
	} {
		t.Run(name, func(t *testing.T) {
			pattern, masked := maskPath(tc.path)
			if pattern != tc.pattern || masked != tc.masked {
				t.Fatalf("got %q %d want %q %d", pattern, masked, tc.pattern, tc.masked)
			}
		})
	}
}

func TestMaskPathBoundsSegments(t *testing.T) {
	fits := strings.Repeat("/a", maxPathSegments-1)
	if pattern, masked := maskPath(fits); pattern != fits || masked != 0 {
		t.Fatal(pattern, masked)
	}
	tooLong := fits + "/a"
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
	want := []pathRow{{1, "GET", "/api/Users/{id}", 1}, {2, "POST", "/users/{id}", 1}, {3, "GET", "/{id}/ok", 1}}
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
