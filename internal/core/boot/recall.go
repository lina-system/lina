// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"

	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/recall"
)

func (r *conversationRuntime) startRecall(ctx context.Context) {
	ctx, r.recallStop = context.WithCancel(ctx)
	r.recallDone = make(chan struct{})
	index := recall.New(r.db, func(ctx context.Context, id string) (string, error) {
		event, err := r.journal().Get(ctx, id)
		if err != nil {
			return "", err
		}
		if event.BytesUnavailable {
			return "", ledger.ErrBytesUnavailable
		}
		return event.Body.DerivedText(), nil
	})
	go func() { defer close(r.recallDone); index.Run(ctx) }()
}
