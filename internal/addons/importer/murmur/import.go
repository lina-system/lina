// SPDX-License-Identifier: Apache-2.0
package murmur

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// Capture takes a backup generation after the run's records are canon. The
// import is not complete until it succeeds.
type Capture func(context.Context) (string, error)

// ImportReport is the finished run: the rehearsal a person approved, what each
// chat wrote, and the room-by-room reconciliation with murmur's own digest.
type ImportReport struct {
	Rehearsal        Report `json:"rehearsal"`
	Added            int    `json:"added"`
	Updated          int    `json:"updated"`
	Linked           int    `json:"linked"`
	Deleted          int    `json:"deleted"`
	Unchanged        int    `json:"unchanged"`
	ReconciledRooms  int    `json:"reconciled_rooms"`
	MismatchedRooms  int    `json:"mismatched_rooms"`
	BackupGeneration string `json:"backup_generation,omitempty"`
}

// Import sends every chat through messenger admission on Core's canon, checks
// each room against murmur's own digest, retains the report and ends with a
// backup generation. A second run with the same sources adds nothing.
func (p *Plan) Import(ctx context.Context, store *messenger.Store, batchID string, capture Capture) (ImportReport, error) {
	if store == nil {
		return ImportReport{}, errors.New("a messenger store is required")
	}
	if !validName(batchID) {
		return ImportReport{}, errors.New("a batch id is required")
	}
	if capture == nil {
		return ImportReport{}, errors.New("a backup capture is required")
	}
	batch := messenger.ImportBatch{ID: batchID, System: systemName, Instance: p.profile.Instance, Person: p.profile.Person, CreatedAt: time.Now().UTC()}
	report := ImportReport{Rehearsal: p.Rehearse()}
	report.Rehearsal.Batch = batchID
	rooms := make([]RoomReport, 0, len(report.Rehearsal.Rooms))
	var readCounts []int
	sources := map[string]messenger.Source{}
	for _, chat := range p.Chats() {
		source, err := ensureSource(ctx, store, p.profile, chat)
		if err != nil {
			return report, err
		}
		sources[chat.Platform+"\x00"+chat.Account] = source
		if err := store.UpsertChats(ctx, source, []messenger.Chat{{ID: chat.Room, Name: chat.Name, Kind: chat.Kind, FirstMessageAt: chat.FirstMessage}}); err != nil {
			return report, err
		}
		result, err := store.AdmitImport(ctx, source, batch, messenger.ImportChat{
			Chat: chat.Room, Name: chat.Name, Kind: chat.Kind, FirstMessageAt: chat.FirstMessage,
			Records: chat.Records, Deletions: chat.Deletions,
		})
		if err != nil {
			return report, err
		}
		entry := RoomReport{Platform: chat.Platform, Account: chat.Account, Room: chat.Room, Name: chat.Name, Kind: chat.Kind}
		for _, planned := range report.Rehearsal.Rooms {
			if planned.Platform == entry.Platform && planned.Account == entry.Account && planned.Room == entry.Room {
				entry = planned
				break
			}
		}
		entry.Added, entry.Updated, entry.Linked = result.Added, result.Updated, result.Linked
		entry.Deleted, entry.Unchanged, entry.Conflicts = result.Deleted, result.Unchanged, result.Conflicts
		keys, err := store.ImportedKeys(ctx, systemName, p.profile.Instance, chat.Platform, chat.Account, chat.Room)
		if err != nil {
			return report, err
		}
		entry.ReadDigest = manifest(keys)
		readCounts = append(readCounts, len(keys))
		report.Added += result.Added
		report.Updated += result.Updated
		report.Linked += result.Linked
		report.Deleted += result.Deleted
		report.Unchanged += result.Unchanged
		rooms = append(rooms, entry)
	}
	// A room reconciles only when both the count and the digest of its imported
	// keys equal what murmur's own manifest states.
	for i := range rooms {
		if rooms[i].UIDDigest == "" || rooms[i].ReadDigest != rooms[i].UIDDigest || readCounts[i] != rooms[i].Messages {
			report.MismatchedRooms++
			continue
		}
		rooms[i].Reconciled = true
		report.ReconciledRooms++
	}
	report.Rehearsal.Rooms = rooms
	learning := report.Rehearsal.Learning
	through := time.Time{}
	if learning.Enabled {
		through = p.newestPersonMessage()
	}
	if err := store.RecordImport(ctx, messenger.ImportRecordRow{
		Batch: batch, System: systemName, Instance: p.profile.Instance, Person: p.profile.Person,
		ConfigHash: p.snapshot.configHash, SpoolHash: p.snapshot.spoolHash,
		Learning: learning.Enabled, LearningMessages: learning.PersonMessages,
		LearningThrough: through, Report: report.Rehearsal,
	}); err != nil {
		return report, err
	}
	if err := p.applyLearning(ctx, store, sources, learning); err != nil {
		return report, err
	}
	generation, err := capture(ctx)
	if err != nil {
		return report, err
	}
	if err := store.SetImportBackup(ctx, batchID, generation); err != nil {
		return report, err
	}
	report.BackupGeneration = generation
	return report, nil
}

// applyLearning states the run's learning choice on each source. A source the
// run created import-only starts at the older end of the history when the person
// turned learning on, and at its newer end when they left it off, so nothing
// older is learned by accident.
func (p *Plan) applyLearning(ctx context.Context, store *messenger.Store, sources map[string]messenger.Source, learning Learning) error {
	for _, source := range sources {
		if source.State != messenger.StateImportOnly {
			// A connected source keeps its live watermark; its own sync decides.
			continue
		}
		var at time.Time
		if learning.Enabled {
			at = p.oldestMessage()
		} else {
			at = p.newestMessage()
		}
		if at.IsZero() {
			continue
		}
		if err := store.SetLearningWatermark(ctx, source.Person, source.Platform, source.Account, at); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plan) oldestMessage() time.Time {
	var oldest time.Time
	for _, room := range p.rooms {
		for _, value := range room.Rows {
			if oldest.IsZero() || value.At.Before(oldest) {
				oldest = value.At
			}
		}
	}
	return oldest
}

func (p *Plan) newestMessage() time.Time {
	var newest time.Time
	for _, room := range p.rooms {
		for _, value := range room.Rows {
			if value.At.After(newest) {
				newest = value.At
			}
		}
	}
	return newest
}

func (p *Plan) newestPersonMessage() time.Time {
	var newest time.Time
	for _, room := range p.rooms {
		for _, value := range room.Rows {
			if route(p.profile, value) == messenger.RoutePerson && value.At.After(newest) {
				newest = value.At
			}
		}
	}
	return newest
}

// ensureSource creates an import-only source for a platform the person has not
// connected, and leaves a connected one exactly as it is.
func ensureSource(ctx context.Context, store *messenger.Store, profile Profile, chat Chat) (messenger.Source, error) {
	mode := messenger.ModeOfficial
	if chat.Platform == "kakaotalk" {
		// KakaoTalk reads a personal account through an unofficial client; the
		// import card states that. LINA asks for the terms again on connect.
		mode = messenger.ModeUnofficial
	}
	return store.EnsureImportSource(ctx, messenger.Source{
		Person: profile.Person, Platform: chat.Platform, Account: chat.Account,
		Mode: mode, Adapter: adapterName, State: messenger.StateImportOnly,
	})
}

// systemName is this adapter's namespace in a legacy reference.
const systemName = "murmur"

func validName(text string) bool {
	return text != "" && !strings.ContainsRune(text, 0)
}

func digestOf(keys []string) string {
	if len(keys) == 0 {
		return manifest(nil)
	}
	return manifest(keys)
}
