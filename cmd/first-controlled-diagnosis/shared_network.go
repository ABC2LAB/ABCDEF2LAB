package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

// api·workbench는 준비 컨테이너의 netns를 같이 쓴다(compose network_mode: service:preparation). 준비 컨테이너가
// 재시작 정책으로 다시 뜨면 새 netns가 생기고, 같이 쓰던 컨테이너는 옛 netns에 lo만 남은 채 남는다(실측). 그러면
// 화면 포트가 아무 데도 닿지 않으므로 스스로 정상 종료해 재시작 정책이 새 netns에 다시 붙이게 한다. Docker 소켓은 쓰지 않는다.
//
// 종료 시점: 준비 컨테이너가 아직 다시 뜨기 전에 종료하면 Docker는 'network_mode: container' 대상이 실행 중이 아니라며
// 시작에 실패하고, 재시작 정책은 시작 실패 뒤 더 시도하지 않는다(2026-11-23 리허설 실측: api·workbench가 exited로 남음).
// 그래서 시간을 재지 않고 준비 서비스가 다시 응답하는 것을 확인한 뒤에만 종료한다. 준비 서비스가 응답하면 그 컨테이너가
// 실행 중이고 새 netns가 있으므로 재시작이 붙는다.

// sharedNetworkCheckInterval은 netns를 잃었는지, 잃은 뒤 준비 서비스가 다시 응답하는지 보는 주기다.
const sharedNetworkCheckInterval = 2 * time.Second

// exitSharedNetworkLost는 공유 netns를 잃어 정상 종료했다는 종료 코드다(EX_TEMPFAIL).
const exitSharedNetworkLost = 75

func hasExternalInterface(list func() ([]net.Interface, error)) (bool, error) {
	ifaces, err := list()
	if err != nil {
		return false, err
	}
	for _, i := range ifaces {
		if i.Flags&net.FlagLoopback == 0 {
			return true, nil
		}
	}
	return false, nil
}

// watchSharedNetwork는 시작 때 lo 밖 인터페이스가 있을 때만 감시를 켠다. 그것이 모두 사라지면 ready가 참이 될 때까지
// 기다렸다가 돌려준 채널을 닫는다. 처음부터 lo만 있는 배치(망 없음)는 감시하지 않는다. 인터페이스를 못 읽은 회차는
// 판단하지 않고 다음 주기에 다시 본다. 준비 서비스가 끝내 돌아오지 않으면 닫지 않는다(운영자가 up -d로 다시 띄운다).
func watchSharedNetwork(ctx context.Context, interval time.Duration, list func() ([]net.Interface, error), ready func(context.Context) bool) <-chan struct{} {
	lost := make(chan struct{})
	armed, err := hasExternalInterface(list)
	if err != nil || !armed {
		return lost
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		gone := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if !gone {
				if ok, err := hasExternalInterface(list); err == nil && !ok {
					gone = true
					log.Print("공유 네트워크를 잃었습니다(준비 컨테이너 재시작). 준비 서비스가 다시 응답하면 정상 종료합니다")
				}
				continue
			}
			check, cancel := context.WithTimeout(ctx, interval)
			ok := ready(check)
			cancel()
			if ok {
				log.Print("준비 서비스가 다시 응답합니다. 새 네트워크에 붙도록 정상 종료합니다")
				close(lost)
				return
			}
		}
	}()
	return lost
}

// preparationAnswers는 api가 비공개 소켓(볼륨, 네트워크 아님)으로 준비 서비스에 닿는지 본다. 옛 준비 서비스는 끝났으므로
// 응답하면 다시 뜬 준비 서비스다.
func preparationAnswers(endpoint diagnosisprep.Endpoint) func(context.Context) bool {
	return func(ctx context.Context) bool {
		res, err := endpoint.Call(ctx, diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "shared-network-rejoin", Principal: "ARTEX", Command: diagnosisprep.Catalog})
		return err == nil && res.Error == ""
	}
}

// apiRejoinReady는 workbench가 옛 netns의 loopback으로 api health를 본다. api는 준비 서비스가 응답할 때 ready를 주고,
// 그 뒤 스스로 종료한다. 그래서 ready이거나, 있던 api가 사라져 연결이 거부되면(api가 이미 종료) 준비 서비스가 돌아온 것이다.
func apiRejoinReady(client *http.Client, origin string) func(context.Context) bool {
	return func(ctx context.Context) bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/api/product/health", nil)
		if err != nil {
			return false
		}
		res, err := client.Do(req)
		if err != nil {
			return errors.Is(err, syscall.ECONNREFUSED)
		}
		defer res.Body.Close()
		var body struct {
			Ready bool `json:"ready"`
		}
		raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		return err == nil && res.StatusCode == http.StatusOK && json.Unmarshal(raw, &body) == nil && body.Ready
	}
}
