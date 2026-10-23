// SPDX-License-Identifier: Apache-2.0
package a2a

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

type Record struct {
	OwnerApproval   string            `json:"owner_approval,omitempty"`
	Published       bool              `json:"published,omitempty"`
	Calls           map[string]string `json:"calls,omitempty"`
	Task            Task              `json:"task"`
	Publication     string            `json:"publication"`
	Caller          string            `json:"caller"`
	Principal       Principal         `json:"principal"`
	Agent           string            `json:"agent"`
	Conversation    string            `json:"conversation"`
	Definition      Definition        `json:"definition"`
	Authority       string            `json:"authority"`
	Phase           string            `json:"phase"`
	Deadline        int64             `json:"deadline"`
	CancelRequested bool              `json:"cancel_requested"`
	Delivered       string            `json:"delivered,omitempty"`
	LocalWork       string            `json:"local_work,omitempty"`
	InputEvent      string            `json:"input_event,omitempty"`
	InboxID         int64             `json:"inbox_id,omitempty"`
	ToolCalls       int               `json:"tool_calls"`
}
type Store struct{ DB *canon.Store }

func opaque() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(id[:])
}
func (s *Store) Admit(ctx context.Context, p Publication, b Binding, d Definition, pr Principal, req SendRequest, now time.Time) (Record, error) {
	var r Record
	// Configuration affects the reply, not the execution identity. The complete
	// message/metadata still participates, including task and context references.
	hash := digest([]any{req.Message, req.Metadata, req.Tenant})
	err := s.DB.Write(ctx, func(tx *canon.Tx) error {
		var savedHash, id string
		err := tx.QueryRowContext(ctx, "SELECT hash,task FROM a2a_messages WHERE publication=? AND caller=? AND message=?", p.ID, pr.Key(), req.Message.MessageID).Scan(&savedHash, &id)
		if err == nil {
			if hash != savedHash {
				return ErrInvalid
			}
			r, err = readRecord(ctx, tx, id)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if req.Message.TaskID != "" || req.Message.ContextID != "" {
			id := req.Message.TaskID
			if id == "" {
				if err := tx.QueryRowContext(ctx, "SELECT id FROM a2a_tasks WHERE publication=? AND caller=? AND json_extract(record,'$.task.contextId')=?", p.ID, pr.Key(), req.Message.ContextID).Scan(&id); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						return ErrNotFound
					}
					return err
				}
			}
			r, err = readRecord(ctx, tx, id)
			if err != nil {
				return err
			}
			if r.Publication != p.ID || r.Caller != pr.Key() || req.Message.ContextID != "" && req.Message.ContextID != r.Task.ContextID {
				return ErrNotFound
			}
			if Terminal(r.Task.Status.State) || r.Definition.ID != d.ID || r.Authority != Fingerprint(p, b, d) || r.CancelRequested || len(r.Task.History) >= 16 {
				return ErrInvalid
			}
			r.Task.History = append(r.Task.History, req.Message)
			history, _ := json.Marshal(r.Task.History)
			if len(history) > MaxInput {
				return ErrInvalid
			}
			raw, err := json.Marshal(r)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE a2a_tasks SET record=? WHERE id=?", raw, r.Task.ID); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO a2a_messages VALUES(?,?,?,?,?)", p.ID, pr.Key(), req.Message.MessageID, hash, r.Task.ID)
			return err
		}
		var total int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM a2a_tasks WHERE phase!='terminal'").Scan(&total); err != nil {
			return err
		}
		if total >= 32 {
			return ErrCapacity
		}
		var active int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM a2a_tasks WHERE publication=? AND caller=? AND phase NOT IN ('terminal')", p.ID, pr.Key()).Scan(&active); err != nil {
			return err
		}
		if active >= b.MaxActive {
			return ErrCapacity
		}
		r = Record{Task: Task{ID: opaque(), ContextID: opaque(), Status: Status{State: Submitted, Timestamp: now.UTC().Format("2006-01-02T15:04:05.000000000Z")}, History: []Message{req.Message}}, Publication: p.ID, Caller: pr.Key(), Principal: pr, Agent: p.Agent, Conversation: p.Conversation, Definition: d, Authority: Fingerprint(p, b, d), Phase: "queued", Deadline: now.Add(time.Duration(d.Seconds) * time.Second).Unix()}
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO a2a_tasks VALUES(?,?,?,?,?,?)", r.Task.ID, p.ID, pr.Key(), r.Phase, r.Task.Status.Timestamp, raw); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO a2a_messages VALUES(?,?,?,?,?)", p.ID, pr.Key(), req.Message.MessageID, hash, r.Task.ID)
		return err
	})
	return r, err
}
func readRecord(ctx context.Context, tx *canon.Tx, id string) (Record, error) {
	var r Record
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT record FROM a2a_tasks WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(raw, &r)
	}
	return r, err
}
func (s *Store) Get(ctx context.Context, pub, caller, id string) (Record, error) {
	var r Record
	err := s.DB.Read(ctx, func(tx *canon.Tx) error {
		var err error
		r, err = readRecord(ctx, tx, id)
		if err == nil && (r.Publication != pub || r.Caller != caller) {
			return ErrNotFound
		}
		return err
	})
	if err != nil {
		return Record{}, err
	}
	return r, nil
}
func (s *Store) Read(ctx context.Context, id string) (Record, error) {
	var r Record
	err := s.DB.Read(ctx, func(tx *canon.Tx) error { var e error; r, e = readRecord(ctx, tx, id); return e })
	return r, err
}
func (s *Store) List(ctx context.Context, pub, caller string) ([]Record, error) {
	return s.records(ctx, " WHERE publication=? AND caller=?", pub, caller)
}
func (s *Store) Pending(ctx context.Context) ([]Record, error) {
	return s.records(ctx, " WHERE phase!='terminal' OR COALESCE(json_extract(record,'$.published'),0)=0")
}
func (s *Store) All(ctx context.Context) ([]Record, error) { return s.records(ctx, "") }
func (s *Store) records(ctx context.Context, where string, args ...any) ([]Record, error) {
	result := []Record{}
	err := s.DB.Read(ctx, func(tx *canon.Tx) error {
		rows, e := tx.QueryContext(ctx, "SELECT record FROM a2a_tasks"+where+" ORDER BY updated DESC,id DESC", args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var r Record
			if e = rows.Scan(&raw); e != nil {
				return e
			}
			if e = json.Unmarshal(raw, &r); e != nil {
				return e
			}
			result = append(result, r)
		}
		return rows.Err()
	})
	return result, err
}
func (s *Store) Update(ctx context.Context, id string, fn func(*Record) error) (Record, error) {
	var r Record
	err := s.DB.Write(ctx, func(tx *canon.Tx) error {
		var e error
		r, e = readRecord(ctx, tx, id)
		if e != nil {
			return e
		}
		before := digest(r)
		terminal, taskHash := Terminal(r.Task.Status.State), digest(r.Task)
		if e = fn(&r); e != nil {
			return e
		}
		if terminal && taskHash != digest(r.Task) {
			return ErrInvalid
		}
		if before == digest(r) {
			return nil
		}
		raw, e := json.Marshal(r)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "UPDATE a2a_tasks SET phase=?,updated=?,record=? WHERE id=?", r.Phase, r.Task.Status.Timestamp, raw, id)
		return e
	})
	return r, err
}
func (s *Store) Claim(ctx context.Context, id string) (bool, error) {
	claimed := false
	_, err := s.Update(ctx, id, func(r *Record) error {
		if r.Phase == "queued" && !r.CancelRequested {
			r.Phase = "executing"
			SetState(r, Working, time.Now())
			claimed = true
		}
		return nil
	})
	return claimed, err
}
func (s *Store) Recover(ctx context.Context) error {
	records, err := s.Pending(ctx)
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.Phase == "executing" {
			if _, err = s.Update(ctx, r.Task.ID, func(r *Record) error { r.Phase = "unknown"; SetState(r, Unspecified, time.Now()); return nil }); err != nil {
				return err
			}
		}
	}
	return nil
}
func SetState(r *Record, state string, now time.Time) {
	if r.Task.Status.State != state {
		r.Task.Status = Status{State: state, Timestamp: now.UTC().Format("2006-01-02T15:04:05.000000000Z")}
	}
	if Terminal(state) {
		r.Phase = "terminal"
	}
}
