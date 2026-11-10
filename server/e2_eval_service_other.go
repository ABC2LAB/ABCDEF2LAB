//go:build !linux

package server

// 제어 전송이 Linux 전용이라 이 값이 쓰이기 전에 시작이 거부된다.
func e2EvalEffectiveUID() uint32 { return 0 }
