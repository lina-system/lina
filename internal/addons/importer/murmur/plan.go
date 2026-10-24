// SPDX-License-Identifier: Apache-2.0
package murmur

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// Profile is the person's confirmed input for one run: who they are in each
// murmur source, which data home this is, and whether learning from the imported
// messages is turned on.
type Profile struct {
	// Person is the person id the records belong to.
	Person string
	// Instance names this data home in the legacy reference. A different home
	// keeps its own namespace.
	Instance string
	// Own maps a murmur source (kakao, discord, imessage) to the person's own
	// author id in it. iMessage's own-message flag proves the person without it.
	Own map[string]string
	// Learning is the person's choice in the import card.
	Learning bool
}

// Request is one run over a stopped data home.
type Request struct {
	Home    string
	Staging string
	Profile Profile
	// MaxSpoolBytes bounds the spool a run accepts; zero uses MaxSpoolBytes.
	MaxSpoolBytes int64
}

// MaxSpoolBytes is the spool size a run accepts by default. A larger home is
// reported rather than streamed into memory.
const MaxSpoolBytes = 1 << 30

// ErrMissingOwnMap reports a source whose messages the run would otherwise file
// with no proven speaker. The person confirms their own author id per source;
// iMessage proves it by its own-message flag and needs no map.
var ErrMissingOwnMap = errors.New("murmur_own_map_required")

// Plan is one immutable preparation of a snapshot. Its rehearsal and its import
// read the same copy, so the report a person approves is the work that runs.
type Plan struct {
	profile  Profile
	snapshot snapshot
	store    *store
	rooms    []*room
	platform map[string]string // room key -> LINA platform
	report   Report
	closed   bool
}

// Chat is one chat the run hands to admission.
type Chat struct {
	Platform, Account, Room, Name, Kind string
	FirstMessage                        time.Time
	Records                             []messenger.ImportRecord
	Deletions                           []messenger.ImportDeletion
}

// Prepare copies the data home into staging, gates its format and builds the
// plan. It writes nothing to canon and is safe to call twice for one run.
func Prepare(ctx context.Context, request Request) (*Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Profile.Person == "" {
		return nil, errors.New("a confirmed person id is required")
	}
	if request.Profile.Instance == "" {
		request.Profile.Instance = "default"
	}
	if strings.ContainsRune(request.Profile.Instance, 0) {
		return nil, errors.New("the data home identity contains a NUL byte")
	}
	limit := request.MaxSpoolBytes
	if limit <= 0 {
		limit = MaxSpoolBytes
	}
	copied, err := copySnapshot(request.Home, request.Staging)
	if err != nil {
		return nil, err
	}
	plan := &Plan{profile: request.Profile, snapshot: copied, platform: map[string]string{}}
	spool, err := os.Open(copied.spool)
	if err != nil {
		return nil, err
	}
	defer spool.Close()
	parsed, err := parseSpool(spool, limit)
	if err != nil {
		return nil, err
	}
	configRaw, err := os.ReadFile(copied.config)
	if err != nil {
		return nil, err
	}
	configRooms, err := parseConfig(configRaw)
	if err != nil {
		return nil, err
	}
	plan.store = parsed
	for _, key := range sortedRoomKeys(parsed.rooms) {
		entry := parsed.rooms[key]
		if metadata, ok := configRooms[entry.Source+"\x00"+entry.RoomID]; ok {
			if metadata.Name != "" {
				entry.Name = metadata.Name
			}
			entry.Business = metadata.Business
		}
		entry.Rows = entry.Rows[:len(entry.Rows):len(entry.Rows)]
		plan.rooms = append(plan.rooms, entry)
		plan.platform[key] = Platforms[entry.Source]
	}
	if err := plan.checkOwnMaps(); err != nil {
		return nil, err
	}
	plan.report = plan.buildReport()
	return plan, nil
}

// Close removes the run's staged copies. Nothing in the data home is touched.
func (p *Plan) Close() error {
	if p == nil || p.closed {
		return nil
	}
	p.closed = true
	var err error
	for _, path := range []string{p.snapshot.spool, p.snapshot.config} {
		if path != "" {
			err = errors.Join(err, os.Remove(path))
		}
	}
	return err
}

// Report is the rehearsal: what a run would do, with no canon write. It carries
// counts, digests and opaque keys, never message text.
type Report struct {
	Batch       string         `json:"batch"`
	DataHome    string         `json:"data_home"`
	Person      string         `json:"person"`
	SpoolHash   string         `json:"spool_sha256"`
	ConfigHash  string         `json:"config_sha256"`
	Sources     []SourceCounts `json:"sources"`
	Rooms       []RoomReport   `json:"rooms"`
	Totals      Counts         `json:"totals"`
	MaskedSpans int            `json:"masked_spans"`
	Attachments int            `json:"attachments"`
	Learning    Learning       `json:"learning"`
	// LinkedSenders counts the other senders the identity rules matched to a
	// people record. No matcher exists yet, so every sender is unmatched and
	// stays search-only.
	LinkedSenders  int    `json:"linked_senders"`
	SenderIdentity string `json:"sender_identity"`
	// NotImported names what the source holds and LINA does not take.
	NotImported []string `json:"not_imported"`
}

// Counts are the message counts of a source or of the whole store.
type Counts struct {
	Messages       int `json:"messages"`
	PersonMessages int `json:"person_messages"`
	Senders        int `json:"senders"`
	Rooms          int `json:"rooms"`
	Edits          int `json:"edits"`
	Deletions      int `json:"deletions"`
}

// SourceCounts is one murmur source in the report.
type SourceCounts struct {
	Source   string `json:"source"`
	Platform string `json:"platform"`
	Accounts int    `json:"accounts"`
	Counts
}

// RoomReport is one chat: the counts a person checks and the reconciliation
// result of the run.
type RoomReport struct {
	Platform, Account, Room, Name, Kind string
	Messages, PersonMessages, Senders   int
	UIDDigest                           string `json:"uid_sha256"`
	// PathDerivedRoom reports a Discord room whose id came from an export
	// file's path rather than from the message's own channel id. Those ids are
	// not the platform's, so a later Discord connection does not meet them.
	PathDerivedRoom                 bool   `json:"path_derived_room,omitempty"`
	ReadDigest                      string `json:"read_uid_sha256,omitempty"`
	Reconciled                      bool   `json:"reconciled"`
	Added, Updated, Linked, Deleted int
	Unchanged                       int
	Conflicts                       []messenger.ImportConflict `json:"conflicts,omitempty"`
}

// Learning is the import card's statement about learning from the imported
// messages: the count it covers and an estimated model cost.
type Learning struct {
	Enabled        bool   `json:"enabled"`
	PersonMessages int    `json:"person_messages"`
	Calls          int    `json:"estimated_calls"`
	InputTokens    int    `json:"estimated_input_tokens"`
	OutputTokens   int    `json:"estimated_output_tokens"`
	Basis          string `json:"basis"`
}

// LearningBatch and LearningBatchTokens are the starting estimate the card
// states: one extraction call per this many of the person's messages, with a
// fixed input and output budget per call. The money price is the model's own
// published price at the time of the run and is not fixed here.
const (
	LearningBatch        = 40
	LearningInputBudget  = 4000
	LearningOutputBudget = 700
)

func (p *Plan) buildReport() Report {
	report := Report{
		Batch:          "",
		DataHome:       p.profile.Instance,
		Person:         p.profile.Person,
		SpoolHash:      p.snapshot.spoolHash,
		ConfigHash:     p.snapshot.configHash,
		SenderIdentity: "no sender identity exists in the store; every author other than the person is an external participant until a people link is made",
		NotImported: []string{
			"the derived database, vectors and model cache are not read",
			"attachment bytes stay in the source; only their count is reported",
			"observation rows, cursors and integrity state are collector bookkeeping, not messages",
			"murmur's own embeddings and search indexes are not imported",
		},
	}
	sources := map[string]*SourceCounts{}
	accounts := map[string]map[string]bool{}
	for _, room := range p.rooms {
		platform := p.platform[roomKeyOf(room.Source, room.Account, room.RoomID)]
		counts := &SourceCounts{Source: room.Source, Platform: platform}
		if existing, ok := sources[room.Source]; ok {
			counts = existing
		} else {
			sources[room.Source] = counts
		}
		if accounts[room.Source] == nil {
			accounts[room.Source] = map[string]bool{}
		}
		accounts[room.Source][room.Account] = true
		entry, chats := p.roomReport(room)
		report.Rooms = append(report.Rooms, entry)
		report.Totals.Messages += chats.Messages
		report.Totals.PersonMessages += chats.PersonMessages
		report.Totals.Senders += chats.Senders
		report.Totals.Rooms++
		report.Totals.Edits += chats.Edits
		report.Totals.Deletions += chats.Deletions
		counts.Messages += chats.Messages
		counts.PersonMessages += chats.PersonMessages
		counts.Senders += chats.Senders
		counts.Rooms++
		counts.Edits += chats.Edits
		counts.Deletions += chats.Deletions
		report.MaskedSpans += chats.Masked
		report.Attachments += chats.Attachments
	}
	for length := range accounts {
		sources[length].Accounts = len(accounts[length])
	}
	for _, source := range []string{"kakao", "discord", "imessage"} {
		if counts, ok := sources[source]; ok {
			report.Sources = append(report.Sources, *counts)
		}
	}
	report.Learning = learningEstimate(p.profile.Learning, report.Totals.PersonMessages)
	return report
}

type roomCounts struct {
	RoomReport
	Masked      int
	Attachments int
	Edits       int
	Deletions   int
}

// roomReport counts one chat and states murmur's own digest for it.
func (p *Plan) roomReport(room *room) (RoomReport, roomCounts) {
	senders := map[string]bool{}
	entry := RoomReport{
		Platform: p.platform[roomKeyOf(room.Source, room.Account, room.RoomID)],
		Account:  room.Account, Room: room.RoomID, Name: room.Name, Kind: chatKind(room),
		UIDDigest:       p.store.digests[roomKeyOf(room.Source, room.Account, room.RoomID)],
		PathDerivedRoom: pathDerived(room),
	}
	var counts roomCounts
	for _, value := range room.Rows {
		entry.Messages++
		if value.Deleted {
			counts.Deletions++
		}
		if !value.Edited.IsZero() {
			counts.Edits++
		}
		senders[value.AuthorID] = true
		counts.Attachments += value.Attachments
		counts.Masked += len(maskSpans(value.Text))
		if route(p.profile, value) == messenger.RoutePerson {
			entry.PersonMessages++
		}
	}
	entry.Senders = len(senders)
	counts.RoomReport = entry
	return entry, counts
}

// Rehearse returns the report of what the run would write. It is stable across
// calls and is the same report the import retains.
func (p *Plan) Rehearse() Report {
	report := p.report
	report.Sources = append([]SourceCounts(nil), p.report.Sources...)
	report.Rooms = append([]RoomReport(nil), p.report.Rooms...)
	report.NotImported = append([]string(nil), p.report.NotImported...)
	for i := range report.Rooms {
		report.Rooms[i].Conflicts = append([]messenger.ImportConflict(nil), p.report.Rooms[i].Conflicts...)
	}
	return report
}

func learningEstimate(enabled bool, messages int) Learning {
	calls := int(math.Ceil(float64(messages) / LearningBatch))
	return Learning{
		Enabled: enabled, PersonMessages: messages, Calls: calls,
		InputTokens: calls * LearningInputBudget, OutputTokens: calls * LearningOutputBudget,
		Basis: fmt.Sprintf("one extraction call per up to %d of the person's messages, about %d input and %d output tokens per call, at the model's published price",
			LearningBatch, LearningInputBudget, LearningOutputBudget),
	}
}

// chatKind names the chat's kind. murmur records only its name and a business
// flag, so a chat it does not mark as business is a group chat of its source;
// the adapter never claims a direct chat it cannot prove.
// checkOwnMaps refuses a run that would guess who spoke. A source other than
// iMessage must have the person's confirmed author id before anything is
// written, because an unconfirmed map files the person's own words as an
// external participant and learning never sees them.
func (p *Plan) checkOwnMaps() error {
	present := map[string]bool{}
	for _, room := range p.rooms {
		present[room.Source] = true
	}
	var missing []string
	for _, source := range []string{"kakao", "discord"} {
		if present[source] && p.profile.Own[source] == "" {
			missing = append(missing, source)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: confirm your own author id for %s", ErrMissingOwnMap, strings.Join(missing, ", "))
	}
	return nil
}

func chatKind(room *room) string {
	if room.Business {
		return "channel"
	}
	return "group"
}

// pathDerived reports a Discord room whose id is the export file's stem rather
// than a channel id the platform issued. murmur names such a room
// "<guild>/<stem>", so the room id equals the last part of its name.
func pathDerived(room *room) bool {
	if room.Source != "discord" || room.Name == "" || room.Name == room.RoomID {
		return false
	}
	return strings.HasSuffix(room.Name, "/"+room.RoomID)
}

// route decides who spoke from the confirmed map and iMessage's own-message
// flag. Text and display names never decide it.
func route(profile Profile, value *row) messenger.Route {
	if value.Source == "imessage" && value.AuthorID == "me" {
		return messenger.RoutePerson
	}
	if own := profile.Own[value.Source]; own != "" && value.AuthorID == own {
		return messenger.RoutePerson
	}
	return messenger.RouteParticipant
}

func maskSpans(text string) []secrets.Span {
	masked, err := secrets.Mask(text)
	if err != nil {
		return nil
	}
	return masked.Spans()
}

// Chats returns the chats in deterministic order with their records in source
// order. Deletions carry the source row they tombstone.
func (p *Plan) Chats() []Chat {
	chats := make([]Chat, 0, len(p.rooms))
	for _, room := range p.rooms {
		native := map[string]string{}
		for _, value := range room.Rows {
			native[value.UID] = value.NativeID
		}
		chat := Chat{
			Platform: p.platform[roomKeyOf(room.Source, room.Account, room.RoomID)],
			Account:  room.Account, Room: room.RoomID, Name: room.Name, Kind: chatKind(room),
			FirstMessage: room.FirstMessage,
		}
		for _, value := range room.Rows {
			reference := messenger.ImportRecord{
				Message: messenger.Message{
					ID: value.NativeID, Route: route(p.profile, value),
					Author: messenger.Author{ID: value.AuthorID, Name: value.AuthorName},
					At:     value.At, Edited: value.Edited, Text: value.Text,
					ReplyTo: native[value.ReplyTo], Thread: native[value.Thread],
				},
				Kind: "uid", Key: value.UID, Hash: value.Hash, Ingest: value.Ingest, ObservedAt: value.Observed,
			}
			if value.Deleted {
				chat.Deletions = append(chat.Deletions, messenger.ImportDeletion{
					Message: value.NativeID, At: value.At, Kind: "uid", Key: value.UID,
					Hash: value.Hash, Ingest: value.Ingest, ObservedAt: value.Observed,
				})
				continue
			}
			chat.Records = append(chat.Records, reference)
		}
		chats = append(chats, chat)
	}
	return chats
}
