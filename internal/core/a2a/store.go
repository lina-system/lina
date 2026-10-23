// SPDX-License-Identifier: Apache-2.0
package a2a

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

type Service struct {
	DB      *canon.Store
	Vault   *secrets.Store
	Network Network
	Epoch   uint64
	// Check is supplied by Core; it checks current task/preset authority before
	// every transmission, including each card or artifact request.
	Check    func(context.Context, Origin, Connection, Profile) error
	mu       sync.Mutex
	opMu     sync.Mutex
	oauthMu  sync.Mutex
	attempts map[string]oauthAttempt
}

func hash(b []byte) string { d := sha256.Sum256(b); return hex.EncodeToString(d[:]) }
func randomID() string     { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func key(c Connection) secrets.Key {
	return secrets.Key{Person: c.Person, Service: "a2a:" + c.ID, Name: "credential"}
}
func (s *Service) epoch(ctx context.Context, tx *canon.Tx) error {
	var current string
	if err := tx.QueryRowContext(ctx, "SELECT epoch FROM canon_meta WHERE singleton=1").Scan(&current); err != nil {
		return err
	}
	if current != fmt.Sprint(s.Epoch) {
		return ErrScope
	}
	return nil
}
func (s *Service) Connection(ctx context.Context, person, id string) (c Connection, err error) {
	err = s.DB.Read(ctx, func(tx *canon.Tx) error {
		var raw []byte
		err := tx.QueryRowContext(ctx, "SELECT record FROM a2a_connections WHERE id=? AND person=?", id, person).Scan(&raw)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, &c)
	})
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrScope
	}
	return
}
func (s *Service) Connections(ctx context.Context, person string) (out []Connection, err error) {
	out = []Connection{}
	err = s.DB.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record FROM a2a_connections WHERE person=? ORDER BY id", person)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var c Connection
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &c); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return
}
func (s *Service) saveConnection(ctx context.Context, c Connection) error {
	raw, _ := json.Marshal(c)
	return s.DB.Write(ctx, func(tx *canon.Tx) error {
		if err := s.epoch(ctx, tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO a2a_connections(id,person,revision,enabled,record) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,enabled=excluded.enabled,record=excluded.record WHERE person=excluded.person`, c.ID, c.Person, c.Revision, c.Enabled, raw)
		return err
	})
}
func (s *Service) Revoke(ctx context.Context, person, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.Connection(ctx, person, id)
	if err != nil {
		return err
	}
	c.Enabled = false
	c.Revision = randomID()
	return s.saveConnection(ctx, c)
}
func (s *Service) Call(ctx context.Context, o Origin, id string) (c Call, err error) {
	err = s.DB.Read(ctx, func(tx *canon.Tx) error {
		var raw []byte
		err := tx.QueryRowContext(ctx, "SELECT record FROM a2a_calls WHERE id=? AND person=? AND agent=? AND conversation=?", id, o.Person, o.Agent, o.Conversation).Scan(&raw)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, &c)
	})
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrScope
	}
	return
}
func (s *Service) save(ctx context.Context, c Call) error {
	return s.DB.Write(ctx, func(tx *canon.Tx) error {
		var prior []byte
		if err := tx.QueryRowContext(ctx, "SELECT record FROM a2a_calls WHERE id=?", c.ID).Scan(&prior); err != nil {
			return err
		}
		var old Call
		if err := json.Unmarshal(prior, &old); err != nil {
			return err
		}
		c.Delivered = c.Delivered || old.Delivered
		raw, _ := json.Marshal(c)
		if err := s.epoch(ctx, tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE a2a_calls SET record=? WHERE id=? AND person=? AND agent=? AND conversation=?`, raw, c.ID, c.Origin.Person, c.Origin.Agent, c.Origin.Conversation)
		return err
	})
}
func (s *Service) Undelivered(ctx context.Context) (out []Call, err error) {
	err = s.DB.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT record FROM a2a_calls WHERE json_extract(record,'$.delivered')=0`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var c Call
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &c); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return
}

// Delivered marks an already idempotently journaled observation. If Core dies
// before this write, the deterministic journal identity prevents another result.
func (s *Service) Delivered(ctx context.Context, c Call) error {
	return s.DB.Write(ctx, func(tx *canon.Tx) error {
		if err := s.epoch(ctx, tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE a2a_calls SET record=json_set(record,'$.delivered',json('true')) WHERE id=? AND person=? AND agent=? AND conversation=?`, c.ID, c.Origin.Person, c.Origin.Agent, c.Origin.Conversation)
		return err
	})
}

type observationAuthority struct{}

func (s *Service) allowed(ctx context.Context, o Origin, c Connection, p Profile) error {
	if current, ok := ctx.Value(observationAuthority{}).(Origin); ok {
		o = current
	}
	if o.Person != c.Person || o.Agent == "" || o.Conversation == "" || o.Turn == "" || o.Epoch != s.Epoch || !c.Enabled {
		return ErrScope
	}
	current, err := s.Connection(ctx, o.Person, c.ID)
	if err != nil {
		return err
	}
	if !current.Enabled || current.Revision != c.Revision {
		return ErrScope
	}
	if err = s.DB.Read(ctx, func(tx *canon.Tx) error { return s.epoch(ctx, tx) }); err != nil {
		return err
	}
	if s.Check == nil {
		return ErrScope
	}
	return s.Check(ctx, o, c, p)
}
func profile(c Connection, name string) (Profile, error) {
	for _, p := range c.Profiles {
		if p.Name == name {
			return p, nil
		}
	}
	return Profile{}, ErrScope
}
func (s *Service) Validate(ctx context.Context, o Origin, in Input) (Connection, Profile, error) {
	c, err := s.Connection(ctx, o.Person, in.Connection)
	if err != nil {
		return c, Profile{}, err
	}
	p, err := profile(c, in.Task)
	if err != nil {
		return c, p, err
	}
	if len(in.Text) == 0 || len(in.Text) > p.MaxInput || (in.Call == "" && !strings.HasPrefix(in.Text, p.InputPrefix)) {
		return c, p, ErrScope
	}
	return c, p, s.allowed(ctx, o, c, p)
}

func (s *Service) Calls(ctx context.Context, person string) (out []Call, err error) {
	out = []Call{}
	err = s.DB.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record FROM a2a_calls WHERE person=? ORDER BY rowid", person)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var c Call
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &c); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return
}
