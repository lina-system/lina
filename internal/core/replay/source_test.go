// SPDX-License-Identifier: Apache-2.0
package replay_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type readOnly struct{ db *canon.Store }

func (r readOnly) Read(ctx context.Context, f func(*canon.Tx) error) error { return r.db.Read(ctx, f) }
func fixture(t *testing.T) (*canon.Store, replay.Source, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	control, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = control.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { control.Close() })
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: control, Migrations: boot.ApplicationMigrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	source := replay.Source{Digest: assemble.Digest, DB: readOnly{db}, Persona: func(_ context.Context, turn, agent string) (agents.Preparation, error) {
		return agents.Preparation{TurnID: turn, AgentID: agent, Commit: "revision", Layers: agents.Layers{Core: "original instructions"}}, nil
	}, Project: func(events []ledger.Event, c, a string) ([]provider.Item, error) {
		return assemble.History(events, c, a)
	}}
	return db, source, root
}
func seed(t *testing.T, db *canon.Store, id string, entry guard.Entry, material ...ledger.Materials) string {
	t.Helper()
	ctx := t.Context()
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("original", 3600))
	in := ledger.Input{ID: id, ConversationID: "conversation", AgentID: "agent", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: clock, RecordedAt: clock, TimeQuality: "observed", Body: "violet PRIVATE", SecretSpans: []secrets.Span{{Start: 7, End: 14, Sensitivity: "secret"}}}
	if entry != guard.EntryOwner {
		in.PersonID = ""
	}
	var materials ledger.Materials
	if len(material) > 0 {
		materials = material[0]
		in.Body = strings.Repeat("large synthetic input ", 1000)
		in.SecretSpans = nil
	}
	event, err := ledger.New(db, materials).Admit(ctx, entry, in)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := inbox.New(db).Admit(ctx, inbox.Input{Key: inbox.Key{Conversation: "conversation", Source: "test", NativeID: id}, Body: []byte(event.Body.Text())})
	if err != nil {
		t.Fatal(err)
	}
	tid := "turn:" + id
	if err = turn.New(db).Claim(ctx, tid, "conversation", receipt.ID); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(event.Body.Text()))
	evidence := ledger.RequestEvidence{TurnID: tid, Class: provider.ModelAPI, Model: "original-model", Effort: "medium", Assembly: ledger.Assembly{AgentID: "agent", PersonaRevision: "revision", History: []ledger.Reference{{ID: id, Revision: hex.EncodeToString(digest[:])}}}}
	items, err := assemble.History([]ledger.Event{event}, "conversation", "agent")
	if err != nil {
		t.Fatal(err)
	}
	evidence.Hash = assemble.Digest(provider.Request{Model: evidence.Model, Effort: evidence.Effort, Instructions: "original instructions\n\n\n", Items: items, Purpose: provider.Turn})
	writer := turn.EvidenceWriter{Ledger: ledger.New(db, materials), ConversationID: "conversation", ParentID: id, Surface: ledger.Surface{Kind: "tui"}, Now: func() time.Time { return clock }}
	if err = writer.Acknowledge(ctx, turn.Acknowledgement{TurnID: tid, PolicyRevision: "policy", Assembly: evidence.Assembly}); err != nil {
		t.Fatal(err)
	}
	if err = writer.RecordRequest(ctx, evidence); err != nil {
		t.Fatal(err)
	}
	err = db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO turn_control(turn_id,input_event) VALUES(?,?)", tid, id)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE turn_runs SET state='completed' WHERE id=?", tid)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return tid
}
func selection() replay.Selection {
	return replay.Selection{Agent: "agent", Conversations: []string{"conversation"}, Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)}
}
func snapshot(t *testing.T, db *canon.Store) string {
	t.Helper()
	var all []string
	err := db.Read(t.Context(), func(tx *canon.Tx) error {
		for _, query := range []string{"SELECT id||':'||COALESCE(body,'') FROM conv_event ORDER BY id", "SELECT turn_id||':'||hex(item) FROM turn_items ORDER BY turn_id,round,ordinal", "SELECT turn_id||':'||hex(call)||':'||COALESCE(hex(result),'') FROM turn_calls ORDER BY turn_id,round,ordinal", "SELECT effect_key||':'||hex(record) FROM authority_effects ORDER BY effect_key", "SELECT id||':'||state FROM turn_runs ORDER BY id"} {
			rows, err := tx.QueryContext(t.Context(), query)
			if err != nil {
				return err
			}
			for rows.Next() {
				var v string
				if err = rows.Scan(&v); err != nil {
					rows.Close()
					return err
				}
				all = append(all, v)
			}
			if err = rows.Close(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(all, "\n")
}
func TestExportSelectionMaskingImmutableSourcesAndNoWrites(t *testing.T) {
	db, source, root := fixture(t)
	scope := replay.Scope{Person: "person", Agent: "agent"}
	id := seed(t, db, "input", guard.EntryOwner)
	seed(t, db, "notice", guard.EntrySystem)
	before := snapshot(t, db)
	episodes, m, err := source.Episodes(t.Context(), scope, replay.Selection{})
	if err != nil || len(episodes) != 0 || m.Selected != 0 {
		t.Fatalf("default selection: %+v %v", m, err)
	}
	episodes, m, err = source.Episodes(t.Context(), scope, selection())
	if err != nil || len(episodes) != 1 || m.LeftOut != 1 || m.Reasons["not_person_turn"] != 1 {
		t.Fatalf("manifest: %+v %v", m, err)
	}
	ep := episodes[0]
	if ep.TurnID != id || ep.Clock.Hour() != 2 || ep.Zone != "UTC" || ep.Evidence.Model != "original-model" || ep.Persona.Core != "original instructions" {
		t.Fatalf("wrong original source: %+v", ep)
	}
	raw, _ := json.Marshal(ep)
	if strings.Contains(string(raw), "PRIVATE") {
		t.Fatal("explicit secret span leaked")
	}
	path, m, err := source.StageExport(t.Context(), root, scope, selection())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, filepath.Join(root, "staging/export/")) {
		t.Fatal("export escaped staging")
	}
	data, err := os.ReadFile(filepath.Join(path, "episodes.jsonl"))
	if err != nil || strings.Contains(string(data), "PRIVATE") {
		t.Fatalf("export masking: %v", err)
	}
	if after := snapshot(t, db); after != before {
		t.Fatal("export changed recorded turns or effect ledger")
	}
	// A source exclusion hides the entire dependent turn, not just the input.
	err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO retract_block(item_id,person,holder,event_id,conversation_id) VALUES('forgotten','person','agent','input','')")
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(t.Context(), "UPDATE retract_meta SET epoch=epoch+1")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, m, err = source.Episodes(t.Context(), scope, selection())
	if err != nil || m.Exported != 0 || m.LeftOut != 2 || m.Reasons["restricted_content"] != 1 {
		t.Fatalf("revoked export: %+v %v", m, err)
	}
	if source.Validate(t.Context(), ep.RevocationEpoch) == nil {
		t.Fatal("accepted stale revocation epoch")
	}
}
func TestReplaySourceRejectsForeignPersonAndUnrecordedAssembly(t *testing.T) {
	db, source, _ := fixture(t)
	seed(t, db, "input", guard.EntryOwner)
	if _, err := source.Load(t.Context(), replay.Scope{Person: "other", Agent: "agent"}, "turn:input"); err == nil {
		t.Fatal("foreign person read source")
	}
	// A changed history revision must not silently select today's source.
	source.Persona = func(context.Context, string, string) (agents.Preparation, error) {
		return agents.Preparation{Commit: "newer"}, nil
	}
	if _, err := source.Load(t.Context(), replay.Scope{Person: "person", Agent: "agent"}, "turn:input"); err == nil {
		t.Fatal("new persona replaced pinned source")
	}
}

func TestRecordedEffectReceiptIsReadWithoutRecoveryOrDispatch(t *testing.T) {
	db, source, _ := fixture(t)
	id := seed(t, db, "input", guard.EntryOwner)
	call := provider.ToolCall{ID: "call", Name: "write_file", Arguments: `{"path":"note","content":"violet"}`}
	raw, _ := json.Marshal(call)
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO turn_calls(turn_id,round,ordinal,call_id,call) VALUES(?,0,0,?,?)", id, call.ID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := replay.Scope{Person: "person", Agent: "agent"}
	if _, err := source.Load(t.Context(), scope, id); err == nil {
		t.Fatal("missing effect result was accepted")
	}
	effect := authority.Effect{Key: authority.EffectKey(id, call.Name, "workspace", call.ID), Tool: call.Name, Target: "workspace", Person: scope.Person, Agent: scope.Agent, State: tools.Applied, Result: tools.Result{State: tools.Applied, Body: json.RawMessage(`{"receipt":"retained"}`)}}
	receipt, _ := json.Marshal(effect)
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO authority_effects(effect_key,fingerprint,record,running) VALUES(?,'fingerprint',?,0)", effect.Key, receipt)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, db)
	ep, err := source.Load(t.Context(), scope, id)
	if err != nil || len(ep.Calls) != 1 || ep.Calls[0].Result.Code != loop.OK || ep.Calls[0].EffectKey != effect.Key {
		t.Fatalf("receipt=%+v err=%v", ep.Calls, err)
	}
	if after := snapshot(t, db); after != before {
		t.Fatal("reading effect receipt mutated Core")
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE authority_effects SET running=1 WHERE effect_key=?", effect.Key)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Load(t.Context(), scope, id); err == nil {
		t.Fatal("running effect was treated as recorded success")
	}
}

func TestReplayRejectsReconstructionThatDoesNotMatchOriginalRequest(t *testing.T) {
	db, source, _ := fixture(t)
	id := seed(t, db, "input", guard.EntryOwner)
	source.Project = func([]ledger.Event, string, string) ([]provider.Item, error) {
		return []provider.Item{json.RawMessage(`{"role":"user","content":"changed"}`)}, nil
	}
	_, err := source.Load(t.Context(), replay.Scope{Person: "person", Agent: "agent"}, id)
	if err == nil || err.Error() != "missing_assembly" {
		t.Fatalf("changed request accepted: %v", err)
	}
}

func TestExportRefusesPrivateEvidenceAndDirectRetractions(t *testing.T) {
	for _, kind := range []string{"private", "retracted"} {
		t.Run(kind, func(t *testing.T) {
			db, source, _ := fixture(t)
			id := seed(t, db, "input", guard.EntryOwner)
			err := db.Write(t.Context(), func(tx *canon.Tx) error {
				if kind == "private" {
					_, err := tx.ExecContext(t.Context(), `INSERT INTO memory_item(id,person,holder,subject_kind,subject_id,normalized,content,scope,state,valid_from,valid_until,supersedes,standing,record)
      VALUES('private-item','person','all','person','person','synthetic','synthetic','private','active',0,0,'',0,'{"evidence":[{"kind":"event","id":"input"}]}')`)
					return err
				}
				if _, err := tx.ExecContext(t.Context(), "INSERT INTO removal_applied VALUES(1,'removal','digest')"); err != nil {
					return err
				}
				_, err := tx.ExecContext(t.Context(), "INSERT INTO retract_target VALUES('event','input','person','','agent','trashed',0,'{}','removal')")
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = source.Load(t.Context(), replay.Scope{Person: "person", Agent: "agent"}, id); err == nil {
				t.Fatal("restricted source exported")
			}
			_, manifest, err := source.Episodes(t.Context(), replay.Scope{Person: "person", Agent: "agent"}, selection())
			if err != nil || manifest.LeftOut != 1 || manifest.Exported != 0 {
				t.Fatalf("%+v %v", manifest, err)
			}
		})
	}
}

func TestExportExcludesTurnDependingOnForgottenRecallResult(t *testing.T) {
	db, source, _ := fixture(t)
	id := seed(t, db, "input", guard.EntryOwner)
	call := provider.ToolCall{ID: "recall", Name: "recall_person", Arguments: `{"question":"What is remembered?","query":"violet"}`}
	result := loop.Result{ID: call.ID, Code: loop.OK, Output: json.RawMessage(`{"Sources":[{"ID":"memory:forgotten","Revision":1,"Text":"revoked synthetic detail","Evidence":[]}],"Answer":[]}`)}
	raw, _ := json.Marshal(call)
	answer, _ := json.Marshal(result)
	err := db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO turn_calls(turn_id,round,ordinal,call_id,call,result) VALUES(?,0,0,?,?,?)", id, call.ID, raw, answer); err != nil {
			return err
		}
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO removal_applied VALUES(1,'removal','digest')"); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), "INSERT INTO retract_target VALUES('memory','forgotten','person','agent','agent','erased',0,'{}','removal')")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, m, err := source.Episodes(t.Context(), replay.Scope{Person: "person", Agent: "agent"}, selection())
	if err != nil || m.Exported != 0 || m.LeftOut != 1 {
		t.Fatalf("forgotten tool evidence exported: %+v %v", m, err)
	}
}

func TestExportExcludesRetractedReplyEvenWhenInputRemainsVisible(t *testing.T) {
	db, source, _ := fixture(t)
	id := seed(t, db, "input", guard.EntryOwner)
	now := time.Date(2026, 1, 2, 2, 4, 6, 0, time.UTC)
	replyID := id + ":1:0"
	_, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryAgent, ledger.Input{ID: replyID, ConversationID: "conversation", ParentID: "input", AgentID: "agent", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "removed synthetic reply"})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO turn_items(turn_id,round,ordinal,item) VALUES(?,0,0,?)", id, []byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"removed synthetic reply"}]}`)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO removal_applied VALUES(1,'removal','digest')"); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), "INSERT INTO retract_target VALUES('event',?,'person','','agent','trashed',0,'{}','removal')", replyID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, m, err := source.Episodes(t.Context(), replay.Scope{Person: "person", Agent: "agent"}, selection())
	if err != nil || m.Exported != 0 || m.LeftOut != 1 {
		t.Fatalf("retracted reply exported: %+v %v", m, err)
	}
}
