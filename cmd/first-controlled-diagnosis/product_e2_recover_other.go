//go:build !linux

package main

import (
	"context"
	"io"
)

func runProductE2Recover(context.Context, []string, io.Writer, io.Writer) int { return 1 }
