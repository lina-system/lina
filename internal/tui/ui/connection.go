// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

// ConnectionStatus holds display metadata from the connection owner. It never
// infers recovery from diagnostic text or work status from conversation text.
type ConnectionStatus struct {
	State   Connection
	Cause   protocol.ErrorCode
	Attempt uint64
	NextTry time.Duration
}

func (s Styles) presentation(status ConnectionStatus) connectionPresentation {
	v := localizedConnectionView(status.State, s.tokens, s.options.ASCII, s.options.Language)
	if status.Cause != "" {
		// A known presentation id is not automatically a valid wire cause.
		code := string(status.Cause)
		switch status.Cause {
		case protocol.ErrorUnsupportedCombination, protocol.ErrorCapabilityNotNegotiated,
			protocol.ErrorMalformed, protocol.ErrorStaleEpoch, protocol.ErrorStaleRevision,
			protocol.ErrorNotAuthorized, protocol.ErrorOutOfScope, protocol.ErrorDuplicate,
			protocol.ErrorUnavailable:
		default:
			code = "unknown"
		}
		v.Detail = design.Text(s.options.Language, code) + " (" + code + "). " + v.Detail
	}
	if status.State == Retrying {
		v.Detail += " " + design.Text(s.options.Language, "connection.attempt") + " " + strconv.FormatUint(status.Attempt, 10)
		v.Detail += "; " + design.Text(s.options.Language, "connection.next_try") + " " + max(0, status.NextTry).String()
	}
	return v
}
