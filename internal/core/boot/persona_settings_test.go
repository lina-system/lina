// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/loop"
)

func TestPersonaSettingsExcludeHiddenEvidence(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "main:"+agent
	r.personaMu, r.personaGrants = &sync.Mutex{}, map[string]grant.Grant{}
	r.activities = lifecycle.NewRegistry(nil)
	r.persona = coreexec.NewPersona(r.db, r.agents, nil)
	var err error
	r.skills, err = skills.Open(t.TempDir(), r.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.skills.Close() })
	r.personaAuthority, err = authority.Open(t.Context(), r.db, authority.Config{Epoch: r.epoch, Resolve: r.resolvePersonaGrant})
	if err != nil {
		t.Fatal(err)
	}
	state := agents.State{
		Settings:       map[string]string{"likes": "violet", "birthday": "June 12", "blank": "", "unsourced": "unverified"},
		SettingSources: map[string]string{"likes": "hidden", "birthday": "visible", "orphan": "unused"},
		WorkMethodFills: map[string]agents.WorkMethodFill{
			"ask.form":     {Value: json.RawMessage(`"narrowed"`), Source: "hidden"},
			"ask.per_turn": {Value: json.RawMessage(`2`), Source: "visible"},
			"retired":      {Value: json.RawMessage(`"old"`), Source: "hidden"},
			"unsourced":    {Value: json.RawMessage(`"unverified"`)},
		},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(t.Context(), "UPDATE agents SET state=? WHERE id=?", raw, agent); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), "INSERT INTO retract_block(item_id,person,holder,event_id) VALUES(?,?,?,?)", "forgotten", r.person, agent, "hidden")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p, err := r.agents.Prepare(t.Context(), "settings", agent)
	if err != nil {
		t.Fatal(err)
	}
	p.WorkMethod = `{"schema":1,"preset":"lina","ask":{"form":"choices","per_turn":1},"fill":["ask.form","ask.per_turn"]}`
	_, packet, err := r.preparePersonaTools(t.Context(), "settings", ledger.Event{ID: "input"}, p, time.Now(), map[string]loop.Tool{})
	if err != nil {
		t.Fatal(err)
	}
	var item assemble.ContextItem
	for _, candidate := range packet {
		if candidate.ID == "persona-settings:"+agent {
			item = candidate
		}
	}
	var projected struct {
		Values     map[string]string         `json:"values"`
		WorkMethod agents.WorkMethodSettings `json:"work_method"`
	}
	if err := json.Unmarshal([]byte(item.Text), &projected); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"birthday": "June 12", "blank": ""}; !reflect.DeepEqual(projected.Values, want) {
		t.Errorf("setting projection=%v, want %v", projected.Values, want)
	}
	if len(projected.WorkMethod.Filled) != 1 || string(projected.WorkMethod.Values["ask.form"]) != `"choices"` {
		t.Errorf("hidden work-method fills still projected: %+v", projected.WorkMethod)
	}
	if projected.WorkMethod.Filled["ask.per_turn"].Source != "visible" || string(projected.WorkMethod.Values["ask.per_turn"]) != `2` {
		t.Error("visible work-method provenance lost")
	}
	if want := []assemble.Ref{{ID: "visible"}}; !reflect.DeepEqual(item.Evidence, want) {
		t.Errorf("evidence=%v, want %v", item.Evidence, want)
	}
	after, err := r.agents.State(t.Context(), agent)
	if err != nil || !reflect.DeepEqual(after, state) || item.Revision != agents.StateRevision(state) {
		t.Fatal("projection changed stored state or its CAS revision", err)
	}
}
