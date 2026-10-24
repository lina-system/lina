// SPDX-License-Identifier: Apache-2.0

// Package murmur imports a stopped murmur data home into LINA's messenger
// records: the spool and config the person hands over, and nothing else. It
// implements the memory importer add-on contract for one source
// (conversation-and-memory.md, "Memory importer add-ons" and "Importing a
// murmur store").
//
// The package writes nothing itself: a run builds a plan from a verified copy of
// the source, reports a rehearsal, and then hands each chat to messenger
// admission on Core's canon. Its own reference, (murmur, data home, uid, uid),
// makes a second run add nothing.
package murmur

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

var (
	// ErrRefused reports a source whose format this adapter does not accept.
	ErrRefused = errors.New("murmur_source_refused")
	// ErrIncompleteTail reports a spool whose last event was not fully written.
	ErrIncompleteTail = errors.New("murmur_spool_incomplete")
)

// Platforms are the murmur sources and the LINA platforms they become.
var Platforms = map[string]string{
	"kakao":    "kakaotalk",
	"discord":  "discord",
	"imessage": "imessage",
}

const adapterName = "murmur"

// murmurModelFields is the message shape of murmur's model, which its spool
// carries. Anything else is a different format.
var murmurModelFields = map[string]bool{
	"source": true, "account": true, "room_id": true, "native_id": true,
	"uid": true, "author_id": true, "author_name": true, "ts": true,
	"edited_ts": true, "deleted": true, "reply_to_uid": true,
	"thread_root_uid": true, "text": true, "attachments": true, "raw": true,
	"ingest_path": true, "privacy_class": true, "filled_from": true,
	"room_name": true,
}

var observationFields = map[string]bool{
	"source": true, "room_id": true, "ingest_path": true, "collected_at": true,
	"coverage_since": true, "coverage_until": true, "first_message_ts": true,
}

type spoolMessage struct {
	Source      string          `json:"source"`
	Account     string          `json:"account"`
	RoomID      string          `json:"room_id"`
	NativeID    string          `json:"native_id"`
	UID         string          `json:"uid"`
	AuthorID    string          `json:"author_id"`
	AuthorName  string          `json:"author_name"`
	TS          string          `json:"ts"`
	EditedTS    *string         `json:"edited_ts"`
	Deleted     int             `json:"deleted"`
	ReplyToUID  *string         `json:"reply_to_uid"`
	ThreadUID   *string         `json:"thread_root_uid"`
	Text        string          `json:"text"`
	Attachments json.RawMessage `json:"attachments"`
	Raw         json.RawMessage `json:"raw"`
	IngestPath  string          `json:"ingest_path"`
	Privacy     string          `json:"privacy_class"`
	FilledFrom  *string         `json:"filled_from"`
	RoomName    string          `json:"room_name"`
}

type spoolObservation struct {
	Source        string  `json:"source"`
	RoomID        string  `json:"room_id"`
	IngestPath    string  `json:"ingest_path"`
	CollectedAt   *string `json:"collected_at"`
	CoverageSince *string `json:"coverage_since"`
	CoverageUntil *string `json:"coverage_until"`
	FirstMessage  *string `json:"first_message_ts"`
}

type spoolEvent struct {
	Version      *int               `json:"version"`
	EventID      string             `json:"event_id"`
	IngestedAt   string             `json:"ingested_at"`
	Messages     []json.RawMessage  `json:"messages"`
	Observations []spoolObservation `json:"observations"`
	Checkpoint   json.RawMessage    `json:"sync_checkpoint"`
}

// row is one source row after murmur's own merge rule: an archive row wins over
// the live row of one key, and within one path the later observation wins.
type row struct {
	Source, Account, RoomID, NativeID, UID string
	AuthorID, AuthorName                   string
	Text                                   string
	At, Edited, Observed                   time.Time
	Deleted                                bool
	ReplyTo, Thread                        string
	Ingest                                 string
	RoomName                               string
	Attachments                            int
	Hash                                   string
}

// room groups the rows of one chat with the metadata the person's config gives.
type room struct {
	Source, Account, RoomID, Name string
	Business                      bool
	FirstMessage                  time.Time
	Rows                          []*row
}

// store is the adapter's view of one stopped data home.
type store struct {
	rows    map[string]*row // key: source\x00account\x00room\x00native
	rooms   map[string]*room
	digests map[string]string // room key -> murmur manifest digest
	spool   string            // spool SHA-256
	config  string            // config SHA-256
}

type roomKey struct{ source, account, room string }

func keyOf(source, account, room, native string) string {
	return strings.Join([]string{source, account, room, native}, "\x00")
}

func roomKeyOf(source, account, room string) string {
	return strings.Join([]string{source, account, room}, "\x00")
}

// uid is murmur's own identity of one message: the SHA-256 of its source, room
// and native id as a compact JSON array.
func uid(source, room, native string) string {
	encoded, _ := json.Marshal([]string{source, room, native})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// manifest is murmur's room digest: the SHA-256 of each sorted UID followed by a
// newline, exactly as its own manifest tool writes it.
func manifest(uids []string) string {
	sorted := append([]string(nil), uids...)
	sort.Strings(sorted)
	sum := sha256.New()
	for _, id := range sorted {
		sum.Write([]byte(id))
		sum.Write([]byte("\n"))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// rowHash hashes one canonical source row: its typed fields, with sorted object
// keys, and never a SELECT rendering.
func rowHash(r *row) string {
	encoded, _ := json.Marshal(struct {
		Source, Account, Room, Native, UID, Author, AuthorName, Text, Ingest, RoomName string
		At, Edited, Observed                                                           string
		Deleted                                                                        bool
		ReplyTo, Thread                                                                string
	}{r.Source, r.Account, r.RoomID, r.NativeID, r.UID, r.AuthorID, r.AuthorName, r.Text, r.Ingest, r.RoomName,
		format(r.At), format(r.Edited), format(r.Observed), r.Deleted, r.ReplyTo, r.Thread})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func format(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano)
}

// parseSpool reads the events of a stopped spool. It refuses the whole store on
// any difference from the registered format; the first byte offset that fails is
// named, so a torn tail is reported rather than half imported.
func parseSpool(reader io.Reader, maxBytes int64) (*store, error) {
	s := &store{rows: map[string]*row{}, rooms: map[string]*room{}, digests: map[string]string{}}
	buffered := bufio.NewReaderSize(reader, 1<<20)
	offset := int64(0)
	line := 0
	for {
		raw, err := buffered.ReadBytes('\n')
		if len(raw) > 0 {
			offset += int64(len(raw))
			line++
			if raw[len(raw)-1] != '\n' {
				return nil, fmt.Errorf("%w: byte %d: the last event is not a complete line", ErrIncompleteTail, offset-int64(len(raw)))
			}
			if err := s.apply(raw[:len(raw)-1], offset-int64(len(raw)), line); err != nil {
				return nil, err
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if maxBytes > 0 && offset > maxBytes {
			return nil, fmt.Errorf("%w: the spool passes the %d byte limit", ErrRefused, maxBytes)
		}
	}
	s.finish()
	return s, nil
}

func (s *store) apply(line []byte, at int64, number int) error {
	var event spoolEvent
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return fmt.Errorf("%w: byte %d (event %d): %v", ErrRefused, at, number, err)
	}
	if event.Version == nil || *event.Version != 1 {
		return fmt.Errorf("%w: byte %d: spool event version %v, want 1", ErrRefused, at, event.Version)
	}
	if event.EventID == "" || event.IngestedAt == "" {
		return fmt.Errorf("%w: byte %d: event id and ingest time are required", ErrRefused, at)
	}
	ingested, err := parseTime(event.IngestedAt)
	if err != nil {
		return fmt.Errorf("%w: byte %d: ingested_at: %v", ErrRefused, at, err)
	}
	observed := map[string]*spoolObservation{}
	for i := range event.Observations {
		observation := event.Observations[i]
		if err := checkFields(&observation, observationFields); err != nil {
			return fmt.Errorf("%w: byte %d: observation: %v", ErrRefused, at, err)
		}
		if _, ok := Platforms[observation.Source]; !ok {
			return fmt.Errorf("%w: byte %d: unknown source %q", ErrRefused, at, observation.Source)
		}
		if observation.RoomID == "" || !validIngest(observation.IngestPath) {
			return fmt.Errorf("%w: byte %d: observation needs a room and a known ingest path", ErrRefused, at)
		}
		key := observation.Source + "\x00" + observation.RoomID
		observed[key] = &event.Observations[i]
	}
	for index := range event.Messages {
		raw := event.Messages[index]
		var message spoolMessage
		if err := decodeInto(raw, &message); err != nil {
			return fmt.Errorf("%w: byte %d: message %d: %v", ErrRefused, at, index+1, err)
		}
		if err := checkMessage(message); err != nil {
			return fmt.Errorf("%w: byte %d: message %d: %v", ErrRefused, at, index+1, err)
		}
		if message.UID != uid(message.Source, message.RoomID, message.NativeID) {
			return fmt.Errorf("%w: byte %d: message %d: uid does not match its source, room and native id", ErrRefused, at, index+1)
		}
		ts, err := parseTime(message.TS)
		if err != nil {
			return fmt.Errorf("%w: byte %d: message %d: ts: %v", ErrRefused, at, index+1, err)
		}
		var edited time.Time
		if message.EditedTS != nil {
			if edited, err = parseTime(*message.EditedTS); err != nil {
				return fmt.Errorf("%w: byte %d: message %d: edited_ts: %v", ErrRefused, at, index+1, err)
			}
		}
		observation, ok := observed[message.Source+"\x00"+message.RoomID]
		if !ok {
			return fmt.Errorf("%w: byte %d: message %d: its room has no observation", ErrRefused, at, index+1)
		}
		collected := ingested
		if observation.CollectedAt != nil {
			if collected, err = parseTime(*observation.CollectedAt); err != nil {
				return fmt.Errorf("%w: byte %d: observation collected_at: %v", ErrRefused, at, err)
			}
		}
		value := &row{
			Source: message.Source, Account: message.Account, RoomID: message.RoomID, NativeID: message.NativeID,
			UID: message.UID, AuthorID: message.AuthorID, AuthorName: message.AuthorName, Text: message.Text,
			At: ts, Edited: edited, Observed: collected, Deleted: message.Deleted != 0, Ingest: message.IngestPath,
			RoomName: message.RoomName, Attachments: attachmentCount(message.Attachments),
		}
		if message.ReplyToUID != nil {
			value.ReplyTo = *message.ReplyToUID
		}
		if message.ThreadUID != nil {
			value.Thread = *message.ThreadUID
		}
		if value.Deleted && (message.Deleted != 1) {
			return fmt.Errorf("%w: byte %d: message %d: deleted must be 0 or 1", ErrRefused, at, index+1)
		}
		key := keyOf(value.Source, value.Account, value.RoomID, value.NativeID)
		if previous, seen := s.rows[key]; seen && !wins(value, previous) {
			continue
		}
		s.rows[key] = value
		roomID := roomKeyOf(value.Source, value.Account, value.RoomID)
		metadata, seen := s.rooms[roomID]
		if !seen {
			metadata = &room{Source: value.Source, Account: value.Account, RoomID: value.RoomID}
			s.rooms[roomID] = metadata
		}
		if metadata.Name == "" {
			metadata.Name = value.RoomName
		}
		if metadata.FirstMessage.IsZero() || ts.Before(metadata.FirstMessage) {
			metadata.FirstMessage = ts
		}
	}
	if len(event.Checkpoint) > 0 {
		// A sync checkpoint describes a connected collector's own cursor. It is
		// not a memory record and carries no message content.
		var checkpoint map[string]json.RawMessage
		if err := json.Unmarshal(event.Checkpoint, &checkpoint); err != nil {
			return fmt.Errorf("%w: byte %d: sync_checkpoint: %v", ErrRefused, at, err)
		}
	}
	return nil
}

// wins applies murmur's own merge rule: an archive row wins over a live row of
// the same key, and within one path the later observation wins.
func wins(candidate, current *row) bool {
	if candidate.Ingest == "archive" && current.Ingest != "archive" {
		return true
	}
	if candidate.Ingest != "archive" && current.Ingest == "archive" {
		return false
	}
	return !candidate.Observed.Before(current.Observed)
}

func (s *store) finish() {
	for key, value := range s.rows {
		value.Hash = rowHash(value)
		s.rows[key] = value
		room := s.rooms[roomKeyOf(value.Source, value.Account, value.RoomID)]
		room.Rows = append(room.Rows, value)
	}
	for _, room := range s.rooms {
		sort.Slice(room.Rows, func(i, j int) bool {
			if !room.Rows[i].At.Equal(room.Rows[j].At) {
				return room.Rows[i].At.Before(room.Rows[j].At)
			}
			return room.Rows[i].NativeID < room.Rows[j].NativeID
		})
		var uids []string
		for _, value := range room.Rows {
			uids = append(uids, value.UID)
		}
		s.digests[roomKeyOf(room.Source, room.Account, room.RoomID)] = manifest(uids)
	}
}

func checkMessage(message spoolMessage) error {
	if message.Source == "" || message.Account == "" || message.RoomID == "" || message.NativeID == "" ||
		message.AuthorID == "" || message.UID == "" {
		return errors.New("source, account, room, native id, author and uid are required")
	}
	if _, ok := Platforms[message.Source]; !ok {
		return fmt.Errorf("unknown source %q", message.Source)
	}
	if !validIngest(message.IngestPath) {
		return fmt.Errorf("unknown ingest path %q", message.IngestPath)
	}
	if message.TS == "" {
		return errors.New("ts is required")
	}
	return nil
}

func validIngest(path string) bool {
	switch path {
	case "archive", "live", "export":
		return true
	}
	return false
}

// checkFields refuses an observation with a field this adapter does not know, so
// a changed source format is named instead of half read.
func checkFields(target any, known map[string]bool) error {
	encoded, err := json.Marshal(target)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return err
	}
	for name := range fields {
		if !known[name] {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	return nil
}

// decodeInto decodes one message strictly: unknown fields are a difference from
// the registered format, not something to skip.
func decodeInto(raw []byte, target *spoolMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for name := range fields {
		if !murmurModelFields[name] {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func attachmentCount(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		return len(list)
	}
	return 0
}

func parseTime(text string) (time.Time, error) {
	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, err
	}
	return at.UTC(), nil
}
