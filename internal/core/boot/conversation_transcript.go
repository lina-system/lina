// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/provider"
)

// Complete the conversation transcript from the durable loop journal, in the
// same round/item/call order used by loop.Run. Result callbacks can finish in
// any order, so their completion order must not become request history order.
// The caller admits this batch with successful turn completion and replies.
func conversationTranscript(ctx context.Context, db turn.Canon, turnID string, input ledger.Event, replies []ledger.Admission) ([]ledger.Admission, error) {
	type row struct {
		round, phase, index int
		item, call, result  []byte
	}
	var rows []row
	err := db.Read(ctx, func(tx *canon.Tx) error {
		records, err := tx.QueryContext(ctx, `
 SELECT round,0,ordinal,item,NULL,NULL FROM turn_items WHERE turn_id=?
 UNION ALL
 SELECT c.round,CASE WHEN EXISTS (
   SELECT 1 FROM turn_items i WHERE i.turn_id=c.turn_id AND i.round=c.round
   AND json_extract(i.item,'$.call_id')=c.call_id
 ) THEN 1 ELSE -1 END,c.ordinal,NULL,c.call,c.result FROM turn_calls c WHERE c.turn_id=?
 ORDER BY 1,2,3`, turnID, turnID)
		if err != nil {
			return err
		}
		defer records.Close()
		for records.Next() {
			var r row
			if err := records.Scan(&r.round, &r.phase, &r.index, &r.item, &r.call, &r.result); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		return records.Err()
	})
	if err != nil {
		return nil, err
	}
	byID := make(map[string]ledger.Admission, len(replies))
	for _, reply := range replies {
		byID[reply.Input.ID] = reply
	}
	now := time.Now()
	admission := func(id string, entry guard.Entry, body []byte, locator string) ledger.Admission {
		return ledger.Admission{Entry: entry, Input: ledger.Input{
			ID: id, ParentID: input.ID, ConversationID: input.ConversationID, AgentID: input.AgentID,
			Surface: input.Surface, OccurredAt: now, RecordedAt: now, TimeQuality: "observed",
			ScaffoldKind: "provider_item", Locator: locator, Body: string(body),
		}}
	}
	var batch []ledger.Admission
	calls := map[string]string{}
	for _, row := range rows {
		id := fmt.Sprintf("%s:%d:%d", turnID, row.round+1, row.index)
		if row.phase == 0 {
			if reply, ok := byID[id]; ok {
				batch = append(batch, reply)
				delete(byID, id)
				continue
			}
			var item struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
			}
			if err := json.Unmarshal(row.item, &item); err != nil {
				return nil, err
			}
			switch item.Type {
			case "reasoning", "function_call", "custom_tool_call":
				batch = append(batch, admission(id, guard.EntryAgent, row.item, ""))
				if item.CallID != "" {
					calls[item.CallID] = id
				}
			}
			continue
		}
		var call provider.ToolCall
		if err := json.Unmarshal(row.call, &call); err != nil {
			return nil, err
		}
		if len(row.result) == 0 || !json.Valid(row.result) {
			return nil, turn.ErrInvalid
		}
		callID, ok := calls[call.ID]
		if !ok {
			// Agent-runtime bridges report calls through the journal instead
			// of a model-API ItemDone. The query places these call/result
			// pairs before the runtime's reported response items.
			callID = "call:" + id
			item := map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": call.Arguments}
			if call.Custom {
				item["type"], item["input"] = "custom_tool_call", call.Arguments
				delete(item, "arguments")
			}
			raw, err := json.Marshal(item)
			if err != nil {
				return nil, err
			}
			batch = append(batch, admission(callID, guard.EntryAgent, raw, ""))
			calls[call.ID] = callID
		}
		kind := "function_call_output"
		if call.Custom {
			kind = "custom_tool_call_output"
		}
		// Match loop.Run's wire envelope, including its recorded result code.
		raw, err := json.Marshal(struct {
			Type   string `json:"type"`
			ID     string `json:"call_id"`
			Output string `json:"output"`
		}{kind, call.ID, string(row.result)})
		if err != nil {
			return nil, err
		}
		result := admission("output:"+id, guard.EntryTool, raw, "tool:"+call.Name+":"+call.ID)
		result.Input.Relations = []ledger.Relation{{TargetID: callID, Kind: "pairs_with"}}
		batch = append(batch, result)
	}
	// System handoff notices are not provider items.
	for _, reply := range replies {
		if _, ok := byID[reply.Input.ID]; ok {
			batch = append(batch, reply)
		}
	}
	return batch, nil
}
