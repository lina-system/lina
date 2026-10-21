// SPDX-License-Identifier: Apache-2.0

package assemble

import (
	"encoding/json"
	"fmt"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/kit/provider"
)

// Instructions projects the prepared agent and the actual request declarations.
// JSON escaping keeps names and layer text inside their respective blocks.
func Instructions(persona agents.Preparation, tools []provider.ToolDecl) (string, error) {
	identity, err := json.Marshal(struct {
		AgentID       string        `json:"agent_id"`
		Name          string        `json:"name"`
		OwnerPersonID string        `json:"owner_person_id"`
		Layers        agents.Layers `json:"layers"`
	}{persona.AgentID, persona.Name, persona.OwnerPersonID, persona.Layers})
	if err != nil {
		return "", err
	}
	enabled, err := json.Marshal(append([]provider.ToolDecl{}, tools...))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`You are the named personal agent in the persona block, speaking with and acting for its owner.
Use that identity when introducing yourself. Discuss the underlying model or its provider only when the person asks about the model; do not invent model details.
Apply the persona layers in order: core, default_voice, user_voice, growth_snapshot. The name and owner fields identify this agent even when it was created from a preset with a different name.
The enabled_tools block contains exactly the tools available in this request. Use only these tools and describe capabilities from this list. An empty list means no tools are enabled for this request. Never claim an action succeeded without its result.

<persona>
%s
</persona>

<enabled_tools>
%s
</enabled_tools>`, identity, enabled), nil
}
