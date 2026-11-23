package main

import (
	"context"
	"log"
	"net"
	"time"
)

// api·workbench는 준비 컨테이너의 netns를 같이 쓴다(compose network_mode: service:preparation). 준비 컨테이너가
// 재시작 정책으로 다시 뜨면 새 netns가 생기고, 같이 쓰던 컨테이너는 옛 netns에 lo만 남은 채 남는다(실측). 그러면
// 화면 포트가 아무 데도 닿지 않으므로 스스로 정상 종료해 재시작 정책이 새 netns에 다시 붙이게 한다. Docker 소켓은 쓰지 않는다.

// sharedNetworkCheckInterval은 netns를 잃었는지 보는 주기다. 화면 재접속 지연은 이 주기와 재시작 정책 대기의 합이다.
const sharedNetworkCheckInterval = 2 * time.Second

// exitSharedNetworkLost는 공유 netns를 잃어 정상 종료했다는 종료 코드다(EX_TEMPFAIL).
const exitSharedNetworkLost = 75

// sharedNetworkRejoinDelay는 netns를 잃은 걸 본 뒤 종료하기 전 기다리는 시간이다. 준비 컨테이너가 아직 다시 뜨기 전에
// 종료하면 Docker가 'network_mode: container' 대상이 실행 중이 아니라며 시작에 한 번 실패하고 더 다시 띄우지 않는다
// (2026-11-23 리허설 실측: 준비 컨테이너가 안 뜬 채 api·workbench가 exited로 남음). 준비 컨테이너는 스스로 종료하면 재시작
// 정책으로 곧 다시 뜨므로, 이만큼 기다린 뒤 종료하면 새 netns에 붙는다.
const sharedNetworkRejoinDelay = 5 * time.Second

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

// watchSharedNetwork는 시작 때 lo 밖 인터페이스가 있을 때만 감시를 켜고, 그것이 모두 사라지면 돌려준 채널을 닫는다.
// 처음부터 lo만 있는 배치(망 없음)는 감시하지 않는다. 인터페이스를 못 읽은 회차는 판단하지 않고 다음 주기에 다시 본다.
func watchSharedNetwork(ctx context.Context, interval, rejoinDelay time.Duration, list func() ([]net.Interface, error)) <-chan struct{} {
	lost := make(chan struct{})
	armed, err := hasExternalInterface(list)
	if err != nil || !armed {
		return lost
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if ok, err := hasExternalInterface(list); err == nil && !ok {
					log.Print("공유 네트워크를 잃었습니다(준비 컨테이너 재시작). 준비 컨테이너가 다시 뜰 시간을 두고 정상 종료합니다")
					select {
					case <-ctx.Done():
						return
					case <-time.After(rejoinDelay):
					}
					close(lost)
					return
				}
			}
		}
	}()
	return lost
}
