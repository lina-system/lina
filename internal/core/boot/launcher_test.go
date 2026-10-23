// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/kit/protocol"
)

// A retry must keep the agent that first admitted the line. The row below has
// the source and native id of every client line, which is also the shape an
// installation admits launcher lines with, so an upgraded store keeps working.
func TestLauncherRetryKeepsTheAdmittingAgent(t *testing.T) {
	r, session, first, second := managementFixture(t)
	if _, err := inbox.New(r.db).Admit(t.Context(), inbox.Input{
		Key:  inbox.Key{Conversation: "main:" + second, Source: "client", NativeID: launcherThreadPrefix + "req-1"},
		Body: []byte("an earlier line"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.agents.Select(t.Context(), first, session.PersonID); err != nil {
		t.Fatal(err)
	}
	agent, err := r.launcherAgent(t.Context(), session, protocol.LauncherRequest{RequestID: "req-1", Text: "again"})
	if err != nil {
		t.Fatal(err)
	}
	if agent != second {
		t.Fatalf("retry moved to %q, want the agent that admitted it %q", agent, second)
	}
}

func TestLauncherResolvesTheNamedOrSelectedAgent(t *testing.T) {
	r, session, first, second := managementFixture(t)
	if err := r.agents.Select(t.Context(), second, session.PersonID); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		agent *string
		want  string
	}{
		{name: "selected", want: second},
		{name: "named", agent: &first, want: first},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent, err := r.launcherAgent(t.Context(), session, protocol.LauncherRequest{RequestID: "req-" + tc.name, Text: "line", AgentID: tc.agent})
			if err != nil {
				t.Fatal(err)
			}
			if agent != tc.want {
				t.Fatalf("agent = %q, want %q", agent, tc.want)
			}
		})
	}
}

// A client line cannot take a launcher request id. Sharing the durable inbox
// key with the launcher capability would let one shadow the other's admission.
func TestClientInputCannotShadowALauncherRequest(t *testing.T) {
	r, session, first, _ := managementFixture(t)
	conversation, id, text := "main:"+first, launcherThreadPrefix+"req-1", "line"
	_, err := r.Input(t.Context(), session, protocol.Input{ConversationID: &conversation, InputID: &id, Text: &text})
	var refused *clientsrv.OperationError
	if !errors.As(err, &refused) || refused.Code != protocol.ErrorMalformed {
		t.Fatalf("reserved input id was not refused: %v", err)
	}
}
