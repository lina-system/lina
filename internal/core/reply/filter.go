// SPDX-License-Identifier: Apache-2.0
package reply

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"github.com/thisisjun786/lina/kit/protocol"
)

var ErrUTF8 = errors.New("reply delta is not valid UTF-8")

// Draft is the generated document_draft preview body. The full original reply
// is one literal paragraph, not parsed as executable or model-authored markup.
// Result tools own the general body schema; this internal value is not a wire
// payload while the protocol's result body remains reserved.
type Draft struct {
	Blocks      []Paragraph `json:"blocks"`
	FromSummary *string     `json:"from_summary"`
}
type Paragraph struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type Preview struct {
	Type   protocol.ResultType   `json:"type"`
	Status protocol.ResultStatus `json:"status"`
	Text   string                `json:"text"`
	Body   Draft                 `json:"body"`
}

// View replaces the previously displayed reply; Text is not an append delta.
// On the first breach it retracts the unfinished sentence. Card keeps all text,
// including text received after the visible answer was cut. Count measures the
// full reply, not its shortened presentation.
type View struct {
	Text  string
	Count Count
	Cut   bool
	Card  *Preview
}

// Filter handles one reply on one surface. Calls must be serialized. It never
// stops generation or rewrites its source. The caller persists FullText through
// the ledger before claiming the answer is recorded.
type Filter struct {
	budget      Budget
	korean      bool
	renderLines func(string) int
	full        strings.Builder
	cut         bool
}

func NewFilter(c Config) (*Filter, error) {
	budget, korean, err := configured(c)
	if err != nil {
		return nil, err
	}
	return &Filter{budget: budget, korean: korean, renderLines: c.RenderLines}, nil
}

// Budget returns the resolved limit in the selected language's scalar values.
func (f *Filter) Budget() Budget   { return f.budget }
func (f *Filter) FullText() string { return f.full.String() }

// Push accepts a decoded provider text delta. Invalid UTF-8 is refused without
// changing state. Combining sequences may span deltas: counting normalizes the
// accumulated text, leaving display and ledger bytes intact.
func (f *Filter) Push(delta string) (View, error) {
	if !utf8.ValidString(delta) {
		return View{}, ErrUTF8
	}
	f.full.WriteString(delta)
	text := f.full.String()
	count := f.count(text)
	f.cut = f.cut || count.exceeds(f.budget)
	v := View{Text: text, Count: count, Cut: f.cut}
	if f.cut {
		v.Text = f.lastSentence(text)
		v.Card = &Preview{Type: protocol.ResultTypeDocumentDraft, Status: protocol.ResultStatusPreview, Text: text,
			Body: Draft{Blocks: []Paragraph{{Kind: "paragraph", Text: text}}}}
	}
	return v, nil
}

// Unicode sentence segmentation handles decimal points, sentence punctuation,
// closing quotes and scripts without spaces. An unterminated final fragment is
// not a complete sentence even though the segmenter returns it at end of input.
func (f *Filter) lastSentence(text string) string {
	rest, state, end, best := text, -1, 0, 0
	for rest != "" {
		var sentence string
		sentence, rest, state = uniseg.FirstSentenceInString(rest, state)
		end += len(sentence)
		prefix := strings.TrimRightFunc(text[:end], unicode.IsSpace)
		if f.count(prefix).exceeds(f.budget) {
			break
		}
		if terminated(prefix) {
			best = len(prefix)
		}
	}
	return text[:best]
}

func terminated(text string) bool {
	for len(text) > 0 {
		r, size := utf8.DecodeLastRuneInString(text)
		if strings.ContainsRune(".!?。！？…", r) {
			return true
		}
		if unicode.IsSpace(r) || unicode.Is(unicode.Pe, r) || unicode.Is(unicode.Pf, r) || r == '\'' || r == '"' {
			text = text[:len(text)-size]
			continue
		}
		return false
	}
	return false
}
