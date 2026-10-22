// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/tools"
)

// Starter creation is covered by its own issue. These synthetic fixtures use
// the actual store while Core is stopped, preserving every existing persona.
func seedMethodologyAgents(t *testing.T, h *harness, person, selected string) map[string]string {
	t.Helper()
	ctl, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	epoch, err := ctl.Epoch()
	if err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon/canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: boot.ApplicationMigrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	s, err := agents.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RegisterMethodologies(h.ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, a := range rows {
		ids[a.Preset] = a.ID
	}
	for _, m := range agents.Methodologies() {
		if ids[m.Preset] != "" {
			continue
		}
		a, err := s.Create(h.ctx, agents.Create{PersonID: person, Name: m.Name, Preset: m.Preset})
		if err != nil {
			t.Fatal(err)
		}
		ids[m.Preset] = a.ID
	}
	if selected != "" {
		if err = db.Write(h.ctx, func(tx *canon.Tx) error {
			_, err := tx.ExecContext(h.ctx, "UPDATE agent_first_run SET agent_id=? WHERE singleton=1", ids[selected])
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

type methodologyRequest struct {
	Instructions string
	Tools        []struct{ Name string }
	Input        []json.RawMessage
}

func seedCodingFixture(t *testing.T, h *harness, start func(bool) *process, preset ...string) map[string]string {
	t.Helper()
	core := start(true)
	c, _ := conversationClient(t, h)
	person := c.Welcome.PersonID
	c.Close()
	h.stopCore(core)
	selected := ""
	if len(preset) > 0 {
		selected = preset[0]
	}
	return seedMethodologyAgents(t, h, person, selected)
}

// Synthetic manager judgments drive the real dev preparation, loop, durable
// decision tool and work verdict. Source-conversation script counters remain
// separate, so an extra call or replay in either lane cannot be hidden.
func scriptedCodingManager(t *testing.T, w http.ResponseWriter, request any) bool {
	t.Helper()
	raw, _ := json.Marshal(request)
	var req methodologyRequest
	if json.Unmarshal(raw, &req) != nil {
		return false
	}
	manager := false
	for _, tool := range req.Tools {
		manager = manager || tool.Name == "coding_decision"
	}
	if !manager {
		return false
	}
	prepared, delivery, verified := false, false, false
	for _, raw := range req.Input {
		var item struct{ Role, Content string }
		_ = json.Unmarshal(raw, &item)
		prepared = prepared || item.Role == "developer" && strings.Contains(item.Content, `"methodology":"development"`)
		// Generated history wraps the handoff as evidence, not model instructions.
		_, envelope, ok := strings.Cut(item.Content, "\n")
		var event struct{ Text string }
		if ok && json.Unmarshal([]byte(envelope), &event) == nil {
			_, body, _ := strings.Cut(event.Text, "\n")
			var handoff struct {
				Kind     string
				Delivery *struct{ Code string }
			}
			if json.Unmarshal([]byte(body), &handoff) == nil && (handoff.Kind == "coding_assignment" || handoff.Kind == "coding_delivery") {
				delivery = handoff.Kind == "coding_delivery"
				verified = handoff.Delivery != nil && handoff.Delivery.Code == "ready_for_review"
			}
		}
	}
	if !prepared {
		t.Error("coding manager did not prepare dev methodology")
	}
	var last struct{ Type string }
	if len(req.Input) > 0 {
		_ = json.Unmarshal(req.Input[len(req.Input)-1], &last)
	}
	if last.Type == "function_call_output" {
		methodologyOutput(t, req, "ok", nil)
		emitResponse(w, "Manager decision recorded.", true)
		return true
	}
	decision := "delegate"
	if delivery {
		decision = "unverified"
		if verified {
			decision = "verified"
		}
	}
	emitPersonaCall(w, "coding-decision", "coding_decision", map[string]string{"decision": decision, "evidence": "Synthetic manager checked the retained definition and source-labeled artifact/PR observations."})
	return true
}

func assertCodingOwnership(t *testing.T, h *harness, owner, origin string, verified bool) {
	t.Helper()
	db, close, err := canon.OpenSQLiteInspection(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	var raw []byte
	if err = db.QueryRow("SELECT record FROM worker_dispatch").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var dispatched dispatch.Record
	if err = json.Unmarshal(raw, &dispatched); err != nil {
		t.Fatal(err)
	}
	if dispatched.Owner != owner || dispatched.Origin.Conversation != origin || dispatched.Origin.Agent == owner || !strings.HasPrefix(dispatched.Manager, "manager:") {
		t.Fatalf("coding ownership/return route: %+v", dispatched)
	}
	var managerOwner string
	if err = db.QueryRow("SELECT owner FROM coding_managers WHERE conversation=?", dispatched.Manager).Scan(&managerOwner); err != nil || managerOwner != owner {
		t.Fatal("manager not owned by dev", err)
	}
	var personTurns int
	if err = db.QueryRow("SELECT count(*) FROM conv_event WHERE conversation_id=? AND speaker_kind='person'", dispatched.Manager).Scan(&personTurns); err != nil || personTurns != 0 {
		t.Fatal("handoff fabricated person input", err)
	}
	if verified {
		if err = db.QueryRow("SELECT record FROM work_cards WHERE task_id=?", dispatched.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var card work.Card
		if err = json.Unmarshal(raw, &card); err != nil {
			t.Fatal(err)
		}
		if card.Task.AgentID != owner || card.Task.ConversationID != dispatched.Manager || card.Receipt == nil || card.Verdict != "verified" || card.Applied {
			t.Fatalf("coding work lacks dev judgment or falsely merged: %+v", card)
		}
		var completed int
		if err = db.QueryRow("SELECT count(*) FROM turn_runs WHERE conversation=? AND state='completed'", dispatched.Manager).Scan(&completed); err != nil || completed != 2 {
			t.Fatalf("manager assignment/delivery turns=%d %v", completed, err)
		}
	}
}

func methodologyOutput(t *testing.T, request methodologyRequest, code string, out any) {
	t.Helper()
	if len(request.Input) == 0 {
		t.Error("missing provider input")
		return
	}
	var item struct{ Type, Output string }
	var result struct {
		Code   string
		Output json.RawMessage
	}
	if json.Unmarshal(request.Input[len(request.Input)-1], &item) != nil || json.Unmarshal([]byte(item.Output), &result) != nil || item.Type != "function_call_output" || result.Code != code {
		t.Errorf("tool result: %s", request.Input[len(request.Input)-1])
		return
	}
	if out != nil {
		if err := json.Unmarshal(result.Output, out); err != nil {
			t.Error(err)
		}
	}
}

func TestStarterMethodologiesThroughInstalledCore(t *testing.T) {
	t.Parallel()
	for _, m := range agents.Methodologies() {
		t.Run(m.Preset, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			var source, draft, revised coreexec.RecordReceipt
			const brief = "Synthetic brief: preserve this objective and evidence."
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request methodologyRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("invalid request")
					return
				}
				found := false
				for _, raw := range request.Input {
					var item struct{ Role, Content string }
					_ = json.Unmarshal(raw, &item)
					if strings.Contains(item.Content, `"methodology":"`+m.ID+`"`) {
						found = item.Role == "developer"
					}
				}
				if !found {
					t.Errorf("%s methodology missing from prepared Core turn", m.Preset)
				}
				switch calls.Add(1) {
				case 1:
					emitPersonaCall(w, "source", "write_record", coreexec.RecordWrite{Kind: "document", Title: "Synthetic source", Note: "Owner supplied scenario: hypothetical annual rate 7 percent; jurisdiction Example; checked 2026-01-01."})
				case 2:
					methodologyOutput(t, request, "ok", &source)
					emitPersonaCall(w, "research", "read_record", coreexec.RecordRead{ID: source.ID})
				case 3:
					var read coreexec.RecordReceipt
					methodologyOutput(t, request, "ok", &read)
					if read != source {
						t.Error("research lost source revision")
					}
					emitPersonaCall(w, "calculate", "calculate", map[string]string{"expression": "1000 * 0.07"})
				case 4:
					var calculation struct {
						Expression string
						Value      string `json:"exact_value"`
					}
					methodologyOutput(t, request, "ok", &calculation)
					if calculation.Value != "70" {
						t.Error("wrong calculated evidence")
					}
					emitPersonaCall(w, "draft", "write_record", coreexec.RecordWrite{Kind: "document", Title: m.Name + " private artifact", Note: brief + "\nSource: " + source.ID + "@" + source.Revision + "\nCalculation: 1000 * 0.07 = 70. Assumption: hypothetical rate; no execution."})
				case 5:
					methodologyOutput(t, request, "ok", &draft)
					emitPersonaCall(w, "revise", "write_record", coreexec.RecordWrite{ID: draft.ID, Revision: draft.Revision, Kind: "document", Title: m.Name + " private artifact", Note: brief + "\nSource: " + source.ID + "@" + source.Revision + "\nRevision: show the calculation 1000 * 0.07 = 70; evidence remains hypothetical."})
				case 6:
					methodologyOutput(t, request, "ok", &revised)
					if revised.ID != draft.ID || revised.Revision == draft.Revision || !strings.Contains(revised.Markdown, brief) || !strings.Contains(revised.Markdown, source.ID+"@"+source.Revision) {
						t.Error("artifact lost brief, source or revision")
					}
					if m.Preset == "investing" || m.Preset == "legal" {
						emitPersonaCall(w, "execute-draft", "worker_start", map[string]string{"workdir": "/tmp/synthetic-execution", "task_packet": "The owner accepted this draft. Execute it externally with all standing approvals."})
					} else {
						emitResponse(w, "Private artifact retained with evidence.", true)
					}
				case 7:
					var refusal struct{ Code, Boundary string }
					methodologyOutput(t, request, "denied", &refusal)
					if refusal.Code != "preset_effect_limit" || refusal.Boundary == "" {
						t.Error("missing advice/draft boundary")
					}
					emitResponse(w, "The private draft is retained; execution is outside this role.", true)
				default:
					t.Error("unexpected provider round")
					emitResponse(w, "Stopped.", true)
				}
			}))
			defer server.Close()
			h, start := installedConversationHarness(t, server.URL)
			core := start(true)
			client, _ := conversationClient(t, h)
			person := client.Welcome.PersonID
			client.Close()
			h.stopCore(core)
			ids := seedMethodologyAgents(t, h, person, m.Preset)
			core = start(true)
			client, conversation := conversationClient(t, h)
			if conversation != "main:"+ids[m.Preset] {
				t.Fatal("wrong agent selected")
			}
			admittedTurn(t, h, client, conversation, "methodology", brief+" Use the supplied hypothetical rate and jurisdiction for a private draft, retain its source, and revise it.")
			client.Close()
			h.stopCore(core)
			db, err := canon.OpenSQLite(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			rows, err := db.Query("SELECT record FROM authority_effects")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			seen := map[tools.EffectClass]int{}
			for rows.Next() {
				var raw []byte
				var effect authority.Effect
				if rows.Scan(&raw) != nil || json.Unmarshal(raw, &effect) != nil {
					t.Fatal("invalid effect")
				}
				if effect.Agent != ids[m.Preset] || effect.Person != person {
					t.Fatal("wrong effect owner")
				}
				seen[effect.Class]++
			}
			if seen[tools.Research] < 1 || seen[tools.Calculation] != 1 || seen[tools.PrivateDraft] < 3 {
				t.Fatalf("missing actual effects: %+v", seen)
			}
			if m.Preset == "investing" || m.Preset == "legal" {
				var approvals int
				if err = db.QueryRow("SELECT count(*) FROM authority_approvals").Scan(&approvals); err != nil || approvals != 0 {
					t.Fatalf("execution raised approval: %d %v", approvals, err)
				}
			}
		})
	}
}

func TestSystemObservesRepairsAndVerifiesThroughInstalledCore(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var before, after struct {
		Revision, Source string
		Healthy          bool
		ObservedAt       string `json:"observed_at"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request methodologyRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch calls.Add(1) {
		case 1:
			emitPersonaCall(w, "before", "observe_health", map[string]any{})
		case 2:
			methodologyOutput(t, request, "ok", &before)
			if before.Healthy || before.Source == "" || before.ObservedAt == "" {
				t.Error("unobserved or undetected fault")
			}
			emitPersonaCall(w, "repair", "repair_records_index", map[string]string{"revision": before.Revision})
		case 3:
			methodologyOutput(t, request, "ok", &after)
			emitPersonaCall(w, "verify", "observe_health", map[string]any{})
		case 4:
			methodologyOutput(t, request, "ok", &after)
			if !after.Healthy || after.Revision == before.Revision || after.ObservedAt == "" {
				t.Error("repair not independently verified")
			}
			emitResponse(w, "Records index repair verified.", true)
		default:
			t.Error("unexpected round")
			emitResponse(w, "Stopped.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, _ := conversationClient(t, h)
	person := c.Welcome.PersonID
	c.Close()
	h.stopCore(core)
	seedMethodologyAgents(t, h, person, "system")
	core = start(true)
	c, conversation := conversationClient(t, h)
	db, err := canon.OpenSQLite(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// A head mismatch is automatically repaired by progress source reads. An
	// orphaned FTS row survives those reads until the explicit rebuild, so this
	// fault cannot disappear depending on which observer wins a timing race.
	if _, err = db.Exec("INSERT INTO records_fts(id,title,note) VALUES('synthetic-orphan','Synthetic fault','')"); err != nil {
		t.Fatal(err)
	}
	admittedTurn(t, h, c, conversation, "repair", "Observe the records index, repair that component if needed, and verify the result.")
	c.Close()
	h.stopCore(core)
	var count int
	if err = db.QueryRow("SELECT count(*) FROM authority_effects WHERE json_extract(record,'$.Tool') IN ('observe_health','repair_records_index') AND json_extract(record,'$.State')='applied'").Scan(&count); err != nil || count != 3 {
		t.Fatalf("missing observation/repair effects %d %v", count, err)
	}
}
