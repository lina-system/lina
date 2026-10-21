// SPDX-License-Identifier: Apache-2.0
package budget

import (
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// Handoff crosses the turn/run flow boundary without a same-layer import.
// Items includes the original input, applied steering and verbatim tool outputs.
// Effects are receipts, never instructions to replay their calls.
type Handoff struct {
	Turn    string
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
