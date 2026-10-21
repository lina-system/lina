// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/compaction"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

func (r *conversationRuntime) replayReads(source replay.Source, ep replay.Episode) (map[string]loop.Tool, error) {
	registry := tools.NewRegistry(replay.ReadGate{}, r.activities)
	reads := map[string]loop.Tool{}
	add := func(spec tools.Spec, handler tools.Handler) error {
		if err := registry.Register(spec, handler); err != nil {
			return err
		}
		tool := turn.Tool(registry, spec, tools.Call{Auth: grant.Request{Person: r.person, Agent: r.agent}}, "replay:"+ep.TurnID)
		execute := tool.Execute
		tool.Execute = func(ctx context.Context, call provider.ToolCall) loop.Result {
			if source.Validate(ctx, ep.RevocationEpoch) != nil {
				return loop.Result{ID: call.ID, Code: loop.Denied}
			}
			result := execute(ctx, call)
			if source.Validate(ctx, ep.RevocationEpoch) != nil {
				return loop.Result{ID: call.ID, Code: loop.Denied}
			}
			return result
		}
		reads[spec.Name] = tool
		return nil
	}
	spec, handler := coreexec.ExpandTool(r.agent, func(ctx context.Context, agent, id string, offset, limit int) (json.RawMessage, error) {
		ids, next, err := compaction.New(r.db).ExpandEvents(ctx, agent, id, offset, limit)
		if errors.Is(err, sql.ErrNoRows) {
			ids = nil
			next = 0
			err = nil
			if offset == 0 {
				ids = []string{id}
			}
		}
		if err != nil {
			return nil, err
		}
		events := []replay.Event{}
		for _, id := range ids {
			e, err := source.ReadEvent(ctx, replay.Scope{Person: r.person, Agent: agent}, id)
			if err != nil {
				return nil, err
			}
			events = append(events, e)
		}
		return json.Marshal(struct {
			Events []replay.Event `json:"events"`
			Next   int            `json:"next"`
		}{events, next})
	})
	if err := add(spec, handler); err != nil {
		return nil, err
	}
	mem := coreexec.NewMemory(r.db, r.materials, nil)
	spec, handler = mem.RecallPersonTool(memory.RecallScope{Person: r.person, Agent: r.agent, ExternalModel: true}, func() int64 { return ep.Clock.Unix() })
	if err := add(spec, handler); err != nil {
		return nil, err
	}
	return reads, nil
}
