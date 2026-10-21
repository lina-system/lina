// SPDX-License-Identifier: Apache-2.0

// Package retract coordinates content removal with the durable removal list.
package retract

import "errors"

var (
	ErrInvalid  = errors.New("invalid_retraction")
	ErrJournal  = errors.New("invalid_removal_list")
	ErrRecovery = errors.New("retraction_recovery_required")
)

type Kind string

const (
	Memory Kind = "memory"
	Event  Kind = "event"
	Source Kind = "source"
)

// LegacyRef identifies an imported record without retaining its content.
type LegacyRef struct {
	System   string `json:"system"`
	Instance string `json:"instance"`
	Kind     string `json:"kind"`
	Key      string `json:"key"`
}

// Target retains only identity and provenance. The coordinator resolves these
// fields from canon before writing the removal list, never from model output.
type Target struct {
	Kind          Kind        `json:"kind"`
	ID            string      `json:"id"`
	Holder        string      `json:"holder,omitempty"`
	Agent         string      `json:"agent,omitempty"`
	Events        []string    `json:"events,omitempty"`
	Conversations []string    `json:"conversations,omitempty"`
	Legacy        []LegacyRef `json:"legacy,omitempty"`
}

type action string

const (
	forget     action = "forget"
	restore    action = "restore"
	erase      action = "erase"
	disconnect action = "disconnect"
)

type entry struct {
	Version int      `json:"version"`
	ID      string   `json:"id"`
	Action  action   `json:"action"`
	At      int64    `json:"at"`
	Person  string   `json:"person"`
	Targets []Target `json:"targets"`
}

func (e entry) valid() bool {
	if e.Version != 1 || e.ID == "" || e.At <= 0 || e.Person == "" || len(e.Targets) == 0 {
		return false
	}
	switch e.Action {
	case forget, restore, erase, disconnect:
	default:
		return false
	}
	seen := map[string]bool{}
	for _, t := range e.Targets {
		if t.ID == "" || seen[string(t.Kind)+"\x00"+t.ID] {
			return false
		}
		seen[string(t.Kind)+"\x00"+t.ID] = true
		switch t.Kind {
		case Memory:
			if t.Holder == "" {
				return false
			}
		case Event:
			if t.Agent == "" {
				return false
			}
		case Source:
			if e.Action != disconnect {
				return false
			}
		default:
			return false
		}
		if e.Action == disconnect && t.Kind != Source {
			return false
		}
		for _, id := range append(append([]string{}, t.Events...), t.Conversations...) {
			if id == "" {
				return false
			}
		}
		for _, ref := range t.Legacy {
			if ref.System == "" || ref.Instance == "" || ref.Kind == "" || ref.Key == "" {
				return false
			}
		}
	}
	return true
}
