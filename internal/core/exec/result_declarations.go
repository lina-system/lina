// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"encoding/json"
	"github.com/thisisjun786/lina/kit/provider"
)

// Only previews and explicit reads are offered by the conversation. Saving
// requires a separate, exact person intent and is not implied by presentation.
func ResultDeclarations() []provider.ToolDecl {
	return []provider.ToolDecl{
		json.RawMessage(`{"type":"function","name":"present_result","description":"Present a complete typed result. summary is the verdict, shown before detail. Optional reply_type: status, comparison, candidates, third_party or chat. For candidates, one table row or comparison target is one artifact option. Bodies: table {columns:[{name,type:text|number|date|boolean,unit}],rows:[[values]],totals?:[values]}; comparison {targets:[{label,old,new,hunks:[{old_start,old_count,new_start,new_count}]}],apply:null}; document_draft {blocks:[{kind:paragraph|heading|list|table|code|quote|image|citation,...}],from_summary:null,recipient?:string}. Draft paragraph/quote blocks use text, heading uses text/level, list uses items, table uses table; optional audience is recipient or person. Other supported types: summary, diagram, chart, plan_view, file. Omit result_id/base_revision to create; include both to revise a preview. This does not send or save the result.","parameters":{"type":"object","properties":{"type":{"type":"string","enum":["summary","table","diagram","chart","document_draft","comparison","plan_view","file"]},"title":{"type":"string"},"summary":{"type":"string"},"body":{"type":"object"},"reply_type":{"type":"string","enum":["chat","status","comparison","candidates","third_party"]},"result_id":{"type":"string"},"base_revision":{"type":"integer"}},"required":["type","title","summary","body"],"additionalProperties":false}}`),
		json.RawMessage(`{"type":"function","name":"read_result","description":"Read the complete current result and its revision from this agent's conversation.","parameters":{"type":"object","properties":{"result_id":{"type":"string"}},"required":["result_id"],"additionalProperties":false}}`),
	}
}
