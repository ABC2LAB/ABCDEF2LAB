package exploreboundary

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// drainPoll은 봉인 전 드레인이 남은 입구 처리 수를 다시 보는 간격이다. 드레인 상한은 Manager.CloseWait다.
const drainPoll = 10 * time.Millisecond

// IngressConns는 입구 http.Server의 끝나지 않은 연결 수와 돌고 있는 Serve 루프 수를 센다. 봉인 전 드레인에 쓴다.
//
// http.Server.Close는 실행 중 핸들러를 기다리지 않는다. 연결 goroutine은 핸들러가 돌아온 뒤에야 StateClosed가 되므로,
// 이 수는 송신 ticket을 받기 전(본문을 읽는 중) 핸들러와, 입구를 닫기 전에 요청을 읽어 Close 뒤에 늦게 시작한
// 핸들러가 끝날 때까지 남는다(#615 리뷰). 가로챈(CONNECT) 연결은 여기서 빠지고 Proxy의 터널 수로 센다.
type IngressConns struct {
	mu      sync.Mutex
	open    map[net.Conn]struct{}
	serving int
}

func NewIngressConns() *IngressConns {
	return &IngressConns{open: map[net.Conn]struct{}{}}
}

// ConnState는 입구 http.Server의 ConnState 훅이다.
func (c *IngressConns) ConnState(conn net.Conn, state http.ConnState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch state {
	case http.StateNew, http.StateActive, http.StateIdle:
		c.open[conn] = struct{}{}
	case http.StateHijacked, http.StateClosed:
		delete(c.open, conn)
	}
}

// Serve는 Serve 루프 수를 센 채로 server.Serve를 돈다. 루프가 돌아오기 전에는 막 받은 연결이 아직 등록되지 않았을 수 있다.
func (c *IngressConns) Serve(server *http.Server, l net.Listener) error {
	c.mu.Lock()
	c.serving++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.serving--
		c.mu.Unlock()
	}()
	return server.Serve(l)
}

func (c *IngressConns) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.open) + c.serving
}

// Drain은 입구 서버를 닫은 뒤, 아직 원자료 행을 남길 수 있는 처리(입구 연결·Serve 루프·터널)가 끝나길 wait까지 기다리고
// 남은 수를 돌려준다. 그 사이 입구를 닫기 전에 읽은 CONNECT가 늦게 연 터널도 닫는다. 연결 수를 터널 수보다 먼저 읽는다:
// 가로챈 연결은 Proxy 잠금 안에서 터널에 들어가므로, 연결 수에서 빠진 뒤 읽은 터널 수에는 들어 있다.
func Drain(wait time.Duration, conns *IngressConns, proxies ...*Proxy) int {
	deadline := time.Now().Add(wait)
	for {
		for _, p := range proxies {
			p.CloseTunnels()
		}
		n := conns.pending()
		for _, p := range proxies {
			n += p.Tunnels()
		}
		if n == 0 || !time.Now().Before(deadline) {
			return n
		}
		time.Sleep(drainPoll)
	}
}
