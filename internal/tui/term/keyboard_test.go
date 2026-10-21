// SPDX-License-Identifier: Apache-2.0

package term

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestKeyboardAdapterCannotAdmitReleaseOrRepeatAsLegacyPress(t *testing.T) {
	for _, events := range []bool{false, true} {
		for _, msg := range []tea.KeyMsg{tea.KeyReleaseMsg{Code: 'q'}, tea.KeyPressMsg{Code: 'q', IsRepeat: true}} {
			key := FromTea(msg, events)
			var gate Gate
			if gate.Admit(key, Toggle, time.Time{}) {
				t.Fatal("quit release or repeat admitted")
			}
		}
		key := FromTea(tea.KeyPressMsg{Code: 'q'}, events)
		var gate Gate
		if key.Name != "q" || !gate.Admit(key, Toggle, time.Time{}) {
			t.Fatal("press lost")
		}
	}
}
