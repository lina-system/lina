// SPDX-License-Identifier: Apache-2.0
package budget

import (
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// Handoff crosses the turn/run flow boundary without a same-layer import.
// Input is the original durable inbox payload. Items includes applied steering
// and this turn's output items, never unrelated history or preparation context.
// Effects are receipts, never instructions to replay their calls.
type Handoff struct {
	Turn    string
	Input   string
	Items   []provider.Item
	Results []loop.Result
	Effects []Effect
}

type Effect struct {
	CallID, Key, Tool string
	Code              loop.Code
}

// Handle identifies continuing work; obtaining one never waits for its result.
type Handle struct {
	ID    string
	State string
}
