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

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/jobs"
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
	raw, err := os.ReadFile("m2/skill-maintenance.json")
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
	core := start(true)
	client, conversation := conversationClient(t, h)
	runTurn := func(next string) []protocol.Envelope {
		t.Helper()
		mu.Lock()
		phase, step = next, 0
		mu.Unlock()
		return admittedTurn(t, h, client, conversation, "maintenance-"+next, next)
	}
	runWorker := func() {
		t.Helper()
		p := h.command("--canon", filepath.Join(h.root, "canon/canon.sqlite"), "--socket", filepath.Join(h.root, "run/jobs.sock"), "--once")
		p.command.Path = filepath.Join(h.root, "system/bin/moirai-worker")
		p.command.Args[0] = p.command.Path
		p.start(t)
		p.wait(t, 0)
	}
	runTurn("seed")
	runWorker()
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
	// Advance only the persisted due time in the stopped fixture. Real worker
	// dispatch, provider calls, versions, undo and suppression remain production.
	ctl, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon/canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Write(h.ctx, func(tx *canon.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(h.ctx, "SELECT record FROM moirai_jobs WHERE id=?", jobs.SkillMaintenanceJob).Scan(&raw); err != nil {
			return err
		}
		var j jobapi.Job
		if err = json.Unmarshal(raw, &j); err != nil {
			return err
		}
		j.NextRun = time.Time{}
		raw, _ = json.Marshal(j)
		_, err := tx.ExecContext(h.ctx, "UPDATE moirai_jobs SET record=? WHERE id=?", raw, j.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = ctl.Close(); err != nil {
		t.Fatal(err)
	}
	core = start(true)
	client, conversation = conversationClient(t, h)
	runWorker()
	checkNotices(runTurn("restart"), 0)
	if _, err = client.History(h.ctx, protocol.HistoryGet{ConversationID: &conversation}); err != nil {
		t.Fatal(err)
	}
	history, err := client.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	historyJSON, _ := json.Marshal(history.Payload)
	for _, reason := range []string{fixture.MergeReason, fixture.PruneReason} {
		if strings.Count(string(historyJSON), reason) != 1 {
			t.Fatalf("notice history lost or duplicated: %s", historyJSON)
		}
	}

	runTurn("disable")
	client.Close()
	h.stopCore(core)
	core = start(true)
	client, conversation = conversationClient(t, h)
	runWorker()
	runTurn("disabled-restart")
	client.Close()
	h.stopCore(core)
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("background model calls=%d", calls)
	}
}
