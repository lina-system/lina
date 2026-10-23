// SPDX-License-Identifier: Apache-2.0
// Package dispatch connects installed workers to durable Core task receipts.
package dispatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type Definition struct {
	Kind       string   `json:"kind,omitempty"`
	Skill      string   `json:"skill,omitempty"`
	Repository string   `json:"repository"`
	Base       string   `json:"base"`
	Branch     string   `json:"branch"`
	What       string   `json:"what"`
	Why        string   `json:"why"`
	Scope      []string `json:"scope"`
	NotTouch   []string `json:"not_touch"`
	Done       string   `json:"done"`
	DesignRefs []string `json:"design_refs"`
	WriteArea  []string `json:"write_area"`
}
type Origin struct{ Person, Agent, Conversation, Turn string }
type Record struct {
	Method         *skills.Body               `json:"method,omitempty"`
	BaseCommit     string                     `json:"base_commit,omitempty"`
	ChangeEvidence string                     `json:"change_evidence,omitempty"`
	Owner          string                     `json:"owner,omitempty"`
	Manager        string                     `json:"manager,omitempty"`
	ID             string                     `json:"id"`
	Origin         Origin                     `json:"origin"`
	Definition     Definition                 `json:"definition"`
	Branch         string                     `json:"branch,omitempty"`
	State          string                     `json:"state"`
	Code           string                     `json:"code,omitempty"`
	Version        string                     `json:"version,omitempty"`
	PR             *antigravity.PullRequest   `json:"pull_request,omitempty"`
	Denied         []antigravity.DeniedAction `json:"denied_actions,omitempty"`
	Announced      bool                       `json:"announced"`
}

// TaskDefinition exposes the actual assigned branch while retaining the
// caller's definition for admission retries. Older records retain their name.
func (r Record) TaskDefinition() Definition {
	d := r.Definition
	if r.Branch != "" {
		d.Branch = r.Branch
	}
	return d
}

func taskBranch(requested, id string) string {
	if strings.Contains(requested, id) {
		return requested
	}
	return requested + "." + id
}

type Config struct {
	Manage      func(context.Context, Record) (Record, error)
	Skills      *skills.Store
	DB          *canon.Store
	Work        *work.Service
	Area        *work.Area
	Root, Main  string
	Epoch       uint64
	Binary, Git string
	PR          antigravity.PullRequestReader
	Environment []string
	Activities  lease.Leaser
	Publish     func(context.Context, Record) error
}
type Service struct {
	cfg     Config
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	running map[string]bool
	wg      sync.WaitGroup
}

func Open(ctx context.Context, cfg Config) (*Service, error) {
	s := &Service{cfg: cfg, running: map[string]bool{}, done: make(chan struct{})}
	s.ctx, s.cancel = context.WithCancel(ctx)
	records, err := s.records(ctx)
	if err != nil {
		s.cancel()
		return nil, err
	}
	for _, r := range records {
		if r.State == "running" {
			r.State = "unknown"
			r.Code = "interrupted_execution"
			if err = s.save(ctx, r); err != nil {
				s.cancel()
				return nil, err
			}
		}
	}
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			s.sync()
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return s, nil
}
func (s *Service) Close(ctx context.Context) error {
	s.cancel()
	select {
	case <-s.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

func validate(d Definition) error {
	if !filepath.IsAbs(d.Repository) || filepath.Clean(d.Repository) != d.Repository || !branchName.MatchString(d.Base) || !branchName.MatchString(d.Branch) || d.Branch == d.Base || d.Branch == "main" || d.Branch == "master" || d.Branch == "dev" || strings.TrimSpace(d.What) == "" || strings.TrimSpace(d.Why) == "" || strings.TrimSpace(d.Done) == "" || len(d.Scope) == 0 || len(d.DesignRefs) == 0 || len(d.WriteArea) == 0 {
		return work.ErrInvalid
	}
	for _, pattern := range d.WriteArea {
		if strings.TrimSpace(pattern) == "" || path.IsAbs(pattern) || strings.Contains(pattern, "..") {
			return work.ErrInvalid
		}
		if _, err := path.Match(pattern, "file"); err != nil {
			return err
		}
	}
	for _, ref := range d.DesignRefs {
		file, anchor, ok := strings.Cut(ref, "#")
		if !ok || strings.TrimSpace(anchor) == "" || file == "" || path.Clean(file) != file || strings.HasPrefix(file, "/") || strings.HasPrefix(file, "../") || strings.ContainsAny(file, ":\x00") {
			return work.ErrInvalid
		}
	}
	return nil
}
func (s *Service) Tool(origin Origin) loop.Tool {
	decode := func(c provider.ToolCall) (Definition, error) {
		var d Definition
		decoder := json.NewDecoder(strings.NewReader(c.Arguments))
		decoder.DisallowUnknownFields()
		if c.ID == "" || c.Custom || c.Name != "dispatch_work" || len(c.Arguments) > 1<<20 {
			return d, work.ErrInvalid
		}
		if err := decoder.Decode(&d); err != nil {
			return d, err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return d, work.ErrInvalid
		}
		masked, err := secrets.Mask(c.Arguments)
		if err != nil || masked.Text() != c.Arguments {
			return d, work.ErrInvalid
		}
		if err := validate(d); err != nil {
			return d, err
		}
		d.Skill, err = skills.MethodID(d.Kind, d.Skill)
		return d, err
	}
	return loop.Tool{Validate: func(c provider.ToolCall) (string, error) {
		d, err := decode(c)
		if err == nil {
			_, _, err = s.recordForAdmission(s.ctx, origin, c.ID, d)
		}
		return d.Repository, err
	}, Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
		d, err := decode(c)
		if err != nil {
			return loop.Result{ID: c.ID, Code: loop.Invalid}
		}
		r, replay, err := s.recordForAdmission(ctx, origin, c.ID, d)
		if err != nil {
			if errors.Is(err, work.ErrState) {
				return loop.Result{ID: c.ID, Code: loop.Denied}
			}
			return loop.Result{ID: c.ID, Code: loop.Invalid}
		}
		if origin.Person == "" || origin.Agent == "" || origin.Conversation == "" || origin.Turn == "" {
			return loop.Result{ID: c.ID, Code: loop.Denied}
		}
		canonical, err := filepath.EvalSymlinks(d.Repository)
		if err != nil {
			return loop.Result{ID: c.ID, Code: loop.Denied}
		}
		d.Repository = canonical
		key := admissionID(origin, c.ID)
		if err := authority.CheckPreset(ctx, s.cfg.DB, s.cfg.Epoch, authority.PolicyRequest{Key: "preset:dispatch:" + key, Person: origin.Person, Agent: origin.Agent, TaskID: origin.Turn, Tool: "dispatch_work", Target: d.Repository, Class: tools.WorkerEffect, Arguments: mustJSON(d)}); err != nil {
			body := map[string]string{"code": err.Error()}
			if errors.Is(err, authority.ErrPresetLimit) {
				body["boundary"] = "This work is limited to advice or private drafts; unrestricted worker execution is unavailable."
			}
			return loop.Result{ID: c.ID, Code: loop.Denied, Output: mustJSON(body)}
		}
		r.Definition = d
		if !replay {
			r.BaseCommit, err = s.resolveBase(ctx, r.TaskDefinition())
			if err != nil {
				return loop.Result{ID: c.ID, Code: loop.Invalid}
			}
		}
		// A durable admission already owns its manager decision and base.
		if !replay && s.cfg.Manage != nil {
			r, err = s.cfg.Manage(ctx, r)
			if err != nil {
				return loop.Result{ID: c.ID, Code: loop.Denied, Output: mustJSON(map[string]string{"code": err.Error()})}
			}
		}
		err = s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
			var prior []byte
			readErr := tx.QueryRowContext(ctx, "SELECT record FROM worker_dispatch WHERE id=?", r.ID).Scan(&prior)
			if readErr == nil {
				var saved Record
				if json.Unmarshal(prior, &saved) != nil || saved.Origin != origin || !reflect.DeepEqual(saved.Definition, d) {
					return work.ErrState
				}
				r = saved
				return nil
			}
			if !errors.Is(readErr, sql.ErrNoRows) {
				return readErr
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO worker_dispatch(id,turn_id,repository,state,record) VALUES(?,?,?,?,?) ON CONFLICT(id) DO NOTHING", r.ID, origin.Turn, d.Repository, r.State, mustJSON(r))
			return err
		})
		if err != nil {
			return loop.Result{ID: c.ID, Code: loop.Denied}
		}
		return loop.Result{ID: c.ID, Code: loop.OK, Output: mustJSON(map[string]string{"id": r.ID, "branch": r.TaskDefinition().Branch, "state": "queued", "owner": r.Owner, "manager": r.Manager})}
	}}
}
func admissionID(origin Origin, callID string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(origin.Turn+"\x00"+callID)))
}

func (s *Service) recordForAdmission(ctx context.Context, origin Origin, callID string, d Definition) (Record, bool, error) {
	key := admissionID(origin, callID)
	var raw []byte
	err := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT record FROM worker_dispatch WHERE id=?", key).Scan(&raw)
	})
	if errors.Is(err, sql.ErrNoRows) {
		method, err := s.cfg.Skills.ResolveMethod(ctx, d.Kind, d.Skill)
		return Record{ID: key, Origin: origin, Definition: d, Branch: taskBranch(d.Branch, key), Method: method, State: "queued"}, false, err
	}
	if err != nil {
		return Record{}, false, err
	}
	var saved Record
	if err := json.Unmarshal(raw, &saved); err != nil {
		return Record{}, false, err
	}
	// A durable admission owns its snapshot even if the catalog entry was
	// subsequently removed. Compare the complete request before replaying it.
	d.Repository, err = filepath.EvalSymlinks(d.Repository)
	if err != nil {
		return Record{}, false, err
	}
	if saved.Origin != origin || !reflect.DeepEqual(saved.Definition, d) {
		return Record{}, false, work.ErrState
	}
	return saved, true, nil
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func (s *Service) save(ctx context.Context, r Record) error {
	return s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE worker_dispatch SET state=?,record=? WHERE id=?", r.State, mustJSON(r), r.ID)
		return err
	})
}
func (s *Service) records(ctx context.Context) ([]Record, error) {
	var result []Record
	err := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record FROM worker_dispatch ORDER BY id")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var r Record
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &r); err != nil {
				return err
			}
			result = append(result, r)
		}
		return rows.Err()
	})
	return result, err
}
func (s *Service) sync() {
	records, err := s.records(s.ctx)
	if err != nil {
		return
	}
	for _, r := range records {
		if r.State == "queued" {
			var committed int
			err = s.cfg.DB.Read(s.ctx, func(tx *canon.Tx) error {
				return tx.QueryRowContext(s.ctx, `SELECT count(*) FROM turn_calls WHERE turn_id=? AND json_extract(call,'$.Name')='dispatch_work' AND json_extract(result,'$.code')='ok' AND json_extract(result,'$.output.id')=?`, r.Origin.Turn, r.ID).Scan(&committed)
			})
			if err != nil || committed == 0 {
				continue
			}
			s.mu.Lock()
			busy := s.running[r.ID]
			if !busy {
				s.running[r.ID] = true
			}
			s.mu.Unlock()
			if busy {
				continue
			}
			r.State = "running"
			if s.save(s.ctx, r) != nil {
				s.mu.Lock()
				delete(s.running, r.ID)
				s.mu.Unlock()
				continue
			}
			s.wg.Add(1)
			go func() { defer s.wg.Done(); s.execute(r) }()
		} else if (r.State == "finished" || r.State == "unknown") && !r.Announced {
			if s.cfg.Publish != nil && s.cfg.Publish(s.ctx, r) == nil {
				r.Announced = true
				_ = s.save(s.ctx, r)
			}
		}
	}
}
func Declaration() provider.ToolDecl {
	return provider.ToolDecl(`{"type":"function","name":"dispatch_work","description":"Delegate a code change to the person's installed Antigravity worker. Core allocates a detached task worktree from a local GitHub clone. It appends the generated task ID to the requested branch unless already present, and returns the assigned branch. The worker creates that branch. Supply the complete authorized task, design sections, done condition and write area. The dev manager checks the definition before Core returns a task receipt; completion returns to this conversation. The worker uses its own permissions and login. Optional kind selects a development method from skill_index methodology=dev; skill overrides it with one catalog ID. Core includes that version in the task packet.","parameters":{"type":"object","properties":{"kind":{"type":"string"},"skill":{"type":"string"},"repository":{"type":"string"},"base":{"type":"string"},"branch":{"type":"string"},"what":{"type":"string"},"why":{"type":"string"},"scope":{"type":"array","items":{"type":"string"}},"not_touch":{"type":"array","items":{"type":"string"}},"done":{"type":"string"},"design_refs":{"type":"array","items":{"type":"string"}},"write_area":{"type":"array","items":{"type":"string"}}},"required":["repository","base","branch","what","why","scope","not_touch","done","design_refs","write_area"],"additionalProperties":false}}`)
}

type output struct{ bytes.Buffer }

func (o *output) Write(p []byte) (int, error) {
	if o.Len()+len(p) > 2<<20 {
		return 0, errors.New("command output limit")
	}
	return o.Buffer.Write(p)
}
func (s *Service) command(ctx context.Context, l lease.Lease, owner *proc.Owner, dir string, args ...string) (string, error) {
	var out output
	err := owner.Run(ctx, l, proc.Command{Path: s.cfg.Git, Args: append([]string{"-C", dir}, args...), Env: s.cfg.Environment, Stdout: &out, Stderr: io.Discard})
	return strings.TrimRight(out.String(), "\r\n"), err
}
