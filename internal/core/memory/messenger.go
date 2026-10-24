// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

// Messenger extraction is one job for the whole main. It reads the person's
// own records that arrived after each source's learning watermark, groups a
// burst in one chat as one utterance and writes common items with no source
// agent. Other people's messages travel as untrusted context only.

// MessengerMessage is one earlier record shown to the model as context. Its ID
// is never an allowed evidence reference.
type MessengerMessage struct {
	ID      string `json:"id,omitempty"`
	Speaker string `json:"speaker"`
	Author  string `json:"author,omitempty"`
	Text    string `json:"text"`
}

// MessengerUtterance is one burst of the person's records in one chat.
type MessengerUtterance struct {
	Platform string `json:"platform"`
	Account  string `json:"account"`
	Chat     string `json:"chat"`
	Scope    Scope  `json:"scope"`
	// From is the chat's excluded-through position at preparation. Apply
	// rebuilds the batch when the chat's switch moved it since.
	From    int64              `json:"from,omitempty"`
	Text    string             `json:"text"`
	Events  []Evidence         `json:"events"`
	Context []MessengerMessage `json:"context,omitempty"`
}

// MessengerSourceProgress is one source's committed ledger position and its
// learning watermark at preparation. Apply advances the cursor only if both
// still hold.
type MessengerSourceProgress struct {
	Platform  string `json:"platform"`
	Account   string `json:"account"`
	Watermark string `json:"watermark"`
	// Exclusive marks a floor whose own instant is not learned input.
	Exclusive bool  `json:"exclusive,omitempty"`
	After     int64 `json:"after"`
	Through   int64 `json:"through"`
}

// MessengerBatch is prepared from a bounded ledger prefix across sources.
type MessengerBatch struct {
	Person     string                    `json:"person"`
	Revocation int64                     `json:"revocation"`
	Sources    []MessengerSourceProgress `json:"sources"`
	Utterances []MessengerUtterance      `json:"utterances"`
}

// messengerReadableRecord is the SQL predicate, over the aliases m (record) and
// e (event), that makes a record readable at all: it is live, canonical,
// visible, not a replay or a view, and outside every interval a learning switch
// excluded.
const messengerReadableRecord = `m.state='live'
 AND json_extract(e.origin,'$.kind')='messenger'
 AND EXISTS(SELECT 1 FROM retract_visible_event v WHERE v.id=e.id)
 AND e.replay_of IS NULL AND NOT EXISTS(SELECT 1 FROM ev_rel r WHERE r.event_id=e.id AND r.kind='view_of')
 AND NOT EXISTS(SELECT 1 FROM messenger_learning_skip k
  WHERE k.platform=m.platform AND k.account=m.account AND (k.chat='' OR k.chat=m.chat)
   AND e.rowid>k.from_rowid AND (k.to_rowid=0 OR e.rowid<=k.to_rowid))`

// messengerEvidenceCandidates lists the events the next preparation could read
// or attach as context, whose bodies are material-backed. Preparation resolves
// quotes inside one read transaction, and canon serves a single reader, so a
// material read there would wait on the connection the transaction holds. A
// caller loads these first, so the preload is bounded by the same batch and
// context limits the preparation uses, not by the person's whole history.
func (s *Store) MessengerEvidenceCandidates(ctx context.Context, person string, limit, contextLimit int) ([]Evidence, error) {
	if s == nil || s.db == nil || !present(person) || limit < 1 || limit > 1000 || contextLimit < 0 || contextLimit > 8 {
		return nil, ErrInvalid
	}
	var ids []string
	seen := map[string]bool{}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		sources, err := messengerSources(ctx, tx, person)
		if err != nil {
			return err
		}
		for _, source := range sources {
			if !source.learns() {
				continue
			}
			found, err := s.messengerCandidateIDs(ctx, tx, person, source, limit, contextLimit)
			if err != nil {
				return err
			}
			for _, id := range found {
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	refs := make([]Evidence, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, Evidence{Kind: Event, ID: id, Precision: Exact})
	}
	return refs, nil
}

// messengerCandidateIDs selects one source's next eligible records and the
// context messages an utterance starting at any of them could attach.
func (s *Store) messengerCandidateIDs(ctx context.Context, tx *canon.Tx, person string, source messengerSourceRow, limit, contextLimit int) ([]string, error) {
	if contextLimit == 0 {
		contextLimit = 0
	}
	floor, err := parseWatermark(source.watermark, source.connected)
	if err != nil {
		return nil, err
	}
	floorText := floor.UTC().Format(time.RFC3339Nano)
	rows, err := tx.QueryContext(ctx, `WITH cand AS (
 SELECT e.rowid AS seq, e.id AS id, m.chat AS chat, e.material_id IS NOT NULL AS backed,
  row_number() OVER (ORDER BY e.rowid) AS rn
 FROM messenger_record m
 JOIN conv_event e ON e.id=m.event
 JOIN messenger_chat c ON c.platform=m.platform AND c.account=m.account AND c.chat=m.chat
 WHERE m.platform=? AND m.account=? AND e.rowid>?
  AND e.speaker_kind='person' AND coalesce(e.person_id,'')=?
  AND c.learn=1 AND c.scope<>?
  AND (CASE WHEN ? THEN julianday(e.occurred_at)>julianday(?) ELSE julianday(e.occurred_at)>=julianday(?) END)
  AND `+messengerReadableRecord+`
)
SELECT id FROM cand WHERE rn<=? AND backed
UNION
SELECT x.id FROM (
 SELECT e.id AS id,
  row_number() OVER (PARTITION BY cand.seq ORDER BY e.occurred_at DESC,e.rowid DESC) AS rn
 FROM cand
 JOIN messenger_record m ON m.platform=? AND m.account=? AND m.chat=cand.chat
 JOIN conv_event e ON e.id=m.event
 WHERE e.rowid<cand.seq AND e.material_id IS NOT NULL AND `+messengerReadableRecord+`
) x WHERE x.rn<=?`,
		source.platform, source.account, source.after, person, string(NeverLeaves), source.exclusive, floorText, floorText, limit,
		source.platform, source.account, contextLimit)
	if err != nil {
		return nil, fmt.Errorf("messenger evidence candidates %s/%s: %w", source.platform, source.account, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// messengerSourceRow is one source at preparation.
type messengerSourceRow struct {
	platform, account, mode, watermark, connected string
	learn, exclusive, gated                       bool
	after                                         int64
}

// learns reports whether this source feeds learning. A gated source stays
// searchable and is passed over like a source with its switch off.
func (s messengerSourceRow) learns() bool { return s.learn && !s.gated }

// messengerGated reports a source the design keeps out of learning until the
// owner confirms the platform's developer-policy reading: a Discord bot-mode
// source is searchable and not learned from until then
// (docs/design/integrations.md).
func messengerGated(platform, mode string) bool { return platform == "discord" && mode == "official" }

// messengerSources reads this person's sources with their cursor state.
func messengerSources(ctx context.Context, tx *canon.Tx, person string) ([]messengerSourceRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT s.platform,s.account,s.mode,s.learning_watermark,s.connected_at,s.learn,s.learn_after,
 coalesce((SELECT c.through FROM messenger_learning_cursor c WHERE c.platform=s.platform AND c.account=s.account),0)
FROM messenger_source s WHERE s.person=? ORDER BY s.platform,s.account`, person)
	if err != nil {
		return nil, fmt.Errorf("messenger sources: %w", err)
	}
	defer rows.Close()
	var sources []messengerSourceRow
	for rows.Next() {
		var source messengerSourceRow
		if err := rows.Scan(&source.platform, &source.account, &source.mode, &source.watermark, &source.connected, &source.learn, &source.exclusive, &source.after); err != nil {
			return nil, err
		}
		source.gated = messengerGated(source.platform, source.mode)
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

func messengerHolder(platform, account string) string {
	return "messenger:" + platform + ":" + account
}

func parseWatermark(text, connected string) (time.Time, error) {
	if text == "" {
		text = connected
	}
	if text == "" {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, ErrInvalid
	}
	return at, nil
}

type messengerChatRow struct {
	platform, account, chat string
	learn                   bool
	scope                   string
}

// PrepareMessengerExtraction reads at most limit of the person's records across
// this person's sources. Non-person records only advance progress. Through is
// the source's committed ledger position, independent of the job's task
// sequence. No utterance means no model task should be enqueued.
//
// The watermark is a time floor. A record at exactly the floor is read, except
// for a learning-off import, whose own last record is the floor and stays
// outside the learned range.
func (s *Store) PrepareMessengerExtraction(ctx context.Context, person string, limit, contextLimit int, window time.Duration, resolve ResolveEvidence) (MessengerBatch, error) {
	batch := MessengerBatch{Person: person}
	if s == nil || s.db == nil || !present(person) || limit < 1 || limit > 1000 || contextLimit < 0 || contextLimit > 8 || window < 0 || resolve == nil {
		return batch, ErrInvalid
	}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&batch.Revocation); err != nil {
			return err
		}
		blocked, err := restrictedEvents(ctx, tx, RecallScope{Person: person, ExternalModel: true})
		if err != nil {
			return err
		}
		sources, err := messengerSources(ctx, tx, person)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		previous := ""
		var started, last int64
		remaining := limit
		for _, source := range sources {
			if remaining == 0 {
				break
			}
			progress := MessengerSourceProgress{Platform: source.platform, Account: source.account, Watermark: source.watermark, Exclusive: source.exclusive, After: source.after, Through: source.after}
			if !source.learns() {
				// Learning off, or gated until the owner confirms the platform
				// policy: the records are not read, and the cursor passes them
				// so turning learning on does not learn them late.
				through, err := messengerSkip(ctx, tx, source.platform, source.account, source.after)
				if err != nil {
					return err
				}
				progress.Through = through
				batch.Sources = append(batch.Sources, progress)
				continue
			}
			floor, err := parseWatermark(source.watermark, source.connected)
			if err != nil {
				return err
			}
			holder := messengerHolder(source.platform, source.account)
			floorText := floor.UTC().Format(time.RFC3339Nano)
			// Another participant's turn is read as well, without being learned
			// input: it breaks the burst, as a non-person turn does in a
			// conversation (docs/design/go-core.md). The window stops at the
			// limit-th person record, so the batch stays bounded.
			rows, err := tx.QueryContext(ctx, `WITH cand AS (
 SELECT e.rowid AS seq, e.id AS id, c.chat AS chat, c.scope AS scope,
  (e.speaker_kind='person' AND coalesce(e.person_id,'')=?) AS mine,
  sum(CASE WHEN e.speaker_kind='person' AND coalesce(e.person_id,'')=? THEN 1 ELSE 0 END) OVER (ORDER BY e.rowid) AS persons
 FROM messenger_record m
 JOIN conv_event e ON e.id=m.event
 JOIN messenger_chat c ON c.platform=m.platform AND c.account=m.account AND c.chat=m.chat
 WHERE m.platform=? AND m.account=? AND e.rowid>?
  AND c.learn=1 AND c.scope<>?
  AND (e.speaker_kind<>'person' OR coalesce(e.person_id,'')<>?
       OR (CASE WHEN ? THEN julianday(e.occurred_at)>julianday(?) ELSE julianday(e.occurred_at)>=julianday(?) END))
  AND `+messengerReadableRecord+`
)
SELECT seq,id,chat,scope,mine FROM cand WHERE persons<=? ORDER BY seq LIMIT ?`,
				person, person, source.platform, source.account, source.after, string(NeverLeaves), person,
				source.exclusive, floorText, floorText, limit, limit*8)
			if err != nil {
				return fmt.Errorf("messenger records %s/%s: %w", source.platform, source.account, err)
			}
			type row struct {
				sequence        int64
				id, chat, scope string
				mine            bool
			}
			var scanned []row
			for rows.Next() {
				var v row
				if err := rows.Scan(&v.sequence, &v.id, &v.chat, &v.scope, &v.mine); err != nil {
					rows.Close()
					return err
				}
				scanned = append(scanned, v)
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return err
			}
			if len(scanned) == 0 {
				// Nothing eligible in this source: skip every current record once.
				// A later import that should learn older history moves the watermark
				// back and resets this cursor.
				through, err := messengerSkip(ctx, tx, source.platform, source.account, source.after)
				if err != nil {
					return err
				}
				if through > progress.Through {
					progress.Through = through
				}
				batch.Sources = append(batch.Sources, progress)
				continue
			}
			for _, v := range scanned {
				progress.Through = v.sequence
				if !v.mine {
					// The participant's turn breaks the burst; it is not read as
					// evidence and never becomes context for the next utterance.
					previous = ""
					continue
				}
				ref := Evidence{Kind: Event, ID: v.id, Precision: Exact}
				origin, err := resolve(ctx, tx, ref)
				if err != nil {
					return err
				}
				if origin.Speaker != guard.Person || origin.Person != person || origin.Agent != holder || !origin.Messenger || origin.PlanEnded {
					return ErrPersonEvidence
				}
				if origin.ID != "" {
					ref.ID = origin.ID
				}
				if blocked[ref.ID] || origin.Scope == NeverLeaves || !present(origin.Quote) {
					previous = ""
					continue
				}
				if !validScope(origin.Scope) || !validScope(Scope(v.scope)) {
					return ErrScope
				}
				masked, err := maskedJobText(origin.Quote)
				if err != nil {
					return err
				}
				if seen[ref.ID] {
					continue
				}
				seen[ref.ID] = true
				ref.SourceAgent = ""
				ref.OccurredAt = origin.OccurredAt
				ref.Quote = masked
				key := source.platform + "\x00" + source.account + "\x00" + v.chat
				gap := int64(window / time.Second)
				if previous != key || origin.OccurredAt < last || origin.OccurredAt-started > gap {
					context, err := s.messengerContext(ctx, tx, source.platform, source.account, v.chat, v.sequence, contextLimit, blocked, resolve)
					if err != nil {
						return err
					}
					scope := Local
					if v.scope == string(Private) {
						scope = Private
					}
					batch.Utterances = append(batch.Utterances, MessengerUtterance{Platform: source.platform, Account: source.account, Chat: v.chat, Scope: scope, Context: context})
					started = origin.OccurredAt
				}
				u := &batch.Utterances[len(batch.Utterances)-1]
				if u.Text != "" {
					u.Text += "\n"
				}
				u.Text += masked
				u.Events = append(u.Events, ref)
				previous = key
				last = origin.OccurredAt
				remaining--
				if remaining == 0 {
					break
				}
			}
			batch.Sources = append(batch.Sources, progress)
			if remaining == 0 {
				break
			}
		}
		return nil
	})
	return batch, err
}

// messengerSkip reports the newest live record of a source, which the caller
// commits as its cursor when no record may be read.
func messengerSkip(ctx context.Context, tx *canon.Tx, platform, account string, after int64) (int64, error) {
	var through int64
	err := tx.QueryRowContext(ctx, `SELECT coalesce(max(e.rowid),?)
FROM messenger_record m JOIN conv_event e ON e.id=m.event
WHERE m.platform=? AND m.account=? AND m.state='live' AND e.rowid>? AND json_extract(e.origin,'$.kind')='messenger'`,
		after, platform, account, after).Scan(&through)
	return through, err
}

// messengerContext reads at most limit earlier records of one chat as untrusted
// context. They are visible, canonical, not excluded by retraction and not in
// an interval a learning switch excluded.
func (s *Store) messengerContext(ctx context.Context, tx *canon.Tx, platform, account, chat string, before int64, limit int, blocked map[string]bool, resolve ResolveEvidence) ([]MessengerMessage, error) {
	if limit == 0 {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.id,e.speaker_kind,m.author_name,
 (EXISTS(SELECT 1 FROM retract_visible_event v WHERE v.id=e.id)),
 (e.replay_of IS NULL AND NOT EXISTS(SELECT 1 FROM ev_rel r WHERE r.event_id=e.id AND r.kind='view_of'))
FROM messenger_record m JOIN conv_event e ON e.id=m.event
WHERE m.platform=? AND m.account=? AND m.chat=? AND e.rowid<? AND `+messengerReadableRecord+`
ORDER BY e.occurred_at DESC,e.rowid DESC LIMIT ?`, platform, account, chat, before, limit)
	if err != nil {
		return nil, err
	}
	type contextRow struct {
		id, speaker, author string
		visible, canonical  bool
	}
	var found []contextRow
	for rows.Next() {
		var v contextRow
		if err := rows.Scan(&v.id, &v.speaker, &v.author, &v.visible, &v.canonical); err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, v)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	var messages []MessengerMessage
	for i := len(found) - 1; i >= 0; i-- {
		v := found[i]
		if !v.visible || !v.canonical || blocked[v.id] {
			continue
		}
		origin, err := resolve(ctx, tx, Evidence{Kind: Event, ID: v.id, Precision: Exact})
		if err != nil {
			return nil, err
		}
		if !origin.Messenger || origin.PlanEnded || !present(origin.Quote) {
			continue
		}
		text, err := maskedJobText(origin.Quote)
		if err != nil {
			return nil, err
		}
		messages = append(messages, MessengerMessage{ID: v.id, Speaker: v.speaker, Author: v.author, Text: text})
	}
	return messages, nil
}

// ValidateMessengerExtraction rejects malformed proposals before caching an
// Aux result. Canonical availability, scope and the source cursor are still
// rechecked at dispatch and Apply.
func ValidateMessengerExtraction(batch MessengerBatch, items []Item) error {
	if len(items) > 100 || !present(batch.Person) {
		return ErrInvalid
	}
	allowed := map[string]bool{}
	for _, utterance := range batch.Utterances {
		for _, event := range utterance.Events {
			allowed[event.ID] = true
		}
	}
	for _, item := range items {
		if item.ID != "" || item.Holder != "" || item.SourceAgent != "" || item.Scope != "" || item.Supersedes != "" || len(item.Evidence) == 0 {
			return ErrInvalid
		}
		// The same write validation Apply runs, so a proposal that could never
		// commit is refused before the result is cached.
		if _, err := validateMode(Write{Person: batch.Person, Item: item}, false, true); err != nil {
			return ErrInvalid
		}
		if item.Subject != (Subject{Kind: Person, ID: batch.Person}) {
			return ErrInvalid
		}
		for _, evidence := range item.Evidence {
			if evidence.Kind != Event || evidence.Precision != Exact || evidence.Revision != 0 || !allowed[evidence.ID] {
				return ErrEvidence
			}
		}
	}
	return nil
}

// ApplyMessengerExtraction is called only inside Core's fenced job Apply
// transaction. It writes common items with no source agent and advances each
// scanned source cursor in the same transaction.
func ApplyMessengerExtraction(ctx context.Context, tx *canon.Tx, batch MessengerBatch, items []Item, resolve ResolveEvidence) error {
	if tx == nil || resolve == nil || !present(batch.Person) || len(items) > 100 {
		return ErrInvalid
	}
	if err := checkPreparedMessenger(ctx, tx, batch, resolve); err != nil {
		return err
	}
	if err := ValidateMessengerExtraction(batch, items); err != nil {
		return err
	}
	blocked, err := restrictedEvents(ctx, tx, RecallScope{Person: batch.Person, ExternalModel: true})
	if err != nil {
		return err
	}
	scopes := map[string]Scope{}
	holders := map[string]bool{}
	for _, utterance := range batch.Utterances {
		holders[messengerHolder(utterance.Platform, utterance.Account)] = true
		for _, event := range utterance.Events {
			scopes[event.ID] = utterance.Scope
		}
	}
	for _, item := range items {
		scope := Local
		for _, event := range item.Evidence {
			if blocked[event.ID] {
				return ErrScope
			}
			if scopeRank(scopes[event.ID]) > scopeRank(scope) {
				scope = scopes[event.ID]
			}
		}
		write := Write{Person: batch.Person, Item: item}
		write.Item.Scope = scope
		if _, err := insertWith(ctx, tx, write, resolve, false, insertKind{messenger: true, holders: holders}); err != nil {
			return err
		}
	}
	for _, source := range batch.Sources {
		if _, err := tx.ExecContext(ctx, `INSERT INTO messenger_learning_cursor(platform,account,through) VALUES(?,?,?)
ON CONFLICT(platform,account) DO UPDATE SET through=excluded.through`, source.Platform, source.Account, source.Through); err != nil {
			return err
		}
	}
	return nil
}

// CheckMessengerExtraction is called immediately before model dispatch. On
// ErrJobConflict the callback returns an empty result with zero usage without
// dispatching, and Apply consumes the obsolete task without writing items.
func (s *Store) CheckMessengerExtraction(ctx context.Context, batch MessengerBatch, resolve ResolveEvidence) error {
	if s == nil || s.db == nil || resolve == nil || !present(batch.Person) {
		return ErrInvalid
	}
	return s.db.Read(ctx, func(tx *canon.Tx) error { return checkPreparedMessenger(ctx, tx, batch, resolve) })
}

// CheckMessengerExtractionTx lets the job owner commit its source cursors only
// when the same transaction still accepts the prepared evidence.
func CheckMessengerExtractionTx(ctx context.Context, tx *canon.Tx, batch MessengerBatch, resolve ResolveEvidence) error {
	return checkPreparedMessenger(ctx, tx, batch, resolve)
}

func checkPreparedMessenger(ctx context.Context, tx *canon.Tx, batch MessengerBatch, resolve ResolveEvidence) error {
	if tx == nil || resolve == nil || !present(batch.Person) {
		return ErrInvalid
	}
	var epoch int64
	if err := tx.QueryRowContext(ctx, "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&epoch); err != nil {
		return err
	}
	if epoch != batch.Revocation {
		return ErrJobConflict
	}
	// A source that supplies no utterance only advances its own cursor past the
	// records it may not read, so its switch is not required to be on. A source
	// that supplied evidence must still be learning-enabled.
	supplying := map[string]bool{}
	for _, utterance := range batch.Utterances {
		supplying[utterance.Platform+"\x00"+utterance.Account] = true
	}
	for _, source := range batch.Sources {
		var watermark string
		var learn, exclusive bool
		var cursor int64
		err := tx.QueryRowContext(ctx, `SELECT s.learning_watermark,s.learn,s.learn_after,
 coalesce((SELECT c.through FROM messenger_learning_cursor c WHERE c.platform=s.platform AND c.account=s.account),0)
FROM messenger_source s WHERE s.person=? AND s.platform=? AND s.account=?`, batch.Person, source.Platform, source.Account).Scan(&watermark, &learn, &exclusive, &cursor)
		if err != nil {
			return ErrJobConflict
		}
		if watermark != source.Watermark || exclusive != source.Exclusive || cursor != source.After ||
			!learn && supplying[source.Platform+"\x00"+source.Account] {
			return ErrJobConflict
		}
	}
	blocked, err := restrictedEvents(ctx, tx, RecallScope{Person: batch.Person, ExternalModel: true})
	if err != nil {
		return err
	}
	for _, utterance := range batch.Utterances {
		var learn bool
		var scope string
		err := tx.QueryRowContext(ctx, "SELECT learn,scope FROM messenger_chat WHERE platform=? AND account=? AND chat=?",
			utterance.Platform, utterance.Account, utterance.Chat).Scan(&learn, &scope)
		if err != nil || !learn || scope != string(utterance.Scope) {
			return ErrJobConflict
		}
		holder := messengerHolder(utterance.Platform, utterance.Account)
		var texts []string
		for _, ref := range utterance.Events {
			if ref.Kind != Event || ref.Precision != Exact {
				return ErrEvidence
			}
			if blocked[ref.ID] {
				return ErrJobConflict
			}
			var visible int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM retract_visible_event WHERE id=?", ref.ID).Scan(&visible); err != nil {
				return err
			}
			if visible != 1 {
				return ErrJobConflict
			}
			// An edit replaces the record's event and leaves the old revision
			// visible. A prepared event that is no longer the live record was
			// superseded after preparation, so the batch rebuilds from the edit.
			// A learning switch that went off since preparation excludes it too.
			var current int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM messenger_record m JOIN conv_event e ON e.id=m.event
WHERE e.id=? AND `+messengerReadableRecord, ref.ID).Scan(&current); err != nil {
				return err
			}
			if current != 1 {
				return ErrJobConflict
			}
			origin, err := resolve(ctx, tx, ref)
			if err != nil {
				return err
			}
			if origin.PlanEnded || origin.Speaker != guard.Person || origin.Person != batch.Person || origin.Agent != holder || !origin.Messenger {
				return ErrPersonEvidence
			}
			if !validScope(origin.Scope) {
				return ErrScope
			}
			if origin.Scope == NeverLeaves {
				return ErrJobConflict
			}
			text, err := maskedJobText(origin.Quote)
			if err != nil {
				return err
			}
			if text != ref.Quote {
				return ErrJobConflict
			}
			texts = append(texts, text)
		}
		if strings.Join(texts, "\n") != utterance.Text {
			return ErrJobConflict
		}
		for _, message := range utterance.Context {
			if !present(message.ID) {
				return ErrJobConflict
			}
			// A memory added since preparation can restrict this message
			// without moving the retraction epoch, so the batch rebuilds rather
			// than sending it to the model.
			if blocked[message.ID] {
				return ErrJobConflict
			}
			var visible int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM messenger_record m JOIN conv_event e ON e.id=m.event
WHERE e.id=? AND `+messengerReadableRecord, message.ID).Scan(&visible); err != nil {
				return err
			}
			if visible != 1 {
				return ErrJobConflict
			}
			origin, err := resolve(ctx, tx, Evidence{Kind: Event, ID: message.ID, Precision: Exact})
			if err != nil {
				return err
			}
			text, err := maskedJobText(origin.Quote)
			if err != nil {
				return err
			}
			if text != message.Text || !origin.Messenger || origin.PlanEnded {
				return ErrJobConflict
			}
		}
	}
	return nil
}

// MessengerExtractionApply binds the job to its person at registration and has
// the signature of jobs.Definition.Apply without importing a higher layer.
func MessengerExtractionApply(person string, resolve ResolveEvidence) func(context.Context, *canon.Tx, jobapi.Task, json.RawMessage) error {
	return func(ctx context.Context, tx *canon.Tx, task jobapi.Task, output json.RawMessage) error {
		var batch MessengerBatch
		var items []Item
		if err := json.Unmarshal(task.Input, &batch); err != nil {
			return ErrInvalid
		}
		if batch.Person != person {
			return ErrScope
		}
		if err := json.Unmarshal(output, &items); err != nil {
			return ErrInvalid
		}
		if tx == nil || resolve == nil {
			return ErrInvalid
		}
		if err := checkPreparedMessenger(ctx, tx, batch, resolve); err != nil {
			if errors.Is(err, ErrJobConflict) {
				return nil
			}
			return err
		}
		return ApplyMessengerExtraction(ctx, tx, batch, items, resolve)
	}
}
