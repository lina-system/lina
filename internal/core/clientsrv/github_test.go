// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"context"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

type githubSpy struct{ calls int }

func (s *githubSpy) ObserveGitHub(context.Context, Session, protocol.WorkRequest) (protocol.WorkResult, error) {
	s.calls++
	return protocol.WorkResult{}, nil
}
func TestGitHubMergeAdmission(t *testing.T) {
	ptr := func(s string) *string { return &s }
	service := &githubSpy{}
	op := GitHubOperations(service)[protocol.KindWorkRequest]
	p := protocol.WorkRequest{Operation: ptr("github_merge"), BindingID: ptr("task")}
	e := protocol.Envelope{Kind: protocol.KindWorkRequest, Payload: p}
	if op.CapabilityFor(e) != GitHubMergeCapability || readOnlyEnvelope(e) {
		t.Fatal("merge admitted as observation")
	}
	if _, err := op.Handle(t.Context(), Session{PersonID: "person"}, e); err == nil || service.calls != 0 {
		t.Fatal("worker initiated merge")
	}
	if _, err := op.Handle(t.Context(), Session{PersonID: "person", CanDecide: true}, e); err != nil || service.calls != 1 {
		t.Fatal("eligible request refused")
	}
	p.Head = ptr("untrusted-head")
	e.Payload = p
	if _, err := op.Handle(t.Context(), Session{CanDecide: true}, e); err == nil || service.calls != 1 {
		t.Fatal("request supplied unaccepted head")
	}
	p = protocol.WorkRequest{Operation: ptr("github_read"), Repository: ptr("o/r"), PrNumber: ptr("1"), Clone: ptr("/untrusted")}
	e.Payload = p
	if _, err := op.Handle(t.Context(), Session{}, e); err == nil || service.calls != 1 {
		t.Fatal("read accepted mutation fields")
	}
}
