// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/secrets"
	goal "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/tools"
)

type codeCheckOutput struct {
	Revision string `json:"revision"`
	Target   string `json:"target"`
	Passed   *bool  `json:"passed"`
	Evidence string `json:"evidence"`
}
type codeCheckRecord struct {
	Command string          `json:"command"`
	Output  codeCheckOutput `json:"output"`
	ExitOK  bool            `json:"exit_ok"`
}
type codeOutput struct{ bytes.Buffer }

func (b *codeOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("code check output too large")
	}
	return b.Buffer.Write(p)
}
func commandKey(c codeCommand) string { raw, _ := json.Marshal(c); return codingID(string(raw)) }
func (d *codeGoalDriver) evidencePath(a goal.Action, c codeCommand) string {
	return filepath.Join(d.runtime.root, "work", "goal-evidence", codingID(a.ID, commandKey(c))+".json")
}
func (d *codeGoalDriver) command(ctx context.Context, dir string, c codeCommand, env []string) ([]byte, error) {
	l, err := d.runtime.activities.Acquire(ctx, lease.Activity{Class: lease.Hard, Reason: "Code goal check"})
	if err != nil {
		return nil, err
	}
	defer l.Release()
	owner := proc.NewOwner()
	defer owner.Close(context.Background())
	var out codeOutput
	err = owner.Run(ctx, l, proc.Command{Path: c.Path, Args: c.Args, Dir: dir, Env: env, Stdout: &out, Stderr: io.Discard})
	return out.Bytes(), err
}
func (d *codeGoalDriver) runCheck(ctx context.Context, g goal.Goal, a goal.Action, repo codeRepository, c codeCommand, target string, checkout bool) (goal.Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	env := proc.WorkerEnvironment()
	env = proc.Environment(env, "LINA_GOAL_REVISION", "LINA_GOAL_TARGET", "LINA_GOAL_ACTION")
	env = append(env, "LINA_GOAL_REVISION="+a.Revision, "LINA_GOAL_TARGET="+target, "LINA_GOAL_ACTION="+a.ID)
	dir := repo.Clone
	if checkout {
		git, err := proc.LookPath("git")
		if err != nil {
			return goal.Observation{}, err
		}
		runGit := func(ctx context.Context, args ...string) error {
			_, err := d.command(ctx, repo.Clone, codeCommand{Path: git, Args: args}, env)
			return err
		}
		if err = runGit(ctx, "cat-file", "-e", a.Revision+"^{commit}"); err != nil {
			if err = runGit(ctx, "fetch", "--no-tags", "origin", a.Revision); err != nil {
				return goal.Observation{}, err
			}
		}
		parent := filepath.Join(d.runtime.root, "work", "goal-checks")
		if err = os.MkdirAll(parent, 0700); err != nil {
			return goal.Observation{}, err
		}
		dir, err = os.MkdirTemp(parent, "check-")
		if err != nil {
			return goal.Observation{}, err
		}
		if err = runGit(ctx, "worktree", "add", "--detach", dir, a.Revision); err != nil {
			_ = os.Remove(dir)
			return goal.Observation{}, err
		}
		defer func() {
			clean, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			_ = runGit(clean, "worktree", "remove", "--force", dir)
		}()
	}
	raw, runErr := d.command(ctx, dir, c, env)
	masked, err := secrets.Mask(string(raw))
	if err != nil {
		return goal.Observation{}, err
	}
	var result codeCheckOutput
	decoder := json.NewDecoder(strings.NewReader(masked.Text()))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&result)
	if decodeErr == nil && decoder.Decode(new(any)) != io.EOF {
		decodeErr = goal.ErrInvalid
	}
	if decodeErr != nil || result.Revision != a.Revision || result.Target != target || result.Passed == nil || strings.TrimSpace(result.Evidence) == "" {
		return goal.Observation{}, errors.New("invalid code check evidence")
	}
	record := codeCheckRecord{Command: commandKey(c), Output: result, ExitOK: runErr == nil}
	saved, _ := json.Marshal(record)
	path := d.evidencePath(a, c)
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return goal.Observation{}, err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".evidence-")
	if err != nil {
		return goal.Observation{}, err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(saved); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err != nil {
		return goal.Observation{}, err
	}
	proof, err := d.readProof(path, c, target, a.Revision)
	status := goal.OutcomeSucceeded
	if !proof.Passed {
		status = goal.OutcomeFailed
	}
	return goal.Observation{Status: status, Proof: proof, EffectsSettled: true}, err
}
func (d *codeGoalDriver) readProof(path string, c codeCommand, target, revision string) (goal.Proof, error) {
	proof := goal.Proof{Revision: revision, Ref: path}
	parent := filepath.Join(d.runtime.root, "work", "goal-evidence")
	if filepath.Dir(path) != parent {
		return proof, goal.ErrInvalid
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return proof, err
	}
	var record codeCheckRecord
	if err = json.Unmarshal(raw, &record); err != nil {
		return proof, err
	}
	if record.Command != commandKey(c) || record.Output.Revision != revision || record.Output.Target != target || record.Output.Passed == nil || record.Output.Evidence == "" {
		return proof, goal.ErrInvalid
	}
	proof.Passed = record.ExitOK && *record.Output.Passed
	return proof, nil
}
func (d *codeGoalDriver) readCheck(a goal.Action, c codeCommand, target string) (goal.Observation, error) {
	proof, err := d.readProof(d.evidencePath(a, c), c, target, a.Revision)
	if err != nil {
		return goal.Observation{Status: goal.OutcomeUnknown}, nil
	}
	status := goal.OutcomeSucceeded
	if !proof.Passed {
		status = goal.OutcomeFailed
	}
	return goal.Observation{Status: status, Proof: proof, EffectsSettled: true}, nil
}

// Retain the exact owner-selected service before an effect can start. A changed
// connection after restart cannot settle an old deploy against a different host.
func (d *codeGoalDriver) deploymentIntent(a goal.Action, repo codeRepository, create bool) error {
	raw, _ := json.Marshal(struct {
		Action, Revision, Clone string
		Service                 *codeService
	}{a.ID, a.Revision, repo.Clone, repo.Service})
	path := filepath.Join(d.runtime.root, "work", "goal-evidence", codingID(a.ID)+".deploy.json")
	if create {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			_, err = f.Write(raw)
			if err == nil {
				err = f.Sync()
			}
			return errors.Join(err, f.Close())
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(saved, raw) {
		return goal.ErrNeedsPerson
	}
	return nil
}

func (d *codeGoalDriver) deploy(ctx context.Context, g goal.Goal, a goal.Action, repo codeRepository) (goal.Observation, error) {
	if repo.Service == nil {
		return goal.Observation{}, goal.ErrNeedsPerson
	}
	r := d.runtime
	service := repo.Service
	if err := d.deploymentIntent(a, repo, true); err != nil {
		return goal.Observation{}, err
	}
	class := tools.LocalWrite
	if service.Public {
		class = tools.ExternalEffect
	}
	args, _ := json.Marshal(map[string]any{"target": service.Target, "revision": a.Revision, "command": service.Deploy})
	spec := tools.Spec{Name: "deploy_code_goal", Classify: tools.Class(class)}
	input := tools.Input{Arguments: args, Target: "code-deploy:" + service.Target, What: "Deploy verified code", Where: service.Target, ScopeKey: "code-goal", Write: true, Undoable: !service.Public}
	auth := grant.Request{GrantID: "goal-deploy:" + a.ID, MainID: r.main, Epoch: r.epoch, TaskID: g.Turn, Host: r.main, OSUser: "core", Person: g.Person, Agent: g.Agent, Profile: "code-goal", Revision: a.Revision}
	r.personaMu.Lock()
	r.personaGrants[auth.GrantID] = grant.Grant{Identity: auth, Expires: time.Now().Add(24 * time.Hour), Scope: []grant.Permission{{Tool: spec.Name, Target: input.Target, Write: true}}, Revisions: map[string]string{input.Target: a.Revision}}
	r.personaMu.Unlock()
	defer func() { r.personaMu.Lock(); delete(r.personaGrants, auth.GrantID); r.personaMu.Unlock() }()
	_, sub, err := r.topics.Subscribe("waiting", nil)
	if err != nil {
		return goal.Observation{}, err
	}
	defer sub.Close()
	call := tools.Call{Key: a.ID, Name: spec.Name, Auth: auth}
	ticket, err := r.personaAuthority.Begin(ctx, call, spec, input)
	for errors.Is(err, tools.ErrApprovalRequired) {
		if _, err = sub.Next(ctx); err != nil {
			return goal.Observation{}, err
		}
		if err = d.Authorize(ctx, a); err != nil {
			return goal.Observation{}, err
		}
		ticket, err = r.personaAuthority.Begin(ctx, call, spec, input)
	}
	if err != nil {
		if errors.Is(err, authority.ErrDenied) || errors.Is(err, authority.ErrNoSurface) || errors.Is(err, authority.ErrStale) {
			return goal.Observation{}, goal.ErrNeedsPerson
		}
		return goal.Observation{}, err
	}
	if ticket.Replay {
		return d.runCheck(ctx, g, a, repo, service.Probe, service.Target, false)
	}
	_, err = d.runCheck(ctx, g, a, repo, service.Deploy, service.Target, true)
	result := tools.Result{State: tools.Unknown}
	var observation goal.Observation
	if err == nil {
		observation, err = d.runCheck(ctx, g, a, repo, service.Probe, service.Target, false)
		if err == nil && observation.Proof.Passed {
			result.State = tools.Applied
			result.Revision = a.Revision
		}
	}
	if finishErr := r.personaAuthority.Finish(ctx, ticket, result); finishErr != nil {
		return goal.Observation{}, errors.Join(err, finishErr)
	}
	if result.State != tools.Applied {
		return goal.Observation{Status: goal.OutcomeUnknown}, nil
	}
	return observation, nil
}
