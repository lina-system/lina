// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/lane"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

// notifyMessengerGap tells the person once that a chat passed its platform's
// read window, so its history may have a hole. The source records the gap
// before this runs, so a restart neither repeats nor loses the notice.
func (r *conversationRuntime) notifyMessengerGap(ctx context.Context, source messenger.Source, chat messenger.ChatRecord) error {
	language := design.EnvironmentLanguage(os.Getenv)
	text := fmt.Sprintf(design.Text(language, "notice.messenger_gap"), chat.Name, source.Platform)
	now := time.Now().UTC()
	id := "messenger-gap:" + rand.Text()
	notice := lane.Notice{ID: id, Summary: text, Landed: now, Surface: ledger.Surface{Kind: "tui"}}
	body, err := json.Marshal([]lane.Notice{notice})
	if err != nil {
		return err
	}
	eventID := "messenger-gap-" + id
	if _, err = r.journal().Admit(ctx, guard.EntryBackgroundNotice, ledger.Input{
		ID: eventID, ConversationID: r.conversation, AgentID: r.agent, Surface: notice.Surface,
		ScaffoldKind: "background_notice", OccurredAt: now, RecordedAt: now, TimeQuality: "recorded",
		Locator: "background-notice:" + id, Body: string(body),
	}); err != nil {
		return err
	}
	return r.publish(ctx, protocol.KindBackgroundNotice, eventID, protocol.BackgroundNotice{Text: &text})
}
