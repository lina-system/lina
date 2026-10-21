// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
)

// OwnerChannel is part of the identity map confirmed before import, never
// inferred from a Honcho peer or message body. Session ids are profile-local.
// Host-backed channels must also match their recorded user and chat identities.
type OwnerChannel struct{ Source, SessionID, UserID, ChatID string }

// SpeakerSources names the rows already matched to one canonical event by the
// import's identity/matching stage. All copies must participate, including ones
// with conflicting roles or sources. HonchoOwner is a claim, not owner proof.
type SpeakerSources struct {
	ID              string
	HostIDs, LCMIDs []int64
	HonchoOwner     bool
}

// SpeakerProfile contains staged, immutable snapshots and one profile's maps.
// Empty paths mean the source was absent. No Honcho schema currently registers
// source proof, so an unmatched Honcho owner claim always remains unproven.
type SpeakerProfile struct {
	Profile, PersonID, StatePath, LCMPath string
	OwnerChannels                         []OwnerChannel
	Events                                []SpeakerSources
}

type SpeakerReport struct {
	Profile    string            `json:"profile"`
	Relabelled int               `json:"relabelled"`
	Unproven   int               `json:"unproven"`
	Events     []ClassifiedEvent `json:"events"`
}

// ClassifiedEvent keeps its decision immutable. Consumers use the classified
// kind, never Honcho's peer label. Raw source bytes remain in staging unchanged.
type ClassifiedEvent struct {
	id, person           string
	kind                 guard.SpeakerKind
	unproven, relabelled bool
}

func (e ClassifiedEvent) ID() string                     { return e.id }
func (e ClassifiedEvent) SpeakerKind() guard.SpeakerKind { return e.kind }
func (e ClassifiedEvent) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID         string            `json:"id"`
		Kind       guard.SpeakerKind `json:"speaker_kind"`
		Person     string            `json:"person_id,omitempty"`
		Unproven   bool              `json:"unproven"`
		Relabelled bool              `json:"relabelled"`
	}{e.id, e.kind, e.person, e.unproven, e.relabelled})
}

// PersonEvidence is the common admission boundary for imported-event Lachesis
// candidates and person-model evidence. It returns no person identity for any
// non-person or unproven event. It does not approve or write a memory item.
type PersonEvidence struct{ EventID, PersonID string }

func (e ClassifiedEvent) PersonEvidence() (PersonEvidence, bool) {
	if e.kind != guard.Person || e.unproven || e.person == "" {
		return PersonEvidence{}, false
	}
	return PersonEvidence{e.id, e.person}, true
}

// ClassifySpeakers reads only provenance columns from the registered schemas.
// It never reads content, prompts, summaries or Honcho text. It writes no canon;
// the ordered import stage retains these decisions with its events and report.
func ClassifySpeakers(ctx context.Context, in SpeakerProfile) (SpeakerReport, error) {
	if in.Profile == "" || in.PersonID == "" {
		return SpeakerReport{}, ErrSnapshot
	}
	owners := map[string]OwnerChannel{}
	for _, o := range in.OwnerChannels {
		if o.SessionID == "" || !interactiveSource(o.Source) {
			return SpeakerReport{}, ErrSnapshot
		}
		if _, exists := owners[o.SessionID]; exists {
			return SpeakerReport{}, ErrSnapshot
		}
		owners[o.SessionID] = o
	}
	state, err := openSpeakerSource(ctx, in.StatePath, HermesState)
	if err != nil {
		return SpeakerReport{}, err
	}
	if state != nil {
		defer state.Close()
	}
	lcm, err := openSpeakerSource(ctx, in.LCMPath, HermesLCM)
	if err != nil {
		return SpeakerReport{}, err
	}
	if lcm != nil {
		defer lcm.Close()
	}
	report := SpeakerReport{Profile: in.Profile, Events: make([]ClassifiedEvent, 0, len(in.Events))}
	seen := map[string]bool{}
	for _, event := range in.Events {
		if event.ID == "" || seen["event:"+event.ID] {
			return SpeakerReport{}, ErrSnapshot
		}
		seen["event:"+event.ID] = true
		var decisions []speakerDecision
		for _, source := range []struct {
			db   *sql.DB
			ids  []int64
			host bool
			name string
		}{{state, event.HostIDs, true, "host"}, {lcm, event.LCMIDs, false, "lcm"}} {
			for _, id := range source.ids {
				key := fmt.Sprintf("%s:%d", source.name, id)
				if source.db == nil || id <= 0 || seen[key] {
					return SpeakerReport{}, ErrSnapshot
				}
				seen[key] = true
				d, err := readSpeaker(ctx, source.db, id, source.host, owners)
				if err != nil {
					return SpeakerReport{}, err
				}
				decisions = append(decisions, d)
			}
		}
		d := combineSpeakers(decisions)
		e := ClassifiedEvent{id: event.ID, kind: d.kind, unproven: d.unproven, relabelled: event.HonchoOwner && d.kind != guard.Person}
		if d.kind == guard.Person {
			e.person = in.PersonID
		}
		if e.relabelled {
			report.Relabelled++
		}
		if e.unproven {
			report.Unproven++
		}
		report.Events = append(report.Events, e)
	}
	return report, nil
}
func openSpeakerSource(ctx context.Context, path string, kind SQLiteSource) (*sql.DB, error) {
	if path == "" {
		return nil, nil
	}
	db, err := canon.OpenSQLiteReader(ctx, path)
	if err != nil {
		return nil, err
	}
	if err = checkSQLite(ctx, db, registeredSQLiteSchemas[kind]); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

type speakerDecision struct {
	kind     guard.SpeakerKind
	unproven bool
}

func unknownSpeaker() speakerDecision { return speakerDecision{guard.Forwarded, true} }

func readSpeaker(ctx context.Context, db *sql.DB, id int64, host bool, owners map[string]OwnerChannel) (speakerDecision, error) {
	var session, role, source, created, user, chat, chatType, config, display, metadata string
	if host {
		err := db.QueryRowContext(ctx, `SELECT m.session_id,m.role,s.source,
   COALESCE(s.created_source,''),COALESCE(s.user_id,''),COALESCE(s.chat_id,''),COALESCE(s.chat_type,''),
   COALESCE(s.model_config,''),COALESCE(m.display_kind,''),COALESCE(m.display_metadata,'')
   FROM messages m JOIN sessions s ON s.id=m.session_id WHERE m.id=?`, id).
			Scan(&session, &role, &source, &created, &user, &chat, &chatType, &config, &display, &metadata)
		if err != nil {
			return speakerDecision{}, err
		}
	} else {
		err := db.QueryRowContext(ctx, `SELECT session_id,role,COALESCE(source,'') FROM messages WHERE store_id=?`, id).Scan(&session, &role, &source)
		if err != nil {
			return speakerDecision{}, err
		}
	}
	// Explicit non-person roles always beat a transport or owner identity.
	switch role {
	case "assistant":
		return speakerDecision{guard.Agent, false}, nil
	case "tool", "function":
		return speakerDecision{guard.Tool, false}, nil
	case "system", "developer":
		return speakerDecision{guard.System, false}, nil
	case "user":
	default:
		return unknownSpeaker(), nil
	}
	var kinds []speakerDecision
	for _, s := range []string{source, created} {
		if s == "" {
			continue
		}
		switch s {
		case "cron", "scheduled", "automation":
			kinds = append(kinds, speakerDecision{guard.Automation, false})
		case "subagent", "agent":
			kinds = append(kinds, speakerDecision{guard.Agent, false})
		case "background_notice", "system":
			kinds = append(kinds, speakerDecision{guard.System, false})
		case "tool":
			kinds = append(kinds, speakerDecision{guard.Tool, false})
		case "bot_relay", "forwarded":
			kinds = append(kinds, speakerDecision{guard.Forwarded, false})
		default:
			if !interactiveSource(s) {
				kinds = append(kinds, unknownSpeaker())
			}
		}
	}
	if host && config != "" {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(config), &fields); err != nil || fields == nil {
			kinds = append(kinds, unknownSpeaker())
		} else if value, ok := fields["_delegate_from"]; ok && string(value) != "null" {
			kinds = append(kinds, speakerDecision{guard.Agent, false})
		}
	}
	// Display payloads have no registered provenance vocabulary. Their presence
	// cannot be silently discarded to elevate a user-role row to person.
	if display != "" || metadata != "" {
		kinds = append(kinds, unknownSpeaker())
	}
	if len(kinds) > 0 {
		return combineSpeakers(kinds), nil
	}
	owner, ok := owners[session]
	if !ok || owner.Source != source {
		if host && interactiveSource(source) && user != "" {
			return speakerDecision{guard.ExternalParticipant, false}, nil
		}
		return unknownSpeaker(), nil
	}
	if host && (owner.UserID != user || owner.ChatID != chat) {
		return speakerDecision{guard.ExternalParticipant, false}, nil
	}
	if host && source != "cli" && (user == "" || chat == "" || (chatType != "private" && chatType != "dm")) {
		return unknownSpeaker(), nil
	}
	return speakerDecision{guard.Person, false}, nil
}
func interactiveSource(source string) bool {
	switch source {
	case "cli", "telegram", "discord", "slack", "signal", "whatsapp":
		return true
	}
	return false
}

// Non-person evidence vetoes person. Incompatible non-person kinds fall back
// to unproven forwarded, rather than choosing arbitrarily between sources.
func combineSpeakers(ds []speakerDecision) speakerDecision {
	if len(ds) == 0 {
		return unknownSpeaker()
	}
	result := speakerDecision{guard.Person, false}
	unproven := false
	for _, d := range ds {
		if d.unproven {
			unproven = true
			continue
		}
		if d.kind == guard.Person {
			continue
		}
		if result.kind != guard.Person && result.kind != d.kind {
			return unknownSpeaker()
		}
		result = d
	}
	// A definite non-person source needs no owner proof. Unknown provenance
	// prevents person attribution but does not erase a known automated source.
	if result.kind == guard.Person && unproven {
		return unknownSpeaker()
	}
	return result
}
