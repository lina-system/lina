// SPDX-License-Identifier: Apache-2.0

package result

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

const SurfaceType Type = "surface"
const SurfaceCatalog = "lina.cards"
const SurfaceCatalogVersion = "1.0"

var ErrSurfaceValidation = errors.New("VALIDATION_FAILED")

// Surface is a complete immutable revision, including a deletion tombstone.
// The catalog schema lives in the client protocol, shared with its consumers.
type Surface struct {
	Version        string                     `json:"version"`
	CatalogID      string                     `json:"catalogId"`
	CatalogVersion string                     `json:"catalogVersion"`
	ID             string                     `json:"surfaceId"`
	Components     []SurfaceComponent         `json:"components"`
	Data           map[string]json.RawMessage `json:"dataModel"`
	Deleted        bool                       `json:"deleted"`
}

func (*Surface) resultBody() {}

type SurfaceComponent struct {
	ID           string              `json:"id"`
	Component    string              `json:"component"`
	Text         json.RawMessage     `json:"text,omitempty"`
	Title        json.RawMessage     `json:"title,omitempty"`
	Label        json.RawMessage     `json:"label,omitempty"`
	Value        json.RawMessage     `json:"value,omitempty"`
	Elapsed      json.RawMessage     `json:"elapsed,omitempty"`
	State        string              `json:"state,omitempty"`
	Variant      string              `json:"variant,omitempty"`
	Children     []string            `json:"children,omitempty"`
	Child        string              `json:"child,omitempty"`
	Align        string              `json:"align,omitempty"`
	Justify      string              `json:"justify,omitempty"`
	Direction    string              `json:"direction,omitempty"`
	Axis         string              `json:"axis,omitempty"`
	Options      []SurfaceOption     `json:"options,omitempty"`
	DisplayStyle string              `json:"displayStyle,omitempty"`
	Filterable   *bool               `json:"filterable,omitempty"`
	EnableDate   *bool               `json:"enableDate,omitempty"`
	EnableTime   *bool               `json:"enableTime,omitempty"`
	Action       *SurfaceEventAction `json:"action,omitempty"`
}
type SurfaceOption struct {
	Label json.RawMessage `json:"label"`
	Value string          `json:"value"`
}
type SurfaceEventAction struct {
	Event SurfaceEvent `json:"event"`
}
type SurfaceEvent struct {
	Name    string                     `json:"name"`
	Context map[string]json.RawMessage `json:"context"`
}

func surfaceError(reason string) error { return fmt.Errorf("%w: %s", ErrSurfaceValidation, reason) }
func validateSurface(raw []byte) (*Surface, error) {
	if len(raw) > 64<<10 {
		return nil, surfaceError("card exceeds 64 KiB")
	}
	if err := protocol.ValidateSurfaceBody(raw); err != nil {
		return nil, surfaceError("catalog schema")
	}
	var s Surface
	if err := Decode(raw, &s); err != nil {
		return nil, surfaceError("card shape")
	}
	if err := s.check(); err != nil {
		return nil, err
	}
	return &s, nil
}

func pathOf(raw json.RawMessage) string {
	var p struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(raw, &p) == nil {
		return p.Path
	}
	return ""
}
func pointerParts(path string) ([]string, error) {
	if path == "" || path == "/" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "/") || len(path) > 512 {
		return nil, surfaceError("data path")
	}
	parts := strings.Split(path[1:], "/")
	for i, p := range parts {
		for j := 0; j < len(p); j++ {
			if p[j] == '~' {
				if j+1 == len(p) || p[j+1] != '0' && p[j+1] != '1' {
					return nil, surfaceError("data path")
				}
				j++
			}
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
	}
	return parts, nil
}
func (s *Surface) resolve(raw json.RawMessage) (json.RawMessage, error) {
	path := pathOf(raw)
	if path == "" {
		return raw, nil
	}
	parts, err := pointerParts(path)
	if err != nil {
		return nil, err
	}
	current, _ := json.Marshal(s.Data)
	for _, part := range parts {
		var object map[string]json.RawMessage
		if json.Unmarshal(current, &object) == nil && object != nil {
			current = object[part]
		} else {
			var array []json.RawMessage
			n, e := strconv.Atoi(part)
			if json.Unmarshal(current, &array) != nil || e != nil || n < 0 || n >= len(array) {
				return nil, surfaceError("unresolved data path")
			}
			current = array[n]
		}
		if current == nil {
			return nil, surfaceError("unresolved data path")
		}
	}
	return current, nil
}
func (s *Surface) stringValue(raw json.RawMessage) (string, error) {
	value, err := s.resolve(raw)
	if err != nil {
		return "", err
	}
	var text string
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &text) != nil {
		return "", surfaceError("expected text")
	}
	return text, nil
}
func (s *Surface) ordered() ([]SurfaceComponent, error) {
	byID := map[string]SurfaceComponent{}
	for _, c := range s.Components {
		if _, ok := byID[c.ID]; ok {
			return nil, surfaceError("duplicate component")
		}
		byID[c.ID] = c
	}
	seen := map[string]bool{}
	out := []SurfaceComponent{}
	var visit func(string, int) error
	visit = func(id string, depth int) error {
		c, ok := byID[id]
		if !ok || seen[id] || depth > 32 {
			return surfaceError("component tree")
		}
		seen[id] = true
		out = append(out, c)
		children := c.Children
		if c.Child != "" {
			children = []string{c.Child}
		}
		for _, child := range children {
			if err := visit(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit("root", 0); err != nil {
		return nil, err
	}
	return out, nil
}
func (s *Surface) check() error {
	if s.Deleted {
		if len(s.Components) != 0 || len(s.Data) != 0 {
			return surfaceError("deleted card has content")
		}
		return nil
	}
	ordered, err := s.ordered()
	if err != nil {
		return err
	}
	if len(ordered) != len(s.Components) {
		return surfaceError("unreachable component")
	}
	names := map[string]bool{}
	paths := map[string]bool{}
	for _, c := range ordered {
		for _, raw := range []json.RawMessage{c.Text, c.Title, c.Label, c.Elapsed} {
			if raw != nil {
				if _, err := s.stringValue(raw); err != nil {
					return err
				}
			}
		}
		for _, o := range c.Options {
			if _, err := s.stringValue(o.Label); err != nil {
				return err
			}
		}
		if c.Value != nil {
			value, err := s.resolve(c.Value)
			if err != nil {
				return err
			}
			if c.Component == "ChoicePicker" {
				var list []string
				if json.Unmarshal(value, &list) != nil || list == nil {
					return surfaceError("choice binding must be a list")
				}
			}
			if err := checkSurfaceValue(c, value); err != nil {
				return err
			}
			if path := pathOf(c.Value); path != "" {
				if paths[path] {
					return surfaceError("shared editable binding")
				}
				paths[path] = true
			}
		}
		if c.Action != nil {
			name := c.Action.Event.Name
			if names[name] || len(names) >= 26 {
				return surfaceError("duplicate or excessive action")
			}
			names[name] = true
			for _, raw := range c.Action.Event.Context {
				resolved, err := s.resolve(raw)
				if err != nil {
					return err
				}
				var v any
				if json.Unmarshal(resolved, &v) != nil {
					return surfaceError("action context")
				}
				switch x := v.(type) {
				case string, bool, float64:
				case []any:
					for _, item := range x {
						if _, ok := item.(string); !ok {
							return surfaceError("action context")
						}
					}
				default:
					return surfaceError("action context")
				}
			}
		}
	}
	return nil
}
func checkSurfaceValue(c SurfaceComponent, raw json.RawMessage) error {
	switch c.Component {
	case "CheckBox":
		var v bool
		if json.Unmarshal(raw, &v) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return surfaceError("checkbox value")
		}
	case "ChoicePicker":
		var values []string
		if json.Unmarshal(raw, &values) != nil {
			var v string
			if json.Unmarshal(raw, &v) != nil {
				return surfaceError("choice value")
			}
			values = []string{v}
		}
		if values == nil || c.Variant != "multipleSelection" && len(values) > 1 {
			return surfaceError("choice count")
		}
		allowed := map[string]bool{}
		seen := map[string]bool{}
		for _, o := range c.Options {
			if allowed[o.Value] {
				return surfaceError("duplicate option")
			}
			allowed[o.Value] = true
		}
		for _, v := range values {
			if !allowed[v] || seen[v] {
				return surfaceError("unknown choice")
			}
			seen[v] = true
		}
	case "TextField", "DateTimeInput":
		var v string
		if json.Unmarshal(raw, &v) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || len(v) > 8192 {
			return surfaceError("input value")
		}
		if c.Component == "DateTimeInput" && v != "" {
			layout := "2006-01-02"
			date, timeEnabled := c.EnableDate != nil && *c.EnableDate, c.EnableTime != nil && *c.EnableTime
			if !date && !timeEnabled {
				return surfaceError("date mode")
			}
			if timeEnabled {
				layout = "15:04"
				if date {
					layout = time.RFC3339
				}
			}
			if _, err := time.Parse(layout, v); err != nil {
				return surfaceError("date value")
			}
		}
	}
	return nil
}

// SurfaceAction is derived by Core in reading order. A client may change only
// values bound to input widgets; constant context fields remain fixed.
type SurfaceAction struct {
	Key    string
	Name   string
	Values map[string]json.RawMessage
}

func (s *Surface) Actions() []SurfaceAction {
	if s.Deleted {
		return nil
	}
	ordered, err := s.ordered()
	if err != nil {
		return nil
	}
	var out []SurfaceAction
	for _, c := range ordered {
		if c.Action == nil {
			continue
		}
		a := SurfaceAction{Key: string(rune('a' + len(out))), Name: c.Action.Event.Name, Values: map[string]json.RawMessage{}}
		for k, v := range c.Action.Event.Context {
			a.Values[k], _ = s.resolve(v)
		}
		out = append(out, a)
	}
	return out
}
func (s *Surface) ValidateAction(name string, values map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if s.Deleted {
		return nil, surfaceError("deleted card")
	}
	for _, c := range s.Components {
		if c.Action == nil || c.Action.Event.Name != name {
			continue
		}
		if len(values) != len(c.Action.Event.Context) {
			return nil, surfaceError("action fields")
		}
		out := map[string]json.RawMessage{}
		for key, expected := range c.Action.Event.Context {
			raw, ok := values[key]
			if !ok {
				return nil, surfaceError("action field")
			}
			bound := false
			if path := pathOf(expected); path != "" {
				for _, input := range s.Components {
					if pathOf(input.Value) != path {
						continue
					}
					if err := checkSurfaceValue(input, raw); err != nil {
						return nil, err
					}
					if input.Component == "ChoicePicker" && input.Variant != "multipleSelection" {
						var list []string
						if json.Unmarshal(raw, &list) == nil {
							if len(list) != 1 {
								return nil, surfaceError("one choice required")
							}
							raw, _ = json.Marshal(list[0])
						}
					}
					bound = true
					break
				}
			}
			if !bound {
				actual, _ := s.resolve(expected)
				var a, b any
				if json.Unmarshal(raw, &a) != nil || json.Unmarshal(actual, &b) != nil {
					return nil, surfaceError("action value")
				}
				x, _ := json.Marshal(a)
				y, _ := json.Marshal(b)
				if string(x) != string(y) {
					return nil, surfaceError("constant action value")
				}
			}
			out[key] = slices.Clone(raw)
		}
		return out, nil
	}
	return nil, surfaceError("unknown action")
}

func (s *Surface) plainText() string {
	if s.Deleted {
		return "Card removed."
	}
	ordered, _ := s.ordered()
	var lines []string
	action := 0
	choice := 0
	value := func(raw json.RawMessage) string { text, _ := s.stringValue(raw); return text }
	for i := 0; i < len(ordered); i++ {
		c := ordered[i]
		switch c.Component {
		case "Text":
			lines = append(lines, value(c.Text))
		case "Divider":
			lines = append(lines, "---")
		case "Button":
			label := ""
			if i+1 < len(ordered) && ordered[i+1].ID == c.Child && ordered[i+1].Component == "Text" {
				i++
				label = value(ordered[i].Text)
			}
			lines = append(lines, fmt.Sprintf("[%c] %s", 'a'+action, label))
			action++
		case "ChoicePicker":
			if c.Label != nil {
				lines = append(lines, value(c.Label))
			}
			selected, _ := s.resolve(c.Value)
			var picks []string
			_ = json.Unmarshal(selected, &picks)
			for _, o := range c.Options {
				choice++
				mark := "○"
				if slices.Contains(picks, o.Value) {
					mark = "●"
				}
				lines = append(lines, fmt.Sprintf("[%d] %s %s", choice, mark, value(o.Label)))
			}
		case "CheckBox":
			raw, _ := s.resolve(c.Value)
			lines = append(lines, value(c.Label)+": "+string(raw))
		case "TextField", "DateTimeInput":
			label := c.Component
			if c.Label != nil {
				label = value(c.Label)
			}
			lines = append(lines, label+": "+value(c.Value))
		case "WorkRow":
			lines = append(lines, fmt.Sprintf("[%c] %s · %s · %s", 'a'+action, value(c.Title), c.State, value(c.Elapsed)))
			action++
		}
	}
	return strings.Join(lines, "\n")
}

// Empty containers retain their required children field when folded or stored.
func (c SurfaceComponent) MarshalJSON() ([]byte, error) {
	type plain SurfaceComponent
	raw, err := json.Marshal(plain(c))
	if err != nil {
		return nil, err
	}
	if c.Component != "Column" && c.Component != "Row" && c.Component != "List" {
		return raw, nil
	}
	var value map[string]json.RawMessage
	_ = json.Unmarshal(raw, &value)
	children := c.Children
	if children == nil {
		children = []string{}
	}
	value["children"], _ = json.Marshal(children)
	return json.Marshal(value)
}

// BoundValue returns a copy for a client's unsent form state. It performs no IO.
func (s *Surface) BoundValue(raw json.RawMessage) json.RawMessage {
	value, _ := s.resolve(raw)
	return slices.Clone(value)
}

// OrderedComponents exposes only reading order for a client's local form state.
func (s *Surface) OrderedComponents() []SurfaceComponent {
	components, _ := s.ordered()
	return components
}
