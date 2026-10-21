// SPDX-License-Identifier: Apache-2.0

// Package reply limits presentation without shortening the recorded answer.
package reply

import (
	"errors"
	"math"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Budget is a presentation limit, selected by the caller for a person, reply
// type and surface. Chars is expressed in Korean units before language scaling.
type Budget struct{ Lines, Chars int }

func ChatBudget() Budget  { return Budget{Lines: 3, Chars: 150} }
func ChatCeiling() Budget { return Budget{Lines: 5, Chars: 300} }

var ErrConfig = errors.New("invalid reply configuration")

// Config supplies the surface's actual layout and its language ratio data.
// RenderLines counts the rendered lines of the original text (including wraps),
// returns zero for empty text, and must be deterministic for the reply lifetime.
// Ratios converts Korean character limits to other languages' scalar limits.
// Korean is always 1:1; other languages require an explicit positive ratio.
// Region tags use their base language. No language is inferred from the text.
type Config struct {
	Budget      Budget
	Language    string
	Ratios      map[string]float64
	RenderLines func(string) int
}

type Count struct{ Lines, Chars int }

func (c Count) exceeds(b Budget) bool { return c.Lines > b.Lines || c.Chars > b.Chars }

func (f *Filter) count(text string) Count {
	n := 0
	for _, r := range norm.NFC.String(text) {
		if f.korean && unicode.IsSpace(r) {
			continue
		}
		n++
	}
	return Count{Lines: f.renderLines(text), Chars: n}
}

func configured(c Config) (Budget, bool, error) {
	lang := strings.ToLower(strings.Split(c.Language, "-")[0])
	ratio := c.Ratios[lang]
	if lang == "ko" {
		ratio = 1
	}
	chars := math.Floor(float64(c.Budget.Chars) * ratio)
	if c.Budget.Lines <= 0 || c.Budget.Chars <= 0 || c.RenderLines == nil || lang == "" || ratio <= 0 || math.IsNaN(chars) || math.IsInf(chars, 0) || chars < 1 || chars >= float64(int(^uint(0)>>1)) {
		return Budget{}, false, ErrConfig
	}
	return Budget{Lines: c.Budget.Lines, Chars: int(chars)}, lang == "ko", nil
}
