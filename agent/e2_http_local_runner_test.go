package agent

import "github.com/Autumn-27/artex/e2runner"
import "github.com/Autumn-27/artex/e2proxy"

// 제품의 로컬 어댑터는 server 패키지에 있다. agent 시험은 같은 위임만 쓴다.
type localRunnerForTest struct{ *e2runner.Runner }

func (r localRunnerForTest) SnapshotForIntent(id int64) e2proxy.ExecutionView {
	return r.Runner.Execution().SnapshotForIntent(id)
}
func (r localRunnerForTest) HasIntent(id int64) bool { return r.Runner.Execution().HasIntent(id) }
func (r localRunnerForTest) RedactOutput(data []byte) []byte {
	return r.Runner.Execution().RedactOutput(data)
}
