// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/thisisjun786/lina/kit/provider"
)

var ErrSummary = errors.New("summary_failed")

const PromptVersion = "compaction-v2-carried-values"

//go:embed summary.txt
var summaryInstructions string

//go:embed values-delta.txt
var valuesDeltaInstructions string

//go:embed values-condense.txt
var valuesCondenseInstructions string

const handoff = "Reference summary of earlier turns. The latest input is the active request; do not resume work mentioned only here unless that input asks for it.\n"
const managerHandoff = "Reference summary of earlier project events. The current project event batch under the existing delegation is the active input, not a new message from the person.\n"

// Auxiliary uses the conversation model/account and reasoning effort. It clears
// unrelated history and tools by default; RetainPrefix preserves an explicit
// conversation prefix for compaction. Bridges are always cleared. The caller
// owns normal cost limits.
type Auxiliary struct {
	// RetainPrefix keeps the caller-supplied fixed prefix and tools for cache reuse.
	// Request.Items must contain only the fixed prefix; Records are exact projections.
	RetainPrefix bool
	Request      provider.Request
	Stream       func(context.Context, provider.Request) (provider.Stream, error)
}

func (a Auxiliary) Summarize(ctx context.Context, segment PassSegment) (Text, error) {
	var previous Text
	carry := len(segment.Root) > 0 && len(segment.Records) > 0
	if carry {
		var err error
		previous, err = summaryText(segment.Root)
		if err != nil {
			return Text{}, err
		}
	}
	instructions := summaryInstructions
	if carry {
		instructions += "\n" + valuesDeltaInstructions
	} else if len(segment.Root) > 0 {
		instructions += "\n" + valuesCondenseInstructions
	}
	text, err := a.generate(ctx, segment, instructions)
	if err != nil {
		return Text{}, err
	}
	if carry {
		text.Values = carryValues(previous.Values, text.Values)
	}
	if len(text.Values) <= maxCarriedValueBytes {
		return text, nil
	}
	// One bounded extra call, never a truncation or an unbounded repair loop.
	// All values (including the newly extracted ones) remain in its input.
	text, err = a.generate(ctx, PassSegment{Root: SummaryItem(text, false)}, summaryInstructions+"\n"+valuesCondenseInstructions)
	if err != nil {
		return Text{}, err
	}
	if len(text.Values) > maxCarriedValueBytes {
		return Text{}, ErrSummary
	}
	return text, nil
}

// SummaryRequest is the exact auxiliary request Summarize sends for segment,
// built without sending it so the provider replay can measure the same bytes.
func (a Auxiliary) SummaryRequest(segment PassSegment) (provider.Request, error) {
	return a.request(segment, a.instructions(segment))
}

func (a Auxiliary) instructions(segment PassSegment) string {
	instructions := summaryInstructions
	switch {
	case len(segment.Root) > 0 && len(segment.Records) > 0:
		instructions += "\n" + valuesDeltaInstructions
	case len(segment.Root) > 0:
		instructions += "\n" + valuesCondenseInstructions
	}
	return instructions
}

// request keeps the conversation's fixed instructions, tool declarations and
// history unchanged through the end of the segment, then adds the summary
// instruction. Tools and tool choice are the conversation's own.
func (a Auxiliary) request(segment PassSegment, instructions string) (provider.Request, error) {
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return provider.Request{}, err
	}
	// JSON encoding keeps role delimiters inert; a fresh nonce prevents source
	// text from closing the data block. Only segment records/direct root are sent.
	body, err := json.Marshal(struct {
		Root    provider.Item `json:"child_summary,omitempty"`
		Records []Record      `json:"events"`
	}{segment.Root, segment.Records})
	if err != nil {
		return provider.Request{}, errors.Join(ErrSummary, err)
	}
	delimiter := "segment_" + hex.EncodeToString(nonce[:])
	req := a.Request
	req.Purpose = provider.Auxiliary
	// Auxiliary requests omit the adapter-level instructions field. Keep the
	// trusted summary contract in a developer input item on the actual wire.
	req.Instructions = ""
	req.Tools = nil
	req.Bridge = nil
	req.Fields = nil
	// A request without the conversation prefix carries none of its boundaries.
	req.Layout = nil
	req.Capabilities.CacheMarks = nil
	req.Items = []provider.Item{textItem("developer", instructions), textItem("user", "<"+delimiter+">\n"+string(body)+"\n</"+delimiter+">")}
	if a.RetainPrefix {
		req.Instructions = a.Request.Instructions
		req.Tools = copyItems(a.Request.Tools)
		req.Fields = append(provider.FieldList(nil), a.Request.Fields...)
		req.Items = copyItems(a.Request.Items)
		if len(segment.Root) > 0 {
			req.Items = append(req.Items, append(provider.Item(nil), segment.Root...))
		}
		for _, record := range segment.Records {
			req.Items = append(req.Items, append(provider.Item(nil), record.Item...))
		}
		// The segment ends at the last retained history item; the instruction
		// is not an input, so no mark names it.
		req.Layout = prefixLayout(a.Request.Layout, len(req.Items), len(req.Tools))
		req.Capabilities.CacheMarks = prefixMarks(a.Request.Capabilities.CacheMarks, req.Layout)
		req.Items = append(req.Items, textItem("developer", instructions))
	}
	return req, nil
}

// prefixLayout keeps the conversation's fixed and previous-input positions and
// ends the marked prefix where the segment ends. A layout that the retained
// prefix cannot satisfy is dropped with its marks.
func prefixLayout(l *provider.RequestLayout, items, tools int) *provider.RequestLayout {
	if l == nil || items <= l.Fixed.Items || l.Fixed.Tools > tools || l.CurrentInput.Tools > tools {
		return nil
	}
	out := &provider.RequestLayout{Fixed: l.Fixed, CurrentInput: provider.Position{Items: items, Tools: l.CurrentInput.Tools}}
	if l.PreviousInput != nil && l.PreviousInput.Items < items && l.PreviousInput.Items > l.Fixed.Items && l.PreviousInput.Tools <= tools {
		previous := *l.PreviousInput
		out.PreviousInput = &previous
	}
	return out
}

// prefixMarks keeps only marks that the retained prefix still covers.
func prefixMarks(marks []provider.CacheMark, l *provider.RequestLayout) []provider.CacheMark {
	if l == nil {
		return nil
	}
	var out []provider.CacheMark
	for _, mark := range marks {
		if mark.Position != l.Fixed && (l.PreviousInput == nil || mark.Position != *l.PreviousInput) {
			continue
		}
		out = append(out, mark)
	}
	return out
}
func (a Auxiliary) generate(ctx context.Context, segment PassSegment, instructions string) (Text, error) {
	if a.Stream == nil || a.Request.Model == "" {
		return Text{}, ErrSummary
	}
	req, err := a.request(segment, instructions)
	if err != nil {
		return Text{}, err
	}
	stream, err := a.Stream(ctx, req)
	if err != nil {
		return Text{}, errors.Join(ErrSummary, err)
	}
	if stream == nil {
		return Text{}, ErrSummary
	}
	defer stream.Close()
	terminal := false
	var output strings.Builder
	for {
		event, err := stream.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Text{}, errors.Join(ErrSummary, err)
		}
		if terminal {
			return Text{}, ErrSummary
		}
		switch event.Kind {
		case provider.Delta: // Only committed assistant items count.
		case provider.ToolCallReady:
			return Text{}, ErrSummary
		case provider.Terminal:
			terminal = true
		case provider.ItemDone:
			var item struct {
				Type, Role string
				Content    json.RawMessage
			}
			if json.Unmarshal(event.Item, &item) != nil {
				return Text{}, ErrSummary
			}
			if item.Type == "reasoning" {
				continue
			}
			if item.Role != "assistant" || (item.Type != "" && item.Type != "message") {
				return Text{}, ErrSummary
			}
			var text string
			if json.Unmarshal(item.Content, &text) == nil {
				output.WriteString(text)
				continue
			}
			var content []struct{ Type, Text string }
			if json.Unmarshal(item.Content, &content) != nil {
				return Text{}, ErrSummary
			}
			for _, c := range content {
				if c.Type != "output_text" {
					return Text{}, ErrSummary
				}
				output.WriteString(c.Text)
			}
		default:
			return Text{}, ErrSummary
		}
	}
	if !terminal || ctx.Err() != nil {
		return Text{}, errors.Join(ErrSummary, ctx.Err())
	}
	var summary Text
	decoder := json.NewDecoder(strings.NewReader(output.String()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&summary) != nil || decoder.Decode(new(any)) != io.EOF || !summary.valid() {
		return Text{}, ErrSummary
	}
	return summary, nil
}

// SummaryItem is a data message. It never upgrades a remembered instruction to
// a developer/system instruction. Manager sessions explicitly retain automation.
func SummaryItem(text Text, manager bool) provider.Item {
	raw, _ := json.Marshal(text)
	note := handoff
	if manager {
		note = managerHandoff
	}
	return textItem("user", note+string(raw))
}
