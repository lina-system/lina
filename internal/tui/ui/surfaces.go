// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"crypto/rand"
	"encoding/json"
	"maps"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/kit/protocol"
)

type surfacePager struct {
	id       string
	revision json.Number
	controls protocol.SurfaceControls
	values   map[string]json.RawMessage
	editing  int
	edit     string
	busy     bool
}

func (m *shellModel) openSurface(id, revision string, controls *protocol.SurfaceControls) {
	if !m.hasCapability("results") || controls == nil {
		return
	}
	m.conversation.pager.surface = &surfacePager{id: id, revision: json.Number(revision), controls: *controls, values: map[string]json.RawMessage{}, editing: -1}
}
func (m *shellModel) surfaceKey(msg tea.KeyMsg, name string) (bool, tea.Cmd) {
	p := m.conversation.pager.surface
	if p == nil || !m.mutationsAllowed() {
		return false, nil
	}
	fields := p.controls.Fields
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
			if field.Kind == "boolean" {
				value, err := strconv.ParseBool(p.edit)
				if err != nil {
					return true, nil
				}
				raw, _ = json.Marshal(value)
			}
			if field.Kind == "choice" {
				selected := []string{}
				for _, number := range strings.Split(p.edit, ",") {
					found := false
					for _, choice := range field.Choices {
						if choice.Key == strings.TrimSpace(number) {
							selected = append(selected, choice.Value)
							found = true
							break
						}
					}
					if !found {
						return true, nil
					}
				}
				raw, _ = json.Marshal(selected)
			}
			p.values[field.ID] = raw
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
	for _, field := range fields {
		for _, choice := range field.Choices {
			if choice.Key != name {
				continue
			}
			picks := []string{choice.Value}
			if field.Multiple {
				current := p.values[field.ID]
				if current == nil {
					current = field.Value
				}
				_ = json.Unmarshal(current, &picks)
				found := -1
				for i, v := range picks {
					if v == choice.Value {
						found = i
					}
				}
				if found >= 0 {
					picks = append(picks[:found], picks[found+1:]...)
				} else {
					picks = append(picks, choice.Value)
				}
			}
			p.values[field.ID], _ = json.Marshal(picks)
			return true, nil
		}
	}
	for _, action := range p.controls.Actions {
		if action.Key != name {
			continue
		}
		if p.busy {
			return true, nil
		}
		values := maps.Clone(action.Values)
		for key, field := range action.Bindings {
			if value, ok := p.values[field]; ok {
				values[key] = value
			}
		}
		request := protocol.SurfaceAction{ResultID: p.id, Revision: p.revision, Action: action.Name, Values: values, InputID: rand.Text()}
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
	fields := p.controls.Fields
	if p.editing >= 0 {
		return "\n\n" + sanitize.Display(fields[p.editing].Label) + ": " + sanitize.Display(p.edit) + "\nEnter · Esc"
	}
	if len(fields) == 0 {
		return ""
	}
	lines := []string{}
	for _, field := range fields {
		raw, ok := p.values[field.ID]
		if !ok {
			continue
		}
		value := string(raw)
		if field.Kind == "choice" {
			var picks []string
			_ = json.Unmarshal(raw, &picks)
			labels := []string{}
			for _, pick := range picks {
				for _, choice := range field.Choices {
					if choice.Value == pick {
						labels = append(labels, choice.Label)
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
		lines = append(lines, sanitize.Display(field.Label+": "+value))
	}
	lines = append(lines, "Ctrl+E: edit fields · Esc: close")
	return "\n\n" + strings.Join(lines, "\n")
}
