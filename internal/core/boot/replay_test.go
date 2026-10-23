// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestReplayExportRoutesAuthorizedAgent(t *testing.T) {
	r := controlRuntime(t)
	r.replaySocket = "/synthetic/core.sock"
	other, err := r.agents.Create(t.Context(), agents.Create{Name: "Other", PersonID: r.person, Preset: "lina"})
	if err != nil {
		t.Fatal(err)
	}
	conversation := "main:" + other.ID
	since, until := "2020-01-01T00:00:00Z", "2100-01-01T00:00:00Z"
	request := protocol.ExportRequest{AgentID: &other.ID, Conversations: &[]string{conversation}, Since: &since, Until: &until}
	owner := clientsrv.Session{PersonID: r.person}
	out, err := r.Export(t.Context(), owner, request)
	if err != nil || out.Directory == "" {
		t.Fatalf("authorized agent export refused: %+v %v", out, err)
	}
	view, err := r.router.view(t.Context(), owner, other.ID)
	if err != nil || view.replaySocket != r.replaySocket {
		t.Fatalf("routed replay lost its sandbox socket restriction: %v", err)
	}
	for _, tc := range []struct {
		name         string
		session      clientsrv.Session
		conversation string
		code         protocol.ErrorCode
	}{
		{"foreign owner", clientsrv.Session{PersonID: "foreign"}, conversation, protocol.ErrorNotAuthorized},
		{"foreign conversation", owner, r.conversation, protocol.ErrorOutOfScope},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request.Conversations = &[]string{tc.conversation}
			_, err := r.Export(t.Context(), tc.session, request)
			var denied *clientsrv.OperationError
			if !errors.As(err, &denied) || denied.Code != tc.code {
				t.Fatalf("scope check: %v", err)
			}
		})
	}
}
