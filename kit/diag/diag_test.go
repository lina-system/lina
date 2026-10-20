// SPDX-License-Identifier: Apache-2.0
package diag_test

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/diag"
)

func TestFreeTextDoesNotCompile(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "test", "-mod=readonly", "./testdata/free_text")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("free text compiled")
	}
	for _, typ := range []string{"diag.Component", "diag.ID", "diag.Epoch", "diag.CauseCode", "diag.Result", "diag.Action", "diag.ErrorClass"} {
		if !strings.Contains(string(output), "as "+typ+" value") {
			t.Fatalf("missing type rejection for %s: %s", typ, output)
		}
	}
}

type sink struct {
	events []diag.Event
	err    error
}

func (s *sink) Append(_ context.Context, e diag.Event) error {
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, e)
	return nil
}
func id(t *testing.T) diag.ID {
	t.Helper()
	id, err := diag.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestEmitterSchemaAndClosedInputs(t *testing.T) {
	s := &sink{}
	emitter := diag.NewEmitter(s)
	root, err := emitter.Root(context.Background(), diag.BootRoot, diag.References{Epoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	e, err := emitter.Emit(context.Background(), diag.Fields{
		Component: diag.Conversation, References: diag.References{AgentID: id(t)},
		Cause:  diag.Cause{Code: diag.ModelFailed, ParentEventID: root.EventID},
		Result: diag.Failure, NextAction: diag.ReportFailure, ErrorClass: diag.Model,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"event_id", "time", "component", "conversation_id", "task_id", "agent_id", "epoch", "node_id", "cause", "result", "next_action", "error_class"}
	got := make([]string, 0, len(fields))
	for k := range fields {
		got = append(got, k)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v", got)
	}
	if fields["task_id"] != nil || fields["epoch"] != nil || fields["component"] != "conversation" || fields["error_class"] != "model" {
		t.Fatalf("event = %s", b)
	}
	if e.EventID == root.EventID || e.Time.Location() != time.UTC {
		t.Fatal("event identity/time")
	}
	var roundtrip diag.Event
	if err := json.Unmarshal(b, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e, roundtrip) {
		t.Fatalf("round trip = %#v, want %#v", roundtrip, e)
	}
	valid := diag.Fields{Component: diag.Task, Cause: diag.Cause{Code: diag.TaskStopped, ParentEventID: root.EventID}, Result: diag.Cancelled, NextAction: diag.ReportFailure, ErrorClass: diag.Interrupted}
	for _, mutate := range []func(*diag.Fields){
		func(f *diag.Fields) { f.Component = diag.Component(255) },
		func(f *diag.Fields) { f.Result = diag.Result(255) },
		func(f *diag.Fields) { f.Cause.Code = diag.CauseCode(255) },
		func(f *diag.Fields) { f.NextAction = diag.Action(255) },
		func(f *diag.Fields) { f.ErrorClass = diag.ErrorClass(255) },
		func(f *diag.Fields) { f.Cause.ParentEventID = diag.ID{} },
	} {
		f := valid
		mutate(&f)
		before := len(s.events)
		if _, err := emitter.Emit(context.Background(), f); err == nil {
			t.Fatal("invalid fields accepted")
		}
		if len(s.events) != before {
			t.Fatal("invalid input reached writer")
		}
	}
	// Neither strings, payload objects nor errors can be fields on the emitter API.
	var inspect func(reflect.Type)
	inspect = func(typ reflect.Type) {
		if typ.Kind() == reflect.String || typ.Kind() == reflect.Interface || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			t.Fatalf("free input: %v", typ)
		}
		if typ.Kind() == reflect.Struct {
			for i := 0; i < typ.NumField(); i++ {
				inspect(typ.Field(i).Type)
			}
		}
	}
	inspect(reflect.TypeFor[diag.Fields]())
}

func TestUntrustedRecordsFailClosed(t *testing.T) {
	s := &sink{}
	root, err := diag.NewEmitter(s).Root(context.Background(), diag.BootRoot, diag.References{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(root)
	for _, bad := range []string{
		strings.Replace(string(b), `"component":"os"`, `"component":"private prompt"`, 1),
		strings.Replace(string(b), `"agent_id":null`, `"agent_id":"/home/person/private"`, 1),
		strings.Replace(string(b), `"epoch":null`, `"epoch":"token-secret"`, 1),
		strings.Replace(string(b), `"result":"pending"`, `"result":"provider body"`, 1),
		strings.Replace(string(b), `"task_id":null,`, ``, 1),
		strings.Replace(string(b), `"task_id":null`, `"task_id":null,"payload":"secret"`, 1),
		strings.Replace(string(b), `"task_id":null`, `"task_id":null,"task_id":null`, 1),
		strings.Replace(string(b), `"code":"boot_started"`, `"code":"boot_started","detail":"secret"`, 1),
	} {
		var e diag.Event
		if err := json.Unmarshal([]byte(bad), &e); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	for _, text := range []string{"secret", "/home/person/private", strings.Repeat("g", 32), strings.Repeat("0", 32)} {
		if _, err := diag.ParseID(text); err == nil {
			t.Fatal("invalid id accepted")
		}
	}
}

func TestRootsAndWriterFailure(t *testing.T) {
	s := &sink{}
	emitter := diag.NewEmitter(s)
	for _, item := range []struct {
		kind diag.RootKind
		refs diag.References
		code diag.CauseCode
	}{
		{diag.BootRoot, diag.References{}, diag.BootStarted},
		{diag.TurnRoot, diag.References{ConversationID: id(t), AgentID: id(t)}, diag.TurnStarted},
		{diag.TaskRoot, diag.References{TaskID: id(t), AgentID: id(t)}, diag.TaskStarted},
	} {
		e, err := emitter.Root(context.Background(), item.kind, item.refs)
		if err != nil {
			t.Fatal(err)
		}
		if e.Cause.Code != item.code || !e.Cause.ParentEventID.IsZero() {
			t.Fatal("bad root")
		}
	}
	for _, kind := range []diag.RootKind{diag.TurnRoot, diag.TaskRoot, diag.RootKind(255)} {
		if _, err := emitter.Root(context.Background(), kind, diag.References{}); err == nil {
			t.Fatal("invalid root accepted")
		}
	}
	s.err = errors.New("writer unavailable")
	if e, err := emitter.Root(context.Background(), diag.BootRoot, diag.References{}); err == nil || !e.EventID.IsZero() {
		t.Fatal("failed append returned an event")
	}
}

func TestVerifyCitationsChecksIDsCodesAndChain(t *testing.T) {
	for _, failure := range []struct {
		code   diag.CauseCode
		class  diag.ErrorClass
		result diag.Result
	}{
		{diag.ModelFailed, diag.Model, diag.Failure}, {diag.TaskStopped, diag.Interrupted, diag.Cancelled}, {diag.StorageFailed, diag.Storage, diag.Failure},
	} {
		s := &sink{}
		emitter := diag.NewEmitter(s)
		root, _ := emitter.Root(context.Background(), diag.TaskRoot, diag.References{TaskID: id(t)})
		leaf, err := emitter.Emit(context.Background(), diag.Fields{Component: diag.Task, Cause: diag.Cause{Code: failure.code, ParentEventID: root.EventID}, Result: failure.result, NextAction: diag.ReportFailure, ErrorClass: failure.class})
		if err != nil {
			t.Fatal(err)
		}
		citation := diag.Citation{EventID: leaf.EventID, RootEventID: root.EventID, ErrorClass: failure.class, CauseCode: failure.code}
		if err := diag.VerifyCitations(s.events, root.EventID, []diag.Citation{citation}); err != nil {
			t.Fatal(err)
		}
		if err := diag.VerifyCitations(s.events, root.EventID, nil); err == nil {
			t.Fatal("uncited diagnosis accepted")
		}
		for _, mutate := range []func(*diag.Citation){
			func(c *diag.Citation) { c.EventID = id(t) }, func(c *diag.Citation) { c.RootEventID = id(t) },
			func(c *diag.Citation) { c.ErrorClass = diag.Internal }, func(c *diag.Citation) { c.CauseCode = diag.OperationFailed },
		} {
			bad := citation
			mutate(&bad)
			if err := diag.VerifyCitations(s.events, root.EventID, []diag.Citation{bad}); err == nil {
				t.Fatal("false claim accepted")
			}
		}
		if err := diag.VerifyCitations(s.events[1:], root.EventID, []diag.Citation{citation}); err == nil {
			t.Fatal("missing root accepted")
		}
		cyclic := slices.Clone(s.events)
		cyclic[1].Cause.ParentEventID = leaf.EventID
		if err := diag.VerifyCitations(cyclic, root.EventID, []diag.Citation{citation}); err == nil {
			t.Fatal("cycle accepted")
		}
		if err := diag.VerifyCitations(append(slices.Clone(s.events), root), root.EventID, []diag.Citation{citation}); err == nil {
			t.Fatal("duplicate id accepted")
		}
	}
}
