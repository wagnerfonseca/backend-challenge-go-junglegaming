// Package health aggregates the readiness probes of the runtime dependencies.
package health

import (
	"context"
	"time"
)

// ReadyTimeout bounds each readiness probe.
const ReadyTimeout = 2 * time.Second

// Checker runs every dependency probe within the readiness budget.
type Checker struct {
	pings []func(ctx context.Context) error
}

// New builds a checker over the given probes.
func New(pings ...func(ctx context.Context) error) *Checker {
	return &Checker{pings: pings}
}

// Ready returns the first probe failure, or nil when all dependencies answer
// within the readiness budget.
func (c *Checker) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, ReadyTimeout)
	defer cancel()
	for _, ping := range c.pings {
		if err := ping(ctx); err != nil {
			return err
		}
	}
	return nil
}
