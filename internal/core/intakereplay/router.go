// SPDX-License-Identifier: Apache-2.0
package intakereplay

import "github.com/thisisjun786/lina/internal/core/binding"

func Decide(router string, state State, input Input) []Binding {
	ids := make([]string, 0, len(state.Items))
	for _, i := range state.Items {
		ids = append(ids, i.ID)
	}
	target, decider := state.Focus, "focus"
	if router == "rules-then-focus" {
		if t, r := binding.Rule(state, input); r != "" {
			target, decider = t, r
		}
	}
	return []Binding{{Target: target, Decider: decider, Candidates: ids}}
}
