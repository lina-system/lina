// SPDX-License-Identifier: Apache-2.0
package reply

import (
	"encoding/json"
	"strings"

	"github.com/thisisjun786/lina/kit/result"
)

// ResultView selects presentation from typed data. Shape violations are audit
// facts only; it neither rejects them nor mutates the authored result.
type ResultView struct {
	Text   string
	Inline bool
	Check  Check
}

func ProjectResult(c Config, r result.Result) (ResultView, error) {
	body, err := result.Validate(r.Type, r.Body, result.Limits{})
	if err != nil {
		return ResultView{}, err
	}
	kind := r.ReplyType
	if kind == "" {
		switch v := body.(type) {
		case *result.Table, *result.Comparison:
			kind = "comparison"
		case *result.Draft:
			if strings.TrimSpace(v.Recipient) != "" {
				kind = "third_party"
			}
		}
	}
	if kind == "status" {
		lines := 3
		if p, ok := body.(*result.Plan); ok && len(p.Entries) > 1 {
			lines = 5
		}
		c.Budget.Lines = min(c.Budget.Lines, lines)
	}
	f, err := NewFilter(c)
	if err != nil {
		return ResultView{}, err
	}
	summary, err := f.Push(r.Summary)
	if err != nil {
		return ResultView{}, err
	}
	out := ResultView{Text: summary.Text, Check: f.Check()}
	out.Check.ReplyType = kind
	if c.Shapes == nil {
		return out, nil
	}
	breach := false
	assessed := false
	switch kind {
	case "status":
		assessed = true
		breach = c.Shapes.Status == "verdict-first" && (summary.Count.Lines != 1 || summary.Count.Chars == 0)
	case "comparison":
		assessed = true
		breach = summary.Count.Lines != 1 || summary.Count.Chars == 0
		if c.Shapes.Comparison == "verdict-then-table" && r.Completion == nil {
			if table := comparisonTable(body); table != nil {
				inline, err := NewFilter(c)
				if err != nil {
					return ResultView{}, err
				}
				view, err := inline.Push(r.Summary + "\n" + result.PlainText(table, nil))
				if err != nil {
					return ResultView{}, err
				}
				if !view.Cut {
					out.Text, out.Inline = view.Text, true
				}
			}
		}
	case "candidates":
		switch options := body.(type) {
		case *result.Table:
			assessed = true
			breach = len(options.Rows) > c.Shapes.Candidates
		case *result.Comparison:
			assessed = true
			breach = len(options.Targets) > c.Shapes.Candidates
		}
	case "third_party":
		if doc, ok := body.(*result.Draft); ok && c.Shapes.ThirdParty == "recipient-only" {
			assessed = true
			breach = strings.TrimSpace(doc.Recipient) == ""
			for _, block := range doc.Blocks {
				breach = breach || block.Audience == "person"
			}
		}
	}
	if assessed {
		out.Check.Assessed = []string{kind}
	}
	if breach {
		out.Check.ShapeBreaches = []string{kind}
	}
	return out, nil
}

// A comparison without hunks or an apply action has an exact table view.
// Structured changes with extra detail keep their card; no fields are dropped
// to make an inline presentation fit.
func comparisonTable(body result.Body) *result.Table {
	switch value := body.(type) {
	case *result.Table:
		return value
	case *result.Comparison:
		if value.Apply != nil {
			return nil
		}
		table := &result.Table{Columns: []result.Column{{Name: "Item", Type: "text"}, {Name: "Before", Type: "text"}, {Name: "After", Type: "text"}}}
		for _, target := range value.Targets {
			if len(target.Hunks) != 0 {
				return nil
			}
			var row []json.RawMessage
			for _, text := range []string{target.Label, target.Old, target.New} {
				raw, _ := json.Marshal(text)
				row = append(row, raw)
			}
			table.Rows = append(table.Rows, row)
		}
		return table
	}
	return nil
}
