package e2proxy

import "time"

// Hard limits keep the supported execution safety envelope bounded.
// Attempts/calls retain references and audit work, so their maps must be bounded.
const (
	maxExecutionAttempts  = 40000
	maxExecutionCalls     = 1000
	maxConcurrentRequests = 100
	maxExecutionRules     = 128
	maxProxyConnections   = 128
	// A random reference uses this many entropy bytes; Journal validates the same length.
	randomRefBytes   = 16
	maxEventPageSize = 200
)

// Bound in-memory request/response buffers and synchronous network/audit work.
const (
	maxRequestBodyBytes = 16 << 20
	// 응답은 메모리에 올려 검사·전달하므로 요청보다 크게 두되 묶는다. 검사 상한은 보호 값 길이만큼 여유를 둔다.
	maxResponseBodyBytes = 64 << 20
	maxResponseScanBytes = maxResponseBodyBytes + 1<<20
	maxRequestTimeout    = 2 * time.Minute
	maxCallTimeout       = time.Hour
	// A call's certificate cannot become an open-ended execution identity.
	maxExecutionCALifetime = 24 * time.Hour
)
