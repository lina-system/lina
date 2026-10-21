// SPDX-License-Identifier: Apache-2.0

package term

import tea "charm.land/bubbletea/v2"

// FromTea preserves explicit repeats/releases even before negotiation reports
// event support. A late capability reply must never turn a release into a press.
func FromTea(msg tea.KeyMsg, events bool) Key {
	k := msg.Key()
	key := Key{Name: k.Keystroke(), Event: Press, Events: events}
	if _, release := msg.(tea.KeyReleaseMsg); release {
		key.Event, key.Events = Release, true
	} else if k.IsRepeat {
		key.Event, key.Events = Repeat, true
	}
	return key
}
