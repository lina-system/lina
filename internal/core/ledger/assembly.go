// SPDX-License-Identifier: Apache-2.0
package ledger

import "github.com/thisisjun786/lina/kit/provider"

// Reference names immutable source evidence; it never contains a context body.
type Reference struct{ ID, Revision string }

type Assembly struct {
	AgentID, PersonaRevision string
	Skills, Items            []Reference
	History                  []Reference `json:",omitempty"`
}

// RequestEvidence records enough configuration to rebuild a request without
// persisting credentials or a transient ContextPacket.
type RequestEvidence struct {
	TurnID, Hash string
	Round        int
	Class        provider.Class
	Assembly     Assembly
	Model        provider.ModelRef   `json:",omitempty"`
	Effort       provider.Effort     `json:",omitempty"`
	Tools        []provider.ToolDecl `json:",omitempty"`
	Fields       provider.FieldList  `json:",omitempty"`
}
