// SPDX-License-Identifier: Apache-2.0
package messenger_test

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

type admissionMaterials struct {
	writer                        *assets.MaterialWriter
	entered, release, historyDone chan struct{}
	calls                         atomic.Int32
}
type admissionMaterial struct{ *assets.PreparedMaterial }

func (m admissionMaterial) Insert(ctx context.Context, tx *canon.Tx) (ledger.Material, error) {
	value, err := m.PreparedMaterial.Insert(ctx, tx)
	return ledger.Material{ID: value.ID, Revision: value.Revision}, err
}
func (m *admissionMaterials) Prepare(ctx context.Context, body secrets.Masked) (ledger.MaterialWrite, error) {
	if m.calls.Add(1) == 1 {
		close(m.entered)
		<-m.release
	} else {
		<-m.historyDone
	}
	value, err := m.writer.Prepare(ctx, strings.NewReader(body.Text()), "ledger-body", assets.Actor{PersonID: "person", AgentID: "agent"})
	if err != nil {
		return nil, err
	}
	return admissionMaterial{value}, nil
}
func (*admissionMaterials) Read(context.Context, ledger.Material) (string, error) { return "", nil }

func TestLiveEditDuringHistoryAdmissionKeepsNewestRevision(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t)
		source := f.connect(t)
		assetStore, err := assets.Open(t.Context(), f.root, f.db)
		if err != nil {
			t.Fatal(err)
		}
		defer assetStore.Close()
		writer, err := assets.OpenMaterialWriter(t.Context(), assetStore)
		if err != nil {
			t.Fatal(err)
		}
		materials := &admissionMaterials{writer: writer, entered: make(chan struct{}), release: make(chan struct{}), historyDone: make(chan struct{})}
		store := messenger.Open(f.db, materials, f.removal).WithClock(func() time.Time { return f.now })
		original := message("concurrent", messenger.RouteParticipant, f.now, strings.Repeat("history ", ledger.InlineBytes/8+1))
		edit := original
		edit.Text = strings.Repeat("edited ", ledger.InlineBytes/7+1)
		edit.Edited = f.now.Add(time.Minute)
		adapter := &fakeAdapter{pages: map[string][]messenger.Page{"": {{Messages: []messenger.Message{original}, Next: "after-history"}}}}
		service := messenger.NewService(store, map[string]messenger.Adapter{"discord": adapter}, time.Hour)
		history := make(chan error, 1)
		go func() { _, err := service.Sync(t.Context(), source); close(materials.historyDone); history <- err }()
		<-materials.entered
		live := make(chan error, 1)
		go func() {
			_, err := service.Apply(t.Context(), source, messenger.Event{Chat: "dm-1", Message: &edit})
			live <- err
		}()
		// Let both admission paths settle at a durable wait before releasing history.
		// This forces overlapping plans in the broken implementation without sleeps.
		synctest.Wait()
		close(materials.release)
		if err := <-history; err != nil {
			t.Fatal(err)
		}
		if err := <-live; err != nil {
			t.Fatalf("live edit lost: %v", err)
		}
		if revisions := count(t, f.db, "SELECT count(*) FROM messenger_record_revision WHERE message='concurrent'"); revisions != 2 {
			t.Fatalf("revisions=%d, want history and edit", revisions)
		}
		var materialID string
		var materialRevision int64
		if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
			return tx.QueryRowContext(t.Context(), "SELECT e.material_id,e.material_revision FROM messenger_record r JOIN conv_event e ON e.id=r.event WHERE r.message='concurrent'").Scan(&materialID, &materialRevision)
		}); err != nil {
			t.Fatal(err)
		}
		body, err := assetStore.OpenRead(t.Context(), materialID, materialRevision)
		if err != nil {
			t.Fatal(err)
		}
		text, err := io.ReadAll(body)
		body.Close()
		if err != nil || string(text) != edit.Text {
			t.Fatalf("newest edit body was not retained: %v", err)
		}
		chats, err := store.Chats(t.Context(), source.Person, source.Platform, source.Account)
		if err != nil || len(chats) != 1 || chats[0].Cursor != "after-history" {
			t.Fatalf("cursor changed: %+v %v", chats, err)
		}
	})
}
