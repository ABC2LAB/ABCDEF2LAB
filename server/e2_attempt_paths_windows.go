//go:build windows

package server

import "os"

// eval-e2 제어 전송이 Linux 전용이라 이 경로는 쓰이기 전에 시작이 거부된다. 빌드만 맞춘다.
const e2AttemptPathsOpenFlags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
