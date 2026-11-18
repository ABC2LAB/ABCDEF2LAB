package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 요청 하드 상한: 두 입구로 몰아서 보내도 대상에 닿는 요청은 정확히 R건이고, 나머지는 봉인 뒤 거부와 같은 503으로 바로
// 거부되며 rejected_over_cap 행과 건수가 남는다(2026-11-19 사용자 결정 1, B·O 같은 계측기 함수).
func TestRequestCapAdmitsExactlyTheCapUnderABurst(t *testing.T) {
	const capN, burst = 5, 40
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(30 * time.Millisecond) // 요청들이 겹치게 둔다
		_, _ = w.Write([]byte("upstream"))
	}))
	t.Cleanup(upstream.Close)
	path, dir := privateTestDir(t)
	m, code := newEngineMeter(dir, targetURL(t, upstream), time.Now, 0, capN)
	if code != "" {
		t.Fatal(code)
	}
	mainFront, exploreFront := httptest.NewServer(m), httptest.NewServer(m.ingress(ingressExplore))
	t.Cleanup(func() { mainFront.Close(); exploreFront.Close(); _ = m.close() })

	var wg sync.WaitGroup
	var ok, refused atomic.Int64
	start := make(chan struct{})
	for i := 0; i < burst; i++ {
		front := mainFront
		if i%2 == 1 {
			front = exploreFront
		}
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			<-start
			resp, err := http.Get(url + "/x")
			if err != nil {
				t.Error(err)
				return
			}
			_ = resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusOK:
				ok.Add(1)
			case http.StatusServiceUnavailable:
				refused.Add(1)
			}
		}(front.URL)
	}
	close(start)
	wg.Wait()
	if hits.Load() != capN || ok.Load() != capN || refused.Load() != burst-capN {
		t.Fatalf("target hits %d, ok %d, refused %d", hits.Load(), ok.Load(), refused.Load())
	}
	// 봉인 뒤 거부는 따로 센다(상한 거부와 섞지 않는다)
	m.seal()
	resp, err := http.Get(mainFront.URL + "/after-seal")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("after seal %d", resp.StatusCode)
	}
	rows := readRows[engineRow](t, filepath.Join(path, engineLogName))
	outcomes := map[string]int{}
	for _, row := range rows {
		outcomes[row.Kind+"/"+row.Outcome]++
		if row.Kind == "rejected" && (row.Status != http.StatusServiceUnavailable || row.Sent != sentNo) {
			t.Fatalf("rejected row %+v", row)
		}
	}
	if outcomes["request/completed"] != capN || outcomes["rejected/rejected_over_cap"] != burst-capN ||
		outcomes["rejected/rejected_after_seal"] != 1 || len(rows) != burst+1 {
		t.Fatalf("rows %v", outcomes)
	}
	drained := readMarker[drainedMarker](t, filepath.Join(path, drainedName))
	sealed := readMarker[sealedMarker](t, filepath.Join(path, sealedName))
	// drain 표식은 진행 중 요청이 없어 봉인 순간에 쓰였다. 그 뒤 봉인 거부 1건은 행에만 있다.
	if drained.Forwarded != capN || drained.RequestCap != capN || drained.RejectedOverCap != burst-capN ||
		drained.RejectedAfterSeal != 0 || sealed.RequestCap != capN || sealed.RejectedOverCap != burst-capN {
		t.Fatalf("drained %+v sealed %+v", drained, sealed)
	}
}

// 상한 0은 지금과 같다(거부 없음).
func TestRequestCapZeroMeansNoCap(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(upstream.Close)
	_, dir := privateTestDir(t)
	m, code := newEngineMeter(dir, targetURL(t, upstream), time.Now, 0, 0)
	if code != "" {
		t.Fatal(code)
	}
	front := httptest.NewServer(m)
	t.Cleanup(func() { front.Close(); _ = m.close() })
	for i := 0; i < 8; i++ {
		resp, err := http.Get(front.URL + "/x")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: %d", i, resp.StatusCode)
		}
	}
}

func TestRequestCapFlagBelongsToTheEngineAndIsBounded(t *testing.T) {
	engine := []string{"-role", "engine", "-listen", "127.0.0.1:0", "-upstream", "http://target:3000", "-log-dir", "/x"}
	cases := [][]string{
		append(append([]string(nil), engine...), "-request-cap", "-1"),
		append(append([]string(nil), engine...), "-request-cap", strconv.Itoa(maxRequestCap+1)),
		{"-role", "observer", "-listen", "127.0.0.1:0", "-upstream", "http://target:3000", "-log-dir", "/x",
			"-allow-path", "/api/Challenges", "-request-cap", "5"},
		{"-role", "probe", "-request-cap", "5"},
	}
	for _, args := range cases {
		var stderr bytes.Buffer
		if code := run(args, strings.NewReader(""), &bytes.Buffer{}, &stderr); code != 2 ||
			!strings.HasPrefix(stderr.String(), invalidInput) {
			t.Fatalf("%v: exit %d stderr %q", args, code, stderr.String())
		}
	}
}
