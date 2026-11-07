//go:build !linux

package server

import "context"

func newFirstDiagnosisHTTPAuthority(context.Context, FirstDiagnosisHTTPHostConfig) (firstDiagnosisHTTPAuthority, error) {
	return nil, ErrFirstDiagnosisHTTPHost
}
