//go:build !linux

package e2sandbox

import "context"

type Process struct{}

func Launch(context.Context, Config) (*Process, error) { return nil, ErrUnavailable }
func (*Process) Start(context.Context) error           { return ErrUnavailable }
func (*Process) PID() int                              { return 0 }
func (*Process) BashDone() <-chan struct{}             { return nil }
func (*Process) LivePeer(int32, uint32, uint32) bool   { return false }
func (*Process) Wait(context.Context) (Result, error)  { return Result{}, ErrUnavailable }
func (*Process) Stop(context.Context) error            { return ErrUnavailable }
func (*Process) Close() error                          { return nil }
func HelperMain() int                                  { return 2 }
