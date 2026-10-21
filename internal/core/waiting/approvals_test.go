// SPDX-License-Identifier: Apache-2.0
package waiting_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/kit/grant"
	"testing"
)

func TestApprovalRowsUseExistingAuthorityAndDecisionRecord(t *testing.T) {
	db, _ := database(t)
	auth, err := authority.Open(t.Context(), db, authority.Config{Epoch: 1, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
		return grant.Grant{}, grant.Environment{}, errors.New("no tool dispatch")
	}})
	if err != nil {
		t.Fatal(err)
	}
	a := authority.Approval{ID: "approval", EffectKey: "effect", Epoch: 1, Person: "person", Agent: "agent", State: authority.Pending, What: "Publish the result", Outward: true, Choices: []authority.Choice{authority.Once, authority.Deny}}
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO authority_approvals(approval_id,effect_key,fingerprint,record) VALUES(?,?,?,?)", a.ID, a.EffectKey, "fingerprint", data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	source := &waiting.ApprovalSource{Service: auth, Decider: authority.Decider{Person: "person", Epoch: 1, Authenticated: true, Surface: authority.Text}}
	svc := waiting.New(db, map[string]waiting.Source{"approval": source})
	list, err := svc.Build(t.Context(), "person", waiting.Options{Now: now})
	if err != nil || len(list.Entries) != 1 {
		t.Fatalf("approvals=%+v %v", list, err)
	}
	ref := list.Entries[0].Ref
	if _, err := svc.Answer(t.Context(), "person", ref, "once", now); !errors.Is(err, authority.ErrDecider) {
		t.Fatalf("text became approval: %v", err)
	}
	source.Decider.Surface = authority.App
	d, err := svc.Answer(t.Context(), "person", ref, "once", now)
	if err != nil || d.Ref != ref || d.Person != "person" || d.Agent != "agent" || d.Answer != "once" {
		t.Fatalf("attributed approval: %+v %v", d, err)
	}
	list, err = svc.Build(t.Context(), "person", waiting.Options{Now: now})
	if err != nil || list.Total != 0 {
		t.Fatalf("answered approval remains: %+v %v", list, err)
	}
	records, err := svc.Decisions(t.Context(), "person")
	if err != nil || len(records) != 1 || records[0] != d {
		t.Fatalf("records=%+v %v", records, err)
	}
	if _, err := svc.Answer(t.Context(), "person", ref, "deny", now); !errors.Is(err, waiting.ErrStale) {
		t.Fatalf("duplicate approval: %v", err)
	}
}
