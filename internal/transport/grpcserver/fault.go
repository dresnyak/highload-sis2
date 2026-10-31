package grpcserver

import (
	"context"
	"math/rand/v2"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type FaultConfig struct {
	UnavailableRate float64
	Delay           time.Duration

	Rand func() float64
}

func (cfg FaultConfig) inject(ctx context.Context) error {
	if cfg.Delay > 0 {
		select {
		case <-time.After(cfg.Delay):
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		}
	}
	if cfg.UnavailableRate > 0 {
		r := cfg.Rand
		if r == nil {
			r = rand.Float64
		}
		if r() < cfg.UnavailableRate {
			return status.Error(codes.Unavailable, "injected fault")
		}
	}
	return nil
}
