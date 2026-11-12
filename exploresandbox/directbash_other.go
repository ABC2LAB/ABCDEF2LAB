//go:build !linux

package exploresandbox

import (
	"context"
	"time"
)

// DirectBash는 Linux에서만 동작한다(e2sandbox가 Linux namespace를 쓴다).
type DirectBash struct{}

func New(Config) (*DirectBash, error) { return nil, ErrUnavailable }

func (*DirectBash) Run(context.Context, string, time.Duration) (Result, error) {
	return Result{}, ErrUnavailable
}

func (*DirectBash) CommandTimeout() time.Duration { return 0 }

func (*DirectBash) String() string { return "exploresandbox.DirectBash[unavailable]" }
