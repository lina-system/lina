// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"encoding/json"

	"github.com/thisisjun786/lina/kit/provider"
)

// SkillDeclarations describes the existing registered operations. Identity and
// authority are supplied by Core, never by skill text or provider arguments.
func SkillDeclarations() []provider.ToolDecl {
	return []provider.ToolDecl{
		json.RawMessage(`{"type":"function","name":"skill_notices","description":"Read background skill maintenance notices with their one-action undo_skill handles. A merge undo restores every member together.","parameters":{"type":"object","properties":{},"additionalProperties":false}}`),
		json.RawMessage(`{"type":"function","name":"skill_maintenance","description":"Read the main skill maintenance job status, schedule and budget. Set enabled only when the person asks to turn skill suggestions on or off; disabling stops automatic skill writes and background maintenance, but permits undo.","parameters":{"type":"object","properties":{"enabled":{"type":"boolean"}},"additionalProperties":false}}`),
		json.RawMessage(`{"type":"function","name":"skill_index","description":"List the common skill catalog, including descriptions and immutable version handles, without loading bodies. Use read_skill to load a needed procedure.","parameters":{"type":"object","properties":{},"additionalProperties":false}}`),
		json.RawMessage(`{"type":"function","name":"read_skill","description":"Read a skill's files using the complete immutable version handle returned by skill_index or write_skill.","parameters":{"type":"object","properties":{"id":{"type":"string"},"name":{"type":"string"},"description":{"type":"string"},"revision":{"type":"integer","minimum":0},"commit":{"type":"string"}},"required":["id","name","description","revision","commit"],"additionalProperties":false}}`),
		json.RawMessage(`{"type":"function","name":"write_skill","description":"Create or patch instruction-only skill files. Supply the current revision as expected (0 for a new or unmodified built-in skill). SKILL.md needs name matching id and a one-line description in front matter. Executable code, tools, connections and permissions require explicit acceptance and are not adopted. A committed receipt contains the notice Core presents and an undo handle. Read the updated procedure on a subsequent turn.","parameters":{"type":"object","properties":{"id":{"type":"string"},"expected":{"type":"integer","minimum":0},"files":{"type":"object","additionalProperties":{"type":"object","properties":{"text":{"type":"string"},"executable":{"type":"boolean"}},"required":["text"],"additionalProperties":false}}},"required":["id","expected","files"],"additionalProperties":false}}`),
		json.RawMessage(`{"type":"function","name":"undo_skill","description":"Undo a skill change using its committed undo handle. The revision must still be current. Undoing creation removes the skill from the active catalog; the undone content cannot be automatically adopted again.","parameters":{"type":"object","properties":{"id":{"type":"string"},"revision":{"type":"integer","minimum":1}},"required":["id","revision"],"additionalProperties":false}}`),
	}
}
