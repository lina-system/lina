// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestProtectedEvidenceAcrossLiveAndReplayReads(t *testing.T) {
	for _, visibility := range []memory.Scope{memory.Local, memory.Private, memory.NeverLeaves} {
		t.Run(string(visibility), func(t *testing.T) {
			r, _ := turnRuntime(t)
			admit := func(id, body string) ledger.Event {
				t.Helper()
				e, err := ledger.New(r.db, r.materials).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: id, ConversationID: r.conversation, PersonID: r.person, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(1, 0), RecordedAt: time.Unix(1, 0), TimeQuality: "observed", Body: body})
				if err != nil {
					t.Fatal(err)
				}
				return e
			}
			public := admit("a-public-source", "The aurora public plan.")
			r.conversation = "protected-history"
			hidden := admit("protected-source", "The aurora protected plan.")
			publish := func(id string, children []string, event string) {
				t.Helper()
				s := compaction.New(r.db)
				frontier, err := s.Frontier(t.Context(), r.conversation)
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.Publish(t.Context(), compaction.Proposal{Expected: frontier, ID: id, Children: children, Cover: []compaction.Coverage{{EventID: event}}, Output: compaction.Output{Text: "aurora summary", Model: "fixture", PromptVersion: "1", BuiltAt: time.Now(), Dropped: "none"}})
				if err != nil {
					t.Fatal(err)
				}
			}
			publish("protected-child", nil, hidden.ID)
			later := admit("later-source", "An independent later event.")
			publish("protected-parent", []string{"protected-child"}, later.ID)
			for _, id := range []string{"protected-copy", "protected-view"} {
				in := ledger.Input{ID: id, ConversationID: "copies", PersonID: r.person, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(2, 0), RecordedAt: time.Unix(2, 0), TimeQuality: "observed", Body: hidden.Body.Text()}
				if id == "protected-copy" {
					in.ReplayOf = hidden.ID
				} else {
					in.Relations = []ledger.Relation{{Kind: "view_of", TargetID: public.ID, Ordinal: 0}, {Kind: "view_of", TargetID: "protected-copy", Ordinal: 1}}
				}
				if _, err := ledger.New(r.db, r.materials).Admit(t.Context(), guard.EntryOwner, in); err != nil {
					t.Fatal(err)
				}
			}
			remember := func(content string, scope memory.Scope, basis memory.Basis, evidence memory.Evidence) memory.Item {
				t.Helper()
				var item memory.Item
				err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
					var err error
					item, err = memory.Insert(t.Context(), tx, memory.Write{Person: r.person, Agent: r.agent, Item: memory.Item{Kind: memory.Fact, Content: content, Scope: scope, Subject: memory.Subject{Kind: memory.World, ID: "observatory"}, Basis: basis, Evidence: []memory.Evidence{evidence}}}, func(context.Context, *canon.Tx, memory.Evidence) (memory.Origin, error) {
						return memory.Origin{Person: r.person, Agent: r.agent, Speaker: guard.Person, Scope: memory.Local}, nil
					})
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				return item
			}
			item := remember("The aurora source memory.", memory.Local, memory.Stated, memory.Evidence{Kind: memory.Event, ID: hidden.ID, Precision: memory.Exact})
			ref := memory.Evidence{Kind: memory.MemoryItem, ID: item.ID, Revision: item.Revision, Precision: memory.Exact}
			derived := remember("The aurora derived conclusion.", memory.Local, memory.Derived, ref)
			remember("Sensitive umbrella classification.", visibility, memory.Derived, ref)
			live := map[string]loop.Tool{}
			if _, err := r.prepareRecallTools("restriction-turn", nil, live); err != nil {
				t.Fatal(err)
			}
			source := r.replaySource()
			epoch, err := source.Epoch(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			replayed, err := r.replayReads(source, replay.Episode{TurnID: "replayed", RevocationEpoch: epoch})
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []struct {
				name                string
				tools               map[string]loop.Tool
				projection, allowed bool
			}{{"live", live, false, visibility != memory.NeverLeaves}, {"replay", replayed, true, visibility == memory.Local}} {
				for _, page := range []struct {
					id     string
					offset int
				}{{"event:" + hidden.ID, 0}, {"event:protected-copy", 0}, {"event:protected-view", 0}, {"node:protected-child", 0}, {"node:protected-parent", 1}, {"event:" + public.ID, 0}} {
					args, _ := json.Marshal(map[string]any{"id": page.id, "offset": page.offset, "limit": 1})
					out := path.tools["expand"].Execute(t.Context(), provider.ToolCall{ID: page.id, Name: "expand", Arguments: string(args)})
					allowed := path.allowed || page.id == "event:"+public.ID
					if (out.Code == loop.OK) != allowed {
						t.Errorf("%s expand %s offset %d: code=%s, allowed=%v", path.name, page.id, page.offset, out.Code, allowed)
					}
				}
				out := path.tools["recall"].Execute(t.Context(), provider.ToolCall{ID: "recall", Name: "recall", Arguments: `{"query":"aurora","include_replay":true,"limit":100}`})
				if out.Code != loop.OK || !strings.Contains(string(out.Output), public.ID) {
					t.Fatalf("%s recall lost public source: %+v", path.name, out)
				}
				if !path.allowed {
					for _, id := range []string{hidden.ID, "protected-copy", "protected-view", "protected-child", "protected-parent", item.ID, derived.ID} {
						if strings.Contains(string(out.Output), id) {
							t.Errorf("%s recall exposed %s", path.name, id)
						}
					}
				}
				hits, err := r.memoryStore().Items(t.Context(), memory.RecallScope{Person: r.person, Agent: r.agent, ExternalModel: true, Projection: path.projection}, []string{item.ID, derived.ID}, time.Now().Unix(), false)
				if err != nil || (len(hits) == 2) != path.allowed || !path.allowed && len(hits) != 0 {
					t.Errorf("%s memory projection: %d hits, allowed=%v, err=%v", path.name, len(hits), path.allowed, err)
				}
			}
		})
	}
}
