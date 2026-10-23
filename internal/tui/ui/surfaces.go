// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"crypto/rand"
	"encoding/json"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

type surfacePager struct {
	id       string
	revision json.Number
	card     *result.Surface
	values   map[string]json.RawMessage
	editing  int
	edit     string
	busy     bool
}

func surfacePath(raw json.RawMessage) string {
	var v struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.Path
}
func (m *shellModel) openSurface(id, revision string, raw json.RawMessage) {
	if !m.hasCapability("results") || raw == nil {
		return
	}
	var card result.Surface
	if json.Unmarshal(raw, &card) != nil || card.Deleted {
		return
	}
	m.conversation.pager.surface = &surfacePager{id: id, revision: json.Number(revision), card: &card, values: map[string]json.RawMessage{}, editing: -1}
}
func (p *surfacePager) fields() []result.SurfaceComponent {
	var fields []result.SurfaceComponent
	for _, c := range p.card.OrderedComponents() {
		if surfacePath(c.Value) != "" {
			fields = append(fields, c)
		}
	}
	return fields
}
func (m *shellModel) surfaceKey(msg tea.KeyMsg, name string) (bool, tea.Cmd) {
	p := m.conversation.pager.surface
	if p == nil || !m.mutationsAllowed() {
		return false, nil
	}
	fields := p.fields()
	if p.editing >= 0 {
		switch name {
		case "esc":
			p.editing = -1
			p.edit = ""
		case "backspace":
			text := []rune(p.edit)
			if len(text) > 0 {
				p.edit = string(text[:len(text)-1])
			}
		case "enter":
			field := fields[p.editing]
			raw, _ := json.Marshal(p.edit)
			if field.Component == "CheckBox" {
				v, err := strconv.ParseBool(p.edit)
				if err != nil {
					return true, nil
				}
				raw, _ = json.Marshal(v)
			}
			if field.Component == "ChoicePicker" {
				var selected []string
				for _, number := range strings.Split(p.edit, ",") {
					n, err := strconv.Atoi(strings.TrimSpace(number))
					if err != nil || n < 1 || n > len(field.Options) {
						return true, nil
					}
					selected = append(selected, field.Options[n-1].Value)
				}
				raw, _ = json.Marshal(selected)
			}
			p.values[surfacePath(field.Value)] = raw
			p.edit = ""
			p.editing++
			if p.editing >= len(fields) {
				p.editing = -1
			}
		default:
			if name != "ctrl+c" {
				p.edit += sanitize.Display(msg.Key().Text)
			}
		}
		return true, nil
	}
	if name == "ctrl+e" && len(fields) > 0 {
		p.editing = 0
		p.edit = ""
		return true, nil
	}
	if n, err := strconv.Atoi(name); err == nil && n > 0 {
		index := 0
		for _, c := range p.card.OrderedComponents() {
			if c.Component != "ChoicePicker" {
				continue
			}
			for _, option := range c.Options {
				index++
				if index != n {
					continue
				}
				path := surfacePath(c.Value)
				if path == "" {
					return true, nil
				}
				picks := []string{option.Value}
				if c.Variant == "multipleSelection" {
					current := p.values[path]
					if current == nil {
						current = p.card.BoundValue(c.Value)
					}
					_ = json.Unmarshal(current, &picks)
					found := -1
					for i, v := range picks {
						if v == option.Value {
							found = i
						}
					}
					if found >= 0 {
						picks = append(picks[:found], picks[found+1:]...)
					} else {
						picks = append(picks, option.Value)
					}
				}
				p.values[path], _ = json.Marshal(picks)
				return true, nil
			}
		}
	}
	for _, a := range p.card.Actions() {
		if a.Key != name {
			continue
		}
		if p.busy {
			return true, nil
		}
		for _, c := range p.card.OrderedComponents() {
			if c.Action == nil || c.Action.Event.Name != a.Name {
				continue
			}
			for key, binding := range c.Action.Event.Context {
				if value, ok := p.values[surfacePath(binding)]; ok {
					a.Values[key] = value
				}
			}
		}
		request := protocol.SurfaceAction{ResultID: p.id, Revision: p.revision, Action: a.Name, Values: a.Values, InputID: rand.Text()}
		p.busy = true
		generation := m.generation
		return true, func() tea.Msg { return shellManagement{Generation: generation, Request: request} }
	}
	return false, nil
}
func (p *surfacePager) status() string {
	if p == nil {
		return ""
	}
	if p.busy {
		return "\n\nSending…"
	}
	fields := p.fields()
	if p.editing >= 0 {
		field := fields[p.editing]
		label := field.Component
		_ = json.Unmarshal(p.card.BoundValue(field.Label), &label)
		return "\n\n" + sanitize.Display(label) + ": " + sanitize.Display(p.edit) + "\nEnter · Esc"
	}
	if len(fields) > 0 {
		lines := []string{}
		for _, field := range fields {
			raw, ok := p.values[surfacePath(field.Value)]
			if !ok {
				continue
			}
			label := field.Component
			_ = json.Unmarshal(p.card.BoundValue(field.Label), &label)
			value := string(raw)
			if field.Component == "ChoicePicker" {
				var picks []string
				_ = json.Unmarshal(raw, &picks)
				labels := []string{}
				for _, pick := range picks {
					for _, option := range field.Options {
						if option.Value == pick {
							var text string
							_ = json.Unmarshal(p.card.BoundValue(option.Label), &text)
							labels = append(labels, text)
						}
					}
				}
				value = strings.Join(labels, ", ")
			} else {
				var text string
				if json.Unmarshal(raw, &text) == nil {
					value = text
				}
			}
			lines = append(lines, sanitize.Display(label+": "+value))
		}
		lines = append(lines, "Ctrl+E: edit fields · Esc: close")
		return "\n\n" + strings.Join(lines, "\n")
	}
	return ""
}
