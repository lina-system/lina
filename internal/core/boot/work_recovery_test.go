// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestWorkAuthorityRecoveryScopesInterruptedCalls(t *testing.T) {
	for _, kind := range []string{"landing", "current epoch", "other agent", "other person", "other tool", "non-file task", "missing task"} {
		t.Run(kind, func(t *testing.T) {
			r, _, agent, _ := managementFixture(t)
			r.work = work.New(r.db)
			task := work.Task{ID: "file-task", PersonID: r.person, AgentID: agent, ConversationID: "conversation", Title: "Deliver files", Done: "Destination verified", Destination: t.TempDir()}
			task.RequiresApply = true
			task.Workspace = &work.Workspace{TaskID: task.ID, Path: t.TempDir()}
			if kind == "non-file task" {
				task.Destination = ""
			}
			if kind != "missing task" {
				if err := r.work.Create(t.Context(), task); err != nil {
					t.Fatal(err)
				}
			}
			revision, err := r.work.Revision(t.Context(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			target := "work:" + task.ID
			call := tools.Call{Key: "interrupted", Name: "land_work_result", Args: json.RawMessage(`{}`), Auth: grant.Request{
				GrantID: "fixture-grant", MainID: r.main, Epoch: r.epoch, TaskID: "turn", Host: r.main, OSUser: "core", Person: r.person, Agent: agent, Profile: "work", Revision: revision,
			}}
			switch kind {
			case "other agent":
				call.Auth.Agent = "another-agent"
			case "other person":
				call.Auth.Person = "another-person"
			case "other tool":
				call.Name = "report_work_result"
			}
			open := func() {
				t.Helper()
				r.personaAuthority, err = authority.Open(t.Context(), r.db, authority.Config{Epoch: r.epoch, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
					return grant.Grant{Identity: call.Auth, Expires: time.Now().Add(time.Hour), Revisions: map[string]string{target: revision}, Scope: []grant.Permission{{Tool: call.Name, Target: target, Write: true}}}, grant.Environment{Epoch: r.epoch, Now: time.Now(), OSPermitted: true, Revision: revision}, nil
				}})
				if err != nil {
					t.Fatal(err)
				}
			}
			open()
			spec := tools.Spec{Name: call.Name}
			input := tools.Input{Arguments: call.Args, Target: target, Write: true, Undoable: true}
			ticket, err := r.personaAuthority.Begin(t.Context(), call, spec, input)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.personaAuthority.Finish(t.Context(), ticket, tools.Result{State: tools.Unknown}); err != nil {
				t.Fatal(err)
			}
			if kind != "current epoch" {
				if err = r.db.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				r.epoch++
				call.Auth.Epoch = r.epoch
				r.db, err = canon.Open(t.Context(), canon.Config{Path: filepath.Join(r.root, "canon.sqlite"), Epoch: r.epoch, Controls: r.controls, Migrations: ApplicationMigrations()})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { r.db.Close(context.Background()) })
				r.work = work.New(r.db)
				open()
			}
			if err = r.work.Recover(t.Context(), "restarted"); err != nil {
				t.Fatal(err)
			}
			if err = r.recoverWorkAuthority(t.Context()); err != nil {
				t.Fatal(err)
			}
			effect, err := r.personaAuthority.Effect(t.Context(), call.Key)
			if err != nil {
				t.Fatal(err)
			}
			var active string
			if err = r.db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT active_key FROM authority_targets WHERE target=?", target).Scan(&active)
			}); err != nil {
				t.Fatal(err)
			}
			if kind != "landing" {
				if effect.State != tools.Unknown || active != call.Key {
					t.Fatalf("unreconciled effect lost reservation: %+v active=%q", effect, active)
				}
				return
			}
			if effect.State != tools.Cancelled || active != "" || effect.Result.Revision != revision {
				t.Fatalf("interrupted landing still reserved: %+v active=%q", effect, active)
			}
			replay, err := r.personaAuthority.Begin(t.Context(), call, spec, input)
			if err != nil || !replay.Replay || replay.Result.State != tools.Cancelled {
				t.Fatalf("old call redispatched: %+v %v", replay, err)
			}
			call.Key = "fresh-authorized-call"
			fresh, err := r.personaAuthority.Begin(t.Context(), call, spec, input)
			if err != nil || fresh.Replay {
				t.Fatalf("fresh call denied: %+v %v", fresh, err)
			}
		})
	}
}
