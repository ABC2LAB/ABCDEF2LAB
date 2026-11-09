//go:build windows

package main

func handleGatewayBrokenPipe() func() { return func() {} }
