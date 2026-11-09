//go:build !linux

package main

import (
	"context"
	"fmt"
	"github.com/Autumn-27/artex/server"
	"io"
)

func urlOperatorUID() uint32 { return 0 }
func (b *urlPreparationRuntime) issue(l *urlLoadedProfile, jobID string, actual server.FirstDiagnosisPreparedTask) (launchConfig, error) {
	return launchConfig{}, errPreparation
}
func runURLPreparationService(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "URL 진단 준비는 Linux 보호 실행 환경이 필요합니다")
	return 1
}
func runURLDiagnosisAPI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "URL 진단 API는 Linux 비공개 연결이 필요합니다")
	return 1
}

func (l *urlLoadedProfile) hold() error { return errPreparation }
