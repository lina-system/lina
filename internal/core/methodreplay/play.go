// SPDX-License-Identifier: Apache-2.0
package methodreplay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

// ErrReply reports a stream that produced no assistant text.
var ErrReply = errors.New("no_reply_text")

// Player plays one case against one variant through the model route. It has no
// tools and performs no effect: the reply text is the only output.
type Player struct {
	Adapter    provider.Adapter
	Model      provider.ModelRef
	Credential provider.Credential
}

// Instructions is the instruction text a case is played with. It mirrors the
// installed conversation's persona-tui-work-method-v1 assembly so the measured
// difference comes from the layers and record, not from a different wrapper.
func Instructions(preset Preset, role string) (string, error) {
	p := agents.Preparation{AgentID: role, Name: role, OwnerPersonID: "owner", Layers: preset.Layers, WorkMethod: preset.WorkMethod}
	return assemble.TUIInstructions(p, nil)
}

func (p Player) Play(ctx context.Context, c Case, v Variant) (string, error) {
	if p.Adapter == nil || p.Model == "" {
		return "", ErrConfig
	}
	preset, ok := v.For(c.Role)
	if !ok {
		return "", ErrConfig
	}
	instructions, err := Instructions(preset, c.Role)
	if err != nil {
		return "", err
	}
	kept, _ := Dropped(c)
	items := make([]provider.Item, 0, len(kept))
	for _, turn := range kept {
		raw, err := json.Marshal(struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{turn.Role, turn.Text})
		if err != nil {
			return "", err
		}
		items = append(items, raw)
	}
	stream, err := p.Adapter.Stream(ctx, lease.New(func() {}), provider.Request{
		Model: p.Model, Credential: p.Credential, Instructions: instructions, Items: items, Purpose: provider.Replay,
	})
	if err != nil {
		return "", err
	}
	defer stream.Close()
	// Completed items carry the authoritative text; deltas are only a fallback
	// for a runtime that never sends a completed message item.
	var reply, deltas strings.Builder
	for {
		event, err := stream.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		switch event.Kind {
		case provider.Delta:
			deltas.WriteString(event.Delta)
		case provider.ItemDone:
			text, err := outputText(event.Item)
			if err != nil {
				return "", err
			}
			reply.WriteString(text)
		case provider.ToolCallReady:
			// No tool is declared for a measured case, so a call is not offered.
			return "", ErrReply
		case provider.Terminal:
			if reply.Len() == 0 {
				reply = deltas
			}
			if reply.Len() == 0 {
				return "", ErrReply
			}
			return TrimTrailing(reply.String()), nil
		}
	}
	if reply.Len() == 0 {
		reply = deltas
	}
	if reply.Len() == 0 {
		return "", ErrReply
	}
	return TrimTrailing(reply.String()), nil
}

// outputText reads the visible text of one completed message item.
func outputText(item provider.Item) (string, error) {
	var message struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(item, &message); err != nil {
		return "", err
	}
	if message.Type != "message" || message.Role != "assistant" {
		return "", nil
	}
	var text strings.Builder
	for _, part := range message.Content {
		if part.Type != "output_text" {
			return "", fmt.Errorf("%w: unexpected part", ErrReply)
		}
		text.WriteString(part.Text)
	}
	return text.String(), nil
}
