// SPDX-License-Identifier: Apache-2.0
package results

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/result"
)

// Planning owns the transition from ledger preview to confirmed records. Core
// calls Accept and Confirm only from authenticated person actions, never tools.
type Planning struct {
	results *Store
	repo    *records.Store
	mu      sync.Mutex
}
type PlanRecord struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}
type PlanReceipt struct {
	State    string   `json:"effect_state"`
	Revision string   `json:"revision"`
	Records  []string `json:"records"`
}

func NewPlanning(s *Store, repo *records.Store) *Planning { return &Planning{results: s, repo: repo} }
func quoted(s string) string                              { b, _ := json.Marshal(s); return string(b) }
func planRecord(id, kind, title, done, parent, stage string, deps []string, order int, scope result.Scope) []byte {
	dep, _ := json.Marshal(deps)
	parents := []string{}
	if parent != "" {
		parents = append(parents, parent)
	}
	belongs, _ := json.Marshal(parents)
	state := "active"
	if kind == "todo" {
		state = "open"
	}
	return []byte(fmt.Sprintf("---\nid: %s\nkind: %s\nstate: %s\norder: %d\nperson_id: %s\nagent_id: %s\nbelongs_to: %s\ndone: %s\nstage: %s\nblocked_by: %s\n---\n\n# %s\n\n%s\n", quoted(id), kind, state, order, quoted(scope.PersonID), quoted(scope.AgentID), belongs, quoted(done), quoted(stage), dep, title, done))
}
func frontField(data []byte, key string) string {
	front, _, ok := strings.Cut(string(data), "\n---\n")
	if !ok {
		return ""
	}
	for _, line := range strings.Split(front, "\n") {
		if v, ok := strings.CutPrefix(line, key+": "); ok {
			var s string
			if json.Unmarshal([]byte(v), &s) == nil {
				return s
			}
			return v
		}
	}
	return ""
}
func (p *Planning) Accept(ctx context.Context, scope result.Scope, kind, title, done string) (PlanRecord, error) {
	if !validScope(scope) || (kind != "goal" && kind != "project") || strings.TrimSpace(title) == "" || strings.TrimSpace(done) == "" || len([]rune(title)) > 200 || len(done) > 4096 || strings.ContainsAny(title, "\r\n") {
		return PlanRecord{}, result.ErrMalformed
	}
	titleMask, e1 := secrets.Mask(title)
	doneMask, e2 := secrets.Mask(done)
	if err := errors.Join(e1, e2); err != nil {
		return PlanRecord{}, err
	}
	title, done = titleMask.Text(), doneMask.Text()
	id := newID()
	data := planRecord(id, kind, title, done, "", "", []string{}, 0, scope)
	p.mu.Lock()
	defer p.mu.Unlock()
	for n := 1; ; n++ {
		name := slug(title)
		if n > 1 {
			name += "-" + strconv.Itoa(n)
		}
		rev, err := p.repo.Commit(ctx, records.Change{ID: id, Path: "projects/" + name + ".md", Data: data}, records.Actor{Person: scope.PersonID, Agent: scope.AgentID})
		if errors.Is(err, records.ErrPathExists) {
			continue
		}
		return PlanRecord{ID: id, Revision: rev}, err
	}
}
func (p *Planning) Input(ctx context.Context, s result.Scope, root, id string, base int, instruction string) (jobapi.PlanInput, error) {
	in := jobapi.PlanInput{RootID: root, ResultID: id, BaseRevision: base, Instruction: instruction, TurnID: s.TurnID}
	if !validScope(s) || root == "" || (id == "") != (base == 0) || base < 0 || len(instruction) > 16384 {
		return in, result.ErrMalformed
	}
	record, rev, err := p.repo.Read(ctx, root)
	if err != nil {
		return in, err
	}
	if frontField(record.Data, "person_id") != s.PersonID {
		return in, ErrOutOfScope
	}
	in.Kind = frontField(record.Data, "kind")
	in.Done = frontField(record.Data, "done")
	_, body, _ := strings.Cut(string(record.Data), "\n---\n")
	_, body, _ = strings.Cut(body, "# ")
	in.Title, _, _ = strings.Cut(body, "\n")
	if (in.Kind != "goal" && in.Kind != "project") || in.Done == "" || in.Title == "" {
		return in, ErrOutOfScope
	}
	in.RootRevision = rev
	if id != "" {
		err = p.results.db.Read(ctx, func(tx *canon.Tx) error {
			old, _, err := p.results.head(ctx, tx, s, id)
			if err != nil {
				return err
			}
			if old.Revision != base {
				return ErrStaleRevision
			}
			var stored string
			if err = tx.QueryRowContext(ctx, "SELECT root_id FROM plan_drafts WHERE result_id=?", id).Scan(&stored); err != nil {
				return err
			}
			if stored != root {
				return ErrOutOfScope
			}
			var confirmed int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM plan_confirmations WHERE result_id=?", id).Scan(&confirmed); err != nil {
				return err
			}
			if confirmed != 0 {
				return ErrOutOfScope
			}
			return nil
		})
	}
	return in, err
}

// DraftTx commits one whole validated preview alongside the Moirai watermark.
// The accepted root is a reference; the proposal never writes plan records.
func (p *Planning) DraftTx(ctx context.Context, tx *canon.Tx, s result.Scope, in jobapi.PlanInput, raw json.RawMessage) (result.Card, error) {
	draft, err := jobapi.DecodePlan(raw, in.Kind)
	if err != nil || !validScope(s) || in.RootID == "" || in.RootRevision == "" {
		return result.Card{}, result.ErrMalformed
	}
	id, rev := in.ResultID, 1
	if id == "" {
		id = newID()
	} else {
		old, _, err := p.results.head(ctx, tx, s, id)
		if err != nil {
			return result.Card{}, err
		}
		if old.Revision != in.BaseRevision {
			return result.Card{}, ErrStaleRevision
		}
		var root string
		var confirmed int
		if err = tx.QueryRowContext(ctx, "SELECT root_id FROM plan_drafts WHERE result_id=?", id).Scan(&root); err != nil {
			return result.Card{}, err
		}
		if root != in.RootID {
			return result.Card{}, ErrOutOfScope
		}
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM plan_confirmations WHERE result_id=?", id).Scan(&confirmed); err != nil {
			return result.Card{}, err
		}
		if confirmed != 0 {
			return result.Card{}, ErrOutOfScope
		}
		rev = old.Revision + 1
	}
	confirm := id
	plan := result.Plan{Confirm: &confirm, Entries: []result.Entry{{ID: in.RootID, Kind: in.Kind, Title: in.Title, State: "active", Done: in.Done}}}
	ids := map[string]string{}
	for _, entry := range draft.Entries {
		ids[entry.ID] = newID()
	}
	for _, entry := range draft.Entries {
		parent := in.RootID
		if entry.Parent != "" {
			parent = ids[entry.Parent]
		}
		deps := []string{}
		for _, dep := range entry.DependsOn {
			deps = append(deps, ids[dep])
		}
		plan.Entries = append(plan.Entries, result.Entry{ID: ids[entry.ID], Kind: entry.Kind, Title: entry.Title, State: "planned", Parent: &parent, Done: entry.Done, Stage: entry.Stage, DependsOn: deps})
	}
	body, _ := json.Marshal(plan)
	presented, err := maskPresentation(result.PresentRequest{Type: result.PlanType, Title: draft.Title, Summary: draft.Summary, Body: body})
	if err != nil {
		return result.Card{}, err
	}
	validated, err := result.Validate(result.PlanType, presented.Body, result.Limits{})
	if err != nil {
		return result.Card{}, err
	}
	r := result.Result{ID: id, Revision: rev, Type: result.PlanType, Title: presented.Title, Summary: presented.Summary, Text: result.PlainText(validated, nil), Body: presented.Body, Status: "preview", RevisedBy: s.AgentID, Provenance: result.Provenance{AgentID: s.AgentID, ConversationID: s.ConversationID, TurnID: s.TurnID}}
	event, err := p.results.cfg.Journal.Append(ctx, tx, s, r)
	if err != nil {
		return result.Card{}, err
	}
	if in.ResultID == "" {
		_, err = tx.ExecContext(ctx, "INSERT INTO result_heads(id,person,agent,conversation,revision,event_id) VALUES(?,?,?,?,?,?)", id, s.PersonID, s.AgentID, s.ConversationID, rev, event)
		if err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO plan_drafts(result_id,root_id,root_revision) VALUES(?,?,?)", id, in.RootID, in.RootRevision)
		}
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE result_heads SET revision=?,event_id=? WHERE id=?", rev, event, id)
		if err == nil {
			_, err = tx.ExecContext(ctx, "UPDATE plan_drafts SET root_revision=? WHERE result_id=?", in.RootRevision, id)
		}
	}
	return result.Card{ID: id, Revision: rev, Type: r.Type, Title: r.Title, Summary: r.Summary, Text: r.Text}, err
}
func (p *Planning) Confirm(ctx context.Context, s result.Scope, id string, revision int) (PlanReceipt, error) {
	if !validScope(s) || id == "" || revision <= 0 {
		return PlanReceipt{}, result.ErrMalformed
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	receipt := PlanReceipt{State: "refused"}
	var changes []records.Change
	claimed := false
	err := p.results.db.Read(ctx, func(tx *canon.Tx) error {
		r, _, err := p.results.head(ctx, tx, s, id)
		if err != nil {
			return err
		}
		if r.Revision != revision {
			return ErrStaleRevision
		}
		var raw []byte
		err = tx.QueryRowContext(ctx, "SELECT receipt FROM plan_confirmations WHERE result_id=?", id).Scan(&raw)
		if err == nil {
			return json.Unmarshal(raw, &receipt)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		claimed = true
		return nil
	})
	if err != nil {
		return receipt, err
	}
	if !claimed {
		if receipt.State == "applied" {
			return receipt, nil
		}
		var raw []byte
		err = p.results.db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT changes FROM plan_confirmations WHERE result_id=?", id).Scan(&raw)
		})
		if err != nil {
			return receipt, err
		}
		if err = json.Unmarshal(raw, &changes); err != nil {
			return receipt, err
		}
		rev := ""
		for _, change := range changes {
			if change.Base != "" {
				continue
			}
			record, current, err := p.repo.Read(ctx, change.ID)
			if errors.Is(err, records.ErrNotFound) {
				return receipt, nil
			}
			if err != nil {
				return receipt, err
			}
			if string(record.Data) != string(change.Data) || record.Path != change.Path || (rev != "" && rev != current) {
				return receipt, nil
			}
			rev = current
		}
		if rev == "" {
			return receipt, nil
		}
		return p.finish(ctx, s, id, rev, receipt)
	}
	r, err := p.results.Read(ctx, s, result.ReadRequest{ID: id})
	if err != nil {
		return receipt, err
	}
	var plan result.Plan
	if err = json.Unmarshal(r.Body, &plan); err != nil || len(plan.Entries) < 2 {
		return receipt, ErrOutOfScope
	}
	var root, rootRev string
	err = p.results.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT root_id,root_revision FROM plan_drafts WHERE result_id=?", id).Scan(&root, &rootRev)
	})
	if err != nil {
		return receipt, ErrOutOfScope
	}
	record, actual, err := p.repo.Read(ctx, root)
	if err != nil {
		return receipt, err
	}
	if actual != rootRev {
		return receipt, ErrStaleRevision
	}
	if frontField(record.Data, "person_id") != s.PersonID {
		return receipt, ErrOutOfScope
	}
	// Reserve human paths from the current repository. The commit CAS checks the
	// root again; unrelated writes cannot authorize a stale root or overwrite files.
	used := map[string]bool{}
	all, err := p.repo.List(ctx)
	if err != nil {
		return receipt, err
	}
	for _, record := range all {
		used[record.Path] = true
	}
	for i, entry := range plan.Entries[1:] {
		name := slug(entry.Title)
		stem := name
		for n := 2; used["projects/"+name+".md"]; n++ {
			name = stem + "-" + strconv.Itoa(n)
		}
		name = "projects/" + name + ".md"
		used[name] = true
		parent := ""
		if entry.Parent != nil {
			parent = *entry.Parent
		}
		changes = append(changes, records.Change{ID: entry.ID, Path: name, Data: planRecord(entry.ID, entry.Kind, entry.Title, entry.Done, parent, entry.Stage, entry.DependsOn, i+1, s)})
		receipt.Records = append(receipt.Records, entry.ID)
	}
	// The unchanged root is included as a CAS guard within the same repository lock.
	changes = append(changes, records.Change{ID: root, Base: rootRev, Path: record.Path, Data: record.Data})
	receipt.State = "unknown"
	rawChanges, _ := json.Marshal(changes)
	rawReceipt, _ := json.Marshal(receipt)
	err = p.results.db.Write(ctx, func(tx *canon.Tx) error {
		current, _, err := p.results.head(ctx, tx, s, id)
		if err != nil {
			return err
		}
		if current.Revision != revision {
			return ErrStaleRevision
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO plan_confirmations(result_id,revision,state,changes,receipt) VALUES(?,?,'unknown',?,?)", id, revision, rawChanges, rawReceipt)
		return err
	})
	if err != nil {
		return PlanReceipt{State: "refused"}, err
	}
	rev, err := p.repo.CommitBatch(ctx, changes, records.Actor{Person: s.PersonID, Agent: s.AgentID})
	if err != nil {
		return receipt, err
	}
	return p.finish(ctx, s, id, rev, receipt)
}
func (p *Planning) finish(ctx context.Context, s result.Scope, id, rev string, receipt PlanReceipt) (PlanReceipt, error) {
	receipt.State, receipt.Revision = "applied", rev
	raw, _ := json.Marshal(receipt)
	err := p.results.db.Write(ctx, func(tx *canon.Tx) error {
		r, _, err := p.results.head(ctx, tx, s, id)
		if err != nil {
			return err
		}
		var body result.Plan
		if err = json.Unmarshal(r.Body, &body); err != nil {
			return err
		}
		body.Confirm = nil
		r.Body, _ = json.Marshal(body)
		r.Text = result.PlainText(&body, nil)
		event, err := p.results.cfg.Journal.Append(ctx, tx, s, r)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE result_heads SET event_id=? WHERE id=?", event, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE plan_confirmations SET state='applied',receipt=? WHERE result_id=?", raw, id)
		return err
	})
	if err != nil {
		receipt.State = "unknown"
	}
	return receipt, err
}

type PlanTarget struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Done  string `json:"done"`
}
type PlanIndex struct {
	Targets []PlanTarget  `json:"targets"`
	Drafts  []result.Card `json:"drafts"`
}

func (p *Planning) Index(ctx context.Context, s result.Scope) (PlanIndex, error) {
	out := PlanIndex{Targets: []PlanTarget{}, Drafts: []result.Card{}}
	if !validScope(s) {
		return out, result.ErrMalformed
	}
	records, err := p.repo.List(ctx)
	if err != nil {
		return out, err
	}
	for _, record := range records {
		kind := frontField(record.Data, "kind")
		if frontField(record.Data, "person_id") != s.PersonID || (kind != "goal" && kind != "project") {
			continue
		}
		_, body, _ := strings.Cut(string(record.Data), "\n---\n")
		_, body, _ = strings.Cut(body, "# ")
		title, _, _ := strings.Cut(body, "\n")
		out.Targets = append(out.Targets, PlanTarget{ID: record.ID, Kind: kind, Title: title, Done: frontField(record.Data, "done")})
	}
	err = p.results.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT h.id FROM result_heads h JOIN plan_drafts p ON p.result_id=h.id WHERE h.person=? AND h.agent=? AND h.conversation=? ORDER BY h.rowid", s.PersonID, s.AgentID, s.ConversationID)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range ids {
			r, _, err := p.results.head(ctx, tx, s, id)
			if err != nil {
				return err
			}
			out.Drafts = append(out.Drafts, result.Card{ID: r.ID, Revision: r.Revision, Type: r.Type, Title: r.Title, Summary: r.Summary})
		}
		return nil
	})
	return out, err
}

// ApplyDraftTx retires an obsolete draft or Core's completed-invalid result in
// the same transaction as the job watermark. The caller verifies the cached
// Core result first; unexpected storage failures still roll back.
func (p *Planning) ApplyDraftTx(ctx context.Context, tx *canon.Tx, s result.Scope, job string, sequence int64, in jobapi.PlanInput, raw json.RawMessage) error {
	if !validScope(s) || job == "" || sequence <= 0 || in.RootID == "" || in.RootRevision == "" || (in.Kind != "goal" && in.Kind != "project") {
		return result.ErrMalformed
	}
	code := "invalid_plan"
	if !jobapi.IsInvalidPlanResult(raw) {
		_, err := p.DraftTx(ctx, tx, s, in, raw)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrStaleRevision) && !errors.Is(err, ErrOutOfScope) && !errors.Is(err, ErrUnavailable) {
			return err
		}
		code = err.Error()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO plan_rejections(job,sequence,person,agent,conversation,turn_id,result_id,code) VALUES(?,?,?,?,?,?,?,?)`, job, sequence, s.PersonID, s.AgentID, s.ConversationID, s.TurnID, in.ResultID, code)
	return err
}

type PlanRejection struct {
	Job      string `json:"job_id"`
	Sequence int64  `json:"sequence"`
	TurnID   string `json:"turn_id"`
	ResultID string `json:"result_id"`
	Code     string `json:"code"`
}

func (p *Planning) Rejections(ctx context.Context, s result.Scope) ([]PlanRejection, error) {
	out := []PlanRejection{}
	err := p.results.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT job,sequence,turn_id,result_id,code FROM plan_rejections WHERE person=? AND agent=? AND conversation=? ORDER BY rowid", s.PersonID, s.AgentID, s.ConversationID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item PlanRejection
			if err = rows.Scan(&item.Job, &item.Sequence, &item.TurnID, &item.ResultID, &item.Code); err != nil {
				return err
			}
			out = append(out, item)
		}
		return rows.Err()
	})
	return out, err
}
