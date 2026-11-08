package server

import (
	"github.com/Autumn-27/artex/transmit"
	"testing"
	"time"
)

func TestControlledAssemblyRegisteredLimitsKeepModesSeparate(t *testing.T) {
	now := time.Now()
	good := transmit.Config{MaxRequests: 2, MinInterval: 500 * time.Millisecond, RequestTimeout: 10 * time.Second, MaxBodyBytes: 1 << 20, ExpiresAt: now.Add(15 * time.Minute)}
	if !validFirstRegisteredLimits(good, now) {
		t.Fatal("정해진 두 GET 한도를 거부했습니다")
	}
	for _, edit := range []func(*transmit.Config){func(c *transmit.Config) { c.MaxRequests = 3 }, func(c *transmit.Config) { c.MinInterval = 499 * time.Millisecond }, func(c *transmit.Config) { c.RequestTimeout = 11 * time.Second }, func(c *transmit.Config) { c.MaxBodyBytes++ }, func(c *transmit.Config) { c.ExpiresAt = c.ExpiresAt.Add(time.Nanosecond) }, func(c *transmit.Config) { c.CumulativeBudget = &transmit.CumulativeBudget{} }, func(c *transmit.Config) { c.Health = &transmit.HealthConfig{} }} {
		c := good
		edit(&c)
		if validFirstRegisteredLimits(c, now) {
			t.Fatal("범위 밖 예산 또는 다른 모드 자료를 수용했습니다")
		}
	}
}
