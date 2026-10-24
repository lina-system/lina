// SPDX-License-Identifier: Apache-2.0
package binding

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxQuote      = 8192
	maxDifference = 4096
)

// NormalizeChoices turns the binding arguments a model produced into canonical
// choices. The model may send the shapes it naturally uses: a target as an ID,
// a title or an object, tied targets as strings, a request as a number or a
// number string, and no excerpt when it quotes nothing.
//
// Recoverable metadata is corrected rather than rejected: an absent or inexact
// excerpt becomes the input text, an unknown kind becomes a to-do, a parent
// that is not an open initiative or project is dropped, and close matches that
// are not open items are removed. Only a choice that would bind to an unknown
// or ambiguous existing item is returned as a ChoiceError, so the model can
// correct it without ever steering the wrong work.
//
// A request below zero means the caller places the choice in message order.
func NormalizeChoices(state State, text string, raws []json.RawMessage) ([]Choice, error) {
	out := make([]Choice, 0, len(raws))
	for n, raw := range raws {
		c, err := normalizeChoice(state, text, raw)
		if err != nil {
			var detail *ChoiceError
			if errors.As(err, &detail) && detail.Field != "" {
				detail.Field = "bindings[" + itoa(n) + "]." + detail.Field
			}
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [8]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}

type rawChoice struct {
	Request    json.RawMessage `json:"request"`
	Index      json.RawMessage `json:"index"`
	Quote      flexText        `json:"quote"`
	Target     json.RawMessage `json:"target"`
	Tied       rawTargets      `json:"tied"`
	Action     string          `json:"action"`
	Close      rawStrings      `json:"close_matches"`
	Difference string          `json:"difference"`
	More       rawTargets      `json:"more"`
}

func normalizeChoice(state State, text string, raw json.RawMessage) (Choice, error) {
	var in rawChoice
	if len(raw) == 0 || json.Unmarshal(raw, &in) != nil {
		return Choice{}, &ChoiceError{Field: "bindings", Reason: "not_an_object"}
	}
	c := Choice{Request: -1}
	if n, ok := flexInt(in.Request); ok {
		c.Request = n
	} else if n, ok := flexInt(in.Index); ok {
		c.Request = n
	}
	if c.Request >= 32 {
		c.Request = -1
	}
	quote := strings.TrimSpace(string(in.Quote))
	if quote == "" || !strings.Contains(text, quote) {
		// A paraphrase still identifies the request; the input text stays the
		// exact excerpt the person sees and the replay records.
		quote = strings.TrimSpace(text)
	}
	c.Quote = truncateRunes(quote, maxQuote)
	if len(in.Target) == 0 || string(in.Target) == "null" {
		return Choice{}, &ChoiceError{Field: "target", Reason: "missing_target", Allowed: []string{"existing", "new", "none"}}
	}
	target, err := NormalizeTarget(state, in.Target)
	if err != nil {
		return Choice{}, err
	}
	c.Target = target
	tied := []Target{target}
	for _, rawTied := range in.Tied {
		other, err := NormalizeTarget(state, rawTied)
		if err != nil {
			// Dropping an alternative would leave the recommendation settled.
			return Choice{}, err
		}
		if !slices.Contains(tied, other) {
			tied = append(tied, other)
		}
	}
	if len(tied) > 1 {
		c.Tied = tied[:min(5, len(tied))]
	}
	// A later page is Core's own overflow. The tool drops a page the model sends
	// before this decode, so a remaining page is part of the inherited set.
	for _, rawMore := range in.More {
		other, err := NormalizeTarget(state, rawMore)
		if err != nil {
			return Choice{}, err
		}
		if slices.Contains(tied, other) || slices.Contains(c.More, other) {
			continue
		}
		c.More = append(c.More, other)
	}
	c.Action = normalizeAction(in.Action)
	for _, id := range in.Close {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(c.Close, id) && slices.ContainsFunc(state.Items, func(i Item) bool { return i.ID == id && Open(i) }) {
			c.Close = append(c.Close, id)
		}
	}
	c.Difference = truncateRunes(strings.TrimSpace(in.Difference), maxDifference)
	return c, nil
}

// NormalizeTarget accepts a target keyword, an item reference or an object.
func NormalizeTarget(state State, raw json.RawMessage) (Target, error) {
	var keyword string
	if json.Unmarshal(raw, &keyword) == nil {
		return targetFromText(state, keyword)
	}
	var obj struct {
		Class  flexText `json:"class"`
		Kind   flexText `json:"kind"`
		ID     flexText `json:"id"`
		Ref    flexText `json:"ref"`
		Title  flexText `json:"title"`
		Name   flexText `json:"name"`
		Parent flexText `json:"parent"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return Target{}, &ChoiceError{Field: "target", Reason: "invalid_target", Allowed: []string{"existing", "new", "none"}}
	}
	class := strings.ToLower(strings.TrimSpace(string(obj.Class)))
	ref := firstText(obj.ID, obj.Ref, obj.Title, obj.Name)
	kind := NormalizeKind(string(obj.Kind))
	parent := strings.TrimSpace(string(obj.Parent))
	switch class {
	case "", "existing", "item", "work", "record", "ref":
		if ref != "" {
			return resolveReference(state, ref)
		}
		if kind != "" || parent != "" {
			return newTarget(state, kind, parent), nil
		}
		return Target{}, &ChoiceError{Field: "target", Reason: "missing_target", Allowed: []string{"existing", "new", "none"}}
	case "focus", "current":
		if state.Focus.Class == "existing" {
			return state.Focus, nil
		}
		return Target{Class: "none"}, nil
	case "new":
		if ref != "" {
			// A model that sends both a new class and an existing identity means
			// that item; creating a second copy is the outcome to avoid.
			if target, err := resolveReference(state, ref); err == nil {
				return target, nil
			}
		}
		return newTarget(state, kind, parent), nil
	case "none", "not-applicable", "not_applicable", "n/a", "na", "nothing":
		return Target{Class: "none"}, nil
	default:
		if k := NormalizeKind(class); k != "" {
			if ref != "" {
				if target, err := resolveReference(state, ref); err == nil {
					return target, nil
				}
			}
			return newTarget(state, k, parent), nil
		}
		return Target{}, &ChoiceError{Field: "target.class", Reason: "unsupported_class", Allowed: []string{"existing", "new", "none"}}
	}
}

func targetFromText(state State, value string) (Target, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Target{}, &ChoiceError{Field: "target", Reason: "missing_target", Allowed: []string{"existing", "new", "none"}}
	}
	lower := strings.ToLower(value)
	switch lower {
	case "new":
		return newTarget(state, "", ""), nil
	case "none", "nothing", "not-applicable", "not_applicable", "n/a", "na":
		return Target{Class: "none"}, nil
	case "focus", "current", "same":
		if state.Focus.Class == "existing" {
			return state.Focus, nil
		}
		return Target{Class: "none"}, nil
	}
	if fields := strings.Fields(lower); len(fields) == 2 && (fields[0] == "new" || fields[0] == "create" || fields[0] == "add") {
		if kind := NormalizeKind(fields[1]); kind != "" {
			return newTarget(state, kind, ""), nil
		}
	}
	// An id such as "task:a" names that candidate even when its prefix is a
	// stored kind. Kind-and-colon text is new work only when no candidate has
	// that exact id.
	if item, ok := itemByID(state, value); ok {
		return Target{Class: "existing", ID: item.ID, Kind: item.Kind}, nil
	}
	if prefix, rest, ok := strings.Cut(value, ":"); ok && strings.TrimSpace(rest) != "" {
		if kind := NormalizeKind(prefix); kind != "" {
			return newTarget(state, kind, ""), nil
		}
		switch strings.ToLower(strings.TrimSpace(prefix)) {
		case "item", "work", "record", "ref", "target":
			value = strings.TrimSpace(rest)
		}
	}
	return resolveReference(state, value)
}

func itemByID(state State, ref string) (Item, bool) {
	folded := Fold(strings.TrimSpace(ref))
	if folded == "" {
		return Item{}, false
	}
	for _, item := range state.Items {
		if Fold(item.ID) == folded {
			return item, true
		}
	}
	return Item{}, false
}

func resolveReference(state State, ref string) (Target, error) {
	if item, ok := itemByID(state, ref); ok {
		return Target{Class: "existing", ID: item.ID, Kind: item.Kind}, nil
	}
	ref = strings.TrimSpace(ref)
	var matches []Item
	for _, item := range state.Items {
		names := append(append([]string{item.Title}, item.Names...), item.Links...)
		if slices.ContainsFunc(names, func(name string) bool { return name != "" && Fold(strings.TrimSpace(name)) == Fold(ref) }) {
			matches = append(matches, item)
		}
	}
	// Unfinished work wins when any open item matches. With none, one offered
	// item, including a finished one, is that target. Its id already binds.
	var open []Item
	for _, item := range matches {
		if Open(item) {
			open = append(open, item)
		}
	}
	if len(open) > 0 {
		matches = open
	}
	switch len(matches) {
	case 1:
		return Target{Class: "existing", ID: matches[0].ID, Kind: matches[0].Kind}, nil
	case 0:
		return Target{}, &ChoiceError{Field: "target", Reason: "unknown_target", Allowed: openIDs(state)}
	default:
		ids := make([]string, len(matches))
		for n, item := range matches {
			ids[n] = item.ID
		}
		return Target{}, &ChoiceError{Field: "target", Reason: "ambiguous_target", Allowed: ids}
	}
}

func newTarget(state State, kind, parent string) Target {
	if kind == "" {
		kind = "todo"
	}
	target := Target{Class: "new", Kind: kind}
	if parent = strings.TrimSpace(parent); parent != "" {
		for _, item := range state.Items {
			if Fold(item.ID) == Fold(parent) && Open(item) && (item.Kind == "initiative" || item.Kind == "project") {
				target.Parent = item.ID
				break
			}
		}
	}
	return target
}

func openIDs(state State) []string {
	ids := make([]string, 0, len(state.Items))
	for _, item := range state.Items {
		if Open(item) {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

// NormalizeKind maps the words a model uses for item kinds onto the stored
// kinds. It returns "" when the word names no work kind.
func NormalizeKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "initiative":
		return "initiative"
	case "project", "goal", "objective":
		return "project"
	case "todo", "to-do", "to_do", "todos":
		return "todo"
	case "task", "job", "work", "step":
		return "task"
	case "run", "background-run", "background_run", "backgroundrun", "background":
		return "run"
	}
	return ""
}

func normalizeAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "read", "edit", "steer", "start", "send", "pay", "delete", "stop":
		return strings.ToLower(strings.TrimSpace(action))
	case "follow", "follow-up", "follow_up", "message", "reply":
		return "steer"
	case "run", "dispatch", "execute", "launch", "begin":
		return "start"
	case "payment", "charge", "buy":
		return "pay"
	case "remove", "trash", "erase":
		return "delete"
	}
	return ""
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func firstText(values ...flexText) string {
	for _, value := range values {
		if text := strings.TrimSpace(string(value)); text != "" {
			return text
		}
	}
	return ""
}

// flexText accepts a JSON string, number or null.
type flexText string

func (f *flexText) UnmarshalJSON(raw []byte) error {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		*f = flexText(text)
		return nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) == nil {
		*f = flexText(number.String())
		return nil
	}
	if string(bytes.TrimSpace(raw)) == "null" {
		*f = ""
		return nil
	}
	return errors.New("not_text")
}

type rawTargets []json.RawMessage

func (r *rawTargets) UnmarshalJSON(raw []byte) error {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		*r = list
		return nil
	}
	var one json.RawMessage
	if json.Unmarshal(raw, &one) != nil {
		return errors.New("not_targets")
	}
	*r = []json.RawMessage{one}
	return nil
}

type rawStrings []string

func (r *rawStrings) UnmarshalJSON(raw []byte) error {
	var list []flexText
	if json.Unmarshal(raw, &list) == nil {
		for _, value := range list {
			*r = append(*r, string(value))
		}
		return nil
	}
	var one flexText
	if json.Unmarshal(raw, &one) != nil {
		return errors.New("not_strings")
	}
	*r = append(*r, string(one))
	return nil
}

func flexInt(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return 0, false
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) != nil {
		return 0, false
	}
	value, err := number.Int64()
	if err != nil || value < 0 {
		return 0, false
	}
	return int(value), true
}

// LooksLikeID reports whether a selector can be a Core identifier. Message
// text usually contains spaces, a script other than Latin, or punctuation that
// an identifier never carries.
func LooksLikeID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if utf8.RuneCountInString(value) > 64 {
		return false
	}
	for _, r := range value {
		if r < unicode.MaxASCII {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_-:./@", r) {
				continue
			}
			return false
		}
		return false
	}
	return true
}
