// SPDX-License-Identifier: Apache-2.0
package authority_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/kit/tools"
)

func workerApproval() authority.Approval {
	return authority.Approval{EffectKey: "worker/session/request", Person: "person", Agent: "agent", Conversation: "conversation", Tool: "command", What: "Run command", Where: "workspace", Target: "workspace", Arguments: json.RawMessage(`{"command":"echo example","token":"[masked]"}`)}
}

func requestWorker(t *testing.T, f *fixture) authority.Approval {
	t.Helper()
	a, err := f.service.RequestWorkerApproval(t.Context(), workerApproval())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestWorkerApprovalRejectsInvalidRequest(t *testing.T) {
	for name, change := range map[string]func(*authority.Approval){
		"effect":              func(a *authority.Approval) { a.EffectKey = "" },
		"person":              func(a *authority.Approval) { a.Person = "" },
		"agent":               func(a *authority.Approval) { a.Agent = "" },
		"conversation":        func(a *authority.Approval) { a.Conversation = "" },
		"tool":                func(a *authority.Approval) { a.Tool = "" },
		"whitespace":          func(a *authority.Approval) { a.Person = " \t" },
		"missing arguments":   func(a *authority.Approval) { a.Arguments = nil },
		"malformed arguments": func(a *authority.Approval) { a.Arguments = json.RawMessage(`{`) },
		"trailing arguments":  func(a *authority.Approval) { a.Arguments = json.RawMessage(`{} {}`) },
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			a := workerApproval()
			change(&a)
			if _, err := f.service.RequestWorkerApproval(t.Context(), a); !errors.Is(err, tools.ErrInvalid) {
				t.Fatalf("invalid request: %v", err)
			}
			if rows, err := f.service.Pending(t.Context()); err != nil || len(rows) != 0 {
				t.Fatalf("invalid request persisted: %+v %v", rows, err)
			}
		})
	}
}

func TestWorkerApprovalRequiresPresentation(t *testing.T) {
	f := setup(t)
	f.surfaceAvailable = false
	if _, err := f.service.RequestWorkerApproval(t.Context(), workerApproval()); !errors.Is(err, authority.ErrNoSurface) {
		t.Fatalf("no surface: %v", err)
	}
	if rows, err := f.service.Pending(t.Context()); err != nil || len(rows) != 0 {
		t.Fatalf("inaccessible request persisted: %+v %v", rows, err)
	}
	f.surfaceAvailable = true
	a := requestWorker(t, f)
	f.surfaceAvailable = false
	if _, err := f.service.RequestWorkerApproval(t.Context(), workerApproval()); !errors.Is(err, authority.ErrNoSurface) {
		t.Fatalf("retry without surface: %v", err)
	}
	if got := pending(t, f); got.ID != a.ID {
		t.Fatal("lost pending request after disconnect")
	}
}

func TestWorkerApprovalOwnsIdentityAndScope(t *testing.T) {
	f := setup(t)
	in := workerApproval()
	in.ID, in.ScopeKey, in.Plugin = "injected", "shared-scope", ""
	in.Epoch, in.State, in.Choice = 99, authority.Approved, authority.Always
	in.Choices = []authority.Choice{authority.Always}
	in.AnsweredBy, in.StandingID = "injected", "standing"
	in.ChatID, in.MessageID, in.AnsweredAt = "chat", "message", time.Now()
	in.Network = true
	a, err := f.service.RequestWorkerApproval(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == "" || a.ID == in.ID || a.ScopeKey == "" || a.ScopeKey == in.ScopeKey || a.Plugin != "worker-relay" || a.Epoch != f.cfg.Epoch || a.State != authority.Pending || a.Choice != "" || !reflect.DeepEqual(a.Choices, []authority.Choice{authority.Once, authority.Deny}) {
		t.Fatalf("untrusted lifecycle fields: %+v", a)
	}
	if a.Conversation != in.Conversation || a.Person != in.Person || a.Agent != in.Agent || a.AnsweredBy != "" || a.StandingID != "" || a.ChatID != "" || a.MessageID != "" || !a.AnsweredAt.IsZero() {
		t.Fatalf("origin or audit: %+v", a)
	}
	retry, err := f.service.RequestWorkerApproval(t.Context(), a)
	if err != nil || retry.ID != a.ID {
		t.Fatalf("idempotent request: %+v %v", retry, err)
	}
	in.EffectKey = "worker/session/second-request"
	b, err := f.service.RequestWorkerApproval(t.Context(), in)
	if err != nil || b.ScopeKey == a.ScopeKey {
		t.Fatalf("shared scope: %+v %v", b, err)
	}
	groups, err := f.service.Groups(t.Context(), in.Person)
	if err != nil || len(groups) != 2 || len(groups[0].Members) != 1 || len(groups[1].Members) != 1 {
		t.Fatalf("requests grouped: %+v %v", groups, err)
	}
	if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: authority.Always}); !errors.Is(err, authority.ErrChoice) {
		t.Fatalf("standing choice: %v", err)
	}
	if grants, err := f.service.Standing(t.Context(), in.Person); err != nil || len(grants) != 0 {
		t.Fatalf("standing grant created: %+v %v", grants, err)
	}
}

func TestWorkerApprovalFingerprintBindsArgumentsAndOrigin(t *testing.T) {
	f := setup(t)
	a := requestWorker(t, f)
	if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: authority.Once}); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*authority.Approval){
		"arguments":    func(a *authority.Approval) { a.Arguments = json.RawMessage(`{"command":"replacement"}`) },
		"person":       func(a *authority.Approval) { a.Person = "other" },
		"agent":        func(a *authority.Approval) { a.Agent = "other" },
		"conversation": func(a *authority.Approval) { a.Conversation = "other" },
		"tool":         func(a *authority.Approval) { a.Tool = "other" },
		"what":         func(a *authority.Approval) { a.What = "different plan" },
		"where":        func(a *authority.Approval) { a.Where = "elsewhere" },
		"target":       func(a *authority.Approval) { a.Target = "other" },
		"undoable":     func(a *authority.Approval) { a.Undoable = true },
		"network":      func(a *authority.Approval) { a.Network = true },
		"outward":      func(a *authority.Approval) { a.Outward = true },
	} {
		t.Run(name, func(t *testing.T) {
			in := workerApproval()
			change(&in)
			if _, err := f.service.RequestWorkerApproval(t.Context(), in); !errors.Is(err, tools.ErrKeyConflict) {
				t.Fatalf("replacement accepted: %v", err)
			}
		})
	}
	in := workerApproval()
	in.Arguments = json.RawMessage(`{ "token": "[masked]", "command": "echo example" }`)
	retry, err := f.service.RequestWorkerApproval(t.Context(), in)
	if err != nil || retry.ID != a.ID || retry.State != authority.Approved {
		t.Fatalf("canonical retry: %+v %v", retry, err)
	}
}

func TestWorkerApprovalUsesAuthenticatedAnswer(t *testing.T) {
	f := setup(t)
	a := requestWorker(t, f)
	for _, test := range []struct {
		name   string
		change func(*authority.Decider)
		want   error
	}{
		{"wrong person", func(d *authority.Decider) { d.Person = "other" }, authority.ErrDecider},
		{"stale epoch", func(d *authority.Decider) { d.Epoch-- }, authority.ErrStale},
		{"unauthenticated", func(d *authority.Decider) { d.Authenticated = false }, authority.ErrDecider},
		{"model descendant", func(d *authority.Decider) { d.CoreDescendant = true }, authority.ErrDecider},
		{"model text", func(d *authority.Decider) { d.Surface = authority.Text }, authority.ErrDecider},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := owner(f)
			test.change(&d)
			if err := f.service.Answer(t.Context(), a.ID, d, authority.Decision{Choice: authority.Once}); !errors.Is(err, test.want) {
				t.Fatalf("answer: %v", err)
			}
			if _, err := f.service.ClaimWorkerAnswer(t.Context(), a.ID); !errors.Is(err, authority.ErrStale) {
				t.Fatalf("unanswered claim: %v", err)
			}
		})
	}
	if got := pending(t, f); got.ID != a.ID {
		t.Fatal("invalid answer changed request")
	}
}
