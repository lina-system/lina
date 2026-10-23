// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/protocol"
)

// launcherThreadPrefix marks the one admission surface that carries a
// launcher line. The line itself is an ordinary person message in the agent's
// main conversation; the marker only routes the short reply to the panel.
const launcherThreadPrefix = "launcher:"

func launcherRequestID(surface ledger.Surface) string {
	if surface.Kind != "launcher" || !strings.HasPrefix(surface.Thread, launcherThreadPrefix) {
		return ""
	}
	return strings.TrimPrefix(surface.Thread, launcherThreadPrefix)
}

// Launcher resolves the last or named agent and admits the line through the
// existing conversation path. No launcher-only conversation exists.
func (s *conversationRouter) Launcher(ctx context.Context, session clientsrv.Session, p protocol.LauncherRequest) (protocol.LauncherResult, error) {
	if err := s.root.authorize(session, nil); err != nil {
		return protocol.LauncherResult{}, err
	}
	agent := ""
	if p.AgentID != nil {
		agent = *p.AgentID
	} else {
		selected, err := s.root.agents.Selected(ctx, session.PersonID)
		if errors.Is(err, agents.ErrNotFound) {
			selected, err = s.root.agents.Default(ctx)
		}
		if err != nil {
			return protocol.LauncherResult{}, managementError(err)
		}
		agent = selected.ID
	}
	view, err := s.view(ctx, session, agent)
	if err != nil {
		return protocol.LauncherResult{}, err
	}
	if _, err = view.launcherInput(ctx, session, p); err != nil {
		return protocol.LauncherResult{}, err
	}
	return protocol.LauncherResult{RequestID: p.RequestID, ConversationID: view.conversation, AgentID: view.agent}, nil
}

// launcherInput keeps the same durable input, deduplication and turn flow.
// The request id is the input id, so a repeated line admits once.
func (r *conversationRuntime) launcherInput(ctx context.Context, s clientsrv.Session, p protocol.LauncherRequest) (protocol.ItemDone, error) {
	thread := launcherThreadPrefix + p.RequestID
	input := protocol.Input{ConversationID: &r.conversation, InputID: &thread, Text: &p.Text, Thread: &thread}
	return r.admitInput(ctx, s, input, "launcher", nil)
}

// publishLauncherReply streams the visible short form, never the full reply.
// The panel replaces text on each event and opens message_id on done.
func (r *conversationRuntime) publishLauncherReply(requestID, key, text string, done bool) error {
	if requestID == "" || r.topics == nil {
		return nil
	}
	reply := protocol.LauncherReply{RequestID: requestID, ConversationID: r.conversation, AgentID: r.agent, Text: text, Done: done}
	if key != "" {
		reply.MessageID = &key
	}
	data, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	return r.topics.Publish("launcher", clientsrv.TopicEvent{Kind: protocol.KindLauncherReply, Key: requestID, Data: data, AgentID: r.agent})
}

var _ clientsrv.LauncherService = (*conversationRouter)(nil)
