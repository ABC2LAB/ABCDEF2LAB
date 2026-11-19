//go:build !linux

package main

import (
	"context"
	"io"
)

func runProductE2Host(context.Context, []string, io.Writer, io.Writer) int { return 1 }
