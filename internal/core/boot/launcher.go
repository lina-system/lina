// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/inbox"
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
	agent, err := s.root.launcherAgent(ctx, session, p)
	if err != nil {
		return protocol.LauncherResult{}, err
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

// launcherAgent keeps a repeated request with the agent that first admitted it.
// Admission deduplication is per conversation, so resolving the retry from the
// person's current selection could admit the same line in a second agent. The
// lookup uses the same inbox source as every client line, so request ids
// admitted before this capability shipped stay retryable.
func (r *conversationRuntime) launcherAgent(ctx context.Context, session clientsrv.Session, p protocol.LauncherRequest) (string, error) {
	first, err := inbox.New(r.db).Find(ctx, "client", launcherThreadPrefix+p.RequestID)
	switch {
	case err == nil:
		return strings.TrimPrefix(first.Conversation, "main:"), nil
	case !errors.Is(err, inbox.ErrNotFound):
		return "", managementError(err)
	}
	if p.AgentID != nil {
		return *p.AgentID, nil
	}
	selected, err := r.agents.Selected(ctx, session.PersonID)
	if errors.Is(err, agents.ErrNotFound) {
		selected, err = r.agents.Default(ctx)
	}
	if err != nil {
		return "", managementError(err)
	}
	return selected.ID, nil
}

// launcherInput keeps the same durable input and deduplication. The request id
// is the input id, so a repeated line admits once. Queue mode is required: a
// steered line would be absorbed by a running turn that publishes no reply
// under this request id, and the panel would wait forever.
func (r *conversationRuntime) launcherInput(ctx context.Context, s clientsrv.Session, p protocol.LauncherRequest) (protocol.ItemDone, error) {
	thread := launcherThreadPrefix + p.RequestID
	mode := protocol.InputModeQueue
	input := protocol.Input{ConversationID: &r.conversation, InputID: &thread, Text: &p.Text, Thread: &thread, Mode: &mode}
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
