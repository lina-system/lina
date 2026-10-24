// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/replay"

	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestLiveRecallPersonAuxiliary(t *testing.T) {
	r, p := turnRuntime(t)
	source := queuedContextEvent(t, r, "recall-source", "My telescope is violet.", guard.EntryOwner, "")
	bindings := map[string]loop.Tool{}
	decls, err := r.prepareRecallTools("recall-turn", nil, bindings)
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := bindings["recall_person"]
	if !ok {
		t.Fatal("live recall_person is not registered")
	}
	declared := false
	for _, d := range decls {
		declared = declared || declarationName(d) == "recall_person"
	}
	if !declared {
		t.Fatal("live recall_person is not declared")
	}
	p.steps = []turnStep{{events: turnText(`{"sentences":[{"text":"Violet.","citations":[{"id":"event:recall-source","revision":0}]},{"text":"Uncited."},{"text":"Wrong revision.","citations":[{"id":"event:recall-source","revision":99}]}]}`), before: func(req provider.Request) {
		if req.Model != r.route.Model || req.Purpose != provider.Auxiliary || len(req.Tools) != 0 || req.Instructions != "" || len(req.Items) != 2 || req.Credential.Account != r.route.Request(nil).Credential.Account {
			t.Error("auxiliary did not use isolated conversation route")
		}
	}}}
	out := tool.Execute(t.Context(), provider.ToolCall{ID: "person-read", Name: "recall_person", Arguments: `{"question":"What color is my telescope?","query":"telescope","depth":1}`})
	var answer coreexec.PersonAnswer
	if out.Code != loop.OK || json.Unmarshal(out.Output, &answer) != nil || len(answer.Answer) != 1 || len(answer.Answer[0].Citations) != 1 || answer.Answer[0].Citations[0].ID != "event:"+source.ID {
		t.Fatalf("cited auxiliary: %+v", out)
	}
	if len(p.requests) != 1 || len(p.streams) != 1 || !p.streams[0].closed {
		t.Fatal("auxiliary must make and close exactly one call")
	}
}

func TestReplayRecallPersonAccountsOutsideCanon(t *testing.T) {
	r, p := turnRuntime(t)
	p.steps = []turnStep{{events: turnText("Understood.")}}
	input, inbox := admitTurnInput(t, r, "My telescope is violet.")
	if err := r.run(t.Context(), &loop.Inputs{}, input, inbox); err != nil {
		t.Fatal(err)
	}
	ep, err := r.replaySource().Load(t.Context(), replay.Scope{Person: r.person, Agent: r.agent}, "turn:"+input.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := func() int {
		n := 0
		if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
			return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM budget_usage").Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	citation := coreexec.Citation{ID: "event:" + input.ID}
	raw, _ := json.Marshal(map[string]any{"sentences": []coreexec.Sentence{{Text: "Violet.", Citations: []coreexec.Citation{citation}}}})
	p.requests = nil
	p.steps = []turnStep{{events: turnText(string(raw))}}
	var usage []provider.Meta
	reads, err := r.replayReads(r.replaySource(), ep, func(meta provider.Meta) { usage = append(usage, meta) })
	if err != nil {
		t.Fatal(err)
	}
	out := reads["recall_person"].Execute(t.Context(), provider.ToolCall{ID: "replay-person", Name: "recall_person", Arguments: `{"question":"Color?","query":"telescope","depth":1}`})
	var answer coreexec.PersonAnswer
	if out.Code != loop.OK || json.Unmarshal(out.Output, &answer) != nil || len(answer.Answer) != 1 || len(usage) != 1 || count() != before {
		t.Fatalf("replay accounting: result=%+v usage=%d", out, len(usage))
	}
}

func TestRecallPersonFailureAccountsAndPreservesSources(t *testing.T) {
	r, p := turnRuntime(t)
	queuedContextEvent(t, r, "failure-source", "My telescope is violet.", guard.EntryOwner, "")
	p.steps = []turnStep{{err: context.DeadlineExceeded}}
	bindings := map[string]loop.Tool{}
	if _, err := r.prepareRecallTools("failed-recall", nil, bindings); err != nil {
		t.Fatal(err)
	}
	out := bindings["recall_person"].Execute(t.Context(), provider.ToolCall{ID: "failed-person", Name: "recall_person", Arguments: `{"question":"Color?","query":"telescope","depth":1}`})
	var answer struct {
		coreexec.PersonAnswer
		Error string `json:"error"`
	}
	if out.Code != loop.Failed || json.Unmarshal(out.Output, &answer) != nil || len(answer.Sources) != 1 || answer.Error != "aux_timeout" {
		t.Fatalf("fallback=%+v", out)
	}
	var charged, reserved int64
	var kind string
	if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT charged,reserved,kind FROM budget_usage WHERE run=?", "failed-recall").Scan(&charged, &reserved, &kind)
	}); err != nil {
		t.Fatal(err)
	}
	if charged == 0 || charged != reserved || kind != string(provider.Auxiliary) {
		t.Fatal("failed call lost its reservation")
	}
	// A second request with no sources makes no call or reservation.
	out = bindings["recall_person"].Execute(t.Context(), provider.ToolCall{ID: "empty-person", Name: "recall_person", Arguments: `{"question":"No match?","query":"zebras","depth":1}`})
	if out.Code != loop.OK || len(p.requests) != 1 {
		t.Fatalf("empty recall called model: %+v", out)
	}
}

func TestRecallPersonDropsRetractedSources(t *testing.T) {
	r, p := turnRuntime(t)
	queuedContextEvent(t, r, "retracted-source", "My telescope is violet.", guard.EntryOwner, "")
	p.steps = []turnStep{{before: func(provider.Request) { forgetQueuedSource(t, r, "retracted-source") }, events: turnText(`{"sentences":[{"text":"Violet.","citations":[{"id":"event:retracted-source","revision":0}]}]}`)}}
	bindings := map[string]loop.Tool{}
	if _, err := r.prepareRecallTools("retracted-recall", nil, bindings); err != nil {
		t.Fatal(err)
	}
	out := bindings["recall_person"].Execute(t.Context(), provider.ToolCall{ID: "retracted-person", Name: "recall_person", Arguments: `{"question":"Color?","query":"telescope","depth":1}`})
	if out.Code == loop.OK || len(out.Output) != 0 {
		t.Fatalf("retracted source escaped: %+v", out)
	}
}
