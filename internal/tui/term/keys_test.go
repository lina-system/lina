// SPDX-License-Identifier: Apache-2.0

package term

import (
	"testing"
	"time"
)

func TestAdmit(t *testing.T) {
	now := time.Unix(100, 0)
	for _, class := range []KeyClass{Toggle, Edit, Approve} {
		for _, event := range []KeyEvent{Press, Repeat, Release} {
			var gate Gate
			want := event == Press || event == Repeat && class == Edit
			if got := gate.Admit(Key{Event: event, Events: true}, class, now); got != want {
				t.Errorf("class=%d event=%d: got %v, want %v", class, event, got, want)
			}
		}
	}
	var gate Gate
	if gate.Admit(Key{Event: KeyEvent(255), Events: true}, Edit, now) || gate.Admit(Key{}, KeyClass(255), now) {
		t.Fatal("unknown event or action admitted")
	}
}

func TestLegacyApprovalCardBarrier(t *testing.T) {
	var gate Gate
	now := time.Unix(100, 0)
	gate.CardChanged(now)
	for _, elapsed := range []time.Duration{-time.Second, 0, ApprovalDeadTime - 1} {
		if gate.Admit(Key{}, Approve, now.Add(elapsed)) {
			t.Fatal("legacy approval admitted during card barrier")
		}
	}
	if !gate.Admit(Key{}, Approve, now.Add(ApprovalDeadTime)) {
		t.Fatal("legacy approval not restored at barrier boundary")
	}
	gate.CardChanged(now.Add(ApprovalDeadTime))
	if gate.Admit(Key{}, Approve, now.Add(ApprovalDeadTime)) {
		t.Fatal("new card did not reset barrier")
	}
	if !gate.Admit(Key{}, Edit, now) || !gate.Admit(Key{}, Toggle, now) || !gate.Admit(Key{Event: Press, Events: true}, Approve, now) {
		t.Fatal("barrier blocked an unrelated action or explicit press")
	}
}
