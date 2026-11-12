//go:build !linux

package main

import (
	"context"
	"io"
)

func runProductInstall(context.Context, []string, io.Writer, io.Writer) int   { return 1 }
func runProductPreflight(context.Context, []string, io.Writer, io.Writer) int { return 1 }
