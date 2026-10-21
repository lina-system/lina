// SPDX-License-Identifier: Apache-2.0

// Package agents owns agent data and immutable persona revisions. Core's Moirai
// write path supplies the person responsible for mutations. This store grants no
// authority and makes no model calls.
package agents

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var (
	ErrInvalid  = errors.New("invalid_agent_input")
	ErrNotFound = errors.New("agent_not_found")
	ErrConflict = errors.New("agent_revision_conflict")
	ErrExpired  = errors.New("agent_trash_expired")
	ErrCorrupt  = errors.New("persona_revision_corrupt")
)

type Layers struct {
	Core           string `json:"core"`
	DefaultVoice   string `json:"default_voice"`
	UserVoice      string `json:"user_voice"`
	GrowthSnapshot string `json:"growth_snapshot"`
}
type Preset struct {
	ID            string   `json:"id"`
	Core          string   `json:"core"`
	DefaultVoice  string   `json:"default_voice"`
	BlankSettings []string `json:"blank_settings,omitempty"`
}
type State struct {
	Emotion        string            `json:"emotion"`
	Settings       map[string]string `json:"settings"`
	SettingSources map[string]string `json:"setting_sources,omitempty"`
}
type Agent struct {
	ID, Name, Preset string
	Revision         int64
}
type Create struct {
	Name, PersonID, Preset string
	Layers                 Layers
}

// Preparation is the durable persona part of the turn's prepare acknowledgement.
// The flow must complete its context/account steps before sending a model call.
type Preparation struct {
	TurnID, AgentID     string
	Name, OwnerPersonID string
	Revision            int64
	Commit              string
	Layers              Layers
}
type Store struct {
	db        *canon.Store
	root      *os.Root
	writeFile func(*os.File, []byte) (int, error) // Per-store fault injection for filesystem tests.
}

func Open(stateRoot string, db *canon.Store) (*Store, error) {
	if db == nil {
		return nil, ErrInvalid
	}
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return nil, err
	}
	if err := privateDirs(root, "versions/agents"); err != nil {
		root.Close()
		return nil, err
	}
	if err := recoverTemporaries(root); err != nil {
		root.Close()
		return nil, err
	}
	return &Store{db: db, root: root}, nil
}
func (s *Store) Close() error { return s.root.Close() }
func newID() string           { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func validID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && hex.EncodeToString(b) == id
}
func present(s string) bool { return strings.TrimSpace(s) != "" }
func found(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func get(ctx context.Context, tx *canon.Tx, id string, trash bool) (Agent, error) {
	var a Agent
	query := "SELECT id,name,preset,revision FROM agents WHERE id=?"
	if !trash {
		query += " AND trashed_at=0"
	}
	err := tx.QueryRowContext(ctx, query, id).Scan(&a.ID, &a.Name, &a.Preset, &a.Revision)
	return a, found(err)
}
func (s *Store) Get(ctx context.Context, id string) (Agent, error) {
	var a Agent
	err := s.db.Read(ctx, func(tx *canon.Tx) error { var err error; a, err = get(ctx, tx, id, false); return err })
	return a, err
}
func preset(ctx context.Context, tx *canon.Tx, id string) (Preset, error) {
	var raw []byte
	var p Preset
	err := tx.QueryRowContext(ctx, "SELECT record FROM agent_presets WHERE id=?", id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &p)
	}
	return p, found(err)
}
func action(ctx context.Context, tx *canon.Tx, id, person, verb string, at int64) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO agent_actions(agent_id,person_id,action,at) VALUES(?,?,?,?)", id, person, verb, at)
	return err
}
func samePreset(a, b Preset) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func (s *Store) Create(ctx context.Context, in Create) (Agent, error) {
	return s.create(ctx, in, false)
}
func (s *Store) create(ctx context.Context, in Create, first bool) (Agent, error) {
	if !present(in.Name) || !present(in.PersonID) {
		return Agent{}, ErrInvalid
	}
	a := Agent{ID: newID(), Name: in.Name, Preset: in.Preset, Revision: 1}
	state := State{Settings: map[string]string{}}
	var p Preset
	if in.Preset != "" {
		err := s.db.Read(ctx, func(tx *canon.Tx) error { var err error; p, err = preset(ctx, tx, in.Preset); return err })
		if err != nil {
			return Agent{}, err
		}
		in.Layers = Layers{Core: p.Core, DefaultVoice: p.DefaultVoice}
		for _, name := range p.BlankSettings {
			state.Settings[name] = ""
		}
	}
	commit, err := s.save(ctx, a.ID, "", in.Layers, in.PersonID)
	if err != nil {
		return Agent{}, err
	}
	raw, _ := json.Marshal(state)
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		if in.Preset != "" {
			current, err := preset(ctx, tx, in.Preset)
			if err != nil {
				return err
			}
			if !samePreset(p, current) {
				return ErrConflict
			}
		}
		if first {
			var n int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM agent_first_run").Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				return ErrConflict
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO agents(id,name,preset,revision,state) VALUES(?,?,?,?,?)", a.ID, a.Name, a.Preset, a.Revision, raw); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO agent_revisions(agent_id,revision,commit_id,person_id) VALUES(?,?,?,?)", a.ID, 1, commit, in.PersonID); err != nil {
			return err
		}
		if first {
			if _, err := tx.ExecContext(ctx, "INSERT INTO agent_first_run VALUES(1,?)", a.ID); err != nil {
				return err
			}
		}
		return action(ctx, tx, a.ID, in.PersonID, "create", 0)
	})
	if err != nil {
		return Agent{}, err
	}
	return a, nil
}
func (s *Store) revision(ctx context.Context, id string, rev int64) (Preparation, error) {
	p := Preparation{AgentID: id, Revision: rev}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return found(tx.QueryRowContext(ctx, "SELECT commit_id FROM agent_revisions WHERE agent_id=? AND revision=?", id, rev).Scan(&p.Commit))
	})
	if err == nil {
		p.Layers, err = s.load(id, p.Commit)
	}
	return p, err
}
func (s *Store) Revise(ctx context.Context, id string, expected int64, layers Layers, person string) (Agent, error) {
	if !present(person) {
		return Agent{}, ErrInvalid
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return Agent{}, err
	}
	if a.Revision != expected {
		return Agent{}, ErrConflict
	}
	old, err := s.revision(ctx, id, expected)
	if err != nil {
		return Agent{}, err
	}
	if a.Preset != "" && (old.Layers.Core != layers.Core || old.Layers.DefaultVoice != layers.DefaultVoice) {
		return Agent{}, ErrInvalid
	}
	if old.Layers == layers {
		return a, nil
	}
	commit, err := s.save(ctx, id, old.Commit, layers, person)
	if err != nil {
		return Agent{}, err
	}
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		current, err := get(ctx, tx, id, false)
		if err != nil {
			return err
		}
		if current.Revision != expected {
			return ErrConflict
		}
		return publish(ctx, tx, id, expected+1, commit, person)
	})
	if err != nil {
		return Agent{}, err
	}
	a.Revision++
	return a, nil
}
func publish(ctx context.Context, tx *canon.Tx, id string, rev int64, commit, person string) error {
	if _, err := tx.ExecContext(ctx, "INSERT INTO agent_revisions(agent_id,revision,commit_id,person_id) VALUES(?,?,?,?)", id, rev, commit, person); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE agents SET revision=? WHERE id=?", rev, id); err != nil {
		return err
	}
	return action(ctx, tx, id, person, "revise", 0)
}
func (s *Store) State(ctx context.Context, id string) (State, error) {
	var state State
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		var raw []byte
		err := tx.QueryRowContext(ctx, "SELECT state FROM agents WHERE id=? AND trashed_at=0", id).Scan(&raw)
		if err != nil {
			return found(err)
		}
		return json.Unmarshal(raw, &state)
	})
	return state, err
}
func (s *Store) SetState(ctx context.Context, id string, state State, person string) error {
	if !present(person) {
		return ErrInvalid
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := get(ctx, tx, id, false); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE agents SET state=? WHERE id=?", raw, id); err != nil {
			return err
		}
		return action(ctx, tx, id, person, "state", 0)
	})
}

// Prepare pins the current revision once per turn. Repeating a turn never
// switches persona, including after a process restart.
func (s *Store) Prepare(ctx context.Context, turn, id string) (Preparation, error) {
	if !present(turn) || !present(id) {
		return Preparation{}, ErrInvalid
	}
	for {
		var a Agent
		var ownerPersonID string
		var pinned bool
		err := s.db.Read(ctx, func(tx *canon.Tx) error {
			var err error
			a, err = get(ctx, tx, id, false)
			if err != nil {
				return err
			}
			// The creating person owns the agent. A later revision's author
			// must not replace that identity in the conversation.
			if err = tx.QueryRowContext(ctx, "SELECT person_id FROM agent_revisions WHERE agent_id=? AND revision=1", id).Scan(&ownerPersonID); err != nil {
				return err
			}
			var owner string
			var rev int64
			err = tx.QueryRowContext(ctx, "SELECT agent_id,revision FROM agent_preparations WHERE turn_id=?", turn).Scan(&owner, &rev)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if owner != id {
				return ErrConflict
			}
			a.Revision = rev
			pinned = true
			return nil
		})
		if err != nil {
			return Preparation{}, err
		}
		p, err := s.revision(ctx, id, a.Revision)
		if err != nil {
			return Preparation{}, err
		}
		p.TurnID = turn
		p.Name, p.OwnerPersonID = a.Name, ownerPersonID
		err = s.db.Write(ctx, func(tx *canon.Tx) error {
			current, err := get(ctx, tx, id, false)
			if err != nil {
				return err
			}
			var owner string
			var rev int64
			err = tx.QueryRowContext(ctx, "SELECT agent_id,revision FROM agent_preparations WHERE turn_id=?", turn).Scan(&owner, &rev)
			if err == nil {
				if owner != id || rev != p.Revision {
					return ErrConflict
				}
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if pinned || current.Revision != p.Revision {
				return ErrConflict
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO agent_preparations VALUES(?,?,?)", turn, id, p.Revision)
			return err
		})
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return Preparation{}, err
		}
		return p, nil
	}
}

type FirstRun struct {
	Agent          Agent
	SignInProvider string
}

func (s *Store) FirstRun(ctx context.Context, person string, usableAccount bool) (FirstRun, error) {
	if !present(person) {
		return FirstRun{}, ErrInvalid
	}
	if err := s.upgradeLinaPlaceholders(ctx, person); err != nil {
		return FirstRun{}, err
	}
	for {
		var id string
		err := s.db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT agent_id FROM agent_first_run WHERE singleton=1").Scan(&id)
		})
		var a Agent
		if errors.Is(err, sql.ErrNoRows) {
			p := Preset{ID: "lina"}
			raw, _ := json.Marshal(p)
			err = s.db.Write(ctx, func(tx *canon.Tx) error {
				_, err := tx.ExecContext(ctx, "INSERT INTO agent_presets VALUES(?,?) ON CONFLICT(id) DO NOTHING", p.ID, raw)
				return err
			})
			if err != nil {
				return FirstRun{}, err
			}
			a, err = s.create(ctx, Create{Name: "LINA", PersonID: person, Preset: "lina"}, true)
			if errors.Is(err, ErrConflict) {
				continue
			}
		} else if err == nil {
			a, err = s.Get(ctx, id)
		}
		if err != nil {
			return FirstRun{}, err
		}
		result := FirstRun{Agent: a}
		if !usableAccount {
			result.SignInProvider = "chatgpt"
		}
		return result, nil
	}
}
