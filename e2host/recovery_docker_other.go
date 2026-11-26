//go:build !linux

package e2host

import "context"

func VerifyDockerTermination(context.Context, string, string, string) (string, error) {
	return "", ErrUnavailable
}
