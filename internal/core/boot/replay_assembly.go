// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"
	"strings"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/kit/provider"
)

func replayInstructions(format string, p agents.Preparation, tools []provider.ToolDecl) (string, error) {
	text, err := assemble.Instructions(p, tools)
	if err != nil {
		return "", err
	}
	switch format {
	case "agent-v1":
		return text, nil
	case "persona-tui-v1":
		return assemble.PersonaCoreV1 + "\n" + assemble.TUIFormatting + "\n\n" + text, nil
	case "persona-v1":
		return assemble.PersonaCoreV1 + "\n\n" + text, nil
	default:
		return "", replay.Exclusion("missing_assembly")
	}
}
func (r *conversationRuntime) replaySkill(ctx context.Context, id, commit string) (string, string, error) {
	body, err := r.skills.ReadCommit(ctx, id, commit)
	if errors.Is(err, skills.ErrNotFound) || errors.Is(err, skills.ErrCorrupt) {
		return "", "", replay.Exclusion("missing_assembly")
	}
	if err != nil {
		return "", "", err
	}
	return body.Version.Description, body.Files["SKILL.md"].Text, nil
}
func rebuildPersonaItems(ep replay.Episode, sources []replay.Context, profile []provider.Item) ([]provider.Item, error) {
	a := ep.Evidence.Assembly
	if len(ep.Items) == 0 {
		return nil, replay.Exclusion("missing_assembly")
	}
	var packet assemble.ContextPacket
	var catalog []assemble.Skill
	for _, source := range sources {
		ref := source.Source
		item := assemble.ContextItem{ID: ref.ID, Revision: ref.Revision, Text: source.Text}
		switch ref.Source {
		case "clock-v1":
			item.Kind = assemble.Clock
		case "skill-v1":
			item.Kind = assemble.SkillBody
			catalog = append(catalog, assemble.Skill{ID: strings.TrimPrefix(ref.ID, "skill:"), Description: source.Description})
		case "persona-state-v1":
			item.AgentID = a.AgentID
			item.Kind = assemble.Commitments
			if item.ID == "persona-emotion:"+a.AgentID+":"+ep.Conversation {
				item.Kind = assemble.Clock
			}
			if !personaStateItem(item, a.AgentID, ep.Conversation) {
				return nil, replay.Exclusion("missing_assembly")
			}
			if item.ID == "persona-settings:"+a.AgentID {
				item.Origin = "persona-record:" + a.AgentID
			}
			for _, e := range ref.Evidence {
				item.Evidence = append(item.Evidence, assemble.Ref{ID: e.ID, Revision: e.Revision})
			}
		case "relationship-v1":
			if source.Memory == nil {
				return nil, replay.Exclusion("missing_assembly")
			}
			item = relationshipContext(*source.Memory, a.AgentID)
		default:
			return nil, replay.Exclusion("missing_assembly")
		}
		packet = append(packet, item)
	}
	history := append(append([]provider.Item(nil), profile...), ep.Items[:len(ep.Items)-1]...)
	core := assemble.PersonaCoreV1
	if a.InstructionFormat == "persona-tui-v1" {
		core += "\n" + assemble.TUIFormatting
	}
	request, report := assemble.Assemble(assemble.Input{
		Core: core, AgentID: a.AgentID, PersonaRevision: a.PersonaRevision,
		AgentName: a.AgentName, OwnerPersonID: a.OwnerPersonID, Persona: ep.Persona,
		Catalog: catalog, AlwaysTools: ep.Evidence.Tools, History: history, Current: ep.Items[len(ep.Items)-1], Packet: packet,
		Model: ep.Evidence.Model, Effort: ep.Evidence.Effort, Fields: ep.Evidence.Fields, MaxTokens: 128000,
	}, conversationRequestSize)
	if report.Failure != "" || len(report.Dropped) != 0 {
		return nil, replay.Exclusion("missing_assembly")
	}
	return request.Items, nil
}
