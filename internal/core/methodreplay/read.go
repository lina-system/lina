// SPDX-License-Identifier: Apache-2.0
package methodreplay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/thisisjun786/lina/persona"
)

const (
	maxLine      = 1 << 20
	maxTurnBytes = 32 << 10
	maxTurns     = 64
	maxAliases   = 32
	maxCaseID    = 64
)

var errCaseTooLarge = errors.New("case_too_large")

// Read returns the valid cases and one rejection per line it left out. A
// rejection carries the line and a reason code, never case text.
func Read(r io.Reader) ([]Case, []Rejection) {
	reader := bufio.NewReader(r)
	var cases []Case
	var rejected []Rejection
	seen := map[string]bool{}
	line := 0
	for {
		raw, err := caseLine(reader)
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			reason := "read_error"
			if errors.Is(err, errCaseTooLarge) {
				reason = "case_too_large"
			}
			rejected = append(rejected, Rejection{Line: line, Reason: reason})
			if errors.Is(err, errCaseTooLarge) {
				continue
			}
			break
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var c Case
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		reason := ""
		if !utf8.Valid(raw) || d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
			reason = "malformed_json"
		} else {
			reason = Validate(c)
		}
		if reason == "" && seen[c.ID] {
			reason = "duplicate_case"
		}
		if reason != "" {
			rejected = append(rejected, Rejection{Line: line, ID: safeID(c.ID), Reason: reason})
			continue
		}
		seen[c.ID] = true
		cases = append(cases, c)
	}
	if len(cases) == 0 && len(rejected) == 0 {
		rejected = append(rejected, Rejection{Reason: "empty_file"})
	}
	return cases, rejected
}

// Drain only the oversized line so later cases remain independently countable.
func caseLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	large := false
	for {
		part, prefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		if len(line)+len(part) > maxLine {
			large = true
		}
		if !large {
			line = append(line, part...)
		}
		if !prefix {
			if large {
				return nil, errCaseTooLarge
			}
			return line, nil
		}
	}
}

// safeID keeps a rejected case's id out of reports when it is not a plain token.
func safeID(id string) string {
	if id == "" || len(id) > maxCaseID {
		return ""
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return ""
		}
	}
	return id
}

func oneOf(value string, choices []string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

// Validate returns "" for a usable case and a reason code otherwise. A case
// declares the checks it must satisfy; a check's data must be present.
func Validate(c Case) string {
	if c.Version != 1 || strings.TrimSpace(c.ID) == "" || safeID(c.ID) != c.ID {
		return "invalid_case"
	}
	if !persona.IsStarter(c.Role) || !oneOf(c.ReplyType, replyTypes) {
		return "invalid_case"
	}
	if c.Kind != "correction" && c.Kind != "accepted" {
		return "invalid_case"
	}
	if c.Kind == "correction" {
		if !oneOf(c.Correction, corrections) {
			return "invalid_case"
		}
	} else if c.Correction != "" {
		return "invalid_case"
	}
	if c.Origin != "" && c.Origin != "native" && c.Origin != "completed" {
		return "invalid_case"
	}
	if len(c.Checks) == 0 || len(c.Checks) > len(checks) {
		return "invalid_case"
	}
	seen := map[Check]bool{}
	for _, check := range c.Checks {
		if !check.known() || seen[check] {
			return "invalid_case"
		}
		seen[check] = true
		switch check {
		case Length:
			if c.Budget <= 0 || c.Budget > 100000 {
				return "invalid_case"
			}
		case NoQuestion:
			if !c.Reversible {
				return "invalid_case"
			}
		case NoInternal, DoneEvidence:
			// Scored from the reply text alone; retired aliases are optional.
		case Language:
			if c.Language != "ko" && c.Language != "en" {
				return "invalid_case"
			}
		}
	}
	if len(c.RetiredAliases) > maxAliases {
		return "invalid_case"
	}
	aliases := map[string]bool{}
	for _, alias := range c.RetiredAliases {
		trimmed := strings.TrimSpace(alias)
		if trimmed == "" || trimmed != alias || len(alias) > 64 || aliases[alias] {
			return "invalid_case"
		}
		aliases[alias] = true
	}
	if len(c.Turns) == 0 || len(c.Turns) > maxTurns {
		return "invalid_case"
	}
	total := 0
	for _, turn := range c.Turns {
		if turn.Role != "user" && turn.Role != "assistant" {
			return "invalid_case"
		}
		if strings.TrimSpace(turn.Text) == "" || len(turn.Text) > maxTurnBytes {
			return "invalid_case"
		}
		total += len(turn.Text)
	}
	if total > 8*maxTurnBytes {
		return "invalid_case"
	}
	kept, _ := Dropped(c)
	if len(kept) == 0 || kept[len(kept)-1].Role != "user" {
		return "invalid_case"
	}
	owner := false
	for _, turn := range kept {
		if turn.Role == "user" {
			owner = true
			break
		}
	}
	if !owner {
		return "invalid_case"
	}
	return ""
}

// injectedPrefixes are the shapes the M4-1 analysis removed before counting:
// compaction summaries and an agent's own delegated briefs. A case may also
// flag a turn explicitly.
var injectedPrefixes = []string{
	"<compaction_summary>", "[compaction summary]", "<summary>",
	"you are a delegated worker", "worker brief:", "<worker_brief>", "<delegated_brief>",
}

func injected(turn Turn) bool {
	if turn.Role != "user" {
		return false
	}
	if turn.Injected {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(turn.Text))
	for _, prefix := range injectedPrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

// Dropped returns the turns the model sees and how many injected user turns
// were removed. It is idempotent.
func Dropped(c Case) ([]Turn, int) {
	kept := make([]Turn, 0, len(c.Turns))
	dropped := 0
	for _, turn := range c.Turns {
		if injected(turn) {
			dropped++
			continue
		}
		kept = append(kept, Turn{Role: turn.Role, Text: turn.Text})
	}
	return kept, dropped
}
