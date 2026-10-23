// SPDX-License-Identifier: Apache-2.0

package turn_test

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

type completionMaterials struct {
	t      *testing.T
	store  *assets.Store
	writer *assets.MaterialWriter
}

type completionMaterial struct{ *assets.PreparedMaterial }

func (p completionMaterial) Insert(ctx context.Context, tx *canon.Tx) (ledger.Material, error) {
	a, err := p.PreparedMaterial.Insert(ctx, tx)
	return ledger.Material{ID: a.ID, Revision: a.Revision}, err
}

func (m completionMaterials) Prepare(ctx context.Context, body secrets.Masked) (ledger.MaterialWrite, error) {
	if _, ok := ctx.Deadline(); ok {
		m.t.Error("material preparation inherited the completion transaction deadline")
	}
	p, err := m.writer.Prepare(ctx, strings.NewReader(body.Text()), "reply", assets.Actor{PersonID: "person", AgentID: "agent"})
	if err != nil {
		return nil, err
	}
	return completionMaterial{p}, nil
}

func (m completionMaterials) Read(ctx context.Context, ref ledger.Material) (string, error) {
	f, err := m.store.OpenRead(ctx, ref.ID, ref.Revision)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	return string(b), err
}

type completionDeadline struct {
	turn.Canon
	t        *testing.T
	deadline time.Time
	checked  bool
}

func (c *completionDeadline) Write(ctx context.Context, fn func(*canon.Tx) error) error {
	return c.Canon.Write(ctx, func(tx *canon.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		var completed int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM turn_runs WHERE state='completed'").Scan(&completed); err != nil {
			return err
		}
		if completed != 0 {
			c.checked = true
			if deadline, ok := ctx.Deadline(); !ok || !deadline.Before(c.deadline) {
				c.t.Error("completion transaction has no separate bounded deadline")
			}
		}
		return nil
	})
}

func TestLargeReplyPreparationAndCompletion(t *testing.T) {
	t.Parallel()
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[reject], func(t *testing.T) {
			db, cfg := database(t)
			input := inputEvent(t, db)
			a, err := assets.Open(t.Context(), filepath.Dir(cfg.Path), db)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			writer, err := assets.OpenMaterialWriter(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			deadline, _ := ctx.Deadline()
			materials := completionMaterials{t: t, store: a, writer: writer}
			observed := &completionDeadline{Canon: db, t: t, deadline: deadline}
			reply := completionReply()
			reply.Input.Body = strings.Repeat("한", ledger.InlineBytes)
			batch := []ledger.Admission{reply}
			if reject {
				batch = append(batch, reply) // collision after completion and the first material insert
			}
			var saved []ledger.Event
			_, err = turn.New(observed).Run(ctx, turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), InputEvent: input,
				Complete: func(ctx context.Context, db turn.Canon) error {
					var err error
					saved, err = ledger.New(db, materials).AdmitBatch(ctx, batch)
					return err
				},
				Loop: loop.Config{Stream: func(context.Context, provider.Request) (provider.Stream, error) {
					return &stream{events: []provider.Event{{Kind: provider.Terminal}}}, nil
				}},
			})
			if (err != nil) != reject {
				t.Fatalf("reject=%t err=%v", reject, err)
			}
			want, count := turn.Completed, 1
			if reject {
				want, count = turn.Active, 0
			} else if !observed.checked {
				t.Fatal("completion transaction was not observed")
			}
			row, err := turn.New(db).Read(t.Context(), "turn")
			if err != nil || row.State != want || len(saved) != count {
				t.Fatalf("completion: state=%s replies=%d err=%v", row.State, len(saved), err)
			}
			manifest, err := a.Backup(t.Context(), t.TempDir())
			if err != nil || len(manifest) != count {
				t.Fatalf("retained materials: %v %v", manifest, err)
			}
			if !reject {
				got, err := ledger.New(db, materials).Get(t.Context(), reply.Input.ID)
				if err != nil || got.Material.ID == "" || got.Body.Text() != reply.Input.Body {
					t.Fatalf("large reply missing: %v", err)
				}
			}
		})
	}
}
