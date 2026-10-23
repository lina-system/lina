// SPDX-License-Identifier: Apache-2.0

package localmodel

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/applefm"
)

// AdapterModel collects one stateless completion from a provider adapter.
type AdapterModel struct {
	Adapter provider.Adapter
	Model   provider.ModelRef
	Request func([]provider.Item) provider.Request
}

func (m AdapterModel) Identity() (provider.ProviderID, provider.ModelRef) {
	return m.Adapter.Provider(), m.Model
}

func (m AdapterModel) Complete(ctx context.Context, l lease.Lease, call Call) (Completion, error) {
	if m.Adapter == nil || m.Request == nil || m.Model == "" {
		return Completion{}, ErrUnavailable
	}
	content := call.Input
	if len(call.Schema) > 0 {
		content += "\n\nJSON schema:\n" + string(call.Schema)
	}
	item, err := json.Marshal(struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "user", Content: content})
	if err != nil {
		return Completion{}, err
	}
	req := m.Request([]provider.Item{item})
	req.Model = m.Model
	req.Purpose = provider.Auxiliary
	req.Instructions = call.Instructions
	req.Tools = nil
	req.Bridge = nil
	stream, err := m.Adapter.Stream(ctx, l, req)
	if err != nil {
		return Completion{}, err
	}
	defer stream.Close()
	var deltas, itemText strings.Builder
	terminal := false
	for {
		event, err := stream.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return Completion{}, err
		}
		if terminal {
			return Completion{}, &provider.Failure{Kind: provider.NotAccepted}
		}
		switch event.Kind {
		case provider.Delta:
			deltas.WriteString(event.Delta)
		case provider.ItemDone:
			itemText.WriteString(textFromItem(event.Item))
		case provider.ToolCallReady:
			return Completion{}, applefm.ErrSchemaMiss
		case provider.Terminal:
			terminal = true
		}
	}
	text := deltas.String()
	if text == "" {
		text = itemText.String()
	}
	if len(call.Schema) > 0 {
		object, err := applefm.ValidateObject(call.Schema, json.RawMessage(stripFence(strings.TrimSpace(text))))
		if err != nil {
			return Completion{}, applefm.ErrSchemaMiss
		}
		return Completion{Object: object, Usage: stream.Meta().Usage}, nil
	}
	return Completion{Text: text, Usage: stream.Meta().Usage}, nil
}

func textFromItem(item json.RawMessage) string {
	var message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(item, &message) != nil {
		return ""
	}
	var text strings.Builder
	for _, part := range message.Content {
		if part.Type == "" || part.Type == "output_text" {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}
