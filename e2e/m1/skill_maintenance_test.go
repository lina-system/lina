// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/jobs"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2InstalledSkillMaintenance(t *testing.T) {
	var fixture struct {
		Skills      []struct{ ID, Description, Body string }
		MergedBody  string `json:"merged_body"`
		MergeReason string `json:"merge_reason"`
		PruneReason string `json:"prune_reason"`
	}
	raw, err := os.ReadFile("../m2/skill-maintenance.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	document := func(i int, body string) skills.Bundle {
		return skills.Bundle{"SKILL.md": {Text: fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n%s", fixture.Skills[i].ID, fixture.Skills[i].Description, body)}}
	}
	var mu sync.Mutex
	phase, step, calls := "seed", 0, 0
	var undo []skills.Undo
	var notices []skills.MaintenanceNotice
	var builtinBefore []skills.Version
	var merged skills.Version
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var request struct {
			Instructions string
			Input, Tools []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		maintenance := len(request.Input) > 0 && strings.Contains(string(request.Input[0]), "Klotho's skill maintenance job")
		if !maintenance {
			offered := map[string]bool{}
			for _, raw := range request.Tools {
				var d struct{ Name string }
				if json.Unmarshal(raw, &d) == nil {
					offered[d.Name] = true
				}
			}
			for _, name := range []string{"skill_index", "read_skill", "write_skill", "undo_skill", "skill_notices", "skill_maintenance"} {
				if !offered[name] {
					t.Errorf("missing declaration %s", name)
				}
			}
		}
		if maintenance {
			calls++
			if calls == 1 {
				emitResponse(w, "invalid maintenance result", true)
				return
			}
			if len(request.Tools) != 0 || request.Instructions != "" {
				t.Error("maintenance inherited conversation tools")
			}
			var item struct{ Content string }
			if len(request.Input) != 2 || json.Unmarshal(request.Input[1], &item) != nil {
				t.Error("missing snapshot")
				return
			}
			content := strings.TrimSuffix(strings.TrimPrefix(item.Content, "<skill_snapshot>\n"), "\n</skill_snapshot>")
			var snapshot []skills.Body
			if json.Unmarshal([]byte(content), &snapshot) != nil {
				t.Error("invalid snapshot")
				return
			}
			if len(snapshot) != 3 {
				t.Errorf("snapshot count=%d; want three user skills", len(snapshot))
			}
			refs := map[string]skills.Undo{}
			for _, b := range snapshot {
				if !strings.HasPrefix(b.Version.ID, "fixture-") {
					t.Error("built-in entered maintenance")
				}
				refs[b.Version.ID] = skills.Undo{ID: b.Version.ID, Revision: b.Version.Revision}
			}
			result := jobs.SkillMaintenanceResult{Actions: []skills.Maintenance{
				{Kind: "merge", Target: refs[fixture.Skills[0].ID], Sources: []skills.Undo{refs[fixture.Skills[1].ID]}, Files: document(0, fixture.MergedBody), Reason: fixture.MergeReason},
				{Kind: "prune", Target: refs[fixture.Skills[2].ID], Reason: fixture.PruneReason},
			}}
			data, _ := json.Marshal(result)
			emitResponse(w, string(data), true)
			return
		}
		output := func(value any) {
			var item struct{ Type, Output string }
			var result struct {
				Code   string
				Output json.RawMessage
			}
			if len(request.Input) == 0 || json.Unmarshal(request.Input[len(request.Input)-1], &item) != nil || json.Unmarshal([]byte(item.Output), &result) != nil || result.Code != "ok" {
				t.Errorf("phase %s step %d failed tool: %+v", phase, step, item)
				return
			}
			if value != nil {
				if err := json.Unmarshal(result.Output, value); err != nil {
					t.Error(err)
				}
			}
		}
		tool := func(name string, args any) { emitPersonaCall(w, fmt.Sprintf("%s-%d", phase, step), name, args) }
		switch phase {
		case "seed":
			if step > 0 {
				output(nil)
			}
			if step < len(fixture.Skills) {
				tool("write_skill", skills.Patch{ID: fixture.Skills[step].ID, Files: document(step, fixture.Skills[step].Body)})
			} else if step == 3 {
				tool("skill_index", struct{}{})
			} else {
				var catalog []skills.Version
				output(&catalog)
				for _, v := range catalog {
					if !strings.HasPrefix(v.ID, "fixture-") {
						builtinBefore = append(builtinBefore, v)
					}
				}
				emitResponse(w, "Seeded.", true)
			}
		case "refresh":
			if step == 0 {
				tool("write_skill", skills.Patch{ID: fixture.Skills[0].ID, Expected: 1, Files: document(0, fixture.Skills[0].Body)})
			} else {
				output(nil)
				emitResponse(w, "Updated.", true)
			}
		case "inspect", "restored", "restart":
			switch step {
			case 0:
				tool("skill_index", struct{}{})
			case 1:
				var catalog []skills.Version
				output(&catalog)
				builtins := []skills.Version{}
				users := []skills.Version{}
				for _, v := range catalog {
					if strings.HasPrefix(v.ID, "fixture-") {
						users = append(users, v)
						if v.ID == fixture.Skills[0].ID {
							merged = v
						}
					} else {
						builtins = append(builtins, v)
					}
				}
				before, _ := json.Marshal(builtinBefore)
				after, _ := json.Marshal(builtins)
				if string(before) != string(after) {
					t.Error("built-in catalog changed")
				}
				want := 3
				if phase == "inspect" {
					want = 1
				}
				if len(users) != want {
					t.Errorf("phase %s active user skills=%d", phase, len(users))
				}
				tool("read_skill", merged)
			case 2:
				var body skills.Body
				output(&body)
				want := fixture.Skills[0].Body
				if phase == "inspect" {
					want = fixture.MergedBody
				}
				if body.Files["SKILL.md"].Text != document(0, want)["SKILL.md"].Text {
					t.Error("wrong active procedure")
				}
				tool("skill_notices", struct{}{})
			case 3:
				output(&notices)
				want := 4
				if phase == "inspect" {
					want = 2
					for _, n := range notices {
						undo = append(undo, n.Undo)
					}
				}
				if len(notices) != want {
					t.Errorf("phase %s notices=%d", phase, len(notices))
				}
				emitResponse(w, "Checked.", true)
			}
		case "undo":
			if step > 0 {
				output(nil)
			}
			if step < len(undo) {
				tool("undo_skill", undo[step])
			} else {
				emitResponse(w, "Restored.", true)
			}
		case "disable":
			switch step {
			case 0:
				tool("skill_maintenance", map[string]bool{"enabled": false})
			case 1:
				var policy jobapi.Job
				output(&policy)
				if policy.Enabled {
					t.Error("job switch did not persist")
				}
				tool("write_skill", skills.Patch{ID: fixture.Skills[0].ID, Expected: 3, Files: document(0, "Must remain unchanged.")})
			case 2:
				var item struct{ Output string }
				var result struct{ Code string }
				if json.Unmarshal(request.Input[len(request.Input)-1], &item) != nil || json.Unmarshal([]byte(item.Output), &result) != nil || result.Code != "denied" {
					t.Errorf("disabled write accepted: %s", item.Output)
				}
				emitResponse(w, "Disabled.", true)
			}
		case "disabled-restart":
			if step == 0 {
				tool("skill_maintenance", struct{}{})
			} else {
				var policy jobapi.Job
				output(&policy)
				if policy.Enabled {
					t.Error("enablement lost on restart")
				}
				emitResponse(w, "Still disabled.", true)
			}

		default:
			t.Errorf("unexpected phase %s", phase)
			emitResponse(w, "Unexpected.", true)
		}
		step++
	}))
	defer server.Close()
	lina, node, workerBinary := buildLina(t), buildCommand(t, "lina-node"), buildCommand(t, "moirai-worker")
	h := newHarness(t, lina)
	bundle := makeInstallBundle(t, h.root, h.binary, node)
	worker, err := os.ReadFile(workerBinary)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bundle, "moirai-worker"), worker, 0700); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(bundle, "manifest.json")
	raw, err = os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["binaries"] = append(manifest["binaries"].([]any), map[string]any{"name": "moirai-worker", "size": len(worker), "sha256": fmt.Sprintf("%x", sha256.Sum256(worker))})
	raw, _ = json.Marshal(manifest)
	if err = os.WriteFile(manifestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	h, start := installConversationBundle(t, h, bundle, server.URL)
	// Each fully stopped Core lifecycle gets the harness's same bounded phase
	// budget. Builds are outside it, and a prior lifecycle cannot spend the
	// restart/suppression phase's deadline. No operation deadline is relaxed.
	nextPhase := func() {
		ctx, cancel := context.WithTimeout(t.Context(), 3*operationTimeout)
		t.Cleanup(cancel)
		h.ctx = ctx
	}
	core := start(true)
	client, conversation := conversationClient(t, h)
	runTurn := func(next string) []protocol.Envelope {
		t.Helper()
		mu.Lock()
		phase, step = next, 0
		mu.Unlock()
		return admittedTurn(t, h, client, conversation, "maintenance-"+next, next)
	}
	runWorker := func(want int) {
		t.Helper()
		p := h.command("--canon", filepath.Join(h.root, "canon/canon.sqlite"), "--socket", filepath.Join(h.root, "run/jobs.sock"), "--once")
		p.command.Path = filepath.Join(h.root, "system/bin/moirai-worker")
		p.command.Args[0] = p.command.Path
		p.start(t)
		p.wait(t, want)
	}
	runTurn("seed")
	runWorker(69)
	mu.Lock()
	fixture.Skills[0].Body += " Check required fields."
	mu.Unlock()
	runTurn("refresh")
	client.Close()
	h.stopCore(core)
	nextPhase()
	var failedReservation string
	withFileCanon(t, h, func(db *canon.Store) {
		if err := db.Write(h.ctx, func(tx *canon.Tx) error {
			var raw []byte
			if err := tx.QueryRowContext(h.ctx, "SELECT record FROM moirai_jobs WHERE id=?", jobs.SkillMaintenanceJob).Scan(&raw); err != nil {
				return err
			}
			var j jobapi.Job
			if err := json.Unmarshal(raw, &j); err != nil {
				return err
			}
			if j.Watermark != 1 {
				t.Fatalf("failed pass still blocks installed worker: %+v", j)
			}
			j.NextRun = time.Time{}
			raw, _ = json.Marshal(j)
			if _, err := tx.ExecContext(h.ctx, "UPDATE moirai_jobs SET record=? WHERE id=?", raw, j.ID); err != nil {
				return err
			}
			return tx.QueryRowContext(h.ctx, "SELECT reservation FROM moirai_aux WHERE job=? AND sequence=1", jobs.SkillMaintenanceJob).Scan(&failedReservation)
		}); err != nil {
			t.Fatal(err)
		}
	})
	core = start(true)
	client, conversation = conversationClient(t, h)
	runWorker(0)
	frames := runTurn("inspect")
	checkNotices := func(frames []protocol.Envelope, want int) {
		t.Helper()
		n := 0
		for _, e := range frames {
			if item, ok := e.Payload.(protocol.ItemDone); ok && item.Text != nil && (strings.Contains(*item.Text, fixture.MergeReason) || strings.Contains(*item.Text, fixture.PruneReason)) {
				n++
				if strings.ContainsAny(*item.Text, "\r\n") {
					t.Error("multiline maintenance notice")
				}
			}
		}
		if n != want {
			t.Fatalf("maintenance lines=%d want=%d", n, want)
		}
	}
	checkNotices(frames, 2)
	runTurn("undo")
	runTurn("restored")
	client.Close()
	h.stopCore(core)
	nextPhase()
	// Advance only the persisted due time in the stopped fixture. Real worker
	// dispatch, provider calls, versions, undo and suppression remain production.
	withFileCanon(t, h, func(db *canon.Store) {
		if err := db.Write(h.ctx, func(tx *canon.Tx) error {
			var raw []byte
			if err := tx.QueryRowContext(h.ctx, "SELECT record FROM moirai_jobs WHERE id=?", jobs.SkillMaintenanceJob).Scan(&raw); err != nil {
				return err
			}
			var j jobapi.Job
			if err := json.Unmarshal(raw, &j); err != nil {
				return err
			}
			j.NextRun = time.Time{}
			raw, _ = json.Marshal(j)
			_, err := tx.ExecContext(h.ctx, "UPDATE moirai_jobs SET record=? WHERE id=?", raw, j.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
	core = start(true)
	client, conversation = conversationClient(t, h)
	runWorker(0)
	checkNotices(runTurn("restart"), 0)
	historyClient, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { historyClient.Close() })
	topic := "conversation:" + conversation
	if _, err = historyClient.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	history, err := historyClient.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	historyClient.Close()
	historyJSON, _ := json.Marshal(history.Payload)
	for _, reason := range []string{fixture.MergeReason, fixture.PruneReason} {
		if strings.Count(string(historyJSON), reason) != 1 {
			t.Fatalf("notice history lost or duplicated: %s", historyJSON)
		}
	}

	runTurn("disable")
	client.Close()
	h.stopCore(core)
	nextPhase()
	core = start(true)
	client, conversation = conversationClient(t, h)
	runWorker(0)
	runTurn("disabled-restart")
	client.Close()
	h.stopCore(core)
	// Seed a long durable history while Core is stopped, and reorder agents.
	// Recovery must neither revive old notices nor move them to the new first agent.
	nextPhase()
	var newFirst string
	withFileCanon(t, h, func(db *canon.Store) {
		if err := db.Read(h.ctx, func(tx *canon.Tx) error {
			var id string
			var output []byte
			var charged int
			if err := tx.QueryRowContext(h.ctx, "SELECT a.reservation,a.output,b.charged FROM moirai_aux a JOIN budget_usage b ON b.id=a.reservation WHERE a.job=? AND a.sequence=1", jobs.SkillMaintenanceJob).Scan(&id, &output, &charged); err != nil {
				return err
			}
			if id != failedReservation || output != nil || charged != 8192 {
				t.Fatalf("failed reservation changed: %s %s %d", id, output, charged)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		store, err := agents.Open(h.root, db)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		all, err := store.List(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(all) == 0 {
			t.Fatal("missing original agent")
		}
		var person string
		if err := db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, "SELECT person_id FROM agent_revisions WHERE agent_id=? AND revision=1", all[0].ID).Scan(&person)
		}); err != nil {
			t.Fatal(err)
		}
		created, err := store.Create(h.ctx, agents.Create{Name: "AAA", PersonID: person, Preset: "lina"})
		if err != nil {
			t.Fatal(err)
		}
		newFirst = created.ID
		journal := ledger.New(db, nil)
		for i := range 205 {
			id := fmt.Sprintf("after-maintenance:%03d", i)
			now := time.Now()
			if _, err := journal.Admit(h.ctx, guard.EntrySystem, ledger.Input{ID: id, ConversationID: conversation, AgentID: strings.TrimPrefix(conversation, "main:"), Surface: ledger.Surface{Kind: "tui"}, ScaffoldKind: "failure", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Later history item."}); err != nil {
				t.Fatal(err)
			}
			if err := db.Write(h.ctx, func(tx *canon.Tx) error {
				_, err := tx.ExecContext(h.ctx, "INSERT INTO conversation_order(conversation_id,item_key) VALUES(?,?)", conversation, id)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
	})
	core = start(true)
	historyClient, err = clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{conversation, "main:" + newFirst} {
		agent := strings.TrimPrefix(target, "main:")
		if _, err = historyClient.History(h.ctx, protocol.HistoryGet{AgentID: &agent}); err != nil {
			t.Fatal(err)
		}
		if reply, err := historyClient.Receive(h.ctx); err != nil || reply.Kind != protocol.KindHistory {
			t.Fatalf("history reply: %+v %v", reply, err)
		}
		topic := "conversation:" + target
		if _, err = historyClient.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
			t.Fatal(err)
		}
		frame, err := historyClient.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, ok := frame.Payload.(protocol.Snapshot)
		if !ok {
			t.Fatalf("expected snapshot: %+v", frame)
		}
		var keys []string
		for _, item := range snapshot.Items {
			if strings.HasPrefix(item.Key, "skill-maintenance:") {
				t.Fatalf("revived or rerouted old notice: %s in %s", item.Key, target)
			}
			if strings.HasPrefix(item.Key, "after-maintenance:") {
				keys = append(keys, item.Key)
			}
		}
		if target == conversation {
			if len(keys) != 199 {
				t.Fatalf("history items=%d", len(keys))
			}
			for i, key := range keys {
				if want := fmt.Sprintf("after-maintenance:%03d", i+6); key != want {
					t.Fatalf("history[%d]=%s want=%s", i, key, want)
				}
			}
		} else if len(keys) != 0 {
			t.Fatal("history crossed conversation")
		}
	}
	historyClient.Close()
	h.stopCore(core)
	// A historical recipient can disappear before the next boot. A new
	// outbox item must still reach the surviving agent through running Core.
	nextPhase()
	var pendingID string
	withFileCanon(t, h, func(db *canon.Store) {
		store, err := agents.Open(h.root, db)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		oldAgent := strings.TrimPrefix(conversation, "main:")
		var person string
		if err = db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, "SELECT person_id FROM agent_revisions WHERE agent_id=? AND revision=1", oldAgent).Scan(&person)
		}); err != nil {
			t.Fatal(err)
		}
		if err = store.Delete(h.ctx, oldAgent, person, time.Now()); err != nil {
			t.Fatal(err)
		}
		skillStore, err := skills.Open(h.root, db, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer skillStore.Close()
		created, err := skillStore.Patch(h.ctx, skills.Patch{ID: "fixture-later", Files: skills.Bundle{"SKILL.md": {Text: "---\nname: fixture-later\ndescription: Retired.\n---\nRetired procedure."}}})
		if err != nil {
			t.Fatal(err)
		}
		if err = db.Write(h.ctx, func(tx *canon.Tx) error {
			_, err := skillStore.MaintainTx(h.ctx, tx, skills.Maintenance{Kind: "prune", Target: created.Undo, Reason: "Retired later procedure."})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		rows, err := skillStore.MaintenanceNotices(h.ctx)
		if err != nil || len(rows) != 5 {
			t.Fatalf("pending notices=%+v %v", rows, err)
		}
		pendingID = fmt.Sprintf("skill-maintenance:%d", rows[len(rows)-1].Sequence)
	})
	for range 2 {
		nextPhase()
		core = start(true)
		historyClient, err = clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
		if err != nil {
			t.Fatal(err)
		}
		target := "main:" + newFirst
		topic := "conversation:" + target
		if _, err = historyClient.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
			t.Fatal(err)
		}
		frame, err := historyClient.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, ok := frame.Payload.(protocol.Snapshot)
		if !ok {
			t.Fatalf("expected snapshot: %+v", frame)
		}
		count := 0
		for _, item := range snapshot.Items {
			if strings.HasPrefix(item.Key, "skill-maintenance:") {
				if item.Key != pendingID {
					t.Fatalf("rerouted unavailable recipient's notice: %s", item.Key)
				}
				count++
			}
		}
		if count != 1 {
			t.Fatalf("unavailable recipient blocked later notice: got %d, want 1", count)
		}
		historyClient.Close()
		h.stopCore(core)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 3 {
		t.Fatalf("background model calls=%d", calls)
	}
}
