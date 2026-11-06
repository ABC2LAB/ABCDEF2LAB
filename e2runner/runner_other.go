//go:build !linux

package e2runner

import (
	"context"
	"github.com/Autumn-27/artex/e2proxy"
)

type Runner struct{}

func New(Config) (*Runner, error)                           { return nil, ErrUnavailable }
func (*Runner) Run(context.Context, string) (Result, error) { return Result{}, ErrUnavailable }
func (*Runner) Close(context.Context) error                 { return ErrUnavailable }

func (*Runner) Execution() *e2proxy.Execution { return nil }

func (*Runner) RunForIntent(context.Context, int64, string) (Result, error) {
	return Result{}, ErrUnavailable
}
func (*Runner) Drain(context.Context) error { return ErrUnavailable }

func (*Runner) DrainIntent(context.Context, int64) error { return ErrUnavailable }
