// SPDX-License-Identifier: Apache-2.0
package moirai

import (
	"context"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"time"
)

func (w Worker) embed(ctx context.Context) error {
	api, ok := w.Core.(jobapi.EmbeddingAPI)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	_, err := api.Embed(ctx)
	return err
}

// The optional embedding job runs independently of model and deterministic
// jobs. One request owns at most 32 documents; failed work stays in Core's queue.
func (w Worker) runEmbeddings(ctx context.Context, interval time.Duration) {
	timer := time.NewTicker(max(interval, time.Second))
	defer timer.Stop()
	for ctx.Err() == nil {
		_ = w.embed(ctx)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
