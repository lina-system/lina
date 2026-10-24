// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

// moveRuntime opens one conversation runtime on an existing root so a test can
// restart Core over the same canon.
func moveRuntime(t *testing.T, root string) (*conversationRuntime, *ctlfiles.Store) {
	t.Helper()
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	r, err := startConversation(t.Context(), root, "test-main", 1, ctl, nil, lifecycle.NewRegistry(nil), "owner", nil, nil, "")
	if err != nil {
		ctl.Close()
		t.Fatal(err)
	}
	return r, ctl
}

func fixtureRecord(t *testing.T, r *conversationRuntime, id, kind, state, belongs, title, note string) {
	t.Helper()
	front := fmt.Sprintf("---\nid: %q\nkind: %s\nstate: %s\nperson_id: %q\nagent_id: %q", id, kind, state, r.person, r.agent)
	if belongs != "" {
		front += fmt.Sprintf("\nbelongs_to: [%q]", belongs)
	}
	data := front + "\n---\n\n# " + title + "\n\n" + note + "\n"
	if _, err := r.records.Commit(t.Context(), records.Change{ID: id, Path: "inbox/" + id + ".md", Data: []byte(data)}, records.Actor{Person: r.person, Agent: r.agent}); err != nil {
		t.Fatal(err)
	}
}

// admitForBinding admits one person input and returns its event id.
func admitForBinding(t *testing.T, r *conversationRuntime, inputID, text string) string {
	t.Helper()
	r.intakeReplay = func(context.Context, ledger.Event) error { return nil }
	if _, err := r.Input(t.Context(), clientsrv.Session{PersonID: r.person, Epoch: r.epoch}, protocol.Input{InputID: &inputID, Text: &text}); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT id FROM conv_event WHERE speaker_kind='person' AND json_extract(origin,'$.locator')=? ORDER BY rowid DESC LIMIT 1", inputID).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func recordEffect(t *testing.T, r *conversationRuntime, input string, name, arguments string, before *effectBefore, output any) {
	t.Helper()
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	call := provider.ToolCall{ID: fmt.Sprintf("call-%d", time.Now().UnixNano()), Name: name, Arguments: arguments}
	if err := r.recordBindingEffect(t.Context(), ledger.Event{ID: input}, "turn:"+input, call, before, loop.Result{ID: call.ID, Code: loop.OK, Output: raw}); err != nil {
		t.Fatal(err)
	}
}

func readRecord(t *testing.T, r *conversationRuntime, id string) (string, string) {
	t.Helper()
	record, _, err := r.records.Read(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	kind, title, note, _, _ := recordParts(record.Data)
	_ = kind
	todo, _, _, _ := records.ReadTodo(record.Data)
	belongs := ""
	if todo != nil && len(todo.BelongsTo) > 0 {
		belongs = todo.BelongsTo[0]
	}
	return title + "|" + note, belongs
}

func TestBindingMoveReattachesRecordEffects(t *testing.T) {
	root, err := os.MkdirTemp("", "lina-move-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	r, ctl := moveRuntime(t, root)
	r.intakeReplay = nil
	fixtureRecord(t, r, "garden", "project", "active", "", "Garden", "Plan the beds.")
	fixtureRecord(t, r, "hallway", "project", "active", "", "Hallway", "Paint the walls.")
	fixtureRecord(t, r, "plants", "todo", "open", "garden", "Plants", "Water them.")
	fixtureRecord(t, r, "lamps", "todo", "open", "garden", "Lamps", "Replace the bulbs.")

	input := admitForBinding(t, r, "one", "Plants")
	// A created to-do records its own receipt; the turn that created it is the
	// turn of this input.
	receipt, err := coreexec.WriteRecord(t.Context(), r.records, records.Actor{Person: r.person, Agent: r.agent}, coreexec.RecordWrite{Kind: "todo", Title: "Seedlings", Note: "Sow them.", Todo: &records.Todo{State: "open", BelongsTo: []string{"garden"}}})
	if err != nil {
		t.Fatal(err)
	}
	recordEffect(t, r, input, "write_record", `{"id":"","revision":"","kind":"todo","title":"Seedlings","note":"Sow them."}`, nil, map[string]string{"id": receipt.ID, "revision": receipt.Revision})
	// Work that started elsewhere cannot be taken back.
	recordEffect(t, r, input, "start_run", `{}`, nil, map[string]string{"id": "run-analysis"})
	if _, belongs := readRecord(t, r, receipt.ID); belongs != "garden" {
		t.Fatalf("created to-do belongs to %q before the move", belongs)
	}
	head, err := r.records.Head(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	move, err := r.moveBinding(t.Context(), input, binding.Target{Class: "existing", ID: "hallway", Kind: "project"}, "move-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, belongs := readRecord(t, r, receipt.ID); belongs != "hallway" {
		t.Errorf("created to-do belongs to %q after the move", belongs)
	}
	after, err := r.records.Head(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after == head {
		t.Error("the move saved no version")
	}
	// Re-applying the same effect is not a second saved version.
	effects := []binding.Effect{{Kind: binding.EffectCreate, RecordID: receipt.ID}}
	outcome, _, err := r.applyBindingEffect(t.Context(), move.ID, effects[0], binding.Target{Class: "existing", ID: "hallway", Kind: "project"})
	if err != nil || !outcome.Moved {
		t.Fatalf("repeat apply: %+v %v", outcome, err)
	}
	repeated, err := r.records.Head(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if repeated != after {
		t.Error("the same move saved a second version")
	}
	var left, created bool
	for _, effect := range move.Effects {
		switch effect.Kind {
		case binding.EffectCreate:
			created = effect.Moved
		case binding.EffectStart:
			left = !effect.Moved && effect.Reason == "left"
		}
	}
	if !created {
		t.Errorf("created to-do was not moved: %+v", move.Effects)
	}
	if !left {
		t.Errorf("started work was not listed as not moved: %+v", move.Effects)
	}

	// A field edit is restored on the wrong item and applied to the right one.
	editInput := admitForBinding(t, r, "two", "Plants")
	beforeRecord, beforeRevision, err := r.records.Read(t.Context(), "plants")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coreexec.WriteRecord(t.Context(), r.records, records.Actor{Person: r.person, Agent: r.agent}, coreexec.RecordWrite{ID: "plants", Revision: beforeRevision, Kind: "todo", Title: "Plants", Note: "Water them daily.", Todo: &records.Todo{State: "open", BelongsTo: []string{"garden"}}}); err != nil {
		t.Fatal(err)
	}
	edited, _, err := r.records.Read(t.Context(), "plants")
	if err != nil {
		t.Fatal(err)
	}
	_, _, editedNote, _, _ := recordParts(edited.Data)
	_, editedRevision, err := r.records.Read(t.Context(), "plants")
	if err != nil {
		t.Fatal(err)
	}
	recordEffect(t, r, editInput, "write_record", `{"id":"plants","revision":"`+beforeRevision+`","kind":"todo","title":"Plants","note":"Water them daily."}`, &effectBefore{body: beforeRecord.Data, revision: beforeRevision}, map[string]string{"id": "plants", "revision": editedRevision})
	editMove, err := r.moveBinding(t.Context(), editInput, binding.Target{Class: "existing", ID: "lamps", Kind: "todo"}, "edit-key")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := readRecord(t, r, "plants"); got != "Plants|Water them." {
		t.Errorf("wrong item was not restored: %q", got)
	}
	if got, _ := readRecord(t, r, "lamps"); got != "Lamps|Water them daily." {
		t.Errorf("right item did not receive the edit: %q (was %q)", got, editedNote)
	}
	if len(editMove.Effects) != 1 || !editMove.Effects[0].Moved || editMove.Effects[0].Receipt != "restored+applied" {
		t.Errorf("edit outcome: %+v", editMove.Effects)
	}

	// The counter and the replay case survive a restart.
	stats, err := r.bindingStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Bindings != 2 || stats.Moves != 2 || stats.PerHundred != 100 {
		t.Errorf("stats before restart: %+v", stats)
	}
	dir, cases, err := r.exportBindingCases(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cases != 2 {
		t.Errorf("exported cases = %d", cases)
	}
	assertExport(t, dir, 2)
	if err = r.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = ctl.Close()
	restarted, restartedCtl := moveRuntime(t, root)
	defer func() { _ = restarted.Close(context.Background()); _ = restartedCtl.Close() }()
	stats, err = restarted.bindingStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Moves != 2 || stats.PerHundred != 100 {
		t.Errorf("stats after restart: %+v", stats)
	}
	dir, cases, err = restarted.exportBindingCases(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cases != 2 {
		t.Errorf("exported cases after restart = %d", cases)
	}
	assertExport(t, dir, 2)
}

func assertExport(t *testing.T, dir string, want int) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "binding-cases-*.jsonl"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no exported case file in %s: %v", dir, err)
	}
	raw, err := os.ReadFile(matches[len(matches)-1])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != want {
		t.Fatalf("exported lines = %d, want %d", len(lines), want)
	}
	for _, line := range lines {
		var c struct {
			Version int    `json:"version"`
			ID      string `json:"id"`
		}
		if json.Unmarshal([]byte(line), &c) != nil || c.Version != 1 || c.ID == "" {
			t.Fatalf("invalid replay case: %s", line)
		}
	}
}

func seedRun(t *testing.T, r *conversationRuntime, id, goal string) {
	t.Helper()
	record := runs.Record{ID: id, Origin: runs.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Epoch: r.epoch}, Brief: runs.Brief{Goal: goal}, State: runs.Waiting, Created: time.Now().UnixNano(), Updated: time.Now().UnixNano()}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, e := tx.ExecContext(t.Context(), "INSERT INTO background_runs(id,record) VALUES(?,?)", id, raw)
		return e
	}); err != nil {
		t.Fatal(err)
	}
}

func runFollowKeys(t *testing.T, r *conversationRuntime, id string) []string {
	t.Helper()
	var body []byte
	if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT record FROM background_runs WHERE id=?", id).Scan(&body)
	}); err != nil {
		t.Fatal(err)
	}
	var record struct {
		FollowKeys []string
		FollowUps  []string
	}
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	return append(record.FollowKeys, record.FollowUps...)
}

// A moved steer sends one withdrawal to the wrong run and one delivery to the
// right one, and both stay receipted in the run records.
func TestBindingMoveReattachesSteer(t *testing.T) {
	r := runRuntime(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	seedRun(t, r, "run-alpha", "Alpha")
	seedRun(t, r, "run-beta", "Beta")
	input := admitForBinding(t, r, "steer", "Alpha")
	recordEffect(t, r, input, "follow_work", `{"id":"run-alpha","text":"Use a table."}`, nil, map[string]string{"state": "submitted"})
	move, err := r.moveBinding(ctx, input, binding.Target{Class: "existing", ID: "run-beta", Kind: "run"}, "steer-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(move.Effects) != 1 || !move.Effects[0].Moved {
		t.Fatalf("steer outcome: %+v", move.Effects)
	}
	if !strings.Contains(move.Effects[0].Receipt, ":withdraw") || !strings.Contains(move.Effects[0].Receipt, ":deliver") {
		t.Errorf("steer receipts missing: %q", move.Effects[0].Receipt)
	}
	alpha := strings.Join(runFollowKeys(t, r, "run-alpha"), "\n")
	beta := strings.Join(runFollowKeys(t, r, "run-beta"), "\n")
	if !strings.Contains(alpha, ":withdraw") || !strings.Contains(alpha, "Withdrawal") {
		t.Errorf("wrong run did not receive one withdrawal: %q", alpha)
	}
	if !strings.Contains(beta, ":deliver") || !strings.Contains(beta, "Use a table.") {
		t.Errorf("right run did not receive the note: %q", beta)
	}
	if strings.Contains(beta, ":withdraw") || strings.Contains(alpha, ":deliver") {
		t.Errorf("a steer crossed targets: alpha=%q beta=%q", alpha, beta)
	}
}

// A text channel reaches the move list with one short reply and applies the
// numbered choice, because a chip line cannot be tapped there.
func TestChannelMoveListsAndAppliesChoice(t *testing.T) {
	r := controlRuntime(t)
	fixtureRecord(t, r, "garden", "project", "active", "", "Garden", "Plan the beds.")
	fixtureRecord(t, r, "plants", "todo", "open", "garden", "Plants", "Water them.")
	fixtureRecord(t, r, "lamps", "todo", "open", "garden", "Lamps", "Replace the bulbs.")
	address := channels.Address{Bot: channels.Bot{Platform: "telegram", Account: "fixture"}, Chat: "100"}
	admitted, err := json.Marshal(gateway.Inbound{Address: address, NativeID: "person-1", Body: []byte(`{"message_id":"person-1","text":"Plants"}`)})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := inbox.New(r.db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: r.conversation, Source: "channel-fixture", NativeID: "person-1"}, Body: admitted})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO channel_connections(bot,person,private_chat,cursor) VALUES(?,?,?,?)", address.Bot.Key(), r.person, "100", "1"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO channel_receipts(bot,native_id,cursor,address,agent,inbox_id) VALUES(?,?,?,?,?,?)", address.Bot.Key(), "person-1", "1", address.Key(), r.agent, receipt.ID); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "channel-input:" + strconv.FormatInt(receipt.ID, 10), ConversationID: r.conversation, PersonID: r.person, AgentID: r.agent, Surface: ledger.Surface{Kind: "channel", Platform: "telegram", ChatID: "100"}, OccurredAt: time.Now(), RecordedAt: time.Now(), TimeQuality: "observed", Locator: "person-1", Body: "Plants"})
	if err != nil {
		t.Fatal(err)
	}
	moveReceipt, err := inbox.New(r.db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: r.conversation, Source: "channel-fixture", NativeID: "person-2"}, Body: []byte(`{"message_id":"person-2","text":"/move"}`)})
	if err != nil {
		t.Fatal(err)
	}
	event := gateway.Inbound{Address: address, NativeID: "person-2"}
	in := channelInput{id: moveReceipt.ID, native: "person-2"}
	b := channels.Binding{Person: r.person, Agent: r.agent, Conversation: r.conversation, Scope: channels.PrivateScope()}
	if err = r.channels.channelMove(t.Context(), r, event, b, in, "person-1", ""); err != nil {
		t.Fatal(err)
	}
	var list string
	if err = r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT body FROM conv_event WHERE id=?", "channel-control:channel-input:"+strconv.FormatInt(moveReceipt.ID, 10)).Scan(&list)
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list, "2. Lamps") || !strings.Contains(list, "new item") || !strings.Contains(list, "no item") || strings.Contains(list, "1. Plants") || !strings.Contains(list, "Now: Plants") {
		t.Fatalf("move list = %q", list)
	}
	choice, err := inbox.New(r.db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: r.conversation, Source: "channel-fixture", NativeID: "person-3"}, Body: []byte(`{"message_id":"person-3","text":"/move 1"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.channels.channelMove(t.Context(), r, event, b, channelInput{id: choice.ID, native: "person-3"}, "person-1", "2"); err != nil {
		t.Fatal(err)
	}
	moves := 0
	if err = r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM binding_moves WHERE input_event=?", input.ID).Scan(&moves)
	}); err != nil {
		t.Fatal(err)
	}
	if moves != 1 {
		t.Fatalf("moves = %d after the channel choice", moves)
	}
}

func TestBindingMoveUpdatesChoiceAndRetries(t *testing.T) {
	r := runRuntime(t)
	fixtureRecord(t, r, "plants", "todo", "open", "", "Plants", "Water them.")
	fixtureRecord(t, r, "lamps", "todo", "open", "", "Lamps", "Replace bulbs.")
	inputID := admitForBinding(t, r, "move-choice", "Plants need watering.")
	target := binding.Target{Class: "existing", ID: "lamps", Kind: "todo"}
	first, err := r.moveBinding(t.Context(), inputID, target, "same-request")
	if err != nil {
		t.Fatal(err)
	}
	input, err := r.journal().Get(t.Context(), inputID)
	if err != nil {
		t.Fatal(err)
	}
	choices, err := r.currentInputChoices(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got := currentChoice(choices).Target; got != target {
		t.Errorf("binding after move = %+v, want %+v", got, target)
	}
	if chip := r.chipFor(t.Context(), input); chip == nil || chip.Name != "Lamps" {
		t.Errorf("chip after move = %+v", chip)
	}
	again, err := r.moveBinding(t.Context(), inputID, target, "same-request")
	if err != nil || again.ID != first.ID || again.At != first.At {
		t.Errorf("retry = %+v, %v; want original receipt", again, err)
	}
	replay, ok := moveReplayCase(first)
	if !ok || replay.Input.Text != input.Body.Text() {
		t.Errorf("replay lost original input: %+v", replay.Input)
	}
}

func TestBindingMoveToGoal(t *testing.T) {
	r := runRuntime(t)
	fixtureRecord(t, r, "goal", "initiative", "active", "", "Healthy garden", "Grow vegetables.")
	fixtureRecord(t, r, "garden", "project", "active", "", "Garden", "Plan beds.")
	input := admitForBinding(t, r, "to-goal", "Garden")
	fixtureRecord(t, r, "seedlings", "todo", "open", "garden", "Seedlings", "Sow them.")
	recordEffect(t, r, input, "write_record", `{"kind":"todo"}`, nil, map[string]string{"id": "seedlings"})
	move, err := r.moveBinding(t.Context(), input, binding.Target{Class: "existing", ID: "goal", Kind: "initiative"}, "goal")
	if err != nil {
		t.Fatal(err)
	}
	if _, parent := readRecord(t, r, "seedlings"); parent != "goal" {
		t.Fatalf("parent=%q, effects=%+v", parent, move.Effects)
	}
}

func TestBindingMoveRefusedEditPreservesOriginal(t *testing.T) {
	r := runRuntime(t)
	fixtureRecord(t, r, "plants", "todo", "open", "", "Plants", "Water them.")
	fixtureRecord(t, r, "garden", "project", "active", "", "Garden", "Plan beds.")
	input := admitForBinding(t, r, "bad-target", "Plants")
	before, rev, err := r.records.Read(t.Context(), "plants")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := coreexec.WriteRecord(t.Context(), r.records, records.Actor{Person: r.person, Agent: r.agent}, coreexec.RecordWrite{ID: "plants", Revision: rev, Kind: "todo", Title: "Plants", Note: "Water daily."})
	if err != nil {
		t.Fatal(err)
	}
	recordEffect(t, r, input, "write_record", `{"id":"plants","kind":"todo","title":"Plants","note":"Water daily."}`, &effectBefore{body: before.Data, revision: rev}, receipt)
	move, err := r.moveBinding(t.Context(), input, binding.Target{Class: "existing", ID: "garden", Kind: "project"}, "bad-target")
	if err != nil {
		t.Fatal(err)
	}
	if len(move.Effects) != 1 || move.Effects[0].Moved {
		t.Errorf("refused edit reported moved: %+v", move.Effects)
	}
	if got, _ := readRecord(t, r, "plants"); got != "Plants|Water daily." {
		t.Errorf("refused edit discarded original change: %s", got)
	}
}

func TestBindingStatsCountModelPick(t *testing.T) {
	r := runRuntime(t)
	fixtureRecord(t, r, "plants", "todo", "open", "", "Plants", "Water them.")
	fixtureRecord(t, r, "lamps", "todo", "open", "", "Lamps", "Replace bulbs.")
	id := admitForBinding(t, r, "model-pick", "Take care of it.")
	input, err := r.journal().Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		return appendInputChoices(t.Context(), tx, input, "model-choice", []binding.Choice{{Request: 0, Quote: input.Body.Text(), Target: binding.Target{Class: "existing", ID: "plants", Kind: "todo"}, Decider: "model"}})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.moveBinding(t.Context(), id, binding.Target{Class: "existing", ID: "lamps", Kind: "todo"}, "model-move"); err != nil {
		t.Fatal(err)
	}
	stats, err := r.bindingStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Deciders) != 1 || stats.Deciders[0].Decider != "model" || stats.Deciders[0].Bindings != 1 || stats.Deciders[0].Moves != 1 || stats.Deciders[0].PerHundred != 100 {
		t.Fatalf("wrong decider counts: %+v", stats)
	}
}

func TestBindingMoveKeepsEverySteer(t *testing.T) {
	r := runRuntime(t)
	seedRun(t, r, "run-alpha", "Alpha")
	seedRun(t, r, "run-beta", "Beta")
	input := admitForBinding(t, r, "two-steers", "Alpha")
	for _, note := range []string{"Use a table.", "Include sources."} {
		raw, _ := json.Marshal(map[string]string{"id": "run-alpha", "text": note})
		recordEffect(t, r, input, "follow_work", string(raw), nil, map[string]string{"state": "submitted"})
	}
	move, err := r.moveBinding(t.Context(), input, binding.Target{Class: "existing", ID: "run-beta", Kind: "run"}, "two-steers")
	if err != nil {
		t.Fatal(err)
	}
	if len(move.Effects) != 2 || move.Effects[0].Receipt == move.Effects[1].Receipt {
		t.Errorf("steers share receipts: %+v", move.Effects)
	}
	for _, id := range []string{"run-alpha", "run-beta"} {
		got := strings.Join(runFollowKeys(t, r, id), "\n")
		for _, note := range []string{"Use a table.", "Include sources."} {
			if !strings.Contains(got, note) {
				t.Errorf("%s lost %s: %s", id, note, got)
			}
		}
	}
}

func TestBindingMoveCombinesEditChain(t *testing.T) {
	r := runRuntime(t)
	fixtureRecord(t, r, "plants", "todo", "open", "", "Plants", "Water them.")
	fixtureRecord(t, r, "lamps", "todo", "open", "", "Lamps", "Replace bulbs.")
	fixtureRecord(t, r, "pots", "todo", "open", "", "Pots", "Empty.")
	input := admitForBinding(t, r, "edits", "Plants")
	for _, write := range []coreexec.RecordWrite{{ID: "plants", Kind: "todo", Title: "Garden plants", Note: "Water them."}, {ID: "plants", Kind: "todo", Title: "Garden plants", Note: "Water daily."}} {
		before, rev, err := r.records.Read(t.Context(), "plants")
		if err != nil {
			t.Fatal(err)
		}
		write.Revision = rev
		receipt, err := coreexec.WriteRecord(t.Context(), r.records, records.Actor{Person: r.person, Agent: r.agent}, write)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(write)
		recordEffect(t, r, input, "write_record", string(raw), &effectBefore{body: before.Data, revision: rev}, receipt)
	}
	if _, err := r.moveBinding(t.Context(), input, binding.Target{Class: "existing", ID: "lamps", Kind: "todo"}, "edits"); err != nil {
		t.Fatal(err)
	}
	if got, _ := readRecord(t, r, "plants"); got != "Plants|Water them." {
		t.Errorf("original not restored: %s", got)
	}
	if got, _ := readRecord(t, r, "lamps"); got != "Garden plants|Water daily." {
		t.Errorf("lost accumulated edit: %s", got)
	}
	if _, err := r.moveBinding(t.Context(), input, binding.Target{Class: "existing", ID: "pots", Kind: "todo"}, "edits-again"); err != nil {
		t.Fatal(err)
	}
	if got, _ := readRecord(t, r, "lamps"); got != "Lamps|Replace bulbs." {
		t.Errorf("first destination kept the chain: %s", got)
	}
	if got, _ := readRecord(t, r, "pots"); got != "Garden plants|Water daily." {
		t.Errorf("second destination missed the chain: %s", got)
	}
	if got, _ := readRecord(t, r, "plants"); got != "Plants|Water them." {
		t.Errorf("original changed on the second move: %s", got)
	}
}

func readTodo(t *testing.T, r *conversationRuntime, id string) *records.Todo {
	t.Helper()
	record, _, err := r.records.Read(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	todo, _, _, err := records.ReadTodo(record.Data)
	if err != nil || todo == nil {
		t.Fatalf("todo %s: %v", id, err)
	}
	return todo
}

// A cleared optional field is part of the edit. omitempty drops it from the
// marshalled to-do, and the destination must still lose that field.
func TestMergeTodoDropsClearedFields(t *testing.T) {
	before := &records.Todo{State: "open", Important: true, Deadline: "2026-10-24", DeadlineSource: "person", Tags: []string{"beds"}}
	edit := &records.Todo{State: "open", Tags: []string{"beds"}}
	right := &records.Todo{State: "open", Important: true, Deadline: "2026-11-01", DeadlineSource: "person", Tags: []string{"keep"}}
	got := mergeTodo(before, edit, right)
	if got.Important || got.Deadline != "" || got.DeadlineSource != "" {
		t.Fatalf("cleared fields kept: %+v", got)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "keep" {
		t.Fatalf("unrelated field changed: %+v", got)
	}
}

func TestBindingMoveTransfersClearedTodoFields(t *testing.T) {
	r := runRuntime(t)
	fixtureRecord(t, r, "plants", "todo", "open", "", "Plants", "Water them.")
	fixtureRecord(t, r, "lamps", "todo", "open", "", "Lamps", "Replace bulbs.")
	writeTodo := func(id, title, rev string, todo records.Todo) {
		t.Helper()
		if _, err := coreexec.WriteRecord(t.Context(), r.records, records.Actor{Person: r.person, Agent: r.agent}, coreexec.RecordWrite{ID: id, Revision: rev, Kind: "todo", Title: title, Note: "Water them.", Todo: &todo}); err != nil {
			t.Fatal(err)
		}
	}
	_, rev, err := r.records.Read(t.Context(), "plants")
	if err != nil {
		t.Fatal(err)
	}
	writeTodo("plants", "Plants", rev, records.Todo{State: "open", Important: true, Deadline: "2026-10-24", DeadlineSource: "person", Tags: []string{"beds"}})
	_, lampRev, err := r.records.Read(t.Context(), "lamps")
	if err != nil {
		t.Fatal(err)
	}
	writeTodo("lamps", "Lamps", lampRev, records.Todo{State: "open", Important: true, Deadline: "2026-11-01", DeadlineSource: "person", Tags: []string{"keep"}})
	input := admitForBinding(t, r, "clear-fields", "Plants")
	before, beforeRev, err := r.records.Read(t.Context(), "plants")
	if err != nil {
		t.Fatal(err)
	}
	cleared := coreexec.RecordWrite{ID: "plants", Revision: beforeRev, Kind: "todo", Title: "Plants", Note: "Water them.", Todo: &records.Todo{State: "open", Tags: []string{"beds"}}}
	receipt, err := coreexec.WriteRecord(t.Context(), r.records, records.Actor{Person: r.person, Agent: r.agent}, cleared)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cleared)
	recordEffect(t, r, input, "write_record", string(raw), &effectBefore{body: before.Data, revision: beforeRev}, receipt)
	if _, err = r.moveBinding(t.Context(), input, binding.Target{Class: "existing", ID: "lamps", Kind: "todo"}, "clear-fields"); err != nil {
		t.Fatal(err)
	}
	restored := readTodo(t, r, "plants")
	if !restored.Important || restored.Deadline != "2026-10-24" || restored.DeadlineSource != "person" {
		t.Errorf("original lost its fields: %+v", restored)
	}
	moved := readTodo(t, r, "lamps")
	if moved.Important || moved.Deadline != "" || moved.DeadlineSource != "" {
		t.Errorf("destination kept cleared fields: %+v", moved)
	}
	if len(moved.Tags) != 1 || moved.Tags[0] != "keep" {
		t.Errorf("destination lost an untouched field: %+v", moved)
	}
}

// A second move restores the item that holds the edit now, and withdraws a
// steer from the run that holds it now.
func TestBindingMoveFollowsCurrentLocation(t *testing.T) {
	r := runRuntime(t)
	fixtureRecord(t, r, "plants", "todo", "open", "", "Plants", "Water them.")
	fixtureRecord(t, r, "lamps", "todo", "open", "", "Lamps", "Replace bulbs.")
	fixtureRecord(t, r, "pots", "todo", "open", "", "Pots", "Empty.")
	input := admitForBinding(t, r, "again", "Plants")
	before, rev, err := r.records.Read(t.Context(), "plants")
	if err != nil {
		t.Fatal(err)
	}
	write := coreexec.RecordWrite{ID: "plants", Revision: rev, Kind: "todo", Title: "Plants", Note: "Water them daily."}
	receipt, err := coreexec.WriteRecord(t.Context(), r.records, records.Actor{Person: r.person, Agent: r.agent}, write)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(write)
	recordEffect(t, r, input, "write_record", string(raw), &effectBefore{body: before.Data, revision: rev}, receipt)
	if _, err = r.moveBinding(t.Context(), input, binding.Target{Class: "existing", ID: "lamps", Kind: "todo"}, "edit-1"); err != nil {
		t.Fatal(err)
	}
	second, err := r.moveBinding(t.Context(), input, binding.Target{Class: "existing", ID: "pots", Kind: "todo"}, "edit-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Effects) != 1 || !second.Effects[0].Moved {
		t.Fatalf("second edit move: %+v", second.Effects)
	}
	if got, _ := readRecord(t, r, "plants"); got != "Plants|Water them." {
		t.Errorf("original not restored: %s", got)
	}
	if got, _ := readRecord(t, r, "lamps"); got != "Lamps|Replace bulbs." {
		t.Errorf("first destination kept the edit: %s", got)
	}
	if got, _ := readRecord(t, r, "pots"); got != "Pots|Water them daily." {
		t.Errorf("second destination missed the edit: %s", got)
	}
	var payload string
	if err = r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT payload FROM binding_effects WHERE input_event=? AND kind=?", input, binding.EffectEdit).Scan(&payload)
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, `"todo"`) {
		t.Errorf("text-only edit gained a to-do on replay: %s", payload)
	}
}

func TestBindingMoveFollowsCurrentSteer(t *testing.T) {
	r := runRuntime(t)
	seedRun(t, r, "run-alpha", "Alpha")
	seedRun(t, r, "run-beta", "Beta")
	seedRun(t, r, "run-gamma", "Gamma")
	steer := admitForBinding(t, r, "steer-again", "Alpha")
	recordEffect(t, r, steer, "follow_work", `{"id":"run-alpha","text":"Use a table."}`, nil, map[string]string{"state": "submitted"})
	if _, err := r.moveBinding(t.Context(), steer, binding.Target{Class: "existing", ID: "run-beta", Kind: "run"}, "steer-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.moveBinding(t.Context(), steer, binding.Target{Class: "existing", ID: "run-gamma", Kind: "run"}, "steer-2"); err != nil {
		t.Fatal(err)
	}
	alpha := strings.Join(runFollowKeys(t, r, "run-alpha"), "\n")
	beta := strings.Join(runFollowKeys(t, r, "run-beta"), "\n")
	gamma := strings.Join(runFollowKeys(t, r, "run-gamma"), "\n")
	if strings.Count(alpha, ":withdraw") != 1 || strings.Contains(alpha, ":deliver") {
		t.Errorf("original run was steered again: %q", alpha)
	}
	if !strings.Contains(beta, ":deliver") || !strings.Contains(beta, ":withdraw") {
		t.Errorf("first destination was not withdrawn: %q", beta)
	}
	if !strings.Contains(gamma, ":deliver") || !strings.Contains(gamma, "Use a table.") || strings.Contains(gamma, ":withdraw") {
		t.Errorf("second destination: %q", gamma)
	}
}

func TestBindingEffectResolvesWorkerSession(t *testing.T) {
	r := runRuntime(t)
	done := make(chan struct{})
	close(done)
	r.workers = &workerRuntime{sessions: map[string]*workerSession{
		"thread": {origin: r, thread: worker.Thread{ID: "thread", TaskID: "task-alpha"}, questions: map[string]*workerQuestion{}, close: func() error { return nil }, done: done},
	}}
	input := admitForBinding(t, r, "worker-steer", "Alpha")
	recordEffect(t, r, input, "worker_input", `{"session":"thread","text":"Use a table."}`, nil, map[string]string{"state": "submitted"})
	recordEffect(t, r, input, "worker_input", `{"session":"missing","text":"Nowhere."}`, nil, map[string]string{"state": "submitted"})
	var targets []string
	if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(t.Context(), "SELECT target FROM binding_effects WHERE input_event=? AND kind=? ORDER BY ordinal", input, binding.EffectSteer)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var target string
			if err = rows.Scan(&target); err != nil {
				return err
			}
			targets = append(targets, target)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0] != "task-alpha" {
		t.Fatalf("worker steer targets = %#v", targets)
	}
}
