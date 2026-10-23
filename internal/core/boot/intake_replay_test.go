// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/records"
	"os"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/intakereplay"
)

func TestIntakeReplayCore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	f, err := os.Open("../intakereplay/testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cases, rejected := intakereplay.Read(f)
	if len(rejected) != 0 {
		t.Fatal(rejected)
	}
	for _, c := range cases {
		if c.ID != "running-task" && c.ID != "option-number" {
			continue
		}
		for _, router := range intakereplay.Routers {
			t.Run(c.ID+"/"+router, func(t *testing.T) {
				got, err := ReplayIntakeCase(t.Context(), c, router)
				if err != nil {
					t.Fatal(err)
				}
				score := intakereplay.Score(c, got)
				if !reflect.DeepEqual(score, c.Expected[router]) {
					t.Fatalf("binding=%+v score=%+v want=%+v", got, score, c.Expected[router])
				}
			})
		}
	}
}

func TestIntakeReplayReadsPersistedState(t *testing.T) {
	r := controlRuntime(t)
	f, err := os.Open("../intakereplay/testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	cases, rejected := intakereplay.Read(f)
	f.Close()
	if len(rejected) != 0 {
		t.Fatal(rejected)
	}
	c := cases[0]
	c.State.Questions = []intakereplay.Question{{ID: "q", Target: c.State.Items[0].ID, Options: []string{"One", "Two"}}}
	c.Input.Recent = []intakereplay.Turn{{Role: "person", Text: "Earlier input"}, {Role: "agent", Text: "Earlier answer"}}
	if err = r.seedIntakeReplay(t.Context(), *c.State, c.Input); err != nil {
		t.Fatal(err)
	}
	state, input, err := r.readIntakeReplay(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, *c.State) || !reflect.DeepEqual(input, c.Input) {
		t.Fatalf("lost state: %+v %+v", state, input)
	}
	record, rev, err := r.records.Read(t.Context(), c.State.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	record.Data = bytes.Replace(record.Data, []byte("# Garden"), []byte("# Updated garden"), 1)
	if _, err = r.records.Commit(t.Context(), records.Change{ID: record.ID, Path: record.Path, Base: rev, Data: record.Data}, records.Actor{Person: r.person, Agent: r.agent}); err != nil {
		t.Fatal(err)
	}
	state, _, err = r.readIntakeReplay(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Items[0].Title != "Updated garden" {
		t.Fatal("router read fixture instead of Core records", state)
	}
	question, err := r.journal().Get(t.Context(), "replay-question-0")
	if err != nil {
		t.Fatal(err)
	}
	if question.SpeakerKind != guard.System {
		t.Fatal("question lost provenance", question)
	}
}
