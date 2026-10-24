// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/authority"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/miniapps"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/result"
)

// A declared host that resolves to a private address is refused, even when one
// public address is in the answer: the fetcher never picks the public one.
func TestMiniAppFetchRefusesANonPublicAnswer(t *testing.T) {
	f := miniAppFetcher{lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
		if host == "mixed.example.com" {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}}
	hosts := []string{"mixed.example.com", "ok.example.com"}
	if _, _, err := f.Fetch(context.Background(), hosts, "https://mixed.example.com/x"); err == nil {
		t.Fatal("a host with a private answer was fetched")
	}
	if _, err := f.resolve(context.Background(), "127.0.0.1"); err == nil {
		t.Fatal("a loopback address was accepted")
	}
	if ip, err := f.resolve(context.Background(), "ok.example.com"); err != nil || ip.String() != "93.184.216.34" {
		t.Fatalf("a public answer was refused: %v %v", ip, err)
	}
}

// A redirect to an undeclared host is refused before the connection is made.
func TestMiniAppFetchRefusesARedirectToAnUndeclaredHost(t *testing.T) {
	f := miniAppFetcher{lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}}
	if _, _, err := f.Fetch(context.Background(), []string{"declared.example.com"}, "https://undeclared.example.com/x"); err == nil {
		t.Fatal("an undeclared host was fetched")
	}
	// The declared host itself passes the checks; only the dial fails, because
	// the answer is a documentation address with no server.
	if _, _, err := f.Fetch(context.Background(), []string{"declared.example.com"}, "https://declared.example.com/x"); errors.Is(err, miniapps.ErrForbidden) {
		t.Fatal("a declared host was refused before the dial")
	}
}

// miniAppRecordRuntime is a Core with the record writer and the authority gate
// todo.done uses. The app supplies only an id and a revision.
func miniAppRecordRuntime(t *testing.T) *conversationRuntime {
	t.Helper()
	r, _, agent, _ := managementFixture(t)
	r.agent = agent
	r.personaMu = &sync.Mutex{}
	r.personaGrants = map[string]grant.Grant{}
	r.activities = lifecycle.NewRegistry(nil)
	repo, err := records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	r.records = repo
	r.personaAuthority, err = authority.Open(t.Context(), r.db, authority.Config{Epoch: r.epoch, Resolve: r.resolvePersonaGrant})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func finishTodo(t *testing.T, ctx context.Context, r *conversationRuntime, id, revision string) miniapps.EffectResult {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"id": id, "revision": revision})
	if err != nil {
		t.Fatal(err)
	}
	out, err := (miniAppEffects{r: r}).Run(ctx, miniapps.EffectRequest{
		Person: r.person, Agent: r.agent, AppID: "app-1", AppName: "Open to-dos",
		RequestID: revision, Name: "todo.done", Arguments: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.personaGrants) != 0 {
		t.Fatal("grant leaked")
	}
	return out
}

func requireSameSeries(t *testing.T, before, after *records.Repeat) {
	t.Helper()
	if after == nil || before == nil || after.Start != before.Start || after.Completed != before.Completed || after.Exhausted != before.Exhausted || after.Next != before.Next || after.Rule != before.Rule || after.FromCompletion != before.FromCompletion || after.Zone != before.Zone {
		t.Fatalf("Core-owned recurrence changed: before=%+v after=%+v", before, after)
	}
}

// A recurring to-do saved through WriteRecord stores Core-owned counters.
// todo.done still has to record the completion; those counters are not arguments.
func TestTodoDoneCompletesARecurringTodo(t *testing.T) {
	r := miniAppRecordRuntime(t)
	receipt, err := coreexec.WriteRecord(t.Context(), r.records, records.Actor{Person: r.person, Agent: r.agent}, coreexec.RecordWrite{
		Kind: "todo", Title: "Water plants",
		Todo: &records.Todo{State: "open", Repeat: &records.Repeat{Rule: "FREQ=DAILY", Next: "2020-01-01", Text: "every day"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := r.records.ViewRecord(t.Context(), r.person, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Todo == nil || before.Todo.Repeat == nil || before.Todo.Repeat.Start == "" {
		t.Fatalf("Core did not store the series anchor: %+v", before.Todo)
	}
	out := finishTodo(t, t.Context(), r, receipt.ID, before.Revision)
	if out.State != "applied" {
		t.Fatalf("todo.done refused a current recurring to-do: %+v", out)
	}
	after, err := r.records.ViewRecord(t.Context(), r.person, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Todo == nil || after.Todo.State != "claimed" || after.Todo.Completion == nil || after.Todo.Completion.State != "claimed" {
		t.Fatalf("completion was not recorded: %+v", after.Todo)
	}
	requireSameSeries(t, before.Todo.Repeat, after.Todo.Repeat)
}

// An exhausted series still carries a completed count and the exhausted flag.
// todo.done records that completion and leaves both counters where Core put them.
func TestTodoDoneCompletesAnExhaustedSeries(t *testing.T) {
	r := miniAppRecordRuntime(t)
	const id = "exhausted"
	data := []byte(fmt.Sprintf("---\nid: %q\nkind: todo\nstate: open\nperson_id: %q\nrepeat: {\"rule\":\"FREQ=DAILY;COUNT=1\",\"next\":\"2020-01-01\",\"start\":\"2020-01-01\",\"completed\":1,\"exhausted\":true}\n---\n\n# Exhausted\n", id, r.person))
	if _, err := r.records.Commit(t.Context(), records.Change{ID: id, Path: "inbox/exhausted.md", Data: data}, records.Actor{Person: r.person, Agent: r.agent}); err != nil {
		t.Fatal(err)
	}
	before, err := r.records.ViewRecord(t.Context(), r.person, id)
	if err != nil {
		t.Fatal(err)
	}
	if before.Todo == nil || before.Todo.Repeat == nil || before.Todo.Repeat.Start == "" || before.Todo.Repeat.Completed == 0 || !before.Todo.Repeat.Exhausted {
		t.Fatalf("exhausted series was not stored: %+v", before.Todo)
	}
	out := finishTodo(t, t.Context(), r, id, before.Revision)
	if out.State != "applied" {
		t.Fatalf("todo.done refused an exhausted series: %+v", out)
	}
	after, err := r.records.ViewRecord(t.Context(), r.person, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Todo == nil || after.Todo.State != "claimed" || after.Todo.Completion == nil {
		t.Fatalf("exhausted completion was not recorded: %+v", after.Todo)
	}
	requireSameSeries(t, before.Todo.Repeat, after.Todo.Repeat)
}

// An open to-do with no recurrence still completes. The copy path has no series
// counters to drop.
func TestTodoDoneCompletesAnOpenTodo(t *testing.T) {
	r := miniAppRecordRuntime(t)
	receipt, err := coreexec.WriteRecord(t.Context(), r.records, records.Actor{Person: r.person, Agent: r.agent}, coreexec.RecordWrite{
		Kind: "todo", Title: "Buy milk", Todo: &records.Todo{State: "open"},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := r.records.ViewRecord(t.Context(), r.person, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := finishTodo(t, t.Context(), r, receipt.ID, before.Revision)
	if out.State != "applied" {
		t.Fatalf("todo.done refused an open to-do: %+v", out)
	}
	after, err := r.records.ViewRecord(t.Context(), r.person, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Todo == nil || after.Todo.State != "claimed" || after.Todo.Completion == nil || after.Todo.Repeat != nil {
		t.Fatalf("open completion changed the to-do: %+v", after.Todo)
	}
}

// A verified completion advances the series and leaves Core's completion on the
// open record. The next todo.done still has to complete that occurrence.
func TestTodoDoneCompletesTheNextOccurrence(t *testing.T) {
	r := miniAppRecordRuntime(t)
	receipt, err := coreexec.WriteRecord(t.Context(), r.records, records.Actor{Person: r.person, Agent: r.agent}, coreexec.RecordWrite{
		Kind: "todo", Title: "Water plants",
		Todo: &records.Todo{State: "open", Repeat: &records.Repeat{Rule: "FREQ=DAILY", Next: "2020-01-01"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := result.WithCompletionPolicy(t.Context(), func(result.CompletionRequest) result.Completion {
		return result.Completion{State: "done", Label: "done"}
	})
	before, err := r.records.ViewRecord(t.Context(), r.person, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out := finishTodo(t, ctx, r, receipt.ID, before.Revision); out.State != "applied" {
		t.Fatalf("first occurrence was refused: %+v", out)
	}
	advanced, err := r.records.ViewRecord(t.Context(), r.person, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if advanced.Todo == nil || advanced.Todo.State != "open" || advanced.Todo.Repeat == nil || advanced.Todo.Repeat.Next == before.Todo.Repeat.Next || advanced.Todo.Repeat.Start != before.Todo.Repeat.Start || advanced.Todo.Completion == nil {
		t.Fatalf("verified completion did not advance one live record: before=%+v after=%+v", before.Todo, advanced.Todo)
	}
	if out := finishTodo(t, ctx, r, receipt.ID, advanced.Revision); out.State != "applied" {
		t.Fatalf("todo.done refused the next occurrence: %+v", out)
	}
	after, err := r.records.ViewRecord(t.Context(), r.person, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Todo == nil || after.Todo.State != "open" || after.Todo.Repeat == nil || after.Todo.Repeat.Next == advanced.Todo.Repeat.Next || after.Todo.Repeat.Start != advanced.Todo.Repeat.Start || after.Todo.Repeat.Completed != advanced.Todo.Repeat.Completed+1 {
		t.Fatalf("the next occurrence did not advance: advanced=%+v after=%+v", advanced.Todo.Repeat, after.Todo)
	}
}
