package agent

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// 크롤 명령은 대상 스킴 하나만 열고, 프로그램 소유 URL만 받는다.
func TestE2HTTPCrawlCommandScheme(t *testing.T) {
	for target, proto := range map[string]string{"https://app.example.test/a?b=1": "--proto '=https'", "http://app.example.test:8080/": "--proto '=http'"} {
		command, ok := e2HTTPCrawlCommand(target)
		if !ok || !strings.Contains(command, proto) || strings.Count(command, "--proto") != 1 || !strings.Contains(command, " --raw ") {
			t.Fatalf("%s: %q", target, command)
		}
	}
	for _, target := range []string{"ftp://app.example.test/", "file:///etc/passwd", "//app.example.test/", "https:///no-host", "%zz"} {
		if _, ok := e2HTTPCrawlCommand(target); ok {
			t.Fatalf("accepted %q", target)
		}
	}
}

// 실제 curl로 chunked 응답을 받아 크롤 파서와 같은 방식(http.ReadResponse)으로 읽는다. --raw가 없으면 curl이 chunk를
// 풀고 헤더엔 chunked가 남아 본문 읽기가 실패한다(11/13 실제 모델 점검의 body_incomplete).
func TestE2HTTPCrawlCommandParsesChunkedResponse(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("SKIP: curl fixture executable unavailable")
	}
	const page = "<html><body><a href=\"/next\">next</a></body></html>"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, page[:20])
		w.(http.Flusher).Flush() // 길이를 모르는 응답이라 chunked로 나간다
		_, _ = io.WriteString(w, page[20:])
	}))
	defer server.Close()
	command, ok := e2HTTPCrawlCommand(server.URL + "/")
	if !ok {
		t.Fatal("fixture URL rejected")
	}
	parse := func(command string) (string, []string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "sh", "-c", command).Output()
		if err != nil {
			t.Fatalf("curl failed: %v", err)
		}
		response, err := http.ReadResponse(bufio.NewReader(strings.NewReader(string(out))), &http.Request{Method: "GET"})
		if err != nil {
			return "", nil, err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		return string(body), response.TransferEncoding, err
	}
	body, encoding, err := parse(command)
	if err != nil || body != page || len(encoding) != 1 || encoding[0] != "chunked" {
		t.Fatalf("chunked page not read intact: err=%v encoding=%v body=%q", err, encoding, body)
	}
	// 대조: --raw를 빼면 같은 응답이 읽기 오류가 난다(이 시험이 실제로 chunked를 거쳤다는 확인).
	if _, _, err := parse(strings.Replace(command, " --raw ", " ", 1)); err == nil {
		t.Fatal("control without --raw unexpectedly parsed; fixture is not exercising chunked framing")
	}
}
