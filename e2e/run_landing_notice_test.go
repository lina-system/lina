// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/lane"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestM1RunLandingNotice(t *testing.T) {
	data, err := os.ReadFile("m1/run-landing-notice.json")
	if err != nil {
		t.Fatal(err)
	}
	var script struct {
		Goal, Scope, Done, Result, Summary, Artifact string
		NotTouch                                     string `json:"not_touch"`
	}
	if err := json.Unmarshal(data, &script); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, "")
	controls, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	ms := canon.Migrations()
	ms = append(ms, canon.Migration{Version: len(ms) + 1, SQL: runs.Schema + runs.RunningListSchema + lane.NoticeSchema})
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: ms})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	materials, err := assets.Open(h.ctx, h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { materials.Close() })
	background, err := runs.New(runs.Config{Epoch: 1, DB: db, Assets: materials, WorkDir: filepath.Join(h.root, "work"), Authorize: func(context.Context, runs.Origin, runs.Brief) error { return nil }, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
		artifact, _ := json.Marshal(runs.Artifact{Body: script.Artifact, Done: true})
		message, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": string(artifact)}}})
		return &scenarioRunStream{events: []provider.Event{{Kind: provider.ItemDone, Item: message}, {Kind: provider.Terminal}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { background.Close() })
	notices := lane.NewNotices(db, nil)
	delivered := make(chan lane.Delivery, 1)
	notices.OnDelivered = func(d lane.Delivery) { delivered <- d }
	lanes := lane.NewWithNotices(notices)
	t.Cleanup(func() { lanes.Close(context.Background()) })
	started, release := make(chan struct{}), make(chan struct{})
	turn, err := lanes.Submit(h.ctx, "conversation", func(context.Context) error { close(started); <-release; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	select {
	case <-started:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	origin := runs.Origin{Person: "person", Agent: "agent", Conversation: "conversation", Turn: "turn"}
	id, _, err := background.Start(h.ctx, origin, runs.Brief{Goal: script.Goal, Scope: script.Scope, NotTouch: script.NotTouch, Done: script.Done, Result: script.Result, Checks: []runs.Check{{Name: "comparison", Kind: runs.Claimed}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := background.Release(h.ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := background.Wait(h.ctx, id); err != nil {
		t.Fatal(err)
	}
	list := runs.NewRunningList(db, map[string]runs.ListSource{"run": runs.RunListSource{}})
	snapshot, err := list.Build(h.ctx, origin.Person)
	if err != nil || len(snapshot.Entries) != 1 || snapshot.Entries[0].View.State != "ready_for_review" {
		t.Fatalf("native list: %+v %v", snapshot, err)
	}
	transfer := func(ctx context.Context, n runs.Notice) error {
		return lanes.Notice(ctx, lane.Notice{ID: n.Key, Run: n.ID, Person: n.Origin.Person, Agent: n.Origin.Agent, Conversation: n.Origin.Conversation, Outcome: string(n.Outcome), Summary: script.Summary, Result: n.Material.ID, ResultRevision: n.Material.Revision, ResultType: n.Result, Landed: n.Landed, Surface: ledger.Surface{Kind: "tui"}})
	}
	for range 2 {
		if err := background.TransferNotices(h.ctx, origin.Conversation, transfer); err != nil {
			t.Fatal(err)
		}
	}
	before, err := notices.Delivered(h.ctx, origin.Conversation)
	if err != nil || len(before) != 0 {
		t.Fatalf("delivered during reply: %v %v", before, err)
	}
	close(release)
	if err := <-turn; err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-delivered:
		if len(d.Notices) != 1 || d.Notices[0].Run != id || d.Notices[0].Result == "" || d.Notices[0].ResultRevision <= 0 {
			t.Fatalf("landing: %+v", d)
		}
		encoded, _ := json.Marshal(d)
		if strings.Contains(string(encoded), script.Artifact) {
			t.Fatal("artifact body entered notice")
		}
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	pending, err := background.Notices(h.ctx, origin.Conversation)
	if err != nil || len(pending) != 0 {
		t.Fatalf("transfer not acknowledged: %v %v", pending, err)
	}
	if err := list.Open(h.ctx, origin.Person, snapshot.Entries[0].Ref); err != nil {
		t.Fatal(err)
	}
	snapshot, err = list.Build(h.ctx, origin.Person)
	if err != nil || len(snapshot.Entries) != 0 {
		t.Fatalf("opened landing retained: %+v %v", snapshot, err)
	}
}

type scenarioRunStream struct{ events []provider.Event }

func (s *scenarioRunStream) Next(context.Context) (provider.Event, error) {
	if len(s.events) == 0 {
		return provider.Event{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func (*scenarioRunStream) Meta() provider.Meta { return provider.Meta{} }
func (*scenarioRunStream) Close() error        { return nil }
