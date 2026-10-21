// SPDX-License-Identifier: Apache-2.0
package eventstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/diag"
)

func newID(t *testing.T) diag.ID {
	t.Helper()
	id, err := diag.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// The memory backend exercises store policy without claiming SQLite durability.
// Its commit operation either publishes the whole event or has no effect.
type memoryBackend struct {
	events      []diag.Event
	fail        error
	calls       int
	writeErrors []error
}

func (b *memoryBackend) all(context.Context) ([]diag.Event, error) {
	return slices.Clone(b.events), b.fail
}
func (b *memoryBackend) append(_ context.Context, e diag.Event) error {
	b.calls++
	if len(b.writeErrors) > 0 {
		err := b.writeErrors[0]
		b.writeErrors = b.writeErrors[1:]
		if err != nil {
			return err
		}
	}
	if b.fail != nil {
		err := b.fail
		b.fail = nil
		return err
	}
	b.events = append(b.events, e)
	return nil
}
func (b *memoryBackend) close() error { return nil }

func memoryStore() (*Store, *memoryBackend) { b := &memoryBackend{}; return &Store{backend: b}, b }
func root(t *testing.T, s *Store, refs diag.References) diag.Event {
	t.Helper()
	e, err := diag.NewEmitter(s).Root(context.Background(), diag.TaskRoot, refs)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestInjectionsFromStoredEventsAlone(t *testing.T) {
	ctx := context.Background()
	for _, f := range []struct {
		code   diag.CauseCode
		class  diag.ErrorClass
		result diag.Result
	}{
		{diag.ModelFailed, diag.Model, diag.Failure}, {diag.TaskStopped, diag.Interrupted, diag.Cancelled}, {diag.StorageFailed, diag.Storage, diag.Failure},
	} {
		s, backend := memoryStore()
		refs := diag.References{TaskID: newID(t), AgentID: newID(t)}
		r := root(t, s, refs)
		// Simulate each caller boundary. Raw errors stay at that boundary; only
		// the mapped class and cause reach the emitter.
		fault := errors.New("synthetic-secret prompt provider-body /home/example/private")
		if f.class == diag.Model {
			provider := func() error { return fault }
			if err := provider(); !errors.Is(err, fault) {
				t.Fatal("provider injection failed")
			}
		} else if f.class == diag.Interrupted {
			running, stop := context.WithCancel(ctx)
			stop()
			if !errors.Is(running.Err(), context.Canceled) {
				t.Fatal("interruption injection failed")
			}
		} else {
			backend.writeErrors = []error{fault}
			_, err := diag.NewEmitter(s).Emit(ctx, diag.Fields{Component: diag.Task, References: refs, Cause: diag.Cause{Code: diag.OperationStarted, ParentEventID: r.EventID}, Result: diag.Pending, NextAction: diag.Continue})
			if !errors.Is(err, ErrStorage) {
				t.Fatal("storage injection failed")
			}
		}
		leaf, err := diag.NewEmitter(s).Emit(ctx, diag.Fields{Component: diag.Task, References: refs, Cause: diag.Cause{Code: f.code, ParentEventID: r.EventID}, Result: f.result, NextAction: diag.ReportFailure, ErrorClass: f.class})
		if err != nil {
			t.Fatal(err)
		}
		page, err := s.Query(ctx, Filter{AgentID: refs.AgentID, TaskID: refs.TaskID})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Events) != 2 {
			t.Fatal("missing chain")
		}
		// Build the machine-readable diagnosis from queried events alone. The
		// oracle then compares that evidence with the independently known root.
		var citation diag.Citation
		for _, event := range page.Events {
			if event.ErrorClass != diag.NoError {
				citation = diag.Citation{EventID: event.EventID, RootEventID: event.Cause.ParentEventID, ErrorClass: event.ErrorClass, CauseCode: event.Cause.Code}
			}
		}
		if citation.ErrorClass != f.class || citation.CauseCode != f.code || citation.EventID != leaf.EventID {
			t.Fatal("diagnosis does not explain injection")
		}
		if err := diag.VerifyCitations(page.Events, r.EventID, []diag.Citation{citation}); err != nil {
			t.Fatal(err)
		}
		chain, err := s.Chain(ctx, leaf.EventID)
		if err != nil || len(chain) != 2 || chain[0].EventID != r.EventID {
			t.Fatalf("chain: %v %v", chain, err)
		}
		for _, event := range backend.events {
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			var keys map[string]json.RawMessage
			json.Unmarshal(encoded, &keys)
			want := []string{"event_id", "time", "component", "conversation_id", "task_id", "agent_id", "epoch", "node_id", "cause", "result", "next_action", "error_class"}
			if len(keys) != len(want) {
				t.Fatalf("stored keys: %v", keys)
			}
			for _, key := range want {
				if _, ok := keys[key]; !ok {
					t.Fatalf("missing %s", key)
				}
			}
			for _, forbidden := range strings.Fields(fault.Error()) {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatal("forbidden content persisted")
				}
			}
		}
		class, _ := json.Marshal(f.class)
		code, _ := json.Marshal(f.code)
		t.Logf("root=%s leaf=%s agent_id=%s task_id=%s class=%s cause=%s", r.EventID, leaf.EventID, refs.AgentID, refs.TaskID, class, code)
	}
}

func TestFilterIntersectionOrderingAndCursor(t *testing.T) {
	ctx := context.Background()
	s, b := memoryStore()
	refs := diag.References{TaskID: newID(t), AgentID: newID(t), ConversationID: newID(t)}
	r := root(t, s, refs)
	other := root(t, s, diag.References{TaskID: newID(t), AgentID: newID(t)})
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b.events[0].Time = base
	b.events[1].Time = base
	for i := 0; i < 4; i++ {
		e, err := diag.NewEmitter(s).Emit(ctx, diag.Fields{Component: diag.Task, References: refs, Cause: diag.Cause{Code: diag.ModelFailed, ParentEventID: r.EventID}, Result: diag.Failure, NextAction: diag.ReportFailure, ErrorClass: diag.Model})
		if err != nil {
			t.Fatal(err)
		}
		b.events[len(b.events)-1].Time = base.Add(time.Second)
		_ = e
	}
	filter := Filter{Since: base, Until: base.Add(2 * time.Second), Components: []diag.Component{diag.Task}, ConversationID: refs.ConversationID, TaskID: refs.TaskID, AgentID: refs.AgentID, CauseCode: diag.ModelFailed, Results: []diag.Result{diag.Failure}, ErrorClasses: []diag.ErrorClass{diag.Model}, Limit: 2}
	p1, err := s.Query(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Events) != 2 || p1.NextCursor == nil {
		t.Fatal("first page")
	}
	encoded, _ := json.Marshal(p1.NextCursor)
	var cursor Cursor
	if err := json.Unmarshal(encoded, &cursor); err != nil {
		t.Fatal(err)
	}
	filter.Cursor = &cursor
	p2, err := s.Query(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Events) != 2 || p2.NextCursor != nil {
		t.Fatal("second page")
	}
	seen := map[diag.ID]bool{}
	for _, e := range append(p1.Events, p2.Events...) {
		if e.EventID == other.EventID || seen[e.EventID] {
			t.Fatal("filter or pagination leak")
		}
		seen[e.EventID] = true
	}
	if p1.Events[1].EventID.String() >= p2.Events[0].EventID.String() {
		t.Fatal("unstable timestamp tie")
	}
	filter.AgentID = other.AgentID
	if _, err := s.Query(ctx, filter); err == nil {
		t.Fatal("cursor accepted with different filters")
	}
	filter.Cursor = nil
	page, err := s.Query(ctx, filter)
	if err != nil || len(page.Events) != 0 {
		t.Fatal("intersection")
	}
	page, err = s.Query(ctx, Filter{Until: base.Add(time.Second)})
	if err != nil || len(page.Events) != 2 {
		t.Fatal("exclusive until")
	}
	page, err = s.Query(ctx, Filter{Since: base.Add(time.Second)})
	if err != nil || len(page.Events) != 4 {
		t.Fatal("inclusive since")
	}
	before := slices.Clone(b.events)
	page.Events[0].Component = diag.OS
	if !reflect.DeepEqual(before, b.events) {
		t.Fatal("query mutated store")
	}
	for _, bad := range []Filter{{Limit: -1}, {Limit: 1001}, {Components: []diag.Component{255}}, {Results: []diag.Result{255}}, {ErrorClasses: []diag.ErrorClass{255}}, {CauseCode: 255}, {Since: base, Until: base}} {
		if _, err := s.Query(ctx, bad); err == nil {
			t.Fatalf("invalid filter accepted: %#v", bad)
		}
	}
}

func TestAppendAndReadFailClosed(t *testing.T) {
	ctx := context.Background()
	s, b := memoryStore()
	r := root(t, s, diag.References{TaskID: newID(t)})
	if err := s.Append(ctx, r); err == nil {
		t.Fatal("duplicate accepted")
	}
	missing := r
	missing.EventID = newID(t)
	missing.Cause = diag.Cause{Code: diag.OperationStarted, ParentEventID: newID(t)}
	if err := s.Append(ctx, missing); err == nil {
		t.Fatal("missing parent accepted")
	}
	b.events = append(b.events, missing)
	if _, err := s.Query(ctx, Filter{}); err == nil {
		t.Fatal("corrupt chain returned")
	}
	if _, err := s.Chain(ctx, missing.EventID); err == nil {
		t.Fatal("missing root returned")
	}
	b.events = b.events[:1]
	b.fail = errors.New("raw private database error")
	if _, err := s.Query(ctx, Filter{}); !errors.Is(err, ErrStorage) {
		t.Fatal("raw storage error exposed")
	}
	b.fail = nil
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Query(ctx, Filter{}); !errors.Is(err, ErrClosed) {
		t.Fatal("closed store readable")
	}
}

func TestReserveIsAllocatedReleasedAndReplenished(t *testing.T) {
	dir := t.TempDir()
	r, err := PrepareReserve(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	path := filepath.Join(dir, "system", "events", "reserve")
	info, err := os.Stat(path)
	if err != nil || info.Size() != reserveBytes || info.Mode().Perm() != 0600 {
		t.Fatalf("reserve: %v %v", info, err)
	}
	if err := r.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("reserve not released")
	}
	if err := r.Replenish(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestDiskFullRetryWithConsumedReserve(t *testing.T) {
	full := errors.New("typed full error")
	other := errors.New("private storage failure")
	for _, result := range []diag.Result{diag.Failure, diag.Unknown, diag.Denied, diag.Cancelled} {
		label, _ := json.Marshal(result)
		for _, item := range []struct {
			name     string
			failures []error
			wantErr  error
			calls    int
		}{
			{"full then success", []error{full}, nil, 2},
			{"still full", []error{full, full}, ErrStorage, 2},
			{"other failure", []error{other}, ErrStorage, 1},
		} {
			t.Run(string(label)+"/"+item.name, func(t *testing.T) {
				reserve, err := PrepareReserve(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer reserve.Close()
				if err := reserve.Release(); err != nil {
					t.Fatal(err)
				}
				s, b := memoryStore()
				s.reserve = reserve
				s.isFull = func(err error) bool { return errors.Is(err, full) }
				r := root(t, s, diag.References{TaskID: newID(t)})
				e := diag.Event{EventID: newID(t), Time: time.Now().UTC(), Fields: diag.Fields{
					Component: diag.Task, Cause: diag.Cause{Code: diag.StorageFailed, ParentEventID: r.EventID},
					Result: result, NextAction: diag.ReportFailure, ErrorClass: diag.Storage,
				}}
				b.calls = 0
				b.writeErrors = item.failures
				err = s.Append(t.Context(), e)
				if !errors.Is(err, item.wantErr) || b.calls != item.calls {
					t.Fatalf("append: err=%v calls=%d; want err=%v calls=%d", err, b.calls, item.wantErr, item.calls)
				}
				want := []diag.Event{r}
				if item.wantErr == nil {
					want = append(want, e)
				}
				if !reflect.DeepEqual(b.events, want) {
					t.Fatal("append did not preserve the exact committed events")
				}
				if reserve.Present() {
					t.Fatal("retry restored a consumed reserve")
				}
			})
		}
	}
}

func TestDiskFullDoesNotRetryAfterReserveReleaseFailure(t *testing.T) {
	for _, kind := range []string{"remove error", "closed"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			reserve, err := PrepareReserve(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer reserve.Close()
			if kind == "closed" {
				if err := reserve.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := reserve.Release(); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, "system", "events", "reserve")
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "occupied"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			s, b := memoryStore()
			s.reserve = reserve
			full := errors.New("typed full error")
			s.isFull = func(err error) bool { return errors.Is(err, full) }
			r := root(t, s, diag.References{TaskID: newID(t)})
			b.calls = 0
			b.writeErrors = []error{full}
			_, err = diag.NewEmitter(s).Emit(t.Context(), diag.Fields{Component: diag.Task, Cause: diag.Cause{Code: diag.StorageFailed, ParentEventID: r.EventID}, Result: diag.Failure, NextAction: diag.ReportFailure, ErrorClass: diag.Storage})
			if !errors.Is(err, ErrStorage) || b.calls != 1 || len(b.events) != 1 {
				t.Fatalf("release failure: err=%v calls=%d events=%d", err, b.calls, len(b.events))
			}
		})
	}
}

func TestDiskFullRetryAndOtherWriteErrors(t *testing.T) {
	full := errors.New("typed full error")
	other := errors.New("private storage failure")
	for _, item := range []struct {
		name     string
		failures []error
		wantErr  bool
		calls    int
		released bool
	}{
		{"full then success", []error{full}, false, 2, true},
		{"still full", []error{full, full}, true, 2, true},
		{"other failure", []error{other}, true, 1, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			s, b := memoryStore()
			dir := t.TempDir()
			reserve, err := PrepareReserve(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer reserve.Close()
			s.reserve = reserve
			s.isFull = func(err error) bool { return errors.Is(err, full) }
			r := root(t, s, diag.References{TaskID: newID(t)})
			b.calls = 0
			b.writeErrors = item.failures
			_, err = diag.NewEmitter(s).Emit(t.Context(), diag.Fields{Component: diag.Task, Cause: diag.Cause{Code: diag.StorageFailed, ParentEventID: r.EventID}, Result: diag.Failure, NextAction: diag.ReportFailure, ErrorClass: diag.Storage})
			if (err != nil) != item.wantErr || (err != nil && !errors.Is(err, ErrStorage)) || b.calls != item.calls {
				t.Fatalf("err=%v calls=%d", err, b.calls)
			}
			_, statErr := os.Stat(filepath.Join(dir, "system", "events", "reserve"))
			if os.IsNotExist(statErr) != item.released {
				t.Fatal("reserve release")
			}
			want := 2
			if item.wantErr {
				want = 1
			}
			if len(b.events) != want {
				t.Fatal("uncommitted event returned")
			}
		})
	}
}

func TestConcurrentWritersAndReaders(t *testing.T) {
	s, _ := memoryStore()
	r := root(t, s, diag.References{TaskID: newID(t)})
	var workers sync.WaitGroup
	for range 24 {
		workers.Go(func() {
			_, err := diag.NewEmitter(s).Emit(t.Context(), diag.Fields{Component: diag.Task, Cause: diag.Cause{Code: diag.OperationSucceeded, ParentEventID: r.EventID}, Result: diag.Success, NextAction: diag.None})
			if err != nil {
				t.Error(err)
			}
			if _, err := s.Query(t.Context(), Filter{}); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	p, err := s.Query(t.Context(), Filter{})
	if err != nil || len(p.Events) != 25 {
		t.Fatalf("events=%d err=%v", len(p.Events), err)
	}
}

func TestOrdinaryEventsPreserveEmergencyReserve(t *testing.T) {
	for _, result := range []diag.Result{diag.Pending, diag.Success} {
		s, b := memoryStore()
		dir := t.TempDir()
		reserve, err := PrepareReserve(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { reserve.Close() })
		s.reserve = reserve
		full := errors.New("typed full error")
		s.isFull = func(err error) bool { return errors.Is(err, full) }
		r := root(t, s, diag.References{TaskID: newID(t)})
		b.calls = 0
		b.writeErrors = []error{full}
		_, err = diag.NewEmitter(s).Emit(t.Context(), diag.Fields{Component: diag.Task, Cause: diag.Cause{Code: diag.OperationStarted, ParentEventID: r.EventID}, Result: result, NextAction: diag.Continue})
		if !errors.Is(err, ErrStorage) || b.calls != 1 {
			t.Fatalf("ordinary event consumed emergency retry: err=%v calls=%d", err, b.calls)
		}
		if _, err := os.Stat(filepath.Join(dir, "system", "events", "reserve")); err != nil {
			t.Fatal("reserve unavailable for the failure event")
		}
	}
}
