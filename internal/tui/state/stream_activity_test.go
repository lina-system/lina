// SPDX-License-Identifier: Apache-2.0

package state

import (
	"reflect"
	"testing"
)

func TestStreamAdvanceRetainsContentAndChecksCursor(t *testing.T) {
	const topic = "conversation:fixture"
	base, _ := Reduce(State{}, Snapshot{Epoch: "2", Revocation: "1", Topic: topic, Cursor: 3, Items: []Block{{ID: "reply", Text: "answer", Done: true}}})
	for _, tt := range []struct {
		name   string
		epoch  string
		cursor uint64
		gap    bool
		want   uint64
	}{
		{"next", "2", 4, false, 4},
		{"duplicate", "2", 3, false, 3},
		{"older cursor", "2", 2, false, 3},
		{"older epoch", "1", 4, false, 3},
		{"invalid epoch", "oops", 4, false, 3},
		{"gap", "2", 5, true, 3},
		{"new epoch", "3", 4, true, 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, commands := Reduce(base, StreamAdvance{Epoch: tt.epoch, Topic: topic, Cursor: tt.cursor})
			if got := s.Topics[topic]; got.Cursor != tt.want || got.OutOfSync != tt.gap || (len(commands) != 0) != tt.gap {
				t.Fatalf("topic=%+v commands=%d", got, len(commands))
			}
			if !reflect.DeepEqual(s.Blocks, base.Blocks) || !reflect.DeepEqual(s.Activity, base.Activity) {
				t.Fatal("cursor-only event changed conversation/activity")
			}
			replayed, commands := Reduce(s, StreamAdvance{Epoch: tt.epoch, Topic: topic, Cursor: tt.cursor})
			if !reflect.DeepEqual(s, replayed) || len(commands) != 0 {
				t.Fatal("replay changed state or repeated resync")
			}
		})
	}
}
