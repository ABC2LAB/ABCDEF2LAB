package e2runner

import "testing"

func TestProcessLimitsReserveEngineAcrossSlots(t *testing.T) {
	for _, tc := range []struct {
		slots int
		want  uint64
	}{{1, 128}, {2, 96}, {3, 64}, {4, 48}, {16, 12}, {24, 8}} {
		got, err := processLimits(tc.slots)
		if err != nil || got.CallTasks != tc.want || got.BashSlots != tc.slots || got.ContainerTasks != 256 || got.EngineReserveTasks != 64 || uint64(tc.slots)*got.CallTasks+got.EngineReserveTasks > got.ContainerTasks {
			t.Fatalf("슬롯 %d의 호출별 PID 배분 오류: %+v, %v", tc.slots, got, err)
		}
	}
	for _, slots := range []int{-1, 0, 25, 1 << 30} {
		if _, err := processLimits(slots); err == nil {
			t.Fatal("helper 최소 task 예산 없이 실행 허용", slots)
		}
	}
}
