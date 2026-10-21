// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
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
			project := func(restored bool) clientsrv.TopicEvent {
				t.Helper()
				topics, err := clientsrv.NewTopics("1", clientsrv.TopicOptions{})
				if err != nil {
					t.Fatal(err)
				}
				defer topics.Close()
				runtime := &conversationRuntime{topics: topics, conversation: "conversation", agent: "agent"}
				if restored {
					err = runtime.publishPartials(turn.Stopped{TurnID: "turn", Partials: []turn.Partial{{Round: 1, Index: 3, Text: stream.sent}}})
				} else {
					err = runtime.publishReply("turn:2:3", "turn", view)
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
			live, restored := project(false), project(true)
			if !reflect.DeepEqual(live, restored) {
				t.Fatalf("restored partial changed its live projection: live=%+v restored=%+v", live, restored)
			}
			if tc.card && restored.Kind != protocol.KindResult {
				t.Fatal("restored card became an assistant text row")
			}
		})
	}
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
