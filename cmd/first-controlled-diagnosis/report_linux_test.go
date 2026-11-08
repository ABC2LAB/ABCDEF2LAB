//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type reportReadyWriter chan []byte

func (w reportReadyWriter) Write(p []byte) (int, error) {
	w <- append([]byte(nil), p...)
	return len(p), nil
}

type reportFailedWriter struct{}

func (reportFailedWriter) Write([]byte) (int, error) {
	return 0, errors.New("synthetic output failure")
}

func reportConfigFixture(t *testing.T, registered bool, modelURL string) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	c := launchTestConfig()
	for _, key := range []string{"data_dir", "key_dir", "skill_dir", "local_llm_manifest_path", "runtime_dsn_file"} {
		c[key] = filepath.Join(root, key)
	}
	c["model_base_url"] = modelURL
	c["shutdown_timeout_seconds"] = 2
	if registered {
		delete(c, "general_http")
		c["mode"] = "registered_get"
		c["registered_get"] = map[string]any{"authority_dsn_file": filepath.Join(root, "absent-authority.dsn"), "host_binding_file": filepath.Join(root, "absent-binding"), "metadata_file": filepath.Join(root, "absent-metadata"), "snapshot_dir": filepath.Join(root, "absent-snapshots"), "state_dir": filepath.Join(root, "absent-state"), "registry_file": "absent-registry"}
	} else {
		c["general_http"] = map[string]any{"policy_file": filepath.Join(root, "absent-policy"), "authority_file": filepath.Join(root, "absent-authority"), "authority_root": filepath.Join(root, "absent-root")}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	c["listen_addr"] = addr
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "launch.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path, addr
}

func TestReportCommandRejectsInvalidScope(t *testing.T) {
	for _, args := range [][]string{
		{"report"}, {"report", "-config", "/absent"}, {"report", "-config", "/absent", "-task-id", "0"},
		{"report", "-config", "/absent", "-task-id", "-1"}, {"report", "-config", "/absent", "-task-id", "not-an-id"},
		{"report", "-config", "/absent", "-task-id", "1", "extra"}, {"report", "-config", "/absent", "-task-id", "1", "-execute"},
	} {
		if code := run(context.Background(), args, io.Discard, io.Discard, nil, nil); code != 2 {
			t.Fatalf("invalid scope admitted: %v", args)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := run(ctx, []string{"report", "-config", "/absent", "-task-id", "7"}, io.Discard, io.Discard, nil, nil); code != 1 {
		t.Fatal("cancelled report admitted")
	}
	if code := run(nil, []string{"report", "-config", "/absent", "-task-id", "7"}, io.Discard, io.Discard, nil, nil); code != 1 {
		t.Fatal("nil context admitted")
	}
}

func TestReportServesWithoutExecutionFilesOrModel(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(strconv.FormatBool(registered), func(t *testing.T) {
			var targetRequests atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetRequests.Add(1) }))
			defer target.Close()
			path, addr := reportConfigFixture(t, registered, target.URL)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var closed atomic.Bool
			var opened atomic.Int64
			ready := make(reportReadyWriter, 1)
			var stderr bytes.Buffer
			done := make(chan int, 1)
			go func() {
				done <- runSavedReport(ctx, []string{"-config", path, "-task-id", "73"}, ready, &stderr, func(ctx context.Context, c launchConfig, id int64) (*savedReportService, error) {
					opened.Add(1)
					if id != 73 || c.ModelBaseURL != target.URL {
						return nil, errPreparation
					}
					// 실행 전용 파일은 전부 부재다. 읽거나 검증하면 여기까지 도달하지 못한다.
					for _, p := range []string{c.SkillDir, c.LocalLLMManifestPath} {
						if _, err := os.Stat(p); !os.IsNotExist(err) {
							return nil, errPreparation
						}
					}
					return &savedReportService{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), close: func() error { closed.Store(true); return nil }}, nil
				})
			}()
			select {
			case data := <-ready:
				var got map[string]any
				if json.Unmarshal(data, &got) != nil || got["mode"] != "saved_report" || got["execution"] != "DISABLED" || got["task_id"] != float64(73) {
					t.Fatalf("unsafe readiness: %s", data)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("report did not start")
			}
			client := &http.Client{Timeout: time.Second}
			response, err := client.Get("http://" + addr + "/api/health")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != 204 {
				t.Fatal("handler not connected")
			}
			cancel()
			select {
			case code := <-done:
				if code != 0 || stderr.Len() != 0 || !closed.Load() || opened.Load() != 1 || targetRequests.Load() != 0 {
					t.Fatalf("report isolation/closure failed: code=%d error=%s", code, stderr.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("report did not stop")
			}
		})
	}
}

func TestReportOutputAndListenFailureClosePool(t *testing.T) {
	for _, occupied := range []bool{false, true} {
		t.Run(strconv.FormatBool(occupied), func(t *testing.T) {
			path, addr := reportConfigFixture(t, false, "http://127.0.0.1:1")
			if occupied {
				l, err := net.Listen("tcp", addr)
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
			}
			closed := false
			opener := func(context.Context, launchConfig, int64) (*savedReportService, error) {
				return &savedReportService{handler: http.NotFoundHandler(), close: func() error { closed = true; return nil }}, nil
			}
			if code := runSavedReport(context.Background(), []string{"-config", path, "-task-id", "8"}, reportFailedWriter{}, io.Discard, opener); code != 1 || !closed {
				t.Fatal("failed startup did not close")
			}
		})
	}
}

func TestReportDrainsHandlersBeforePoolClose(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(strconv.FormatBool(timeout), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			ended := make(chan struct{})
			var closed atomic.Bool
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; close(ended) })}
			serving := make(chan error, 1)
			go func() { serving <- s.Serve(l) }()
			requestDone := make(chan struct{})
			go func() {
				defer close(requestDone)
				r, e := http.Get("http://" + l.Addr().String())
				if e == nil {
					r.Body.Close()
				}
			}()
			<-entered
			duration := time.Second
			if timeout {
				duration = 20 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), duration)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- closeSavedReport(ctx, s, func() error {
					select {
					case <-ended:
					default:
						return errors.New("pool closed before handler")
					}
					closed.Store(true)
					return nil
				})
			}()
			if timeout {
				if err := <-done; !errors.Is(err, context.DeadlineExceeded) || closed.Load() {
					t.Fatal("drain failure closed pool")
				}
				close(release)
			} else {
				time.Sleep(20 * time.Millisecond)
				if closed.Load() {
					t.Fatal("pool closed during handler")
				}
				close(release)
				if err := <-done; err != nil || !closed.Load() {
					t.Fatal("drain did not close pool after handler")
				}
			}
			<-requestDone
			if err := <-serving; !errors.Is(err, http.ErrServerClosed) {
				t.Fatal(err)
			}
		})
	}
}

func TestReportCloseErrorIsNotSuccess(t *testing.T) {
	sentinel := errors.New("synthetic close failure")
	if err := closeSavedReport(context.Background(), &http.Server{}, func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal("pool close failure hidden")
	}
	if !strings.Contains(usage, "report -config <launch-file> -task-id <actual-id>") {
		t.Fatal("report usage missing")
	}
}
