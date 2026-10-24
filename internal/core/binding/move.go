// SPDX-License-Identifier: Apache-2.0
package binding

import "sort"

// FreshFocusSeconds is the starting age at which the focus stops being the
// default binding target and becomes one candidate among the rest.
const FreshFocusSeconds = 3600

// Chip is the one-line note under a person's message. It shows when the final
// binding is not the fresh focus: it leaves the focus, returns to an aged focus
// or creates an item.
type Chip struct {
	Show   bool   `json:"show"`
	Name   string `json:"name,omitempty"`
	Target Target `json:"target"`
}

func Fresh(state State) bool {
	return state.Focus.Class == "existing" && state.FocusAgeSeconds < FreshFocusSeconds
}

// ChipFor decides the chip from the ranked snapshot and the final target.
func ChipFor(state State, target Target) Chip {
	chip := Chip{Target: target}
	if target.Class == "existing" && Fresh(state) && target.ID == state.Focus.ID {
		return chip
	}
	if target.Class == "none" && state.Focus.Class == "none" {
		return chip
	}
	chip.Show = true
	return chip
}

// Offer is one entry of the move list: another candidate, "new" or "none".
type Offer struct {
	Target Target `json:"target"`
	Name   string `json:"name"`
}

// Offers lists what a move may choose: every other ranked candidate, then a
// new item and no item. The current target is not offered.
func Offers(decision Decision, current Target) []Offer {
	var offers []Offer
	for _, item := range decision.Candidates {
		if item.ID == current.ID || !Open(item) {
			continue
		}
		offers = append(offers, Offer{Target: Target{Class: "existing", ID: item.ID, Kind: item.Kind}, Name: item.Title})
	}
	offers = append(offers,
		Offer{Target: Target{Class: "new", Kind: "todo", Parent: parentOf(current)}, Name: ""},
		Offer{Target: Target{Class: "none"}, Name: ""})
	return offers
}

// parentOf keeps a created item under the item the person was working in.
func parentOf(current Target) string {
	if current.Class == "existing" && current.Parent != "" {
		return current.Parent
	}
	return ""
}

// Offered reports whether target is one of offers; a matching class and kind
// and, for existing items, the same id.
func Offered(offers []Offer, target Target) bool {
	for _, offer := range offers {
		if offer.Target.Class != target.Class {
			continue
		}
		switch target.Class {
		case "none":
			return target == Target{Class: "none"}
		case "new":
			return target.Kind == "todo" && target.ID == ""
		case "existing":
			if offer.Target.ID == target.ID && offer.Target.Kind == target.Kind {
				return true
			}
		}
	}
	return false
}

// Effect kinds a move can re-attach. Anything else left the person.
const (
	EffectCreate = "create"
	EffectEdit   = "edit"
	EffectSteer  = "steer"
	EffectStart  = "start"
)

// Movable reports whether a move may re-attach this effect in principle.
func Movable(kind string) bool {
	switch kind {
	case EffectCreate, EffectEdit, EffectSteer:
		return true
	}
	return false
}

// Effect is one recorded thing the bound turn did, kept with the state a move
// needs to undo or repeat it.
type Effect struct {
	InputEvent     string `json:"input_event"`
	TurnID         string `json:"turn_id"`
	CallID         string `json:"call_id"`
	Ordinal        int    `json:"ordinal"`
	Kind           string `json:"kind"`
	Target         string `json:"target"`
	RecordID       string `json:"record_id,omitempty"`
	BeforeBody     []byte `json:"-"`
	BeforeRevision string `json:"before_revision,omitempty"`
	AfterRevision  string `json:"after_revision,omitempty"`
	Payload        []byte `json:"payload"`
	LeftPerson     bool   `json:"left_person,omitempty"`
}

// Outcome is what a move did with one effect.
type Outcome struct {
	Kind    string `json:"kind"`
	Item    string `json:"item"`
	Moved   bool   `json:"moved"`
	Reason  string `json:"reason,omitempty"`
	Receipt string `json:"receipt,omitempty"`
}

// Move is the durable record of one rebinding.
type Move struct {
	ID         string    `json:"id"`
	InputEvent string    `json:"input_event"`
	Person     string    `json:"person"`
	Agent      string    `json:"agent"`
	Decider    string    `json:"decider"`
	From       Target    `json:"from"`
	To         Target    `json:"to"`
	Effects    []Outcome `json:"effects"`
	State      State     `json:"state"`
	Candidates []Item    `json:"candidates,omitempty"`
	At         string    `json:"at"`
	Input      Input     `json:"input"`
}

// Count is one decider's binding and move totals.
type Count struct {
	Decider    string `json:"decider"`
	Bindings   int    `json:"bindings"`
	Moves      int    `json:"moves"`
	PerHundred int    `json:"per_hundred"`
}

// Stats counts moves per hundred bindings, overall and by who decided.
type Stats struct {
	Bindings   int     `json:"bindings"`
	Moves      int     `json:"moves"`
	PerHundred int     `json:"per_hundred"`
	Deciders   []Count `json:"deciders"`
}

// Tally combines per-decider bindings and moves into rates per hundred.
func Tally(bindings, moves map[string]int) Stats {
	deciders := make([]string, 0, len(bindings))
	for decider := range bindings {
		deciders = append(deciders, decider)
	}
	for decider := range moves {
		if _, ok := bindings[decider]; !ok {
			deciders = append(deciders, decider)
		}
	}
	sort.Strings(deciders)
	stats := Stats{Deciders: []Count{}}
	for _, decider := range deciders {
		c := Count{Decider: decider, Bindings: bindings[decider], Moves: moves[decider]}
		if c.Bindings > 0 {
			c.PerHundred = c.Moves * 100 / c.Bindings
		}
		stats.Bindings += c.Bindings
		stats.Moves += c.Moves
		stats.Deciders = append(stats.Deciders, c)
	}
	if stats.Bindings > 0 {
		stats.PerHundred = stats.Moves * 100 / stats.Bindings
	}
	return stats
}
