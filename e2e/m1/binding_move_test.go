// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/protocol"
)

// A moved binding re-attaches the edit the turn made, counts the move by
// decider and exports its replay case. Every check is repeated after a real
// Core restart.
func TestM2BindingMoveInstalledCore(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var plants, lamps, plantsRevision string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		switch calls.Add(1) {
		case 1:
			emitPersonaCall(w, "plants", "write_record", map[string]any{"id": "", "revision": "", "kind": "todo", "title": "Plants", "note": "Water them."})
		case 2:
			var result struct{ ID string }
			_ = json.Unmarshal(assertAskResult(t, request.Input, "plants", "ok", ""), &result)
			plants = result.ID
			emitPersonaCall(w, "lamps", "write_record", map[string]any{"id": "", "revision": "", "kind": "todo", "title": "Lamps", "note": "Replace the bulbs."})
		case 3:
			var result struct{ ID string }
			_ = json.Unmarshal(assertAskResult(t, request.Input, "lamps", "ok", ""), &result)
			lamps = result.ID
			emitResponse(w, "Saved both to-dos.", true)
		case 4:
			emitPersonaCall(w, "read", "read_record", map[string]any{"id": plants})
		case 5:
			var result struct{ Revision string }
			_ = json.Unmarshal(assertAskResult(t, request.Input, "read", "ok", ""), &result)
			plantsRevision = result.Revision
			if plantsRevision == "" {
				t.Error("plants revision missing")
			}
			emitPersonaCall(w, "edit", "write_record", map[string]any{"id": plants, "revision": plantsRevision, "kind": "todo", "title": "Plants", "note": "Water them daily."})
		case 6:
			assertAskResult(t, request.Input, "edit", "ok", "")
			emitResponse(w, "Updated the to-do.", true)
		default:
			t.Error("unexpected model round")
			emitResponse(w, "Finished.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	defer func() { h.stopCore(core) }()
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "create", "Add two to-dos for the house.")
	if plants == "" || lamps == "" {
		t.Fatal("to-dos were not created")
	}
	frames := admittedTurn(t, h, c, conversation, "edit", "Plants need water.")
	var input string
	for _, frame := range frames {
		if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Binding != nil {
			if item.Binding.Show && item.Binding.Name == "Plants" {
				input = item.Binding.InputID
			}
		}
	}
	if input == "" {
		t.Fatalf("the person's message has no chip")
	}
	key := "get"
	id, err := c.Binding(h.ctx, protocol.BindingGet{InputID: input})
	if err != nil {
		t.Fatal(err)
	}
	var list protocol.Binding
	for list.InputID == "" {
		frame, err := c.Receive(h.ctx)
		if err != nil || frame.Error != nil {
			t.Fatalf("binding read: %+v %v", frame, err)
		}
		if frame.ReplyTo != nil && *frame.ReplyTo == id {
			value, ok := frame.Payload.(protocol.Binding)
			if !ok {
				t.Fatalf("wrong binding payload: %T", frame.Payload)
			}
			list = value
		}
	}
	_ = key
	if list.Target.ID == nil || *list.Target.ID != plants {
		t.Fatalf("binding target = %+v", list.Target)
	}
	var offer *protocol.BindingCandidate
	for i := range list.Candidates {
		if list.Candidates[i].ID == lamps {
			offer = &list.Candidates[i]
		}
	}
	if offer == nil {
		t.Fatalf("the other to-do is not offered: %+v", list.Candidates)
	}
	moveID, err := c.BindingMove(h.ctx, protocol.BindingMove{InputID: input, Target: protocol.BindingTarget{Class: "existing", ID: &offer.ID, Kind: &offer.Kind}})
	if err != nil {
		t.Fatal(err)
	}
	var move protocol.BindingMoveResult
	for move.MoveID == "" {
		frame, err := c.Receive(h.ctx)
		if err != nil || frame.Error != nil {
			t.Fatalf("move: %+v %v", frame, err)
		}
		if frame.ReplyTo != nil && *frame.ReplyTo == moveID {
			value, ok := frame.Payload.(protocol.BindingMoveResult)
			if !ok {
				t.Fatalf("wrong move payload: %T", frame.Payload)
			}
			move = value
		}
	}
	if len(move.Effects) != 1 || !move.Effects[0].Moved || move.Effects[0].Kind != "edit" || move.Effects[0].Receipt == nil || *move.Effects[0].Receipt != "restored+applied" {
		t.Fatalf("move effects = %+v", move.Effects)
	}
	readNotes := func() (string, string) {
		t.Helper()
		db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		notes := map[string]string{}
		rows, err := db.QueryContext(h.ctx, "SELECT id,note FROM records_text")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, note string
			if err = rows.Scan(&id, &note); err != nil {
				t.Fatal(err)
			}
			notes[id] = note
		}
		return notes[plants], notes[lamps]
	}
	plantsNote, lampsNote := readNotes()
	if strings.TrimSpace(plantsNote) != "Water them." {
		t.Errorf("wrong item was not restored: %q", plantsNote)
	}
	if strings.TrimSpace(lampsNote) != "Water them daily." {
		t.Errorf("right item did not receive the edit: %q", lampsNote)
	}
	checkStats := func(c *clientapi.Client) {
		t.Helper()
		id, err := c.BindingStats(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		for {
			frame, err := c.Receive(h.ctx)
			if err != nil || frame.Error != nil {
				t.Fatalf("stats: %+v %v", frame, err)
			}
			if frame.ReplyTo == nil || *frame.ReplyTo != id {
				continue
			}
			stats, ok := frame.Payload.(protocol.BindingStats)
			if !ok {
				t.Fatalf("wrong stats payload: %T", frame.Payload)
			}
			if string(stats.Moves) != "1" || string(stats.Bindings) != "2" || string(stats.PerHundred) != "50" {
				t.Fatalf("stats = %s bindings %s moves %s per 100", stats.Bindings, stats.Moves, stats.PerHundred)
			}
			found := false
			for _, decider := range stats.Deciders {
				if decider.Decider == move.Decider && string(decider.Moves) == "1" {
					found = true
				}
			}
			if !found {
				t.Fatalf("move is not counted by its decider %q: %+v", move.Decider, stats.Deciders)
			}
			return
		}
	}
	exportCases := func(c *clientapi.Client) int {
		t.Helper()
		id, err := c.BindingExport(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		for {
			frame, err := c.Receive(h.ctx)
			if err != nil || frame.Error != nil {
				t.Fatalf("export: %+v %v", frame, err)
			}
			if frame.ReplyTo == nil || *frame.ReplyTo != id {
				continue
			}
			result, ok := frame.Payload.(protocol.BindingExportResult)
			if !ok {
				t.Fatalf("wrong export payload: %T", frame.Payload)
			}
			if result.Directory == "" {
				t.Fatal("export returned no directory")
			}
			cases, err := strconv.Atoi(string(result.Cases))
			if err != nil {
				t.Fatal(err)
			}
			return cases
		}
	}
	checkStats(c)
	if count := exportCases(c); count != 1 {
		t.Fatalf("exported cases = %d", count)
	}
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h)
	defer c.Close()
	checkStats(c)
	if count := exportCases(c); count != 1 {
		t.Fatalf("exported cases after restart = %d", count)
	}
	files, err := filepath.Glob(filepath.Join(h.root, "derived", "intake-cases", "binding-cases-*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no exported case file: %v", err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var replay struct {
		Version int    `json:"version"`
		Set     string `json:"set"`
		Gold    []struct {
			Target struct {
				ID string `json:"id"`
			} `json:"target"`
		} `json:"gold"`
	}
	if err = json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &replay); err != nil {
		t.Fatal(err)
	}
	if replay.Version != 1 || replay.Set != "hard" || len(replay.Gold) != 1 || replay.Gold[0].Target.ID != lamps {
		t.Fatalf("replay case = %s", raw)
	}
}
