// SPDX-License-Identifier: Apache-2.0
package macos

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/thisisjun786/lina/internal/node/seat"
)

type source struct{ events []Notification }

func (s *source) Next(context.Context) (Notification, error) {
	if len(s.events) == 0 {
		return Notification{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func TestOSNotifications(t *testing.T) {
	s := &source{events: []Notification{{Kind: PhysicalInput, Session: "seat"}, {Kind: SyntheticInput, Session: "seat"}, {Kind: SessionLocked, Session: "seat"}, {Kind: SessionUnlocked, Session: "seat"}, {Kind: SystemResumed}}}
	var got []seat.Event
	err := Forward(t.Context(), s, func(e seat.Event) error { got = append(got, e); return nil })
	if !errors.Is(err, io.EOF) {
		t.Fatalf("source failure lost: %v", err)
	}
	want := []seat.Kind{seat.HumanInput, seat.Locked, seat.Unlocked, seat.Resumed, seat.EventsLost}
	if len(got) != len(want) {
		t.Fatalf("events: %+v", got)
	}
	for i, k := range want {
		if got[i].Kind != k {
			t.Fatalf("event %d: %+v", i, got[i])
		}
	}
}
func TestCancellationAndUnknownEventsFailClosed(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "cancelled"}[cancel], func(t *testing.T) {
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			if cancel {
				stop()
			}
			s := &source{events: []Notification{{Kind: "unknown"}}}
			var last seat.Kind
			err := Forward(ctx, s, func(e seat.Event) error { last = e.Kind; return nil })
			if err == nil || last != seat.EventsLost {
				t.Fatalf("not fail closed: %v %v", last, err)
			}
		})
	}
}
