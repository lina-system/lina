// SPDX-License-Identifier: Apache-2.0

package ui

import "github.com/thisisjun786/lina/kit/protocol"

type trackedReply struct {
	attempt uint64
	apply   func(uint64, protocol.Envelope)
}

// Request IDs are unique within a connection. A view owns the attempt guard;
// the tracker consumes its reply even when that view has since been closed.
type replyTracker map[string]trackedReply

func (t replyTracker) track(id string, attempt uint64, apply func(uint64, protocol.Envelope)) {
	t[id] = trackedReply{attempt: attempt, apply: apply}
}

func (t replyTracker) response(e protocol.Envelope) bool {
	if e.ReplyTo == nil {
		return false
	}
	reply, ok := t[*e.ReplyTo]
	if !ok {
		return false
	}
	delete(t, *e.ReplyTo)
	reply.apply(reply.attempt, e)
	return true
}
