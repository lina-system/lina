// SPDX-License-Identifier: Apache-2.0
package free_text

import "github.com/thisisjun786/lina/kit/diag"

var fields = diag.Fields{
	Component:  "provider body",
	References: diag.References{AgentID: "secret", Epoch: "/home/example"},
	Cause:      diag.Cause{Code: "prompt", ParentEventID: "provider body"},
	Result:     "secret", NextAction: "prompt", ErrorClass: "provider body",
}
