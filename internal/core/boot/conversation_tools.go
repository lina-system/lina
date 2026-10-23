// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/reply"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// toolPreparation is local to one preparation. Persona tools also populate the
// catalog and packet used by that same request's assembly.
type toolPreparation struct {
	ctx         context.Context
	event       ledger.Event
	persona     agents.Preparation
	settings    agents.State
	at          time.Time
	replyShapes reply.Shapes
	questions   *questionTurn
	catalog     []assemble.Skill
	packet      assemble.ContextPacket
}

type toolPreparer func(string, *toolPreparation, map[string]loop.Tool) ([]provider.ToolDecl, error)

func (r *conversationRuntime) prepareRunTools(turnID string, _ *toolPreparation, bindings map[string]loop.Tool) ([]provider.ToolDecl, error) {
	if r.channelBinding == nil || r.channelBinding.Scope.Tools {
		bindings[runs.StartToolName] = r.background.service.StartTool(r.runOrigin(turnID))
		bindings["build_mock"] = r.buildMockTool(turnID)
	}
	return []provider.ToolDecl{startRunDeclaration(), buildMockDeclaration()}, nil
}

func (r *conversationRuntime) prepareDispatchTools(turnID string, _ *toolPreparation, bindings map[string]loop.Tool) ([]provider.ToolDecl, error) {
	if r.router.root.workerDispatch == nil {
		return nil, nil
	}
	if r.channelBinding == nil || r.channelBinding.Scope.Tools {
		bindings["dispatch_work"] = r.router.root.workerDispatch.Tool(dispatch.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: turnID})
	}
	return []provider.ToolDecl{r.router.root.workerDispatch.Declaration()}, nil
}
