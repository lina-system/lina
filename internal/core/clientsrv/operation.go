// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"context"

	"github.com/thisisjun786/lina/kit/protocol"
)

// Operation declares the capability of an installed service handler. Recovery
// admission is fixed by the protocol kind, independent of handler registration.
// Handlers must honor cancellation and must not retain the request payload.
type Operation struct {
	Capability protocol.Capability
	Handle     func(context.Context, Session, protocol.Envelope) (Result, error)
	Disconnect func(Session)
}

// Session contains connection facts established by Core. No request payload
// supplies its person, epoch or decider eligibility.
type Session struct {
	ConnectionID string
	Capabilities []protocol.Capability
	PersonID     string
	Epoch        uint64
	ClientID     string
	CanDecide    bool
}

// Result leaves envelope identity and correlation under the listener's control.
type Result struct {
	Kind    protocol.Kind
	Payload protocol.Payload
	Effect  protocol.EffectState
}

// OperationError carries only a closed protocol refusal, never handler detail.
type OperationError struct{ Code protocol.ErrorCode }

func (e *OperationError) Error() string { return string(e.Code) }

// Unlisted kinds fail closed as mutations. Reserved management payloads may
// contain writes; they cannot become reads merely by registering a handler.
var readOnlyKinds = map[protocol.Kind]bool{
	protocol.KindRestartGet: true,
	protocol.KindHistoryGet: true, protocol.KindSubscribe: true,
	protocol.KindResultGet: true, protocol.KindRunningGet: true,
	protocol.KindProgressGet: true, protocol.KindProgressDigestGet: true,
	protocol.KindWaitingGet: true, protocol.KindDetailsGet: true,
	protocol.KindAgentTalkGet: true, protocol.KindAgentTalkExpand: true, protocol.KindAgentTalkSubscribe: true,
}
