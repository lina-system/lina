// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"context"
	"sync"
)

// controlGate serializes session controls, including their RPC replies, without
// making a canceled snapshot wait for an unrelated control. The zero value is ready.
type controlGate struct {
	once sync.Once
	busy chan struct{}
}

func (g *controlGate) Lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.once.Do(func() { g.busy = make(chan struct{}, 1) })
	select {
	case g.busy <- struct{}{}:
		// Cancellation and release can become ready together.
		if err := ctx.Err(); err != nil {
			g.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *controlGate) Unlock() { <-g.busy }
