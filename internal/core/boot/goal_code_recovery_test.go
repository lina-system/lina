// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/records"
	goal "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestCodeGoalUnconfirmedDeployRemainsObservable(t *testing.T) {
	for _, tc := range []struct {
		name                string
		passed, exitFailure bool
	}{
		{"negative evidence", false, false},
		{"negative evidence and failed command", false, true},
		{"positive evidence but failed command", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, agent, _ := managementFixture(t)
			r.activities = lifecycle.NewRegistry(nil)
			recordsRepo, err := records.Open(r.root, r.db)
			if err != nil {
				t.Fatal(err)
			}
			defer recordsRepo.Close()
			e, err := goal.NewEngine(goal.Config{DB: r.db, Records: workDefinitions{recordsRepo}, Epoch: r.epoch})
			if err != nil {
				t.Fatal(err)
			}
			g := goal.Goal{ID: "g", Person: r.person, Agent: agent, Conversation: "conversation", Turn: "turn", Summary: "Run the service", Done: "The service is healthy", Scope: goal.GoalScope{Repositories: []string{"repo"}, Includes: "service"}, ServiceTarget: "service", Criteria: []goal.Criterion{{ID: "healthy", Statement: "Service is healthy", Check: goal.CriterionCheck{Kind: goal.RunEvidence, Method: "acceptance", Target: "repo"}}}}
			if err = e.Admit(t.Context(), g); err != nil {
				t.Fatal(err)
			}
			a := goal.Action{ID: "deploy", GoalID: g.ID, Kind: goal.Deploy, Revision: "merged"}
			shell, err := exec.LookPath("sh")
			if err != nil {
				t.Fatal(err)
			}
			output, failed := filepath.Join(r.root, "probe.json"), filepath.Join(r.root, "probe.failed")
			command := codeCommand{Path: shell, Args: []string{"-c", `cat "$1"; if [ -f "$2" ]; then exit 1; fi`, "probe", output, failed}}
			repo := codeRepository{Clone: r.root, Service: &codeService{Target: "service", Probe: command}}
			d := &codeGoalDriver{runtime: r, engine: e, connections: codeConnections{Repositories: map[string]codeRepository{"repo": repo}}}
			if err = d.deploymentIntent(a, repo, true); err != nil {
				t.Fatal(err)
			}
			target := "code-deploy:service"
			call := tools.Call{Key: a.ID, Name: "deploy_code_goal", Auth: grant.Request{GrantID: "deploy-grant", MainID: r.main, Epoch: r.epoch, TaskID: g.Turn, Host: r.main, OSUser: "core", Person: g.Person, Agent: agent, Profile: "code-goal", Revision: a.Revision}}
			r.personaAuthority, err = authority.Open(t.Context(), r.db, authority.Config{Epoch: r.epoch, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
				return grant.Grant{Identity: call.Auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: call.Name, Target: target, Write: true}}, Revisions: map[string]string{target: a.Revision}}, grant.Environment{Epoch: r.epoch, Now: time.Now(), OSPermitted: true, Revision: a.Revision}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			spec := tools.Spec{Name: call.Name, Classify: tools.Class(tools.LocalWrite)}
			input := tools.Input{Arguments: json.RawMessage(`{}`), Target: target, Write: true, Undoable: true}
			ticket, err := r.personaAuthority.Begin(t.Context(), call, spec, input)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.personaAuthority.Finish(t.Context(), ticket, tools.Result{State: tools.Unknown}); err != nil {
				t.Fatal(err)
			}
			writeProbe := func(passed bool) {
				t.Helper()
				raw, err := json.Marshal(codeCheckOutput{Revision: a.Revision, Target: "service", Passed: &passed, Evidence: "Read service health"})
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(output, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeProbe(tc.passed)
			if tc.exitFailure {
				if err = os.WriteFile(failed, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			observed, err := d.Observe(t.Context(), a)
			if err != nil || observed.Status != goal.OutcomeUnknown || observed.EffectsSettled || observed.Proof.Passed || observed.Proof.Ref == "" {
				t.Fatalf("unconfirmed deployment became terminal: %+v err=%v", observed, err)
			}
			effect, err := r.personaAuthority.Effect(t.Context(), a.ID)
			if err != nil || effect.State != tools.Unknown {
				t.Fatalf("unconfirmed authority effect: %+v %v", effect, err)
			}
			fresh := call
			fresh.Key = "next-deploy"
			if _, err = r.personaAuthority.Begin(t.Context(), fresh, spec, input); !errors.Is(err, tools.ErrInProgress) {
				t.Fatalf("unconfirmed target was released: %v", err)
			}
			writeProbe(true)
			if tc.exitFailure {
				if err = os.Remove(failed); err != nil {
					t.Fatal(err)
				}
			}
			observed, err = d.Observe(t.Context(), a)
			if err != nil || observed.Status != goal.OutcomeSucceeded || !observed.Proof.Passed || !observed.EffectsSettled {
				t.Fatalf("healthy deployment did not reconcile: %+v %v", observed, err)
			}
			effect, err = r.personaAuthority.Effect(t.Context(), a.ID)
			if err != nil || effect.State != tools.Applied {
				t.Fatalf("healthy authority effect: %+v %v", effect, err)
			}
			ticket, err = r.personaAuthority.Begin(t.Context(), fresh, spec, input)
			if err != nil || ticket.Replay {
				t.Fatalf("reconciled target stayed reserved: %+v %v", ticket, err)
			}
			if err = r.personaAuthority.Finish(t.Context(), ticket, tools.Result{State: tools.Cancelled}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
