// SPDX-License-Identifier: Apache-2.0
package replay

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type Reader interface {
	Read(context.Context, func(*canon.Tx) error) error
}
type Scope struct{ Person, Agent string }
type Selection struct {
	Agent         string   `json:"agent"`
	Conversations []string `json:"conversations"`
	Since, Until  time.Time
}
type Event struct {
	ID        string            `json:"id"`
	Revision  string            `json:"revision"`
	Speaker   guard.SpeakerKind `json:"speaker_kind"`
	At        time.Time         `json:"occurred_at"`
	Text      string            `json:"text"`
	Relations []ledger.Relation `json:"relations,omitempty"`
}
type Episode struct {
	Version         int                    `json:"version"`
	TurnID          string                 `json:"turn_id"`
	Conversation    string                 `json:"conversation_id"`
	Input           Event                  `json:"input"`
	Evidence        ledger.RequestEvidence `json:"evidence"`
	History         []Event                `json:"history"`
	Persona         agents.Layers          `json:"persona"`
	Items           []provider.Item        `json:"items"`
	ReplyRefs       []ledger.Reference     `json:"reply_refs,omitempty"`
	Reply           []provider.Item        `json:"reply"`
	Calls           []Call                 `json:"calls"`
	Next            []Event                `json:"next,omitempty"`
	Clock           time.Time              `json:"clock"`
	Zone            string                 `json:"zone"`
	RevocationEpoch uint64                 `json:"revocation_epoch"`
}
type Manifest struct {
	Version         int            `json:"version"`
	Selected        int            `json:"selected"`
	Exported        int            `json:"exported"`
	LeftOut         int            `json:"left_out"`
	Reasons         map[string]int `json:"left_out_reasons"`
	RevocationEpoch uint64         `json:"revocation_epoch"`
}

// Source borrows the running Core's services. It opens no store and its database
// interface deliberately has no write method. Project is the existing assembler.
type Source struct {
	Instructions InstructionBuilder
	Rebuild      func(Episode, []Context, []provider.Item) ([]provider.Item, error)
	Skill        func(context.Context, string, string) (string, string, error)
	DB           Reader
	Materials    ledger.Materials
	Persona      func(context.Context, string, string) (agents.Preparation, error)
	Project      func([]ledger.Event, string, string) ([]provider.Item, error)
	Digest       func(provider.Request) string
}
type readTx struct{ tx *canon.Tx }

func (r readTx) Read(_ context.Context, f func(*canon.Tx) error) error { return f(r.tx) }
func (readTx) Write(context.Context, func(*canon.Tx) error) error      { return canon.ErrReadOnly }

type Exclusion string

func (e Exclusion) Error() string { return string(e) }

func (s Source) Epoch(ctx context.Context) (uint64, error) {
	var epoch uint64
	err := s.DB.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&epoch)
	})
	return epoch, err
}
func (s Source) Validate(ctx context.Context, epoch uint64) error {
	current, err := s.Epoch(ctx)
	if err == nil && current != epoch {
		return Exclusion("revocation_changed")
	}
	return err
}

func (s Source) Select(ctx context.Context, scope Scope, q Selection) ([]string, error) {
	// An incomplete selection is empty, never an implicit whole-install export.
	if q.Agent == "" && len(q.Conversations) == 0 && q.Since.IsZero() && q.Until.IsZero() {
		return nil, nil
	}
	if q.Agent != scope.Agent || scope.Person == "" || len(q.Conversations) == 0 || q.Since.IsZero() || !q.Until.After(q.Since) {
		return nil, ErrInvalid
	}
	var ids []string
	seen := map[string]bool{}
	err := s.DB.Read(ctx, func(tx *canon.Tx) error {
		for _, conversation := range q.Conversations {
			if conversation == "" || seen[conversation] {
				return ErrInvalid
			}
			seen[conversation] = true
			rows, err := tx.QueryContext(ctx, `SELECT t.id FROM turn_runs t JOIN turn_control c ON c.turn_id=t.id JOIN conv_event e ON e.id=c.input_event
    WHERE t.conversation=? AND e.agent_id=? AND julianday(e.occurred_at)>=julianday(?) AND julianday(e.occurred_at)<julianday(?)
    AND EXISTS(SELECT 1 FROM conv_event owner WHERE owner.conversation_id=e.conversation_id AND owner.speaker_kind='person' AND owner.person_id=?)
    AND NOT EXISTS(SELECT 1 FROM conv_event other WHERE other.conversation_id=e.conversation_id AND other.speaker_kind='person' AND other.person_id<>?) ORDER BY e.rowid,t.id`, conversation, scope.Agent, q.Since.Format(time.RFC3339Nano), q.Until.Format(time.RFC3339Nano), scope.Person, scope.Person)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, id)
			}
			if err = errors.Join(rows.Err(), rows.Close()); err != nil {
				return err
			}
		}
		return nil
	})
	return ids, err
}

func eventValue(e ledger.Event) Event {
	digest := sha256.Sum256([]byte(e.Body.Text()))
	return Event{ID: e.ID, Revision: hex.EncodeToString(digest[:]), Speaker: e.SpeakerKind, At: e.OccurredAt, Text: e.Body.Text(), Relations: e.Relations}
}

// visible gates original sources before reading content. Forgotten evidence and
// copies follow canon's existing revocation projection; scope is checked across
// holders so a common copy cannot launder a private source into an export.
func visible(ctx context.Context, tx *canon.Tx, scope Scope, id string) error {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM retract_visible_event e WHERE e.id=? AND e.agent_id=?
 AND (e.material_id IS NULL OR EXISTS(SELECT 1 FROM assets a JOIN asset_revisions r ON r.asset_id=a.id WHERE a.id=e.material_id AND r.revision=e.material_revision AND a.trashed_at=0))
 AND NOT EXISTS(SELECT 1 FROM retract_block b WHERE b.person=? AND b.conversation_id=e.conversation_id)
 AND NOT EXISTS(SELECT 1 FROM conv_event other WHERE other.conversation_id=e.conversation_id AND other.speaker_kind='person' AND other.person_id<>?)
 AND EXISTS(SELECT 1 FROM conv_event owner WHERE owner.conversation_id=e.conversation_id AND owner.agent_id=e.agent_id AND owner.speaker_kind='person' AND owner.person_id=?)
 AND NOT EXISTS(SELECT 1 FROM memory_item m,json_each(m.record,'$.evidence') x WHERE m.person=? AND m.scope IN ('private','never-leaves') AND json_extract(x.value,'$.kind')='event' AND json_extract(x.value,'$.id')=e.id)`, id, scope.Agent, scope.Person, scope.Person, scope.Person, scope.Person).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return Exclusion("restricted_content")
	}
	return nil
}

func (s Source) loadSnapshot(ctx context.Context, scope Scope, id string) (Episode, error) {
	ep := Episode{Version: 1, TurnID: id, Reply: []provider.Item{}, Calls: []Call{}, History: []Event{}}
	var history []ledger.Event
	var packet []Context
	var profile []provider.Item
	err := s.DB.Read(ctx, func(tx *canon.Tx) error {
		var inputID, state string
		if err := tx.QueryRowContext(ctx, "SELECT t.conversation,t.state,c.input_event FROM turn_runs t JOIN turn_control c ON c.turn_id=t.id WHERE t.id=?", id).Scan(&ep.Conversation, &state, &inputID); err != nil {
			return err
		}
		if state != "completed" {
			return Exclusion("incomplete_turn")
		}
		if err := tx.QueryRowContext(ctx, "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&ep.RevocationEpoch); err != nil {
			return err
		}
		journal := ledger.New(readTx{tx}, s.Materials)
		get := func(id string) (ledger.Event, error) {
			if err := visible(ctx, tx, scope, id); err != nil {
				return ledger.Event{}, err
			}
			e, err := journal.Get(ctx, id)
			var needed *materialNeeded
			if errors.As(err, &needed) {
				return e, err
			}
			if errors.Is(err, ledger.ErrBytesUnavailable) || errors.Is(err, sql.ErrNoRows) {
				return e, Exclusion("missing_source")
			}
			return e, err
		}
		// Inspect only the scoped, text-free assembly before removal-gated
		// content so a forgotten packet source retains its specific reason.
		var speaker string
		if err := tx.QueryRowContext(ctx, "SELECT speaker_kind FROM conv_event WHERE id=?", inputID).Scan(&speaker); err != nil {
			return err
		}
		if speaker != "person" {
			return Exclusion("not_person_turn")
		}
		var owned int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM conv_event e WHERE e.id=? AND e.agent_id=? AND e.person_id=? AND e.conversation_id=?
         AND NOT EXISTS(SELECT 1 FROM conv_event other WHERE other.conversation_id=e.conversation_id AND other.speaker_kind='person' AND other.person_id<>?)`, inputID, scope.Agent, scope.Person, ep.Conversation, scope.Person).Scan(&owned); err != nil {
			return err
		}
		if owned != 1 {
			return Exclusion("restricted_content")
		}
		evidence, err := journal.Get(ctx, "request:"+id+":0")
		if err != nil {
			var needed *materialNeeded
			if errors.As(err, &needed) {
				return err
			}
			var exclusion Exclusion
			if errors.As(err, &exclusion) || errors.Is(err, sql.ErrNoRows) || errors.Is(err, ledger.ErrBytesUnavailable) {
				return Exclusion("missing_assembly")
			}
			return err
		}
		if evidence.AgentID != scope.Agent || evidence.ConversationID != ep.Conversation || evidence.ScaffoldKind != "request_assembly" {
			return Exclusion("missing_assembly")
		}
		if err = json.Unmarshal([]byte(evidence.Body.Text()), &ep.Evidence); err != nil {
			return Exclusion("missing_assembly")
		}
		a := ep.Evidence.Assembly
		if ep.Evidence.TurnID != id || ep.Evidence.Round != 0 || ep.Evidence.Class != provider.ModelAPI || ep.Evidence.Model == "" || a.AgentID != scope.Agent || a.PersonaRevision == "" || len(a.History) == 0 {
			return Exclusion("missing_assembly")
		}
		// Older producers did not record reconstructible context source kinds. Do
		// not replace those packets with today's memory or silently drop them.
		if a.InstructionFormat == "persona-v1" || a.InstructionFormat == "persona-tui-v1" {
			if a.OwnerPersonID != scope.Person {
				return Exclusion("missing_assembly")
			}
			packet, profile, err = s.contextSnapshot(ctx, tx, scope, ep)
			if err != nil {
				return err
			}
		} else if len(a.Items) > 0 || len(a.Skills) > 0 {
			return Exclusion("missing_assembly")
		}
		if err := visible(ctx, tx, scope, evidence.ID); err != nil {
			return err
		}
		input, err := get(inputID)
		if err != nil {
			return err
		}
		if input.PersonID != scope.Person || input.SpeakerKind != guard.Person || input.Origin.Kind != "native" || input.ReplayOf != "" || input.ScaffoldKind != "" {
			return Exclusion("not_person_turn")
		}
		ep.Input = eventValue(input)
		ep.Clock = input.OccurredAt
		ep.Zone = "UTC"
		hasInput := false
		for _, ref := range a.History {
			e, err := get(ref.ID)
			if err != nil {
				return err
			}
			v := eventValue(e)
			if e.ConversationID != ep.Conversation || ref.Revision != v.Revision {
				return Exclusion("source_changed")
			}
			hasInput = hasInput || e.ID == inputID
			history = append(history, e)
			ep.History = append(ep.History, v)
		}
		if !hasInput {
			return Exclusion("missing_assembly")
		}
		rows, err := tx.QueryContext(ctx, "SELECT round,ordinal,item FROM turn_items WHERE turn_id=? ORDER BY round,ordinal", id)
		if err != nil {
			return err
		}
		type output struct {
			round   int
			ordinal int64
			item    []byte
		}
		var outputs []output
		for rows.Next() {
			var o output
			if err = rows.Scan(&o.round, &o.ordinal, &o.item); err != nil {
				rows.Close()
				return err
			}
			outputs = append(outputs, o)
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, o := range outputs {
			var header struct{ Type, Role string }
			if json.Unmarshal(o.item, &header) != nil {
				return Exclusion("missing_source")
			}
			if header.Role == "assistant" && (header.Type == "message" || header.Type == "") {
				// Native completion uses the provider round (one-based) and item index
				// as the immutable output event identity. Do not search by reply text.
				event, err := get(fmt.Sprintf("%s:%d:%d", id, o.round+1, o.ordinal))
				if err != nil {
					return err
				}
				if event.ParentID != inputID || event.ConversationID != ep.Conversation || event.SpeakerKind != guard.Agent {
					return Exclusion("missing_source")
				}
				value := eventValue(event)
				ep.ReplyRefs = append(ep.ReplyRefs, ledger.Reference{ID: value.ID, Revision: value.Revision})
			}
			masked, err := maskJSON(o.item)
			if err != nil {
				return err
			}
			ep.Reply = append(ep.Reply, masked)
		}
		rows, err = tx.QueryContext(ctx, "SELECT call,result FROM turn_calls WHERE turn_id=? ORDER BY round,ordinal", id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var raw, result []byte
			var call Call
			if err = rows.Scan(&raw, &result); err != nil {
				rows.Close()
				return err
			}
			raw, err = maskJSON(raw)
			if err == nil {
				err = json.Unmarshal(raw, &call.Call)
			}
			if err != nil {
				rows.Close()
				return err
			}
			if len(result) > 0 {
				result, err = maskJSON(result)
				if err == nil {
					call.Result = new(loop.Result)
					err = json.Unmarshal(result, call.Result)
				}
			}
			if err != nil {
				rows.Close()
				return err
			}
			ep.Calls = append(ep.Calls, call)
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}

		for i := range ep.Calls {
			call := &ep.Calls[i]
			receipt, found, err := authority.RecordedCall(ctx, tx, id, scope.Person, scope.Agent, call.Call.Name, call.Call.ID)
			if err != nil {
				return err
			}
			if found {
				call.EffectKey = receipt.Key
				code := loop.Unknown
				switch receipt.State {
				case tools.Applied:
					code = loop.OK
				case tools.Failed:
					code = loop.Failed
				case tools.Refused:
					code = loop.Denied
				case tools.Cancelled:
					code = loop.Aborted
				case tools.None, tools.Unknown:
				}
				body := receipt.Result.Body
				if len(body) > 0 {
					body, err = maskJSON(body)
					if err != nil {
						return err
					}
				}
				call.Result = &loop.Result{ID: call.Call.ID, Code: code, Output: body}
			}
			if err := callReferences(ctx, tx, scope, *call); err != nil {
				return err
			}
			if !LocalRead(call.Call.Name) && !usable(call.Result) {
				return Exclusion("recorded_result_unavailable")
			}
		}
		// The next person event and corrections are evidence, never new input.
		rows, err = tx.QueryContext(ctx, `SELECT e.id FROM conv_event e WHERE e.conversation_id=? AND e.speaker_kind='person' AND e.person_id=? AND e.rowid>(SELECT rowid FROM conv_event WHERE id=?) ORDER BY e.rowid LIMIT 1`, ep.Conversation, scope.Person, inputID)
		if err != nil {
			return err
		}
		var next []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			next = append(next, id)
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, id := range next {
			e, err := get(id)
			if err != nil {
				var excluded Exclusion
				if errors.As(err, &excluded) {
					continue
				}
				return err
			}
			if e.Origin.Kind == "native" && e.ReplayOf == "" {
				ep.Next = append(ep.Next, eventValue(e))
			}
		}
		return nil
	})
	if err != nil {
		return Episode{}, err
	}
	if s.Persona == nil || s.Project == nil || s.Digest == nil {
		return Episode{}, ErrInvalid
	}
	persona, err := s.Persona(ctx, id, scope.Agent)
	if err != nil || persona.Commit != ep.Evidence.Assembly.PersonaRevision {
		return Episode{}, Exclusion("missing_persona")
	}
	raw, err := json.Marshal(persona.Layers)
	if err != nil {
		return Episode{}, err
	}
	raw, err = maskJSON(raw)
	if err != nil {
		return Episode{}, err
	}
	if err = json.Unmarshal(raw, &ep.Persona); err != nil {
		return Episode{}, err
	}
	ep.Items, err = s.Project(history, ep.Conversation, scope.Agent)
	if err == nil && (ep.Evidence.Assembly.InstructionFormat == "persona-v1" || ep.Evidence.Assembly.InstructionFormat == "persona-tui-v1") {
		if s.Rebuild == nil || s.Skill == nil {
			return Episode{}, Exclusion("missing_assembly")
		}
		for i := range packet {
			c := &packet[i]
			if c.Source.Source == "skill-v1" {
				c.Description, c.Text, err = s.Skill(ctx, strings.TrimPrefix(c.Source.ID, "skill:"), c.Source.Revision)
				if err != nil {
					return Episode{}, err
				}
			}
		}
		ep.Items, err = s.Rebuild(ep, packet, profile)
	}
	if err == nil {
		original, e := Request(ep, nil, "", "", s.Instructions)
		if e != nil {
			return Episode{}, e
		}
		original.Purpose = provider.Turn
		if s.Digest(original) != ep.Evidence.Hash {
			return Episode{}, Exclusion("missing_assembly")
		}
	}

	if err == nil {
		err = s.ValidateEpisode(ctx, ep)
	}
	return ep, err
}

// maskJSON delegates to the existing secret scanner, which preserves JSON
// structure and recognizes both secret keys and nested encoded tool arguments.
func maskJSON(raw []byte) ([]byte, error) {
	if !json.Valid(raw) {
		return nil, ErrInvalid
	}
	masked, err := secrets.Mask(string(raw))
	if err != nil {
		return nil, err
	}
	if !json.Valid([]byte(masked.Text())) {
		return nil, ErrInvalid
	}
	return []byte(masked.Text()), nil
}
