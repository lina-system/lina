// SPDX-License-Identifier: Apache-2.0
package messenger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

var (
	ErrInvalid      = errors.New("invalid_messenger_source")
	ErrNotFound     = sql.ErrNoRows
	ErrNotConnected = errors.New("messenger_source_not_connected")
)

// Removals is the one retraction path. Deleted records are hidden through it and
// follow the trash rule with the rest of the person's conversation content.
type Removals interface {
	Forget(context.Context, retract.Request) error
}

// Store owns the source registry and messenger admission.
type Store struct {
	db        *canon.Store
	materials ledger.Materials
	removals  Removals
	now       func() time.Time
}

// Open composes the store. materials carries bodies above the ledger's inline
// size; removals hides deleted records. Core supplies both.
func Open(db *canon.Store, materials ledger.Materials, removals Removals) *Store {
	return &Store{db: db, materials: materials, removals: removals, now: time.Now}
}

// WithClock replaces the clock for deterministic tests.
func (s *Store) WithClock(now func() time.Time) *Store { s.now = now; return s }

func validName(text string) bool { return text != "" && !strings.ContainsRune(text, 0) }

// Connect records a source the person connected, or updates its adapter state.
// Connecting is the person's act; nothing here discovers an account.
func (s *Store) Connect(ctx context.Context, source Source) error {
	if !validName(source.Person) || !validName(source.Platform) || !validName(source.Account) || !validName(source.Adapter) {
		return ErrInvalid
	}
	if source.Mode != ModeOfficial && source.Mode != ModeUnofficial {
		return ErrInvalid
	}
	if !source.State.valid() || source.State == StateDisconnected {
		return ErrInvalid
	}
	if source.Mode == ModeUnofficial && source.AcceptedTerms == nil {
		// An unofficial platform's terms must be accepted before connecting.
		return ErrInvalid
	}
	terms := ""
	if source.AcceptedTerms != nil {
		terms = fmt.Sprintf(`{"person":%q,"at":%q,"text":%q}`, source.AcceptedTerms.Person, source.AcceptedTerms.At.UTC().Format(time.RFC3339Nano), source.AcceptedTerms.Text)
	}
	at := source.ConnectedAt
	if at.IsZero() {
		at = s.now()
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO messenger_source(person,platform,account,mode,adapter,state,failure,learning_watermark,accepted_terms,connected_at)
VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(platform,account) DO UPDATE SET person=excluded.person,mode=excluded.mode,adapter=excluded.adapter,state=excluded.state,failure=excluded.failure,learning_watermark=excluded.learning_watermark,accepted_terms=excluded.accepted_terms`,
			source.Person, source.Platform, source.Account, string(source.Mode), source.Adapter, string(source.State), source.Failure, source.LearningWatermark, terms, at.UTC().Format(time.RFC3339Nano))
		return err
	})
}

func (s State) valid() bool {
	switch s {
	case StateConnected, StateNeedsSignIn, StateFailing, StateUnreachable, StateImportOnly, StateDisconnected:
		return true
	}
	return false
}

// Disconnect stops sync for a source and marks it disconnected. Its records
// stay; the caller deletes the credentials it holds.
func (s *Store) Disconnect(ctx context.Context, person, platform, account string) error {
	return s.setState(ctx, person, platform, account, StateDisconnected, "")
}

// MarkFailure records a failing source and its cause. It never blocks another
// source or conversation.
func (s *Store) MarkFailure(ctx context.Context, person, platform, account, cause string) error {
	return s.setState(ctx, person, platform, account, StateFailing, cause)
}

// MarkConnected clears a failure after a completed sync.
func (s *Store) MarkConnected(ctx context.Context, person, platform, account string) error {
	return s.setState(ctx, person, platform, account, StateConnected, "")
}

func (s *Store) setState(ctx context.Context, person, platform, account string, state State, cause string) error {
	if !validName(person) || !validName(platform) || !validName(account) || !state.valid() {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE messenger_source SET state=?,failure=? WHERE person=? AND platform=? AND account=?", string(state), cause, person, platform, account)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// Sources lists a person's sources.
func (s *Store) Sources(ctx context.Context, person string) ([]Source, error) {
	if !validName(person) {
		return nil, ErrInvalid
	}
	var sources []Source
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT person,platform,account,mode,adapter,state,failure,learning_watermark,accepted_terms,connected_at
FROM messenger_source WHERE person=? ORDER BY platform,account`, person)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			source, err := scanSource(rows)
			if err != nil {
				return err
			}
			sources = append(sources, source)
		}
		return rows.Err()
	})
	return sources, err
}

func scanSource(rows interface{ Scan(...any) error }) (Source, error) {
	var source Source
	var mode, state, terms, connected string
	if err := rows.Scan(&source.Person, &source.Platform, &source.Account, &mode, &source.Adapter, &state, &source.Failure, &source.LearningWatermark, &terms, &connected); err != nil {
		return Source{}, err
	}
	source.Mode, source.State = Mode(mode), State(state)
	if connected != "" {
		at, err := time.Parse(time.RFC3339Nano, connected)
		if err != nil {
			return Source{}, err
		}
		source.ConnectedAt = at
	}
	if terms != "" {
		var record struct{ Person, At, Text string }
		if err := json.Unmarshal([]byte(terms), &record); err != nil {
			return Source{}, err
		}
		at, err := time.Parse(time.RFC3339Nano, record.At)
		if err != nil {
			return Source{}, err
		}
		source.AcceptedTerms = &Terms{Person: record.Person, At: at, Text: record.Text}
	}
	return source, nil
}

// allSources lists every registered source. Core serves one person, so this set
// is the whole registry.
func (s *Store) allSources(ctx context.Context) ([]Source, error) {
	var sources []Source
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT person,platform,account,mode,adapter,state,failure,learning_watermark,accepted_terms,connected_at
FROM messenger_source ORDER BY platform,account`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			source, err := scanSource(rows)
			if err != nil {
				return err
			}
			sources = append(sources, source)
		}
		return rows.Err()
	})
	return sources, err
}

// Chats lists a source's chats in platform order.
func (s *Store) Chats(ctx context.Context, person, platform, account string) ([]ChatRecord, error) {
	if !validName(person) || !validName(platform) || !validName(account) {
		return nil, ErrInvalid
	}
	var chats []ChatRecord
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT chat,name,kind,first_message_at,sync,learn,embed,cursor FROM messenger_chat
WHERE platform=? AND account=? AND EXISTS(SELECT 1 FROM messenger_source o WHERE o.platform=platform AND o.account=account AND o.person=?)
ORDER BY chat`, platform, account, person)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			chat := ChatRecord{Platform: platform, Account: account}
			var first, cursor string
			if err := rows.Scan(&chat.Chat, &chat.Name, &chat.Kind, &first, &chat.Sync, &chat.Learn, &chat.Embed, &cursor); err != nil {
				return err
			}
			if first != "" {
				if chat.FirstMessageAt, err = time.Parse(time.RFC3339Nano, first); err != nil {
					return err
				}
			}
			chat.Cursor = Cursor(cursor)
			chats = append(chats, chat)
		}
		return rows.Err()
	})
	return chats, err
}

// UpsertChats adds newly listed chats and refreshes their names. The person's
// switches and the committed cursor are never overwritten by a listing.
func (s *Store) UpsertChats(ctx context.Context, source Source, chats []Chat) error {
	if source.Platform == "" || source.Account == "" || len(chats) == 0 {
		return nil
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		for _, chat := range chats {
			if !validName(chat.ID) {
				return ErrInvalid
			}
			switch chat.Kind {
			case "direct", "group", "channel", "thread":
			default:
				return ErrInvalid
			}
			first := ""
			if !chat.FirstMessageAt.IsZero() {
				first = chat.FirstMessageAt.UTC().Format(time.RFC3339Nano)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO messenger_chat(platform,account,chat,name,kind,first_message_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(platform,account,chat) DO UPDATE SET name=excluded.name,kind=excluded.kind,
 first_message_at=CASE WHEN messenger_chat.first_message_at='' THEN excluded.first_message_at ELSE messenger_chat.first_message_at END`,
				source.Platform, source.Account, chat.ID, chat.Name, chat.Kind, first); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetSwitches leaves a chat out of sync, learning or embedding.
func (s *Store) SetSwitches(ctx context.Context, person, platform, account, chat string, switches Switches) error {
	if !validName(person) || !validName(platform) || !validName(account) || !validName(chat) {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE messenger_chat SET sync=?,learn=?,embed=? WHERE platform=? AND account=? AND chat=?
AND EXISTS(SELECT 1 FROM messenger_source o WHERE o.platform=? AND o.account=? AND o.person=?)`,
			boolInt(switches.Sync), boolInt(switches.Learn), boolInt(switches.Embed), platform, account, chat, platform, account, person)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// RecordEffect records a write LINA made before its message can return through
// sync.
func (s *Store) RecordEffect(ctx context.Context, effect Effect) error {
	if effect.ID == "" || effect.Platform == "" || effect.Account == "" || effect.Chat == "" || effect.Agent == "" || effect.ClientID == "" {
		return ErrInvalid
	}
	if effect.State == "" {
		effect.State = EffectPending
	}
	if !validEffectState(effect.State) {
		return ErrInvalid
	}
	if effect.CreatedAt.IsZero() {
		effect.CreatedAt = s.now()
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO messenger_effect(id,platform,account,chat,agent,verb,client_id,message,state,created_at,settled_at)
VALUES(?,?,?,?,?,?,?,?,?,?,'')`, effect.ID, effect.Platform, effect.Account, effect.Chat, effect.Agent, effect.Verb, effect.ClientID, effect.Message, effect.State, effect.CreatedAt.UTC().Format(time.RFC3339Nano))
		return err
	})
}

// SettleEffect stores the platform's message id or the settled state of a write.
func (s *Store) SettleEffect(ctx context.Context, id, state, message string) error {
	if id == "" || !validEffectState(state) {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE messenger_effect SET state=?,message=?,settled_at=? WHERE id=?", state, message, s.now().UTC().Format(time.RFC3339Nano), id)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func validEffectState(state string) bool {
	switch state {
	case EffectPending, EffectSent, EffectUnknown, EffectFailed:
		return true
	}
	return false
}

type recordRow struct {
	revision   int
	event      string
	edited     time.Time
	state      string
	author     string
	authorName string
	occurredAt time.Time
	visible    bool
}

func (s *Store) recordRows(ctx context.Context, source Source, chat string, ids []string) (map[string]recordRow, error) {
	rows := map[string]recordRow{}
	if len(ids) == 0 {
		return rows, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{source.Platform, source.Account, chat}
	for _, id := range ids {
		args = append(args, id)
	}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		cursor, err := tx.QueryContext(ctx, `SELECT message,revision,event,edited_at,state,author,author_name,occurred_at,
 EXISTS(SELECT 1 FROM retract_visible_event v WHERE v.id=r.event) FROM messenger_record r
 WHERE platform=? AND account=? AND chat=? AND message IN (`+placeholders+`)`, args...)
		if err != nil {
			return err
		}
		defer cursor.Close()
		for cursor.Next() {
			var id string
			var row recordRow
			var edited, occurred string
			if err := cursor.Scan(&id, &row.revision, &row.event, &edited, &row.state, &row.author, &row.authorName, &occurred, &row.visible); err != nil {
				return err
			}
			if row.edited, err = parseTime(edited); err != nil {
				return err
			}
			if row.occurredAt, err = parseTime(occurred); err != nil {
				return err
			}
			rows[id] = row
		}
		return cursor.Err()
	})
	return rows, err
}

func parseTime(text string) (time.Time, error) {
	if text == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, text)
}

type effectRow struct {
	agent, state string
}

func (s *Store) effectsFor(ctx context.Context, source Source, chat string, ids []string) (map[string]effectRow, error) {
	effects := map[string]effectRow{}
	if len(ids) == 0 {
		return effects, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{source.Platform, source.Account, chat}
	// The two IN lists take the same ids in the same order: the first list
	// matches the client id and the second the platform's message id.
	for _, id := range ids {
		args = append(args, id)
	}
	for _, id := range ids {
		args = append(args, id)
	}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT client_id,message,agent,state FROM messenger_effect
WHERE platform=? AND account=? AND chat=? AND state<>'failed' AND (client_id IN (`+placeholders+`) OR message IN (`+placeholders+`))`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var client, message string
			var row effectRow
			if err := rows.Scan(&client, &message, &row.agent, &row.state); err != nil {
				return err
			}
			effects[client] = row
			if message != "" {
				effects[message] = row
			}
		}
		return rows.Err()
	})
	return effects, err
}

// PageResult reports one committed page.
type PageResult struct {
	Added, Updated, Deleted, Waiting int
}

// ErrPageWaiting reports a page held back because an own-account message matches
// a write whose outcome is not yet known. Nothing commits and the cursor stays,
// so the next read sees the page again after the write settles.
var ErrPageWaiting = errors.New("messenger_write_pending")

// pendingEffects reports whether the chat has a write whose outcome is not yet
// known. Until it settles, an own-account message can belong to that write and
// the page must wait rather than attribute it to the person.
func (s *Store) pendingEffects(ctx context.Context, source Source, chat string) (bool, error) {
	var pending bool
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM messenger_effect WHERE platform=? AND account=? AND chat=? AND state='pending')",
			source.Platform, source.Account, chat).Scan(&pending)
	})
	return pending, err
}

// AdmitPage commits one page of a chat: its new records, edits and the chat's
// cursor and coverage in one canon transaction. Reading the same messages again
// adds nothing. A message missing from a page changes nothing.
func (s *Store) AdmitPage(ctx context.Context, source Source, chat string, from Cursor, page Page) (PageResult, error) {
	if !validName(source.Person) || !validName(source.Platform) || !validName(source.Account) || !validName(chat) {
		return PageResult{}, ErrInvalid
	}
	ids := make([]string, 0, len(page.Messages))
	for _, message := range page.Messages {
		if !validName(message.ID) || !message.Route.Valid() {
			return PageResult{}, ErrInvalid
		}
		ids = append(ids, message.ID)
	}
	existing, err := s.recordRows(ctx, source, chat, ids)
	if err != nil {
		return PageResult{}, err
	}
	effects, err := s.effectsFor(ctx, source, chat, ids)
	if err != nil {
		return PageResult{}, err
	}
	pending, err := s.pendingEffects(ctx, source, chat)
	if err != nil {
		return PageResult{}, err
	}
	deleting := make(map[string]bool, len(page.Deletions))
	for _, deletion := range page.Deletions {
		if !validName(deletion.Message) {
			return PageResult{}, ErrInvalid
		}
		deleting[deletion.Message] = true
	}
	var result PageResult
	var plans []recordPlan
	var repairs []string
	for _, message := range page.Messages {
		if deleting[message.ID] {
			// The page's own deletion wins; its tombstone is written below.
			continue
		}
		row, seen := existing[message.ID]
		if seen && row.state == "deleted" {
			// A tombstone keeps the legacy reference: the content does not return.
			continue
		}
		if seen && !row.visible && row.event != "" {
			// A retraction hid this record. Record the tombstone too, so a later
			// read does not try to admit the same event again.
			repairs = append(repairs, message.ID)
			continue
		}
		if seen && (message.Edited.IsZero() || !message.Edited.After(row.edited)) {
			continue
		}
		plan := recordPlan{message: message, revision: 1, added: true}
		if seen {
			plan.revision, plan.added = row.revision+1, false
		}
		effect, hasEffect := effects[message.ID]
		if message.Route == RoutePerson {
			// An own-account message can be the return of a write the platform has
			// not identified yet, so an unsettled write anywhere in the chat holds
			// the page instead of attributing the words to the person.
			if (hasEffect && effect.state == EffectPending) || pending {
				return PageResult{Waiting: result.Waiting + 1}, ErrPageWaiting
			}
		}
		plan.ownWrite = message.Route == RoutePerson && hasEffect && (effect.state == EffectSent || effect.state == EffectUnknown)
		plan.effect = effect
		plan.relations, err = s.relations(ctx, source, chat, message, existing)
		if err != nil {
			return PageResult{}, err
		}
		if seen {
			plan.relations = append(plan.relations, ledger.Relation{TargetID: row.event, Kind: "supersedes"})
		}
		plans = append(plans, plan)
	}
	if len(page.Deletions) > 0 {
		// A deletion is durable before the cursor passes it: a crash after the
		// progress commit must not skip it, because the next read starts past it.
		deleted, err := s.deleteRecords(ctx, source, chat, page.Deletions)
		if err != nil {
			return PageResult{}, err
		}
		result.Deleted = deleted
	}
	if len(plans) == 0 && len(repairs) == 0 {
		// Nothing new: still record the completed read and advance the cursor.
		return result, s.commitProgress(ctx, source, chat, from, page, 0)
	}
	if len(plans) == 0 {
		if err := s.db.Write(ctx, func(tx *canon.Tx) error {
			for _, id := range repairs {
				if _, err := tx.ExecContext(ctx, `UPDATE messenger_record SET state='deleted',deleted_at=? WHERE platform=? AND account=? AND chat=? AND message=? AND state='live'`, s.now().UTC().Format(time.RFC3339Nano), source.Platform, source.Account, chat, id); err != nil {
					return err
				}
			}
			return s.writeProgress(ctx, tx, source, chat, from, page, 0)
		}); err != nil {
			return PageResult{}, err
		}
		return result, nil
	}
	batch := make([]ledger.Admission, 0, len(plans))
	index := make(map[string]int, len(plans))
	for i, plan := range plans {
		entry, in, err := s.admission(source, chat, plan)
		if err != nil {
			return PageResult{}, err
		}
		batch = append(batch, ledger.Admission{Entry: entry, Input: in})
		index[in.ID] = i
	}
	last := len(plans) - 1
	events := ledger.New(s.db, s.materials).AfterAdmission(func(ctx context.Context, tx *canon.Tx, event ledger.Event) error {
		i, ok := index[event.ID]
		if !ok {
			return nil
		}
		plan := plans[i]
		if err := s.writeRecord(ctx, tx, source, chat, plan, event); err != nil {
			return err
		}
		if i == last {
			for _, id := range repairs {
				if _, err := tx.ExecContext(ctx, `UPDATE messenger_record SET state='deleted',deleted_at=? WHERE platform=? AND account=? AND chat=? AND message=? AND state='live'`, s.now().UTC().Format(time.RFC3339Nano), source.Platform, source.Account, chat, id); err != nil {
					return err
				}
			}
			return s.writeProgress(ctx, tx, source, chat, from, page, len(plans))
		}
		return nil
	})
	if _, err := events.AdmitBatch(ctx, batch); err != nil {
		return PageResult{}, err
	}
	for _, plan := range plans {
		if plan.added {
			result.Added++
		} else {
			result.Updated++
		}
	}
	return result, nil
}

type recordPlan struct {
	message   Message
	revision  int
	added     bool
	ownWrite  bool
	effect    effectRow
	relations []ledger.Relation
}

func (s *Store) relations(ctx context.Context, source Source, chat string, message Message, existing map[string]recordRow) ([]ledger.Relation, error) {
	var ids []string
	for _, id := range []string{message.ReplyTo, message.Thread} {
		if id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.recordRows(ctx, source, chat, ids)
	if err != nil {
		return nil, err
	}
	var relations []ledger.Relation
	for _, target := range []struct{ id, kind string }{{message.ReplyTo, "replies_to"}, {message.Thread, "in_thread"}} {
		if target.id == "" {
			continue
		}
		if row, ok := rows[target.id]; ok && row.state == "live" && row.visible && row.event != "" {
			relations = append(relations, ledger.Relation{TargetID: row.event, Kind: target.kind})
		}
	}
	return relations, nil
}

// admission builds the trusted entry and ledger input of one record.
func (s *Store) admission(source Source, chat string, plan recordPlan) (guard.Entry, ledger.Input, error) {
	message := plan.message
	entry := guard.EntryMessengerPerson
	agent, person := source.Holder(), source.Person
	switch {
	case plan.ownWrite:
		entry, agent, person = guard.EntryMessengerAgent, plan.effect.agent, ""
	case message.Route == RoutePerson:
		entry = guard.EntryMessengerPerson
	case message.Route == RouteAutomation:
		entry, person = guard.EntryMessengerAutomation, ""
	case message.Route == RouteSystem:
		entry, person = guard.EntryMessengerSystem, ""
	case message.Route == RouteParticipant:
		entry, person = guard.EntryMessengerParticipant, ""
	default:
		return 0, ledger.Input{}, ErrInvalid
	}
	if plan.ownWrite && plan.effect.agent == "" {
		return 0, ledger.Input{}, ErrInvalid
	}
	id := eventID(source, chat, message.ID, plan.revision)
	ref := ledger.MessengerRef{Platform: source.Platform, Account: source.Account, Chat: chat, Message: message.ID}
	tags, err := thirdPartyTag(plan)
	if err != nil {
		return 0, ledger.Input{}, err
	}
	occurred := message.At
	if occurred.IsZero() {
		occurred = s.now()
	}
	return entry, ledger.Input{
		ID: id, ConversationID: conversationID(source, chat), PersonID: person, AgentID: agent,
		Surface:   ledger.Surface{Kind: "messenger", Platform: source.Platform, Account: source.Account, ChatID: chat},
		Messenger: &ref, OccurredAt: occurred.UTC(), RecordedAt: s.now().UTC(), TimeQuality: "recorded",
		Locator: id, Body: message.Text, Tags: tags, Relations: plan.relations,
	}, nil
}

// thirdPartyTag marks everything someone other than the person wrote as
// external, third-party evidence.
func thirdPartyTag(plan recordPlan) ([]secrets.Span, error) {
	if plan.message.Route == RoutePerson && !plan.ownWrite {
		return nil, nil
	}
	masked, err := secrets.Mask(plan.message.Text)
	if err != nil {
		return nil, err
	}
	if len(masked.Text()) == 0 {
		return nil, nil
	}
	return []secrets.Span{{Start: 0, End: len(masked.Text()), Sensitivity: "third_party"}}, nil
}

func (s *Store) writeRecord(ctx context.Context, tx *canon.Tx, source Source, chat string, plan recordPlan, event ledger.Event) error {
	message := plan.message
	if message.Author.ID != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO messenger_author(platform,account,author,name,email) VALUES(?,?,?,?,?)
ON CONFLICT(platform,account,author) DO UPDATE SET name=excluded.name,email=excluded.email`, source.Platform, source.Account, message.Author.ID, message.Author.Name, message.Author.Email); err != nil {
			return err
		}
	}
	occurred := event.OccurredAt.UTC().Format(time.RFC3339Nano)
	edited := ""
	if !message.Edited.IsZero() {
		edited = message.Edited.UTC().Format(time.RFC3339Nano)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messenger_record(platform,account,chat,message,revision,event,author,author_name,occurred_at,edited_at)
VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(platform,account,chat,message) DO UPDATE SET revision=excluded.revision,event=excluded.event,
 author=excluded.author,author_name=excluded.author_name,edited_at=excluded.edited_at`,
		source.Platform, source.Account, chat, message.ID, plan.revision, event.ID, message.Author.ID, message.Author.Name, occurred, edited); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO messenger_record_revision(platform,account,chat,message,revision,event) VALUES(?,?,?,?,?,?)`,
		source.Platform, source.Account, chat, message.ID, plan.revision, event.ID)
	return err
}

func (s *Store) commitProgress(ctx context.Context, source Source, chat string, from Cursor, page Page, messages int) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error { return s.writeProgress(ctx, tx, source, chat, from, page, messages) })
}

// writeProgress stores the cursor and, when the page completes the read, the
// coverage of that range. Intermediate pages advance the cursor only.
func (s *Store) writeProgress(ctx context.Context, tx *canon.Tx, source Source, chat string, from Cursor, page Page, messages int) error {
	readAt := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, "UPDATE messenger_chat SET cursor=? WHERE platform=? AND account=? AND chat=?", string(page.Next), source.Platform, source.Account, chat); err != nil {
		return err
	}
	if page.HasMore {
		return nil
	}
	through := page.Through
	if through.IsZero() {
		for _, message := range page.Messages {
			if message.At.After(through) {
				through = message.At
			}
		}
	}
	if through.IsZero() {
		through = s.now()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO messenger_coverage(platform,account,chat,read_from,read_through,read_at,messages)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(platform,account,chat,read_from,read_through) DO UPDATE SET read_at=excluded.read_at,messages=excluded.messages`,
		source.Platform, source.Account, chat, string(from), through.UTC().Format(time.RFC3339Nano), readAt, messages)
	return err
}

// deleteRecords applies platform-reported deletions through the retraction path
// and keeps the legacy reference as a tombstone.
func (s *Store) deleteRecords(ctx context.Context, source Source, chat string, deletions []Deletion) (int, error) {
	if len(deletions) == 0 {
		return 0, nil
	}
	if s.removals == nil {
		return 0, ErrInvalid
	}
	ids := make([]string, 0, len(deletions))
	for _, deletion := range deletions {
		if !validName(deletion.Message) {
			return 0, ErrInvalid
		}
		ids = append(ids, deletion.Message)
	}
	events, err := s.currentEvents(ctx, source, chat, ids)
	if err != nil {
		return 0, err
	}
	targets := make([]retract.Ref, 0, len(events))
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		for _, id := range events {
			var state string
			err := tx.QueryRowContext(ctx, "SELECT state FROM retract_target WHERE kind='event' AND id=?", id).Scan(&state)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			// An already hidden record keeps its removal and its trash deadline.
			if state == "" || state == "active" {
				targets = append(targets, retract.Ref{Kind: retract.Event, ID: id})
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(targets) > 0 {
		if err := s.removals.Forget(ctx, retract.Request{Person: source.Person, Targets: targets}); err != nil {
			return 0, err
		}
	}
	at := s.now().UTC().Format(time.RFC3339Nano)
	if err := s.db.Write(ctx, func(tx *canon.Tx) error {
		for _, deletion := range deletions {
			if _, err := tx.ExecContext(ctx, `INSERT INTO messenger_record(platform,account,chat,message,revision,event,occurred_at,state,deleted_at)
VALUES(?,?,?,?,0,'',?, 'deleted',?)
ON CONFLICT(platform,account,chat,message) DO UPDATE SET state='deleted',deleted_at=excluded.deleted_at
 WHERE messenger_record.state='live'`, source.Platform, source.Account, chat, deletion.Message, deletionTime(deletion, s.now()).Format(time.RFC3339Nano), at); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return len(deletions), nil
}

// DeleteRecords is the person's deletion of records in one chat.
func (s *Store) DeleteRecords(ctx context.Context, source Source, chat string, messages []string) (int, error) {
	var deletions []Deletion
	for _, message := range messages {
		deletions = append(deletions, Deletion{Message: message, At: s.now()})
	}
	return s.deleteRecords(ctx, source, chat, deletions)
}

// DeleteChat is the person's deletion of one chat's records.
func (s *Store) DeleteChat(ctx context.Context, source Source, chat string) (int, error) {
	ids, err := s.chatMessages(ctx, source, chat)
	if err != nil {
		return 0, err
	}
	return s.DeleteRecords(ctx, source, chat, ids)
}

// DeleteSourceRecords is the person's deletion of every record of a source.
func (s *Store) DeleteSourceRecords(ctx context.Context, source Source) (int, error) {
	chats, err := s.Chats(ctx, source.Person, source.Platform, source.Account)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, chat := range chats {
		n, err := s.DeleteChat(ctx, source, chat.Chat)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func (s *Store) chatMessages(ctx context.Context, source Source, chat string) ([]string, error) {
	var ids []string
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT message FROM messenger_record WHERE platform=? AND account=? AND chat=? AND state='live' ORDER BY message", source.Platform, source.Account, chat)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}

// currentEvents lists every revision event of the named messages.
func (s *Store) currentEvents(ctx context.Context, source Source, chat string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{source.Platform, source.Account, chat}
	for _, id := range ids {
		args = append(args, id)
	}
	var events []string
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT event FROM messenger_record_revision WHERE platform=? AND account=? AND chat=? AND message IN ("+placeholders+") ORDER BY event", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			events = append(events, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		// A record admitted before its revision row existed still has one event.
		err = s.db.Read(ctx, func(tx *canon.Tx) error {
			rows, err := tx.QueryContext(ctx, "SELECT event FROM messenger_record WHERE platform=? AND account=? AND chat=? AND message IN ("+placeholders+") AND event<>'' ORDER BY event", args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return err
				}
				events = append(events, id)
			}
			return rows.Err()
		})
	}
	return events, err
}

// Freshness derives a chat's coverage state from its last complete read.
func (s *Store) Freshness(ctx context.Context, source Source, chat string, interval, window time.Duration) (Freshness, error) {
	if interval <= 0 {
		return Freshness{State: "fresh"}, nil
	}
	var last time.Time
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		var readAt string
		err := tx.QueryRowContext(ctx, `SELECT read_at FROM messenger_coverage WHERE platform=? AND account=? AND chat=? ORDER BY read_at DESC LIMIT 1`,
			source.Platform, source.Account, chat).Scan(&readAt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		last, err = time.Parse(time.RFC3339Nano, readAt)
		return err
	})
	if err != nil {
		return Freshness{}, err
	}
	if last.IsZero() {
		return Freshness{State: "stale"}, nil
	}
	freshness := Freshness{LastRead: last, Age: s.now().Sub(last)}
	switch {
	case window > 0 && freshness.Age > window:
		freshness.State = "gap"
	case freshness.Age > interval:
		freshness.State = "stale"
	default:
		freshness.State = "fresh"
	}
	return freshness, nil
}

func deletionTime(deletion Deletion, fallback time.Time) time.Time {
	if deletion.At.IsZero() {
		return fallback
	}
	return deletion.At
}

func eventID(source Source, chat, message string, revision int) string {
	return fmt.Sprintf("messenger:%s:%s:%s:%s#%d", source.Platform, source.Account, chat, message, revision)
}

func conversationID(source Source, chat string) string {
	return fmt.Sprintf("messenger:%s:%s:%s", source.Platform, source.Account, chat)
}
