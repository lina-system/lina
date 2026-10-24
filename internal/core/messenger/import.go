// SPDX-License-Identifier: Apache-2.0
package messenger

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

var ErrImportConflict = errors.New("messenger_import_conflict")

// ImportBatch identifies one run of a memory importer over a stopped store. The
// batch row commits with the first record the run writes, so a run that changes
// nothing leaves no batch behind.
type ImportBatch struct {
	ID, System, Instance, Person string
	CreatedAt                    time.Time
}

// ImportChat is one chat handed over by an importer. Records are in source
// order: a later record for the same platform message is a revision of the
// earlier one. Deletions are the messages the source reports deleted, applied
// after the records and before the chat's turn to the next chat.
type ImportChat struct {
	Chat           string
	Name, Kind     string
	FirstMessageAt time.Time
	Records        []ImportRecord
	Deletions      []ImportDeletion
}

// ImportRecord is one source row the importer hands over. Kind and Key are the
// row's own reference inside the source store; Hash is the SHA-256 of its
// canonical source row. Ingest names the path the row came by (archive, live or
// export), which decides precedence for one platform key.
type ImportRecord struct {
	Message    Message
	Kind, Key  string
	Hash       string
	Ingest     string
	ObservedAt time.Time
}

// ImportDeletion is a deletion the source store reports for one of its rows.
type ImportDeletion struct {
	Message    string
	At         time.Time
	Kind, Key  string
	Hash       string
	Ingest     string
	ObservedAt time.Time
}

// ImportConflict names a source row whose content changed without an edit, so
// the import kept what it already had.
type ImportConflict struct {
	Message string `json:"message"`
	Key     string `json:"key"`
	Reason  string `json:"reason"`
}

// ImportResult reports what one chat's import wrote. Added counts new records,
// Updated new revisions, Linked references written onto records another path
// already admitted, Deleted tombstones, and Unchanged rows the source repeated.
type ImportResult struct {
	Added, Updated, Linked, Deleted, Unchanged int
	Conflicts                                  []ImportConflict
}

// Wrote reports whether the import changed canon.
func (r ImportResult) Wrote() bool {
	return r.Added+r.Updated+r.Linked+r.Deleted > 0
}

func validHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, r := range hash {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// EnsureImportSource registers a source the person handed over from another
// store. An absent source is created import-only; a source the person already
// connected keeps its live state, adapter and terms, so imported records join
// what the source syncs later.
func (s *Store) EnsureImportSource(ctx context.Context, source Source) (Source, error) {
	if !validName(source.Person) || !validName(source.Platform) || !validName(source.Account) || !validName(source.Adapter) {
		return Source{}, ErrInvalid
	}
	if source.Mode != ModeOfficial && source.Mode != ModeUnofficial {
		return Source{}, ErrInvalid
	}
	if source.State == "" {
		source.State = StateImportOnly
	}
	if source.State != StateImportOnly && source.State != StateConnected {
		return Source{}, ErrInvalid
	}
	at := source.ConnectedAt
	if at.IsZero() {
		at = s.now()
	}
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO messenger_source(person,platform,account,mode,adapter,state,failure,learning_watermark,accepted_terms,connected_at)
VALUES(?,?,?,?,?,'import_only','','','',?)
ON CONFLICT(platform,account) DO NOTHING`,
			source.Person, source.Platform, source.Account, string(source.Mode), source.Adapter, at.UTC().Format(time.RFC3339Nano))
		return err
	})
	if err != nil {
		return Source{}, err
	}
	var stored Source
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT person,platform,account,mode,adapter,state,failure,learning_watermark,learn,accepted_terms,connected_at
FROM messenger_source WHERE platform=? AND account=?`, source.Platform, source.Account)
		value, err := scanSource(row)
		stored = value
		return err
	})
	if err != nil {
		return Source{}, err
	}
	if stored.Person != source.Person {
		return Source{}, ErrImportConflict
	}
	return stored, nil
}

type importRef struct {
	Kind, Key, Hash, Ingest string
}

// importRefs reads the source references of the named records.
func (s *Store) importRefs(ctx context.Context, system, instance string, source Source, chat string, ids []string) (map[string]importRef, error) {
	refs := map[string]importRef{}
	if len(ids) == 0 {
		return refs, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{system, instance, source.Platform, source.Account, chat}
	for _, id := range ids {
		args = append(args, id)
	}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT message,kind,key,source_hash,fields FROM messenger_legacy_ref
WHERE system=? AND instance=? AND platform=? AND account=? AND chat=? AND message IN (`+placeholders+`)`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var message, kind, key, hash, fields string
			if err := rows.Scan(&message, &kind, &key, &hash, &fields); err != nil {
				return err
			}
			ref := importRef{Kind: kind, Key: key, Hash: hash}
			var stored struct {
				Ingest string `json:"ingest_path"`
			}
			if err := json.Unmarshal([]byte(fields), &stored); err != nil {
				return err
			}
			ref.Ingest = stored.Ingest
			refs[message] = ref
		}
		return rows.Err()
	})
	return refs, err
}

// importDecision says whether a source row becomes a new record or revision.
// It never decides from text: the platform key, the source row's hash, the
// ingest path and the edit time are the inputs.
func importDecision(record ImportRecord, seen bool, row recordRow, ref importRef, hasRef bool) (bool, string) {
	if hasRef {
		if ref.Kind != record.Kind || ref.Key != record.Key {
			return false, "source key changed"
		}
		if ref.Hash == record.Hash {
			// The same content may arrive again by another path; the reference
			// records the path, and no revision is needed.
			return false, ""
		}
		// murmur keeps the archive value of one key over the live value.
		if ref.Ingest == "archive" && record.Ingest != "archive" {
			return false, ""
		}
		if seen && record.Message.Edited.After(row.edited) {
			return true, ""
		}
		if record.Ingest == "archive" && ref.Ingest != "archive" {
			return true, ""
		}
		return false, "source row changed without an edit"
	}
	if !seen {
		return true, ""
	}
	if record.Message.Edited.After(row.edited) {
		return true, ""
	}
	if record.Ingest == "archive" {
		// An archive row is authoritative for its key, so it replaces what a
		// live read admitted.
		return true, ""
	}
	return false, ""
}

type importPlan struct {
	recordPlan
	record   ImportRecord
	previous string
}

// orderImport puts each record after the records of this run it replies to or
// joins the thread of. Placement is shared with AdmitPage.
func (s *Store) orderImport(ctx context.Context, source Source, chat string, plans []importPlan) ([]importPlan, error) {
	nodes := make([]relationNode, len(plans))
	for i, plan := range plans {
		nodes[i] = relationNode{index: i, id: plan.message.ID, replyTo: plan.message.ReplyTo, thread: plan.message.Thread, revision: plan.revision, previous: plan.previous}
	}
	ordered, err := s.placeRelations(ctx, source, chat, nodes)
	if err != nil {
		return nil, err
	}
	out := make([]importPlan, len(ordered))
	for i, node := range ordered {
		plan := plans[node.index]
		plan.relations = node.relations
		out[i] = plan
	}
	return out, nil
}

// importRelations resolves a record's reply and thread targets against what the
// chat already holds and what this admission has already placed. AdmitPage and
// AdmitImport share it. planned maps a platform message id to the event id of
// a record placed earlier in the same batch. Each target names one revision:
// the planned one when the batch also edits it, else the committed one.
func (s *Store) importRelations(ctx context.Context, source Source, chat string, message Message, planned map[string]string) ([]ledger.Relation, error) {
	targets := []struct{ id, kind string }{{message.ReplyTo, "replies_to"}, {message.Thread, "in_thread"}}
	var stored []string
	for _, target := range targets {
		if _, ok := planned[target.id]; target.id != "" && !ok {
			stored = append(stored, target.id)
		}
	}
	rows := map[string]recordRow{}
	if len(stored) > 0 {
		var err error
		if rows, err = s.recordRows(ctx, source, chat, stored); err != nil {
			return nil, err
		}
	}
	var relations []ledger.Relation
	for _, target := range targets {
		if target.id == "" {
			continue
		}
		if id, ok := planned[target.id]; ok {
			relations = append(relations, ledger.Relation{TargetID: id, Kind: target.kind})
		} else if row, ok := rows[target.id]; ok && row.state == "live" && row.visible && row.event != "" {
			relations = append(relations, ledger.Relation{TargetID: row.event, Kind: target.kind})
		}
	}
	return relations, nil
}

// AdmitImport commits one chat of an importer's run through the same admission
// path a platform read uses. Each record keeps its source reference, so a
// second import, or a later live read of the same chat, adds nothing.
func (s *Store) AdmitImport(ctx context.Context, source Source, batch ImportBatch, chat ImportChat) (ImportResult, error) {
	if !validName(source.Person) || !validName(source.Platform) || !validName(source.Account) || !validName(chat.Chat) ||
		!validName(batch.ID) || !validName(batch.System) || !validName(batch.Instance) || batch.Person != source.Person {
		return ImportResult{}, ErrInvalid
	}
	switch chat.Kind {
	case "direct", "group", "channel", "thread":
	default:
		return ImportResult{}, ErrInvalid
	}
	ids := make([]string, 0, len(chat.Records))
	for _, record := range chat.Records {
		if !validName(record.Message.ID) || !record.Message.Route.Valid() || !validName(record.Kind) || !validName(record.Key) || !validHash(record.Hash) || !validIngest(record.Ingest) {
			return ImportResult{}, ErrInvalid
		}
		ids = append(ids, record.Message.ID)
	}
	deleting := make(map[string]ImportDeletion, len(chat.Deletions))
	for _, deletion := range chat.Deletions {
		if !validName(deletion.Message) || !validName(deletion.Kind) || !validName(deletion.Key) || !validHash(deletion.Hash) || !validIngest(deletion.Ingest) {
			return ImportResult{}, ErrInvalid
		}
		deleting[deletion.Message] = deletion
		ids = append(ids, deletion.Message)
	}
	existing, err := s.recordRows(ctx, source, chat.Chat, ids)
	if err != nil {
		return ImportResult{}, err
	}
	refs, err := s.importRefs(ctx, batch.System, batch.Instance, source, chat.Chat, ids)
	if err != nil {
		return ImportResult{}, err
	}
	var result ImportResult
	var plans []importPlan
	var links []ImportRecord
	var references []ImportRecord
	var deletions []ImportDeletion
	var repairs []string
	for _, deletion := range chat.Deletions {
		row, seen := existing[deletion.Message]
		if seen && row.state == "deleted" {
			result.Unchanged++
			continue
		}
		deletions = append(deletions, deletion)
		if ref, hasRef := refs[deletion.Message]; !hasRef || ref.Hash != deletion.Hash || ref.Ingest != deletion.Ingest {
			references = append(references, ImportRecord{Message: Message{ID: deletion.Message}, Kind: deletion.Kind, Key: deletion.Key, Hash: deletion.Hash, Ingest: deletion.Ingest, ObservedAt: deletion.ObservedAt})
		}
	}
	// One source row is one record: two rows of one key in a single run are the
	// same message as the source holds it, and murmur's archive value wins.
	winners := map[string]int{}
	for i, record := range chat.Records {
		current, seen := winners[record.Message.ID]
		if !seen {
			winners[record.Message.ID] = i
			continue
		}
		held := chat.Records[current]
		if held.Ingest != "archive" && record.Ingest == "archive" || held.Ingest == record.Ingest {
			winners[record.Message.ID] = i
		}
	}
	for i, record := range chat.Records {
		message := record.Message
		if winners[message.ID] != i {
			result.Unchanged++
			continue
		}
		if _, deleted := deleting[message.ID]; deleted {
			// The deletion supplies the tombstone; the content does not enter.
			continue
		}
		row, seen := existing[message.ID]
		desired := speakerOf(message.Route)
		if seen && row.state != "live" {
			// A tombstone keeps the source reference: the content does not return.
			result.Unchanged++
			continue
		}
		if seen && !row.visible {
			repairs = append(repairs, message.ID)
			continue
		}
		ref, hasRef := refs[message.ID]
		admit, conflict := importDecision(record, seen, row, ref, hasRef)
		if conflict != "" {
			result.Conflicts = append(result.Conflicts, ImportConflict{Message: message.ID, Key: record.Key, Reason: conflict})
			continue
		}
		if !admit && seen && row.state == "live" && row.visible && row.speaker != desired && (!hasRef || ref.Hash == record.Hash) {
			// The source row is unchanged; the person's confirmed own-author map
			// now proves a different speaker. That is derived metadata, so a new
			// revision supersedes the earlier attribution instead of silently
			// keeping a wrong one.
			admit = true
		}
		if !admit {
			result.Unchanged++
			if !hasRef {
				// The record came from another path; the source row keeps its
				// own reference to it.
				links = append(links, record)
			}
			continue
		}
		plan := importPlan{recordPlan: recordPlan{message: message, revision: 1, added: true}, record: record}
		if seen {
			plan.revision, plan.added, plan.previous = row.revision+1, false, row.event
		}
		plans = append(plans, plan)
	}
	plans, err = s.orderImport(ctx, source, chat.Chat, plans)
	if err != nil {
		return ImportResult{}, err
	}
	if len(deletions) > 0 {
		remove := make([]Deletion, 0, len(deletions))
		for _, deletion := range deletions {
			remove = append(remove, Deletion{Message: deletion.Message, At: deletion.At})
		}
		deleted, err := s.deleteRecords(ctx, source, chat.Chat, remove)
		if err != nil {
			return ImportResult{}, err
		}
		result.Deleted = deleted
	}
	if len(plans) == 0 {
		if len(links) == 0 && len(references) == 0 && len(repairs) == 0 {
			return result, nil
		}
		err = s.db.Write(ctx, func(tx *canon.Tx) error {
			if err := ensureImportBatch(ctx, tx, batch, s.now()); err != nil {
				return err
			}
			for _, id := range repairs {
				if _, err := tx.ExecContext(ctx, `UPDATE messenger_record SET state='deleted',deleted_at=? WHERE platform=? AND account=? AND chat=? AND message=? AND state='live'`, s.now().UTC().Format(time.RFC3339Nano), source.Platform, source.Account, chat.Chat, id); err != nil {
					return err
				}
			}
			for _, record := range append(links, references...) {
				if err := s.writeImportRef(ctx, tx, batch, source, chat.Chat, record, s.now()); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return ImportResult{}, err
		}
		result.Linked += len(links) + len(references)
		result.Deleted += len(repairs)
		return result, nil
	}
	batchInputs := make([]ledger.Admission, 0, len(plans))
	index := make(map[string]int, len(plans))
	for i, plan := range plans {
		entry, in, err := s.admission(source, chat.Chat, plan.recordPlan)
		if err != nil {
			return ImportResult{}, err
		}
		batchInputs = append(batchInputs, ledger.Admission{Entry: entry, Input: in})
		index[in.ID] = i
	}
	last := len(plans) - 1
	events := ledger.New(s.db, s.materials).AfterAdmission(func(ctx context.Context, tx *canon.Tx, event ledger.Event) error {
		i, ok := index[event.ID]
		if !ok {
			return nil
		}
		plan := plans[i]
		if err := s.writeRecord(ctx, tx, source, chat.Chat, plan.recordPlan, event); err != nil {
			return err
		}
		if err := ensureImportBatch(ctx, tx, batch, s.now()); err != nil {
			return err
		}
		if err := s.writeImportRef(ctx, tx, batch, source, chat.Chat, plan.record, s.now()); err != nil {
			return err
		}
		if i != last {
			return nil
		}
		for _, id := range repairs {
			if _, err := tx.ExecContext(ctx, `UPDATE messenger_record SET state='deleted',deleted_at=? WHERE platform=? AND account=? AND chat=? AND message=? AND state='live'`, s.now().UTC().Format(time.RFC3339Nano), source.Platform, source.Account, chat.Chat, id); err != nil {
				return err
			}
		}
		for _, record := range append(links, references...) {
			if err := s.writeImportRef(ctx, tx, batch, source, chat.Chat, record, s.now()); err != nil {
				return err
			}
		}
		return nil
	})
	if _, err := events.AdmitBatch(ctx, batchInputs); err != nil {
		return ImportResult{}, err
	}
	for _, plan := range plans {
		if plan.added {
			result.Added++
		} else {
			result.Updated++
		}
	}
	result.Linked += len(links) + len(references)
	result.Deleted += len(repairs)
	return result, nil
}

// speakerOf is the ledger speaker kind a route is admitted as.
func speakerOf(route Route) string {
	switch route {
	case RoutePerson:
		return "person"
	case RouteAutomation:
		return "automation"
	case RouteSystem:
		return "system"
	case RouteParticipant:
		return "external_participant"
	default:
		return "external_participant"
	}
}

func validIngest(path string) bool {
	switch path {
	case "archive", "live", "export":
		return true
	}
	return false
}

func ensureImportBatch(ctx context.Context, tx *canon.Tx, batch ImportBatch, now time.Time) error {
	created := batch.CreatedAt
	if created.IsZero() {
		created = now
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO import_batch(id,created_at) VALUES(?,?) ON CONFLICT(id) DO NOTHING",
		batch.ID, created.UTC().Format(time.RFC3339Nano))
	return err
}

// writeImportRef ties one source row to the record it became, with the hash of
// the canonical source row and the path it arrived by.
func (s *Store) writeImportRef(ctx context.Context, tx *canon.Tx, batch ImportBatch, source Source, chat string, record ImportRecord, now time.Time) error {
	fields, err := json.Marshal(struct {
		IngestPath string `json:"ingest_path"`
		ObservedAt string `json:"observed_at,omitempty"`
	}{record.Ingest, formatOptional(record.ObservedAt)})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO messenger_legacy_ref(system,instance,kind,key,platform,account,chat,message,source_hash,batch_id,imported_at,fields)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(system,instance,kind,key) DO UPDATE SET source_hash=excluded.source_hash,
 batch_id=excluded.batch_id,imported_at=excluded.imported_at,fields=excluded.fields`,
		batch.System, batch.Instance, record.Kind, record.Key, source.Platform, source.Account, chat, record.Message.ID,
		record.Hash, batch.ID, now.UTC().Format(time.RFC3339Nano), string(fields))
	return err
}

func formatOptional(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano)
}

// ImportedKeys lists the source keys of one chat's imported records, sorted.
// Reconciliation compares their count and digest with the source store's own
// manifest.
func (s *Store) ImportedKeys(ctx context.Context, system, instance, platform, account, chat string) ([]string, error) {
	if !validName(system) || !validName(instance) || !validName(platform) || !validName(account) || !validName(chat) {
		return nil, ErrInvalid
	}
	var keys []string
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT key FROM messenger_legacy_ref
WHERE system=? AND instance=? AND platform=? AND account=? AND chat=? ORDER BY key`, system, instance, platform, account, chat)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				return err
			}
			keys = append(keys, key)
		}
		return rows.Err()
	})
	return keys, err
}
