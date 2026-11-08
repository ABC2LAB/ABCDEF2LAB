//go:build !linux

package server

func startE2ControlTransport(*E2ControlService) (e2ControlTransport, error) { return nil, ErrE2Control }
