// SPDX-License-Identifier: Apache-2.0
package ledger

import "github.com/thisisjun786/lina/kit/provider"

// InputBeforeContextV1 identifies the layout used by new conversation requests.
// An absent RequestLayout denotes historical context-before-input assembly.
const InputBeforeContextV1 = "input-before-context-v1"

// Reference names immutable source evidence; it never contains a context body.
type Reference struct{ ID, Revision string }

type Assembly struct {
	AgentID, PersonaRevision string
	WorkMethodRevision       string `json:",omitempty"`
	InstructionFormat        string `json:",omitempty"`
	RequestLayout            string `json:",omitempty"`
	AgentName                string `json:",omitempty"`
	OwnerPersonID            string `json:",omitempty"`
	Skills, Items            []Reference
	History                  []Reference     `json:",omitempty"`
	Profile                  []Reference     `json:",omitempty"`
	Context                  []ContextSource `json:",omitempty"`
	PreparedAt               string          `json:",omitempty"`
}

// RequestEvidence records enough configuration to rebuild a request without
// persisting credentials or a transient ContextPacket.
type RequestEvidence struct {
	CacheIdentity string                  `json:",omitempty"`
	Layout        *provider.RequestLayout `json:",omitempty"`
	Capabilities  *provider.CapabilityUse `json:",omitempty"`
	TurnID, Hash  string
	Round         int
	Class         provider.Class
	Assembly      Assembly
	Model         provider.ModelRef   `json:",omitempty"`
	Effort        provider.Effort     `json:",omitempty"`
	Tools         []provider.ToolDecl `json:",omitempty"`
	Fields        provider.FieldList  `json:",omitempty"`
}

// ContextSource is a text-free source locator, not a stored ContextPacket.
// Material is permitted only for the four persona-owned state projections.
type ContextSource struct {
	Reference
	Source   string
	Material *Material   `json:",omitempty"`
	Evidence []Reference `json:",omitempty"`
}
