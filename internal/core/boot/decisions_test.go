// SPDX-License-Identifier: Apache-2.0
package boot

import "testing"

func TestDecisionsShareConversationAuthority(t *testing.T) {
	r := controlRuntime(t)
	if r.decisions == nil || r.personaAuthority == nil || r.decisions.Authority != r.personaAuthority {
		t.Fatal("decisions do not use the conversation authority instance")
	}
}
