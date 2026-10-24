// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/design"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestSteeringHandoffPreservesOnlyUndrainedInputs(t *testing.T) {
	first := provider.Item(`{"role":"user","content":"first"}`)
	second := provider.Item(`{"role":"user","content":"late"}`)
	runtime := &conversationRuntime{steering: map[string][]acceptedSteering{
		string(first):  {{event: ledger.Event{ID: "first"}, inboxID: 1}},
		string(second): {{event: ledger.Event{ID: "late"}, inboxID: 2}},
	}}
	inputs := &loop.Inputs{}
	queue := &conversationInputs{runtime: runtime, inputs: inputs}
	inputs.Push(first)
	drained := queue.Drain()
	if len(drained) != 1 {
		t.Fatal("steering not applied at boundary")
	}
	inputs.Push(second)
	remaining := queue.Close()
	if len(remaining) != 1 || string(remaining[0]) != string(second) {
		t.Fatal("late input lost")
	}
	if _, exists := runtime.takeSteering(first); exists {
		t.Fatal("applied steering would be dispatched twice")
	}
	pending, exists := runtime.takeSteering(remaining[0])
	if !exists || pending.event.ID != "late" || pending.inboxID != 2 {
		t.Fatal("follow-up lost durable identity")
	}
	if inputs.Push(first) {
		t.Fatal("closed turn accepted steering")
	}
}

func TestRestoredPartialKeepsLiveReplyProjection(t *testing.T) {
	// The admitted input records the reply language and restart recovery reads
	// that record. A later process locale must not retitle a restored card, and
	// an input without the record must not borrow one from the environment.
	type variant struct {
		name, turn, input string
		language          design.Language
		control           bool
	}
	variants := []variant{
		{"recorded-language", "turn-recorded", "input-recorded", design.English, true},
		{"unrecorded-language", "turn-untagged", "input-untagged", "", true},
		{"missing-turn-record", "turn-unrecorded", "input-unrecorded", "", false},
	}
	for _, locale := range []string{"en_US.UTF-8", "ko_KR.UTF-8"} {
		t.Run(locale, func(t *testing.T) {
			t.Setenv("LC_ALL", "")
			t.Setenv("LC_MESSAGES", "")
			t.Setenv("LANGUAGE", "")
			t.Setenv("LANG", locale)
			stored, _, agent, _ := managementFixture(t)
			inputs := make(map[string]ledger.Event, len(variants))
			for _, v := range variants {
				inputs[v.input] = admitTurn(t, stored.db, agent, v.input, v.turn, v.language, v.control)
			}
			for _, tc := range []struct {
				name, text string
				card       bool
			}{
				{"short-with-held-prefix", "Public text. sk-", false},
				{"long-card", "Public text. " + strings.Repeat("unfinished text ", 100), true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					filter, err := conversationFilter()
					if err != nil {
						t.Fatal(err)
					}
					stream := &conversationReplyStream{filter: filter}
					view, _, err := stream.Push(tc.text)
					if err != nil || (view.Card != nil) != tc.card {
						t.Fatalf("unexpected live projection: %+v %v", view, err)
					}
					// Each projection is a Core process: the ledger survives, the
					// in-memory topic does not.
					project := func(restored bool, v variant) clientsrv.TopicEvent {
						t.Helper()
						topics, err := clientsrv.NewTopics("1", clientsrv.TopicOptions{})
						if err != nil {
							t.Fatal(err)
						}
						defer topics.Close()
						// Keep runtime.ctx unset: publication must use its caller's context.
						runtime := &conversationRuntime{db: stored.db, topics: topics, conversation: "conversation", agent: agent, person: "person"}
						if restored {
							err = runtime.publishPartials(t.Context(), turn.Stopped{TurnID: v.turn, Partials: []turn.Partial{{Round: 1, Index: 3, Text: stream.sent}}})
						} else {
							err = runtime.publishReply(t.Context(), v.turn+":2:3", v.turn, view, eventLanguage(inputs[v.input]))
						}
						if err != nil {
							t.Fatal(err)
						}
						start, sub, err := topics.Subscribe("conversation:conversation", nil)
						if err != nil {
							t.Fatal(err)
						}
						defer sub.Close()
						if start.Snapshot == nil || len(start.Snapshot.Items) != 1 {
							t.Fatal("expected one projected row")
						}
						return start.Snapshot.Items[0]
					}
					for _, v := range variants {
						t.Run(v.name, func(t *testing.T) {
							live, restored := project(false, v), project(true, v)
							if !reflect.DeepEqual(live, restored) {
								t.Fatalf("restored partial changed its live projection: live=%+v restored=%+v", live, restored)
							}
							if !tc.card {
								return
							}
							if restored.Kind != protocol.KindResult {
								t.Fatal("restored card became an assistant text row")
							}
							var result protocol.Result
							if err := json.Unmarshal(restored.Data, &result); err != nil {
								t.Fatal(err)
							}
							if want := design.Text(design.English, "reply.full"); result.Title != want {
								t.Fatalf("restored card took its language from the process locale: title=%q want %q", result.Title, want)
							}
						})
					}
				})
			}
		})
	}
}

// admitTurn records one person input and, when the language is known, the turn
// that Core must recover. The admitted input is the only source of the reply
// language; the process locale must not stand in for it.
func admitTurn(t *testing.T, db ledger.Canon, agent, inputID, turnID string, language design.Language, control bool) ledger.Event {
	t.Helper()
	ctx := t.Context()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	input, err := ledger.New(db, nil).Admit(ctx, guard.EntryOwner, ledger.Input{
		ID: inputID, ConversationID: "conversation", AgentID: agent, PersonID: "person",
		Surface: ledger.Surface{Kind: "tui", Language: string(language)}, OccurredAt: now, RecordedAt: now,
		TimeQuality: "recorded", Body: "…",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !control {
		return input
	}
	err = db.Write(ctx, func(tx *canon.Tx) error {
		receipt, err := inbox.Insert(ctx, tx, inbox.Input{Key: inbox.Key{Conversation: "conversation", Source: "fixture", NativeID: inputID}})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO turn_runs(id,conversation,inbox_id,state) VALUES(?,'conversation',?,'aborted')`, turnID, receipt.ID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO turn_control(turn_id,input_event) VALUES(?,?)`, turnID, inputID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return input
}

// Every observable prefix must already agree with admission masking, even
// when the provider splits a credential before its recognizable minimum length.
func TestConversationStreamMasksEveryPrefix(t *testing.T) {
	texts := []string{
		"Public sentence. More public text. ",
		"Public PARTIAL-366",
		"Key: sk-" + strings.Repeat("a", 24) + " safe. ",
		"Authorization: Bearer synthetic-credential safe. ",
		"password = synthetic-credential safe. ",
		"It's password='synthetic private value' safe. ",
		"password=\"synthetic private value\" safe. ",
		"-----BEGIN PRIVATE KEY-----\nsynthetic-private-body\n-----END PRIVATE KEY-----\n",
		`{"\u0074oken":"synthetic private value"}`,
	}
	for _, credential := range []string{
		"sk-proj-" + strings.Repeat("a", 24), "sk-ant-api03-" + strings.Repeat("a", 24),
		"ghp_" + strings.Repeat("a", 24), "github_pat_" + strings.Repeat("a", 24),
		"AIza" + strings.Repeat("a", 35), "xoxb-" + strings.Repeat("a", 20),
		"AKIA" + strings.Repeat("A", 16), "ASIA" + strings.Repeat("A", 16), "eyJabc.def.ghi",
	} {
		texts = append(texts, "Public "+credential+" safe. ")
	}
	for _, text := range texts {
		masked, err := secrets.Mask(text)
		if err != nil {
			t.Fatal(err)
		}
		filter, err := conversationFilter()
		if err != nil {
			t.Fatal(err)
		}
		stream := &conversationReplyStream{filter: filter}
		var visible string
		for _, char := range text {
			view, delta, err := stream.Push(string(char))
			if err != nil {
				t.Fatal(err)
			}
			visible += delta
			if !strings.HasPrefix(masked.Text(), visible) {
				t.Fatal("stream exposed text that admission masks")
			}
			if view.Card != nil && !strings.HasPrefix(masked.Text(), view.Card.Text) {
				t.Fatal("card exposed text that admission masks")
			}
		}
		if strings.HasPrefix(text, "Public") && visible != masked.Text() {
			t.Fatal("ordinary text did not stream")
		}
	}
}

func TestRunNoticeUsesConversationLanguage(t *testing.T) {
	for _, outcome := range []runs.Outcome{runs.Complete, runs.Partial, runs.Failed, runs.Cancelled, runs.InputNeeded} {
		notice := runs.Notice{Outcome: outcome}
		ko := runSummary(notice, design.Korean)
		en := runSummary(notice, design.English)
		if !strings.ContainsFunc(ko, func(r rune) bool { return unicode.Is(unicode.Hangul, r) }) || ko == en {
			t.Errorf("unlocalized outcome %s", outcome)
		}
	}
}
