// SPDX-License-Identifier: Apache-2.0

package compaction

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestBuilderQueuedInputCoverage(t *testing.T) {
	for _, scenario := range []string{"person", "forwarded", "unscoped", "missing-agent-reply", "summarized-queue", "unretained-current", "append-during-summary"} {
		t.Run(scenario, func(t *testing.T) {
			b, v, _ := builderFixture(t)
			b.CurrentInput = v.Input.ID
			entry := guard.EntryOwner
			if scenario == "forwarded" {
				entry = guard.EntryForwarded
			} else if scenario == "missing-agent-reply" {
				entry = guard.EntryAgent
			}
			appendEvent := func(id string) {
				t.Helper()
				person := ""
				if entry == guard.EntryOwner {
					person = "owner"
				}
				_, err := b.Events.Admit(t.Context(), entry, ledger.Input{ID: id, ConversationID: b.Conversation, AgentID: b.Agent, PersonID: person, Locator: "fixture:" + id, Surface: ledger.Surface{Kind: "app"}, OccurredAt: time.Unix(3, 0), RecordedAt: time.Unix(3, 0), TimeQuality: "observed", Body: "Future input"})
				if err != nil {
					t.Fatal(err)
				}
			}
			appendEvent("queued")
			segment := PassSegment{Records: v.History, Protected: []Record{v.Input}}
			switch scenario {
			case "unscoped":
				b.CurrentInput = ""
			case "summarized-queue":
				segment.Records = append(segment.Records, Record{ID: "queued"})
			case "unretained-current":
				segment.Protected = nil
			}
			generate := b.Generate
			calls := 0
			b.Generate = func(ctx context.Context, sources PassSegment) (Text, error) {
				calls++
				if len(sources.Records) != 1 || sources.Records[0].ID != "fact" || len(sources.Verbatim) != 0 || len(sources.Protected) != 0 {
					t.Fatalf("queued/protected inputs reached the summarizer: %+v", sources)
				}
				if scenario == "append-during-summary" {
					appendEvent("later")
				}
				return generate(ctx, sources)
			}
			text, err := b.Summarize(t.Context(), segment)
			switch scenario {
			case "person", "forwarded":
				if err != nil {
					t.Fatal(err)
				}
				published, err := b.Publish(t.Context(), segment, text)
				if err != nil {
					t.Fatal(err)
				}
				ids, _, err := b.Store.ExpandEvents(t.Context(), b.Agent, published.ID, 0, 10)
				if err != nil || !reflect.DeepEqual(ids, []string{"fact"}) {
					t.Fatalf("queued input entered summary coverage: ids=%v err=%v", ids, err)
				}
			case "append-during-summary":
				if !errors.Is(err, ErrStaleFrontier) || calls != 1 {
					t.Fatalf("publication fence lost: calls=%d err=%v", calls, err)
				}
			default:
				if !errors.Is(err, ErrCoverage) || calls != 0 {
					t.Fatalf("invalid coverage accepted: calls=%d err=%v", calls, err)
				}
			}
		})
	}
}
