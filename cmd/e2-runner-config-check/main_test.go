package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestArgumentsDoNotExposeInputs(t *testing.T) {
	for _, args := range [][]string{{}, {"--unknown=private-value"}, {"--config", "/private/not-present"}, {"--config", "/private/not-present", "private-extra"}} {
		var out, errout bytes.Buffer
		if run(args, &out, &errout) == 0 || out.Len() != 0 || strings.Contains(errout.String(), "private") {
			t.Fatal("오류 입력이나 비밀 경로를 노출함")
		}
	}
}
func TestHelp(t *testing.T) {
	var out, errout bytes.Buffer
	if run([]string{"--help"}, &out, &errout) != 0 || out.String() != usage || errout.Len() != 0 {
		t.Fatal("도움말 실패")
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("private writer error") }
func TestHelpOutputFailure(t *testing.T) {
	if run([]string{"--help"}, brokenWriter{}, io.Discard) != 1 {
		t.Fatal("출력 실패 수용")
	}
}
