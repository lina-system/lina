// SPDX-License-Identifier: Apache-2.0
package intakereplay

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestRuntimeExclusionReasons(t *testing.T) {
	raw, err := os.ReadFile("testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Split(string(raw), "\n")[0]
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{context.DeadlineExceeded, "runtime_timeout"},
		{context.Canceled, "runtime_cancelled"},
		{errors.Join(ErrModelReplay, ErrModelRoundLimit), "runtime_model_round_limit"},
		{errors.Join(ErrModelReplay, errors.New("untrusted provider detail")), "runtime_model_failure"},
		{errors.New("untrusted runtime detail"), "runtime_error"},
	} {
		report := Run(t.Context(), strings.NewReader(first), func(context.Context, Case, string) ([]Binding, error) { return nil, tc.err }, "core-model")
		if report.Scored != 0 || report.ExclusionReasons[tc.reason] != 1 {
			t.Fatalf("lost exclusion reason: %+v", report.ExclusionReasons)
		}
		raw, _ := json.Marshal(report)
		if strings.Contains(string(raw), "untrusted") {
			t.Fatal("raw error leaked into report")
		}
	}
}

func TestSyntheticScores(t *testing.T) {
	f, err := os.Open("testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cases, rejected := Read(f)
	if len(rejected) != 0 || len(cases) < 15 {
		t.Fatalf("cases=%d rejected=%v", len(cases), rejected)
	}
	for _, c := range cases {
		for _, router := range Routers {
			t.Run(c.ID+"/"+router, func(t *testing.T) {
				got := Score(c, Decide(router, *c.State, c.Input))
				want, ok := c.Expected[router]
				if !ok || !reflect.DeepEqual(got, want) {
					t.Fatalf("score: %+v; want %+v", got, want)
				}
			})
		}
	}
}

func TestMalformedCasesNeverReachCore(t *testing.T) {
	raw, err := os.ReadFile("testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Split(strings.TrimSpace(string(raw)), "\n")[0]
	var c map[string]any
	if err = json.Unmarshal([]byte(first), &c); err != nil {
		t.Fatal(err)
	}
	delete(c, "gold")
	missingGold, _ := json.Marshal(c)
	input := first + "\n" + first + "\n{}\nnull\n{\n" + string(missingGold) + "\n"
	report := Run(context.Background(), strings.NewReader(input), func(_ context.Context, c Case, router string) ([]Binding, error) {
		return Decide(router, *c.State, c.Input), nil
	})
	if report.Selected != 6 || report.Scored != 1 || report.Excluded != 5 || report.ExclusionReasons["duplicate_case"] != 1 || report.ExclusionReasons["malformed_json"] != 1 || report.ExclusionReasons["missing_gold"] != 1 {
		t.Fatalf("%+v", report)
	}
	for _, s := range report.Summaries {
		if s.Metrics.Cases != 1 {
			t.Fatalf("invalid cases scored: %+v", s)
		}
	}
}

func TestQuestionsAndMissingBindings(t *testing.T) {
	target := Target{Class: "existing", ID: "task", Kind: "task"}
	c := Case{State: &State{Items: []Item{{ID: "task", Kind: "task", State: "running"}}}, Gold: []Gold{{Target: target, Ask: true}}}
	ask := []Binding{{Ask: true, Decider: "judge"}}
	if s := Score(c, ask); s.Passed != 1 || s.AskedWarranted != 1 || s.Picks != 0 {
		t.Fatal(s)
	}
	c.Gold[0].Ask = false
	if s := Score(c, ask); s.Passed != 0 || s.NeedlessQuestions != 1 {
		t.Fatal(s)
	}
	if s := Score(c, nil); s.Passed != 0 || s.MissingBindings != 1 || s.RightItem != 0 {
		t.Fatal(s)
	}
	c.Gold = append(c.Gold, c.Gold[0])
	if s := Score(c, []Binding{{Target: target, Decider: "focus"}}); s.Passed != 0 || s.RightItem != 1 || s.MissingBindings != 1 {
		t.Fatal(s)
	}
}

func TestValidationReasonsAndIsolation(t *testing.T) {
	raw, err := os.ReadFile("testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Split(strings.TrimSpace(string(raw)), "\n")[0]
	for _, tc := range []struct {
		name, reason string
		edit         func(*Case)
	}{
		{"missing state", "missing_state", func(c *Case) { c.State = nil }},
		{"missing gold", "missing_gold", func(c *Case) { c.Gold = nil }},
		{"unknown gold", "invalid_gold", func(c *Case) { c.Gold[0].Target.ID = "absent" }},
		{"bad parent", "invalid_gold", func(c *Case) { c.Gold[0].Target = Target{Class: "new", Kind: "todo", Parent: "absent"} }},
		{"unknown focus", "invalid_focus", func(c *Case) { c.State.Focus.ID = "absent" }},
		{"duplicate item", "duplicate_item", func(c *Case) { c.State.Items = append(c.State.Items, c.State.Items[0]) }},
		{"dangling reply", "invalid_surface", func(c *Case) { c.Input.ReplyTo = "absent" }},
		{"dangling question", "invalid_question", func(c *Case) {
			c.State.Questions = []Question{{ID: "q", Target: "absent", Options: []string{"a", "b"}}}
		}},
		{"negative age", "invalid_state", func(c *Case) { c.State.FocusAgeSeconds = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c Case
			if err := json.Unmarshal([]byte(first), &c); err != nil {
				t.Fatal(err)
			}
			tc.edit(&c)
			b, _ := json.Marshal(c)
			report := Run(t.Context(), strings.NewReader(string(b)), func(context.Context, Case, string) ([]Binding, error) {
				t.Fatal("invalid case reached Core")
				return nil, nil
			})
			if report.Scored != 0 || report.Excluded != 1 || report.ExclusionReasons[tc.reason] != 1 {
				t.Fatal(report)
			}
		})
	}
	for _, input := range []string{"", "{\"version\":1,\"unknown\":true}", first + "{}"} {
		report := Run(t.Context(), strings.NewReader(input), nil)
		if report.Excluded != 1 || report.Scored != 0 || len(report.Summaries) != 0 {
			t.Fatal(report)
		}
	}
	report := File(t.Context(), t.TempDir()+"/missing", nil)
	if report.Scored != 0 || report.ExclusionReasons["missing_file"] != 1 {
		t.Fatal(report)
	}
	report = Run(t.Context(), strings.NewReader(first), func(_ context.Context, c Case, router string) ([]Binding, error) {
		if router == "rules-then-focus" {
			return nil, os.ErrPermission
		}
		return Decide(router, *c.State, c.Input), nil
	})
	if report.Scored != 0 || report.ExclusionReasons["runtime_error"] != 1 || len(report.Summaries) != 0 {
		t.Fatal("partial run scored", report)
	}
}

func TestSetAndDeciderDenominators(t *testing.T) {
	f, err := os.Open("testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	report := Run(t.Context(), f, func(_ context.Context, c Case, router string) ([]Binding, error) {
		return Decide(router, *c.State, c.Input), nil
	})
	sets := map[string]int{"random": 4, "hard": 26}
	deciders := map[string]bool{}
	for _, s := range report.Summaries {
		n, ok := sets[s.Set]
		if !ok {
			t.Fatal("pooled sets", s)
		}
		if s.Coverage.Denominator < n || s.PassRate.Denominator != s.Metrics.Cases || s.RightItem.Denominator != s.Metrics.Picks || s.NewOrExisting.Denominator != s.Metrics.Picks {
			t.Fatal("wrong denominator", s)
		}
		if s.Decider == "all" && s.Metrics.Cases != n {
			t.Fatal("missing cases", s)
		}
		deciders[s.Decider] = true
	}
	for _, name := range []string{"focus", "reply-or-card", "exact-reference", "offered-option", "missing"} {
		if !deciders[name] {
			t.Fatal("missing decider", name)
		}
	}
	if r := ratio(0, 0); r.Rate != nil {
		t.Fatal("empty set claimed a rate", r)
	}
}

func TestQuestionScoreDistinguishesKindParentAndRunningWork(t *testing.T) {
	focus := Target{Class: "new", Kind: "todo", Parent: "one"}
	c := Case{Gold: []Gold{{Target: focus}}, State: &State{}}
	for _, pick := range []Target{{Class: "new", Kind: "todo", Parent: "two"}, {Class: "new", Kind: "project", Parent: "one"}} {
		s := Score(c, []Binding{{Target: pick, Decider: "judge"}})
		if s.Passed != 0 || s.RightItem != 0 || s.ClassRight != 1 || s.SilentWrong != 1 {
			t.Fatal(s)
		}
	}
	c.Gold[0] = Gold{Target: Target{Class: "none"}}
	c.State.Items = []Item{{ID: "run", Kind: "run", State: "running"}}
	s := Score(c, []Binding{{Target: Target{Class: "existing", Kind: "run", ID: "run"}, Decider: "judge"}})
	if s.RunningSilentWrong != 1 || s.Confusion["none->existing"] != 1 {
		t.Fatal(s)
	}
}

func TestOversizedCaseDoesNotHideFollowingCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Split(strings.TrimSpace(string(raw)), "\n")[0]
	cases, rejected := Read(strings.NewReader(strings.Repeat("x", maxLine+1) + "\n" + first + "\n"))
	if len(cases) != 1 || len(rejected) != 1 || rejected[0].Reason != "case_too_large" {
		t.Fatalf("cases=%d rejected=%v", len(cases), rejected)
	}
}

func TestBaselineDeciders(t *testing.T) {
	f, err := os.Open("testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cases, rejected := Read(f)
	if len(rejected) != 0 {
		t.Fatal(rejected)
	}
	wants := map[string]string{
		"reply": "reply-or-card", "card": "reply-or-card", "thread": "reply-or-card", "reply-precedes-name": "reply-or-card",
		"exact-id": "exact-reference", "exact-link": "exact-reference", "exact-title-particle": "exact-reference", "normalized-name": "exact-reference", "normalized-nfc": "exact-reference", "name-precedes-option": "exact-reference", "new-under-named-project": "exact-reference", "running-task": "exact-reference", "running-run": "exact-reference",
		"todo-backlog": "exact-reference", "todo-started": "exact-reference",
		"option-number": "offered-option", "option-label": "offered-option", "yes-no": "offered-option",
	}
	for _, c := range cases {
		want := wants[c.ID]
		if want == "" {
			want = "focus"
		}
		bs := Decide("rules-then-focus", *c.State, c.Input)
		if len(bs) != 1 || bs[0].Decider != want || bs[0].Ask {
			t.Fatalf("%s: %+v want %s", c.ID, bs, want)
		}
		if bs := Decide("focus-only", *c.State, c.Input); len(bs) != 1 || bs[0].Target != c.State.Focus || bs[0].Decider != "focus" {
			t.Fatalf("focus baseline: %+v", bs)
		}
	}
}

func TestTodoSnapshotStates(t *testing.T) {
	raw, err := os.ReadFile("testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Split(strings.TrimSpace(string(raw)), "\n")[0]
	for _, state := range []string{"backlog", "open", "started", "completed", "canceled", "active", "closed", ""} {
		t.Run(state, func(t *testing.T) {
			var c Case
			if err := json.Unmarshal([]byte(first), &c); err != nil {
				t.Fatal(err)
			}
			item := c.State.Items[1]
			item.Kind = "todo"
			item.State = state
			c.State.Items[1] = item
			wantValid := state != "active" && state != "closed" && state != ""
			reason := Validate(c)
			if (reason == "") != wantValid {
				t.Fatalf("state=%q validation=%q want valid=%v", state, reason, wantValid)
			}
		})
	}
}
