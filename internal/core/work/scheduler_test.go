// SPDX-License-Identifier: Apache-2.0
package work

import (
	"errors"
	"reflect"
	"sync"
	"testing"
)

func scheduled(id string, deps ...string) Task {
	return Task{ID: id, AgentID: "agent", ConversationID: "conversation", Title: id, Done: "checked", Schedule: &Schedule{Group: "graph", Repository: "owner/repo", Areas: []string{id + "/**"}, DependsOn: deps, Worker: "worker", Host: "local"}}
}
func available() Availability {
	return Availability{Hosts: map[string]int{"local": 10}, BlockedWorkers: map[string]bool{}}
}
func ids(cards []Card) []string {
	var out []string
	for _, c := range cards {
		out = append(out, c.Task.ID)
	}
	return out
}
func startScheduled(t *testing.T, s *Service, id string) Run {
	t.Helper()
	r := Run{ID: "run:" + id, TaskID: id, Generation: 1, Worker: "worker", NodeID: "local", ThreadID: "thread:" + id, TurnID: "turn"}
	if err := s.Start(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestGraphRejectsCycleAndUnorderedOverlapAtomically(t *testing.T) {
	for _, kind := range []string{"cycle", "missing", "overlap", "empty area"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := testStore(t)
			a, b := scheduled("a"), scheduled("b")
			switch kind {
			case "cycle":
				a.Schedule.DependsOn = []string{"b"}
				b.Schedule.DependsOn = []string{"a"}
			case "missing":
				b.Schedule.DependsOn = []string{"absent"}
			case "overlap":
				b.Schedule.Areas = []string{"a/file.go"}
			case "empty area":
				b.Schedule.Areas = nil
			}
			if err := s.Schedule(t.Context(), []Task{a, b}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted %s: %v", kind, err)
			}
			if _, err := s.Get(t.Context(), "a"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("partial admission: %v", err)
			}
		})
	}
}
func TestReleaseWaitsForObservedMergeAndNeverHoldsIndependentTask(t *testing.T) {
	s, _ := testStore(t)
	a, b, c := scheduled("a"), scheduled("b", "a"), scheduled("c")
	b.Schedule.Areas = []string{"a/file.go"}
	if err := s.Schedule(t.Context(), []Task{a, b, c}); err != nil {
		t.Fatal(err)
	}
	released, err := s.Release(t.Context(), available())
	if err != nil || !reflect.DeepEqual(ids(released), []string{"a", "c"}) {
		t.Fatalf("release %v %v", ids(released), err)
	}
	waiting, _ := s.Get(t.Context(), "b")
	if waiting.WaitReason == nil || waiting.WaitReason.Kind != "dependency" || waiting.WaitReason.Target != "a" {
		t.Fatalf("wait %+v", waiting)
	}
	r := startScheduled(t, s, "a")
	if err = s.Finish(t.Context(), "a", r.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Release(t.Context(), available()); err != nil || len(got) != 0 {
		t.Fatalf("finish released dependent: %v %v", got, err)
	}
	if _, err = s.ObservePullRequest(t.Context(), "a", r.ID, PullRequest{Number: 1, Head: "head-a", State: "OPEN", Mergeable: "MERGEABLE"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Release(t.Context(), available()); len(got) != 0 {
		t.Fatal("open PR released dependent")
	}
	if _, err = s.ObservePullRequest(t.Context(), "a", r.ID, PullRequest{Number: 1, Head: "head-a", State: "MERGED", MergeCommit: "merge-a"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Release(t.Context(), available())
	if err != nil || !reflect.DeepEqual(ids(got), []string{"b"}) {
		t.Fatalf("landed release: %v %v", ids(got), err)
	}
	before, _ := s.Get(t.Context(), "a")
	if before.State == "done" {
		t.Fatal("merge forged acceptance")
	}
	if err = s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Get(t.Context(), "a")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rebuild changed scheduler state")
	}
}
func TestAreaLeaseSurvivesFinishRecoveryAndConcurrentWake(t *testing.T) {
	s, db := testStore(t)
	a, b := scheduled("a"), scheduled("b")
	b.Schedule.Group = "other"
	b.Schedule.Areas = []string{"a/**"}
	if err := s.Schedule(t.Context(), []Task{a, b}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	out := make(chan []Card, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); cs, err := New(db).Release(t.Context(), available()); out <- cs; errs <- err }()
	}
	wg.Wait()
	close(out)
	close(errs)
	count := 0
	for cs := range out {
		count += len(cs)
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("concurrent releases %d", count)
	}
	r := startScheduled(t, s, "a")
	if err := s.Finish(t.Context(), "a", r.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(t.Context(), "restart"); err != nil {
		t.Fatal(err)
	}
	if cs, err := s.Release(t.Context(), available()); err != nil || len(cs) != 0 {
		t.Fatalf("lease lost: %v %v", cs, err)
	}
	c, _ := s.Get(t.Context(), "b")
	if c.WaitReason == nil || c.WaitReason.Kind != "area" || c.WaitReason.Target != "a" {
		t.Fatalf("area wait: %+v", c)
	}
}
func TestWaitPriorityAndCapacityRelease(t *testing.T) {
	s, _ := testStore(t)
	a, b, c := scheduled("a"), scheduled("b", "a"), scheduled("c")
	if err := s.Schedule(t.Context(), []Task{a, b, c}); err != nil {
		t.Fatal(err)
	}
	av := available()
	av.Hosts["local"] = 0
	av.BlockedWorkers["worker"] = true
	if _, err := s.Release(t.Context(), av); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"a": "quota", "b": "dependency", "c": "quota"} {
		card, _ := s.Get(t.Context(), id)
		if card.WaitReason == nil || card.WaitReason.Kind != want {
			t.Fatalf("%s: %+v", id, card.WaitReason)
		}
	}
	av.BlockedWorkers["worker"] = false
	if _, err := s.Release(t.Context(), av); err != nil {
		t.Fatal(err)
	}
	card, _ := s.Get(t.Context(), "a")
	if card.WaitReason.Kind != "host_capacity" {
		t.Fatal(card.WaitReason)
	}
	av.Hosts["local"] = 1
	got, err := s.Release(t.Context(), av)
	if err != nil || !reflect.DeepEqual(ids(got), []string{"a"}) {
		t.Fatalf("%v %v", got, err)
	}
	r := startScheduled(t, s, "a")
	if err = s.Finish(t.Context(), "a", r.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	got, err = s.Release(t.Context(), av)
	if err != nil || !reflect.DeepEqual(ids(got), []string{"c"}) {
		t.Fatalf("independent did not use free slot: %v %v", got, err)
	}
}
func TestUnmergeableReturnedOnlyOnceAndBoundToRun(t *testing.T) {
	s, _ := testStore(t)
	if err := s.Schedule(t.Context(), []Task{scheduled("a")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Release(t.Context(), available()); err != nil {
		t.Fatal(err)
	}
	r := startScheduled(t, s, "a")
	if err := s.Finish(t.Context(), "a", r.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	pr := PullRequest{Number: 7, Head: "head-a", State: "OPEN", Mergeable: "CONFLICTING"}
	if _, err := s.ObservePullRequest(t.Context(), "a", "stale", pr); !errors.Is(err, ErrState) {
		t.Fatalf("stale run %v", err)
	}
	for i := range 2 {
		request, err := s.ObservePullRequest(t.Context(), "a", r.ID, pr)
		if err != nil || request != (i == 0) {
			t.Fatalf("return %d: %v %v", i, request, err)
		}
	}
	if err := s.Recover(t.Context(), "next-epoch"); err != nil {
		t.Fatal(err)
	}
	pr.Head = "head-b"
	request, err := s.ObservePullRequest(t.Context(), "a", r.ID, pr)
	if err != nil || request {
		t.Fatalf("second return: %v %v", request, err)
	}
	card, _ := s.Get(t.Context(), "a")
	if card.State != "input_needed" {
		t.Fatalf("second conflict not escalated: %+v", card)
	}
}
func TestGlobIntersection(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"src/**", "src/a/b.go", true}, {"src/**/a.go", "src/a.go", true}, {"src/*.go", "src/sub/a.go", false}, {"src/*.go", "src/*.md", false}, {"src/a?.go", "src/[bc]*.go", false}, {"src/[ab]*.go", "src/b?.go", true}, {"go.mod", "go.mod", true}, {"src/**", "src2/**", false}, {"**/go.mod", "go.mod", true},
	} {
		got, err := AreasOverlap([]string{tc.a}, []string{tc.b})
		if err != nil || got != tc.want {
			t.Errorf("%s / %s = %v %v", tc.a, tc.b, got, err)
		}
	}
	for _, p := range []string{"", "../a", "/a", "a/../b", "a/[", "a/**x"} {
		if _, err := AreasOverlap([]string{p}, []string{"**"}); err == nil {
			t.Errorf("accepted invalid glob %q", p)
		}
	}
}
