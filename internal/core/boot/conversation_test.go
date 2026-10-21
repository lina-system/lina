// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/loop"
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
