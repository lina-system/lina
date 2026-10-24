// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/jobs"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

func messengerExtractionFixture(t *testing.T) (*conversationRuntime, *turnProvider, *jobs.Service, *messenger.Store, messenger.Source, time.Time) {
	t.Helper()
	r, p := turnRuntime(t)
	r.messengerExtraction = &messengerExtractionRuntime{}
	service, err := r.messengerExtractionService(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store := messenger.Open(r.db, r.materials, nil)
	start := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	source := messenger.Source{Person: r.person, Platform: "fixture", Account: "acct-1", Mode: messenger.ModeOfficial,
		Adapter: "fixture", State: messenger.StateConnected, ConnectedAt: start}
	if err := store.Connect(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertChats(t.Context(), source, []messenger.Chat{{ID: "dm", Name: "Direct", Kind: "direct"}}); err != nil {
		t.Fatal(err)
	}
	return r, p, service, store, source, start
}

func admitMessenger(t *testing.T, store *messenger.Store, source messenger.Source, messages ...messenger.Message) {
	t.Helper()
	if _, err := store.AdmitPage(t.Context(), source, "dm", "", messenger.Page{Messages: messages, Next: "page"}); err != nil {
		t.Fatal(err)
	}
}

func messengerRecord(id string, route messenger.Route, at time.Time, text string) messenger.Message {
	return messenger.Message{ID: id, Route: route, At: at, Text: text, Author: messenger.Author{ID: "author-" + id, Name: "someone"}}
}

func messengerDue(t *testing.T, r *conversationRuntime, s *jobs.Service) jobapi.AuxRequest {
	t.Helper()
	job, err := s.Policy(t.Context(), jobs.MessengerExtractionJob)
	if err != nil {
		t.Fatal(err)
	}
	job.NextRun = time.Time{}
	raw, _ := json.Marshal(job)
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE moirai_jobs SET record=? WHERE id=?", raw, jobs.MessengerExtractionJob)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return jobapi.AuxRequest{Epoch: r.epoch, JobID: jobs.MessengerExtractionJob, From: job.Watermark, Through: job.Watermark + 1}
}

func messengerStep(t *testing.T, r *conversationRuntime, s *jobs.Service) {
	t.Helper()
	req := messengerDue(t, r, s)
	result, err := s.Aux(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	proposal := jobapi.Proposal{Epoch: req.Epoch, JobID: req.JobID, From: req.From, Through: req.Through, Output: result.Output}
	if _, err := s.Apply(t.Context(), proposal); err != nil {
		t.Fatal(err)
	}
}

func TestMessengerExtractionJobRegistered(t *testing.T) {
	_, _, service, _, _, _ := messengerExtractionFixture(t)
	policy, err := service.Policy(t.Context(), jobs.MessengerExtractionJob)
	if err != nil || !policy.Enabled || policy.Deterministic {
		t.Fatalf("policy: %+v %v", policy, err)
	}
	if policy.Adoption != "stated messenger memory" || policy.Budget <= 0 || policy.MaxTokens <= 0 {
		t.Fatalf("own budget or adoption missing: %+v", policy)
	}
}

// A batch reads only the person's own records after the connection watermark.
// Context carries the participant's line, and a later scan makes no model call.
func TestMessengerExtractionInstalledPath(t *testing.T) {
	r, p, service, store, source, start := messengerExtractionFixture(t)
	admitMessenger(t, store, source,
		messengerRecord("history", messenger.RoutePerson, start.Add(-time.Hour), "I lived in Busan."),
	)
	// The pre-watermark record makes no call, and its ineligible prefix advances.
	if err := r.queueMessengerExtraction(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Aux(t.Context(), messengerDue(t, r, service)); !errors.Is(err, jobapi.ErrUnavailable) {
		t.Fatalf("pre-watermark record scheduled work: %v", err)
	}
	if len(p.requests) != 0 {
		t.Fatal("pre-watermark record reached the model")
	}

	admitMessenger(t, store, source,
		messengerRecord("other", messenger.RouteParticipant, start.Add(time.Minute), "The person lives on Mars."),
		messengerRecord("mine", messenger.RoutePerson, start.Add(2*time.Minute), "I prefer jasmine tea."),
		messengerRecord("mine-2", messenger.RoutePerson, start.Add(3*time.Minute), "Especially in the morning."),
	)
	output, _ := json.Marshal([]memory.Item{{Kind: memory.Preference, Content: "Prefers jasmine tea in the morning",
		Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated,
		Evidence: []memory.Evidence{{Kind: memory.Event, ID: "messenger:fixture:acct-1:dm:mine#1", Precision: memory.Exact},
			{Kind: memory.Event, ID: "messenger:fixture:acct-1:dm:mine-2#1", Precision: memory.Exact}}}})
	p.steps = []turnStep{{events: turnText(string(output)), before: func(req provider.Request) {
		if req.Purpose != provider.Auxiliary || len(req.Tools) != 0 || req.Instructions != "" || len(req.Items) != 2 {
			t.Error("wrong auxiliary route")
		}
		var input struct{ Content string }
		var batch memory.MessengerBatch
		if json.Unmarshal(req.Items[1], &input) != nil || json.Unmarshal([]byte(input.Content), &batch) != nil {
			t.Fatal("missing batch")
		}
		if len(batch.Utterances) != 1 || len(batch.Utterances[0].Events) != 2 {
			t.Fatalf("scope: %+v", batch.Utterances)
		}
		if !strings.Contains(batch.Utterances[0].Context[len(batch.Utterances[0].Context)-1].Text, "Mars") {
			t.Fatalf("context: %+v", batch.Utterances[0].Context)
		}
	}}}
	if err := r.queueMessengerExtraction(t.Context()); err != nil {
		t.Fatal(err)
	}
	messengerStep(t, r, service)
	hits, err := r.memoryStore().Recall(t.Context(), memory.RecallScope{Agent: r.agent, Person: r.person}, memory.Query{At: time.Now().Unix()})
	if err != nil || len(hits) != 1 {
		t.Fatalf("memory: %+v %v", hits, err)
	}
	if hits[0].Holder != memory.All || hits[0].SourceAgent != "" || len(hits[0].Evidence) != 2 || hits[0].Evidence[0].SourceAgent != "" {
		t.Fatalf("item shape: %+v", hits[0])
	}
	if len(p.requests) != 1 {
		t.Fatalf("calls=%d, want 1", len(p.requests))
	}
	if err := r.queueMessengerExtraction(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Aux(t.Context(), messengerDue(t, r, service)); !errors.Is(err, jobapi.ErrUnavailable) {
		t.Fatalf("committed records were extracted again: %v", err)
	}
	if len(p.requests) != 1 {
		t.Fatalf("repeat scan called the model: %d", len(p.requests))
	}
}

// A model that proposes the participant's line as a fact about the person is
// refused, and the batch is not cached.
func TestMessengerExtractionRefusesParticipantEvidence(t *testing.T) {
	r, p, service, store, source, start := messengerExtractionFixture(t)
	admitMessenger(t, store, source,
		messengerRecord("other", messenger.RouteParticipant, start.Add(time.Minute), "The person lives on Mars."),
		messengerRecord("mine", messenger.RoutePerson, start.Add(2*time.Minute), "I prefer jasmine tea."),
	)
	output, _ := json.Marshal([]memory.Item{{Kind: memory.Fact, Content: "Lives on Mars",
		Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated,
		Evidence: []memory.Evidence{{Kind: memory.Event, ID: "messenger:fixture:acct-1:dm:other#1", Precision: memory.Exact}}}})
	p.steps = []turnStep{{events: turnText(string(output))}}
	if err := r.queueMessengerExtraction(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Aux(t.Context(), messengerDue(t, r, service)); err == nil {
		t.Fatal("participant evidence was accepted")
	}
	hits, err := r.memoryStore().Recall(t.Context(), memory.RecallScope{Agent: r.agent, Person: r.person}, memory.Query{At: time.Now().Unix()})
	if err != nil || len(hits) != 0 {
		t.Fatalf("partial memory: %+v %v", hits, err)
	}
}

// Switching learning off for the source or the chat, or marking the chat
// never-leaves, schedules no model call.
func TestMessengerExtractionHonoursSwitches(t *testing.T) {
	r, p, service, store, source, start := messengerExtractionFixture(t)
	admitMessenger(t, store, source,
		messengerRecord("mine", messenger.RoutePerson, start.Add(time.Minute), "I prefer jasmine tea."),
	)
	if err := store.SetChatScope(t.Context(), r.person, source.Platform, source.Account, "dm", messenger.ChatNeverLeaves); err != nil {
		t.Fatal(err)
	}
	if err := r.queueMessengerExtraction(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Aux(t.Context(), messengerDue(t, r, service)); !errors.Is(err, jobapi.ErrUnavailable) {
		t.Fatalf("never-leaves chat scheduled work: %v", err)
	}
	if err := store.SetChatScope(t.Context(), r.person, source.Platform, source.Account, "dm", messenger.ChatLocal); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSourceLearn(t.Context(), r.person, source.Platform, source.Account, false); err != nil {
		t.Fatal(err)
	}
	if err := r.queueMessengerExtraction(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Aux(t.Context(), messengerDue(t, r, service)); !errors.Is(err, jobapi.ErrUnavailable) {
		t.Fatalf("disabled source scheduled work: %v", err)
	}
	if len(p.requests) != 0 {
		t.Fatal("disabled learning reached the model")
	}
	// With learning off the cursor still advances, so a record from that window
	// is not learned later; a new record after re-enabling is.
	if err := store.SetSourceLearn(t.Context(), r.person, source.Platform, source.Account, true); err != nil {
		t.Fatal(err)
	}
	admitMessenger(t, store, source,
		messengerRecord("later", messenger.RoutePerson, start.Add(5*time.Minute), "I enjoy hiking."),
	)
	if err := r.queueMessengerExtraction(t.Context()); err != nil {
		t.Fatal(err)
	}
	output, _ := json.Marshal([]memory.Item{{Kind: memory.Preference, Content: "Enjoys hiking",
		Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated,
		Evidence: []memory.Evidence{{Kind: memory.Event, ID: "messenger:fixture:acct-1:dm:later#1", Precision: memory.Exact}}}})
	p.steps = []turnStep{{events: turnText(string(output)), before: func(req provider.Request) {
		var input struct{ Content string }
		var batch memory.MessengerBatch
		if json.Unmarshal(req.Items[1], &input) != nil || json.Unmarshal([]byte(input.Content), &batch) != nil {
			t.Fatal("missing batch")
		}
		for _, utterance := range batch.Utterances {
			for _, event := range utterance.Events {
				if event.ID == "messenger:fixture:acct-1:dm:mine#1" {
					t.Fatal("record from the disabled window was learned late")
				}
			}
		}
	}}}
	messengerStep(t, r, service)
	if len(p.requests) != 1 {
		t.Fatalf("calls=%d, want 1", len(p.requests))
	}
}

// messengerCostProvider answers each batch with one item per utterance and
// reports usage estimated from the request it received. The estimate is a
// deterministic stand-in for a provider's tokenizer (UTF-8 bytes / 4).
type messengerCostProvider struct {
	calls  int
	input  int64
	output int64
}

func (*messengerCostProvider) Provider() provider.ProviderID { return provider.ChatGPT }
func (*messengerCostProvider) Capabilities(provider.ModelRef) provider.CapabilitySet {
	return nil
}
func (*messengerCostProvider) Class() provider.Class { return provider.ModelAPI }
func (*messengerCostProvider) Models(context.Context, lease.Lease, provider.Credential) (provider.ModelList, error) {
	return provider.ModelList{}, errors.New("unexpected model discovery")
}
func (*messengerCostProvider) Quota(context.Context, provider.Credential) ([]provider.QuotaRow, error) {
	return nil, errors.New("unexpected quota request")
}
func (p *messengerCostProvider) Stream(_ context.Context, _ lease.Lease, request provider.Request) (provider.Stream, error) {
	p.calls++
	var input struct{ Content string }
	if len(request.Items) != 2 || json.Unmarshal(request.Items[1], &input) != nil {
		return nil, errors.New("unexpected request")
	}
	var batch memory.MessengerBatch
	if json.Unmarshal([]byte(input.Content), &batch) != nil {
		return nil, errors.New("unexpected batch")
	}
	var items []memory.Item
	for i, utterance := range batch.Utterances {
		if len(utterance.Events) == 0 {
			continue
		}
		items = append(items, memory.Item{Kind: memory.Preference, Content: strings.Join([]string{"Prefers jasmine tea", utterance.Chat, string(rune('a' + i%26))}, " "),
			Subject: memory.Subject{Kind: memory.Person, ID: batch.Person}, Basis: memory.Stated, Evidence: utterance.Events})
	}
	raw, _ := json.Marshal(items)
	in := int64((len(input.Content) + 3) / 4)
	out := int64((len(raw) + 3) / 4)
	p.input += in
	p.output += out
	return &turnStream{events: turnText(string(raw)), usage: provider.Usage{Input: &in, Output: &out}}, nil
}

// The cost measurement runs the installed job path on a synthetic corpus and
// reports extraction calls and tokens per 1,000 of the person's own messages.
func TestMessengerExtractionCostPerThousandMessages(t *testing.T) {
	r, _ := turnRuntime(t)
	cost := &messengerCostProvider{}
	r.route.Adapter = cost
	r.messengerExtraction = &messengerExtractionRuntime{}
	service, err := r.messengerExtractionService(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store := messenger.Open(r.db, r.materials, nil)
	start := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	source := messenger.Source{Person: r.person, Platform: "fixture", Account: "acct-cost", Mode: messenger.ModeOfficial,
		Adapter: "fixture", State: messenger.StateConnected, ConnectedAt: start}
	if err := store.Connect(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	chats := []messenger.Chat{}
	for i := range 4 {
		chats = append(chats, messenger.Chat{ID: "chat-" + string(rune('0'+i)), Name: "Chat", Kind: "direct"})
	}
	if err := store.UpsertChats(t.Context(), source, chats); err != nil {
		t.Fatal(err)
	}
	const bursts = 5
	const perBurst = 4
	for _, chat := range chats {
		var messages []messenger.Message
		for burst := range bursts {
			for n := range perBurst {
				at := start.Add(time.Duration(10+burst*10) * time.Minute).Add(time.Duration(n) * 20 * time.Second)
				messages = append(messages, messengerRecord(chat.ID+"-"+string(rune('a'+burst))+"-"+string(rune('0'+n)), messenger.RoutePerson, at, "I prefer jasmine tea in the morning."))
			}
		}
		if _, err := store.AdmitPage(t.Context(), source, chat.ID, "", messenger.Page{Messages: messages, Next: "done"}); err != nil {
			t.Fatal(err)
		}
	}
	messages := len(chats) * bursts * perBurst
	for {
		if err := r.queueMessengerExtraction(t.Context()); err != nil {
			t.Fatal(err)
		}
		req := messengerDue(t, r, service)
		result, err := service.Aux(t.Context(), req)
		if errors.Is(err, jobapi.ErrUnavailable) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Apply(t.Context(), jobapi.Proposal{Epoch: req.Epoch, JobID: req.JobID, From: req.From, Through: req.Through, Output: result.Output}); err != nil {
			t.Fatal(err)
		}
	}
	if cost.calls == 0 {
		t.Fatal("no extraction call on a nonempty corpus")
	}
	t.Logf("messenger extraction cost: messages=%d calls=%d calls_per_1000=%.1f input_tokens=%d output_tokens=%d tokens_per_1000=%d",
		messages, cost.calls, float64(cost.calls)*1000/float64(messages), cost.input, cost.output,
		(cost.input+cost.output)*1000/int64(messages))
}
