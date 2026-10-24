// SPDX-License-Identifier: Apache-2.0

package localmodel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/applefm"
)

type replyAdapter struct {
	provider.Adapter
	text  string
	usage provider.Usage
}

func (a replyAdapter) Provider() provider.ProviderID { return provider.ChatGPT }

func (a replyAdapter) Stream(context.Context, lease.Lease, provider.Request) (provider.Stream, error) {
	return &replyStream{events: []provider.Event{{Kind: provider.Delta, Delta: a.text}, {Kind: provider.Terminal}}, usage: a.usage}, nil
}

type replyStream struct {
	events []provider.Event
	usage  provider.Usage
}

func (s *replyStream) Next(context.Context) (provider.Event, error) {
	if len(s.events) == 0 {
		return provider.Event{}, io.EOF
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}

func (s *replyStream) Meta() provider.Meta { return provider.Meta{Usage: s.usage} }
func (s *replyStream) Close() error        { return nil }

func TestMainModelSchemaMissKeepsReportedUsage(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"title":{"type":"string"}},"required":["title"]}`)
	input, output := int64(42), int64(7)
	main := AdapterModel{
		Adapter: replyAdapter{text: `{"guess":"draft"}`, usage: provider.Usage{Input: &input, Output: &output}},
		Model:   "main-model",
		Request: func(items []provider.Item) provider.Request { return provider.Request{Items: items} },
	}
	hold := lease.New(func() {})

	completion, err := main.Complete(t.Context(), hold, Call{Input: "draft the title", Schema: schema})
	if !errors.Is(err, applefm.ErrSchemaMiss) || completion.Object != nil || completion.Usage.Input == nil || *completion.Usage.Input != input || completion.Usage.Output == nil || *completion.Usage.Output != output {
		t.Fatalf("completion=%+v err=%v", completion, err)
	}

	ledger := &memoryLedger{}
	out, err := Serve(t.Context(), hold, Task{Kind: Extract, Text: "draft the title", Schema: schema, Preference: ForceMain}, Deps{Main: main, Ledger: ledger})
	if err != nil || out.Object != nil || out.Row.Reason != ReasonSchemaMiss || len(ledger.rows) != 1 {
		t.Fatalf("out=%+v err=%v rows=%d", out, err, len(ledger.rows))
	}
	row := ledger.rows[0]
	if row.Model != "main-model" || row.Input == nil || *row.Input != input || row.Output == nil || *row.Output != output {
		t.Fatalf("row=%+v", row)
	}
}
