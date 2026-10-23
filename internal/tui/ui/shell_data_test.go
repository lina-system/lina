// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestShellProjectFrameSeparatesProjectionFromReduction(t *testing.T) {
	now := time.Unix(100, 0)
	m := newShell(Options{Width: 80}, func() time.Time { return now })
	frame := state.Frame{Data: streamFixture(t, "normal/snapshot")}
	before := m.content
	projected, command, handled := m.projectFrame(frame)
	if handled || command != nil || projected == nil {
		t.Fatal("snapshot did not reach the reduction boundary")
	}
	snapshot, ok := projected.message.(state.Snapshot)
	if !ok || !snapshot.ClientTime.Equal(now) || projected.topic != "conversation:stream-fixture" {
		t.Fatalf("snapshot projection = %+v", projected)
	}
	if !reflect.DeepEqual(m.content, before) || len(m.blocks) != 0 {
		t.Fatal("projection reduced or rendered the snapshot")
	}
	m.applyData(frame)
	if len(m.content.Blocks) != 1 || m.content.Topics[projected.topic].Cursor != snapshot.Cursor {
		t.Fatal("data application did not reduce the projected snapshot")
	}
	block := m.content.Blocks[0]
	if cached := m.blocks[block.ID]; cached.stream == nil || cached.text != block.Text {
		t.Fatal("data application did not refresh the rendered block")
	}
}

func TestShellProjectFrameStopsForeignAndResyncFrames(t *testing.T) {
	m := newShell(Options{Width: 80}, time.Now)
	m.conversation.id = "selected"
	projected, command, handled := m.projectFrame(state.Frame{Data: streamFixture(t, "normal/snapshot")})
	if !handled || command != nil || projected != nil || len(m.content.Blocks) != 0 {
		t.Fatal("foreign conversation reached the reducer")
	}
	m.conversation.id = "stream-fixture"
	projected, command, handled = m.projectFrame(state.Frame{Data: streamFixture(t, "normal/resync_required")})
	if !handled || projected != nil || command == nil || m.status.State != Resyncing {
		t.Fatal("resync did not stop reduction and request a snapshot")
	}
	request := command().(shellSubscription)
	if request.Generation != m.generation || request.Request.Topic == nil || *request.Request.Topic != "conversation:stream-fixture" {
		t.Fatalf("resync subscription = %+v", request)
	}
}

func TestShellEffectCmdsDeferExecutionAndCaptureGeneration(t *testing.T) {
	m := newShell(Options{Width: 80}, time.Now)
	messages := []state.Msg{state.Notice{ID: "first"}, state.Notice{ID: "second"}}
	calls := 0
	var commands []state.Cmd
	for _, message := range messages {
		commands = append(commands, func(ctx context.Context) state.Msg {
			if err := ctx.Err(); err != nil {
				t.Fatal(err)
			}
			calls++
			return message
		})
	}
	effects := m.effectCmds(commands)
	if calls != 0 || len(effects) != len(messages) {
		t.Fatal("wrapping commands executed or lost an effect")
	}
	m.Update(shellBegin{Generation: 2})
	for i, command := range effects {
		effect, ok := command().(shellEffect)
		if !ok || effect.Generation != 1 || !reflect.DeepEqual(effect.Message, messages[i]) {
			t.Fatalf("effect %d = %+v", i, effect)
		}
	}
	if calls != len(messages) {
		t.Fatalf("executed %d effects, want %d", calls, len(messages))
	}
}

func TestShellDataCardReplyPrecedesConnectionError(t *testing.T) {
	m := cardModel(t)
	m.cardKey(term.Key{Name: "1", Events: true})
	answer, _ := m.cardKey(term.Key{Name: "enter", Events: true})
	id := "answer-request"
	m.cardRequests[id] = answer().(shellCardAnswer).Request
	var envelope protocol.Envelope
	if err := json.Unmarshal(streamFixture(t, "normal/snapshot"), &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Epoch = m.content.Epoch
	envelope.Kind = protocol.KindError
	envelope.ReplyTo = &id
	envelope.Error = &protocol.Error{Code: protocol.ErrorUnavailable}
	envelope.Payload = protocol.ErrorPayload{}
	_, command := m.Update(shellData{Generation: m.generation, Message: streamFrame(t, envelope)})
	if command != nil || m.status.State != Live || m.content.Cards[0].Outcome != state.CardRefused {
		t.Fatalf("card refusal changed connection: status=%+v card=%+v", m.status, m.content.Cards[0])
	}
	if _, pending := m.cardRequests[id]; pending {
		t.Fatal("card reply retained its pending request")
	}
}
