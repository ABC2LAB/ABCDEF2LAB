//go:build !windows

package server

import (
	"os"
	"syscall"
)

// 링크를 따라가지 않고 새로 만든다. eval-e2는 Linux에서만 시작한다.
const e2AttemptPathsOpenFlags = os.O_WRONLY | os.O_CREATE | os.O_EXCL | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
