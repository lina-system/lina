// SPDX-License-Identifier: Apache-2.0

package localmodel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/applefm"
)

func profile(tokens int) applefm.Profile {
	return applefm.Profile{ContextTokens: tokens, ToolCalling: true, GuidedGeneration: true, ImageInput: false, Model: applefm.OnDeviceModel}
}

func TestDecideKeepsAShortIntentLocal(t *testing.T) {
	route, reason := Decide(Task{Kind: Intent, Text: "open safari", Preference: Auto}, profile(4096), true)
	if route != RouteLocal || reason != ReasonFit {
		t.Fatalf("route=%s reason=%s", route, reason)
	}
}

func TestDecideSendsOverflowToolsImagesAndUnknownKindsToMain(t *testing.T) {
	fit := profile(32)
	cases := []struct {
		name   string
		task   Task
		ready  bool
		reason string
	}{
		{name: "context", task: Task{Kind: Summary, Text: "0123456789abcdef0123456789abcdef", Preference: Auto}, ready: true, reason: ReasonOverContext},
		{name: "tools", task: Task{Kind: Intent, Text: "open", Tools: []string{"shell"}}, ready: true, reason: ReasonTools},
		{name: "image", task: Task{Kind: Classify, Text: "photo", Images: 1}, ready: true, reason: ReasonImage},
		{name: "kind", task: Task{Kind: "conversation", Text: "hello"}, ready: true, reason: ReasonKind},
		{name: "down", task: Task{Kind: Title, Text: "notes"}, ready: false, reason: ReasonUnavailable},
		{name: "forced", task: Task{Kind: Rewrite, Text: "short", Preference: ForceMain}, ready: true, reason: ReasonForced},
		{name: "forced local yields", task: Task{Kind: Summary, Text: "0123456789abcdef0123456789abcdef", Preference: ForceLocal}, ready: true, reason: ReasonOverContext},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route, reason := Decide(tc.task, fit, tc.ready)
			if route != RouteMain || reason != tc.reason {
				t.Fatalf("route=%s reason=%s", route, reason)
			}
		})
	}
}

type scriptModel struct {
	provider provider.ProviderID
	model    provider.ModelRef
	object   string
	text     string
	err      error
	calls    int
}

func (s *scriptModel) Identity() (provider.ProviderID, provider.ModelRef) {
	return s.provider, s.model
}

func (s *scriptModel) Complete(_ context.Context, _ lease.Lease, call Call) (Completion, error) {
	s.calls++
	if s.err != nil {
		return Completion{}, s.err
	}
	if len(call.Schema) > 0 {
		return Completion{Object: json.RawMessage(s.object)}, nil
	}
	return Completion{Text: s.text}, nil
}

type memoryLedger struct{ rows []Row }

func (m *memoryLedger) Record(_ context.Context, row Row) error {
	m.rows = append(m.rows, row)
	return nil
}

func TestServeFallsBackOnSchemaMissAndUnavailableModel(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"title":{"type":"string"}},"required":["title"]}`)
	hold := lease.New(func() {})
	t.Run("schema miss", func(t *testing.T) {
		local := &scriptModel{provider: applefm.ProviderID, model: applefm.OnDeviceModel, err: applefm.ErrSchemaMiss}
		main := &scriptModel{provider: provider.ChatGPT, model: "main-model", object: `{"title":"draft"}`}
		ledger := &memoryLedger{}
		out, err := Serve(t.Context(), hold, Task{Kind: Extract, Text: "draft the title", Schema: schema}, Deps{Local: local, Profile: profile(4096), Main: main, Ledger: ledger})
		if err != nil || local.calls != 1 || main.calls != 1 || out.Object == nil || out.Row.Model != "main-model" || out.Row.Route != RouteMain || out.Row.Reason != ReasonSchemaMiss || len(ledger.rows) != 1 {
			t.Fatalf("out=%+v err=%v local=%d main=%d rows=%d", out, err, local.calls, main.calls, len(ledger.rows))
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		local := &scriptModel{provider: applefm.ProviderID, model: applefm.OnDeviceModel, err: &provider.Failure{Kind: provider.Unavailable}}
		main := &scriptModel{provider: provider.ChatGPT, model: "main-model", text: "Safari"}
		ledger := &memoryLedger{}
		out, err := Serve(t.Context(), hold, Task{Kind: Intent, Text: "open safari"}, Deps{Local: local, Profile: profile(4096), Main: main, Ledger: ledger})
		if err != nil || out.Text != "Safari" || out.Row.Provider != provider.ChatGPT || out.Row.Reason != ReasonUnavailable || ledger.rows[0].Model != "main-model" {
			t.Fatalf("out=%+v err=%v", out, err)
		}
	})
	t.Run("local serves", func(t *testing.T) {
		local := &scriptModel{provider: applefm.ProviderID, model: applefm.OnDeviceModel, object: `{"title":"draft"}`}
		main := &scriptModel{provider: provider.ChatGPT, model: "main-model", object: `{"title":"other"}`}
		ledger := &memoryLedger{}
		out, err := Serve(t.Context(), hold, Task{Kind: Extract, Text: "draft the title", Schema: schema}, Deps{Local: local, Profile: profile(4096), Main: main, Ledger: ledger})
		if err != nil || local.calls != 1 || main.calls != 0 || string(out.Object) == "" || out.Row.Model != applefm.OnDeviceModel || out.Row.Route != RouteLocal || out.Row.Reason != ReasonFit || strings.Contains(out.Row.Digest, "draft the title") {
			t.Fatalf("out=%+v err=%v calls=%d/%d", out, err, local.calls, main.calls)
		}
	})
	t.Run("neither served", func(t *testing.T) {
		local := &scriptModel{provider: applefm.ProviderID, model: applefm.OnDeviceModel, err: &provider.Failure{Kind: provider.Unavailable}}
		main := &scriptModel{provider: provider.ChatGPT, model: "main-model", err: &provider.Failure{Kind: provider.Unavailable}}
		ledger := &memoryLedger{}
		_, err := Serve(t.Context(), hold, Task{Kind: Title, Text: "notes"}, Deps{Local: local, Profile: profile(4096), Main: main, Ledger: ledger})
		if !errors.Is(err, ErrUnavailable) || len(ledger.rows) != 0 {
			t.Fatalf("err=%v rows=%d", err, len(ledger.rows))
		}
	})
}
