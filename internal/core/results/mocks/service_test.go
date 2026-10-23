// SPDX-License-Identifier: Apache-2.0
package mocks

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/kit/result"
)

type renderFunc func(context.Context, string, []result.MockItem) (Rendered, error)

func (f renderFunc) Draw(c context.Context, s string, i []result.MockItem) (Rendered, error) {
	return f(c, s, i)
}
func TestExecuteCommitsChecklistBeforeDrawingAndGatesPublication(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scores []int
		stop   string
	}{{"pass", []int{3, 7}, "all_passed"}, {"plateau", []int{3, 3}, "no_improvement"}, {"limit", []int{1, 2, 3, 4}, "redraw_limit"}} {
		t.Run(tc.name, func(t *testing.T) {
			var saved State
			draws, published, assets := 0, 0, 0
			c := Config{
				Save: func(_ context.Context, s State) error { raw, _ := json.Marshal(s); return json.Unmarshal(raw, &saved) },
				Model: func(_ context.Context, in ModelInput) (json.RawMessage, error) {
					if in.Phase == "checklist" {
						return json.RawMessage(`{"checklist":[{"id":"tasks","description":"Tasks","action":"none","target":""}]}`), nil
					}
					if len(saved.Checklist) != 1 {
						t.Fatal("drawing before committed checklist")
					}
					if draws == 0 && in.Previous != "prior revision" {
						t.Fatal("feedback lost its source")
					}
					return json.RawMessage(`{"fragment":"<h1 data-check=\"tasks\">Tasks</h1>"}`), nil
				},
				Renderer: renderFunc(func(_ context.Context, fragment string, _ []result.MockItem) (Rendered, error) {
					if len(saved.Attempts) != draws+1 || saved.Attempts[draws].Fragment != fragment {
						t.Fatal("candidate not committed before checks")
					}
					out := Rendered{Page: "checked page", PNG: []byte("png")}
					for i, id := range Dimensions {
						out.Checks = append(out.Checks, result.MockCheck{ID: id, Passed: i < tc.scores[draws]})
					}
					draws++
					return out, nil
				}),
				Asset: func(_ context.Context, name string, _ []byte) (result.AssetRef, error) {
					if saved.StopReason != "all_passed" {
						t.Fatal("failed draw became public asset")
					}
					assets++
					return result.AssetRef{ID: name, Revision: 1}, nil
				},
				Present: func(_ context.Context, m result.Mock) (result.Card, error) {
					if assets != 2 || m.Previous == nil || m.Previous.Revision != 2 {
						t.Fatal("missing assets or previous revision")
					}
					published++
					return result.Card{ID: "mock", Revision: 3}, nil
				},
			}
			state, err := Execute(t.Context(), c, State{Request: "Make smaller"}, "prior revision", &result.ResultRef{ID: "mock", Revision: 2})
			if err != nil {
				t.Fatal(err)
			}
			if state.StopReason != tc.stop || draws != len(tc.scores) {
				t.Fatalf("state=%+v draws=%d", state, draws)
			}
			want := 0
			if tc.stop == "all_passed" {
				want = 1
			}
			if published != want {
				t.Fatal("publication gate failed")
			}
			if _, err = Execute(t.Context(), c, state, "", nil); err != nil || published != want {
				t.Fatal("terminal resume repeated work")
			}
		})
	}
}
func TestInterruptedCheckResumesSameCandidate(t *testing.T) {
	saved := State{Request: "Tasks", Checklist: []result.MockItem{{ID: "tasks", Description: "Tasks", Action: "none"}}, Attempts: []Attempt{{Fragment: "committed candidate"}}}
	original := saved.Attempts[0].Fragment
	calls := 0
	c := Config{Model: func(context.Context, ModelInput) (json.RawMessage, error) {
		t.Fatal("regenerated committed candidate")
		return nil, nil
	}, Save: func(context.Context, State) error { return nil }, Renderer: renderFunc(func(_ context.Context, s string, _ []result.MockItem) (Rendered, error) {
		calls++
		if s != original {
			t.Fatal("candidate changed")
		}
		return Rendered{}, errors.New("interrupted")
	})}
	got, err := Execute(t.Context(), c, saved, "", nil)
	if err == nil || calls != 1 || !reflect.DeepEqual(got, saved) {
		t.Fatal("interrupted phase lost its checkpoint")
	}
}
