package diagnosisflow

import "testing"

// NoDeepRuns는 진단 Worker가 한 번도 없고 흐름이 깨지지 않았을 때만 참이다(단계와 무관).
func TestControllerNoDeepRuns(t *testing.T) {
	c := &Controller{}
	c.record.Snapshot.Stage = "exploring"
	if !c.NoDeepRuns() || c.DeepReady() {
		t.Fatal("진단 Worker가 없는 흐름")
	}
	c.record.Snapshot.DeepRuns = 1
	if c.NoDeepRuns() {
		t.Fatal("진단 Worker가 있었는데 없음으로 봄")
	}
	c.record.Snapshot.DeepRuns = 0
	c.record.Snapshot.Failure = "x"
	if c.NoDeepRuns() {
		t.Fatal("실패한 흐름")
	}
	c.record.Snapshot.Failure, c.record.Snapshot.Stage = "", "failed"
	if c.NoDeepRuns() {
		t.Fatal("실패 단계")
	}
	c.record.Snapshot.Stage, c.poisoned = "diagnosing", true
	if c.NoDeepRuns() {
		t.Fatal("깨진 흐름")
	}
}
