//go:build linux

package server

import "os"

func e2EvalEffectiveUID() uint32 { return uint32(os.Geteuid()) }
