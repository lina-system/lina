// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestCanceledConversationPublishesDurableNotice(t *testing.T) {
	r, p := turnRuntime(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p.steps = []turnStep{{before: func(provider.Request) { cancel() }}}
	input, inboxID := admitTurnInput(t, r, "canceled-input")
	if err := r.run(ctx, &loop.Inputs{}, input, inboxID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled turn: %v", err)
	}
	assertTurnRecord(t, r, input, inboxID, turn.Aborted, 1)
	stopped, err := turn.New(r.db).Stopped(t.Context(), input.ID)
	if err != nil || stopped.NoticeID == "" {
		t.Fatalf("durable stop notice: %+v %v", stopped, err)
	}
	assertTurnPublished(t, r, input.ID, stopped.NoticeID)
}
