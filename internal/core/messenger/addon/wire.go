// SPDX-License-Identifier: Apache-2.0

// The wire frames the add-on emits, checked strictly. A field Core does not
// know, a missing identifier or a time it cannot parse is ErrUpstreamChanged:
// the batch is refused and the source keeps its cursor.
package addon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// maxLine bounds one frame in each direction. The add-on applies the same
// bound to what it reads.
const maxLine = 8 << 20

type wireResponse struct {
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

type wireEvent struct {
	Event    string        `json:"event"`
	Chat     string        `json:"chat"`
	Message  *wireMessage  `json:"message"`
	Deletion *wireDeletion `json:"deletion"`
	Error    string        `json:"error"`
}

// An "ended" frame closes a live subscription the add-on did not stop on
// request and names the cause's code. It carries nothing else.
func (e wireEvent) checkEnded() error {
	if e.Message != nil || e.Deletion != nil || e.Error == "" {
		return fmt.Errorf("%w: live end", ErrUpstreamChanged)
	}
	return nil
}

// liveEnded is the error Live returns for an ended subscription. A session the
// platform revoked needs sign-in; any other cause is the add-on's code.
func liveEnded(cause string) error {
	if cause == "needs_sign_in" {
		return errors.Join(ErrLiveEnded, messenger.ErrNeedsSignIn)
	}
	return fmt.Errorf("%w: %s", ErrLiveEnded, cause)
}

func (e wireEvent) event() (messenger.Event, error) {
	if !validIdentifier(e.Chat) {
		return messenger.Event{}, fmt.Errorf("%w: live event chat", ErrUpstreamChanged)
	}
	if e.Error != "" {
		return messenger.Event{}, fmt.Errorf("%w: live event error", ErrUpstreamChanged)
	}
	switch e.Event {
	case "message":
		if e.Message == nil || e.Deletion != nil {
			return messenger.Event{}, fmt.Errorf("%w: live message", ErrUpstreamChanged)
		}
		message, err := e.Message.message()
		if err != nil {
			return messenger.Event{}, err
		}
		return messenger.Event{Chat: e.Chat, Message: &message}, nil
	case "deletion":
		if e.Deletion == nil || e.Message != nil {
			return messenger.Event{}, fmt.Errorf("%w: live deletion", ErrUpstreamChanged)
		}
		deletion, err := e.Deletion.deletion()
		if err != nil {
			return messenger.Event{}, err
		}
		return messenger.Event{Chat: e.Chat, Deletion: &deletion}, nil
	}
	return messenger.Event{}, fmt.Errorf("%w: live event %q", ErrUpstreamChanged, e.Event)
}

type wireAuthor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (a wireAuthor) author() (messenger.Author, error) {
	if !validIdentifier(a.ID) {
		return messenger.Author{}, fmt.Errorf("%w: author id", ErrUpstreamChanged)
	}
	return messenger.Author{ID: a.ID, Name: a.Name}, nil
}

type wireAttachment struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MIMEType string `json:"mimeType"`
	Size     int64  `json:"size"`
}

type wireMessage struct {
	ID               string           `json:"id"`
	Author           wireAuthor       `json:"author"`
	Route            string           `json:"route"`
	At               string           `json:"at"`
	Edited           string           `json:"edited"`
	Text             string           `json:"text"`
	DecryptionFailed bool             `json:"decryptionFailed"`
	ReplyTo          string           `json:"replyTo"`
	Thread           string           `json:"thread"`
	Attachments      []wireAttachment `json:"attachments"`
}

func (m wireMessage) message() (messenger.Message, error) {
	if !validIdentifier(m.ID) || m.At == "" {
		return messenger.Message{}, fmt.Errorf("%w: message id", ErrUpstreamChanged)
	}
	author, err := m.Author.author()
	if err != nil {
		return messenger.Message{}, err
	}
	route := messenger.Route(m.Route)
	if !route.Valid() {
		return messenger.Message{}, fmt.Errorf("%w: route %q", ErrUpstreamChanged, m.Route)
	}
	if m.DecryptionFailed && m.Text != "" {
		return messenger.Message{}, fmt.Errorf("%w: decryption flag with text", ErrUpstreamChanged)
	}
	at, err := wireTime(m.At)
	if err != nil {
		return messenger.Message{}, err
	}
	edited, err := wireTime(m.Edited)
	if err != nil {
		return messenger.Message{}, err
	}
	attachments := make([]messenger.Attachment, 0, len(m.Attachments))
	for _, value := range m.Attachments {
		if !validIdentifier(value.ID) || value.Size < 0 {
			return messenger.Message{}, fmt.Errorf("%w: attachment", ErrUpstreamChanged)
		}
		attachments = append(attachments, messenger.Attachment{ID: value.ID, Name: value.Name, MIMEType: value.MIMEType, Size: value.Size})
	}
	return messenger.Message{
		ID: m.ID, Author: author, Route: route, At: at, Edited: edited,
		Text: m.Text, DecryptionFailed: m.DecryptionFailed, ReplyTo: m.ReplyTo, Thread: m.Thread, Attachments: attachments,
	}, nil
}

type wireDeletion struct {
	Message string `json:"message"`
	At      string `json:"at"`
}

func (d wireDeletion) deletion() (messenger.Deletion, error) {
	if !validIdentifier(d.Message) || d.At == "" {
		return messenger.Deletion{}, fmt.Errorf("%w: deletion", ErrUpstreamChanged)
	}
	at, err := wireTime(d.At)
	if err != nil {
		return messenger.Deletion{}, err
	}
	return messenger.Deletion{Message: d.Message, At: at}, nil
}

type wireChat struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	FirstMessageAt string `json:"firstMessageAt"`
	// LastMessageID is present for a platform that lists the newest message.
	LastMessageID string `json:"lastMessageId"`
}

func (c wireChat) chat() (messenger.Chat, error) {
	if !validIdentifier(c.ID) {
		return messenger.Chat{}, fmt.Errorf("%w: chat id", ErrUpstreamChanged)
	}
	switch c.Kind {
	case "direct", "group", "channel", "thread":
	default:
		return messenger.Chat{}, fmt.Errorf("%w: chat kind %q", ErrUpstreamChanged, c.Kind)
	}
	first, err := wireTime(c.FirstMessageAt)
	if err != nil {
		return messenger.Chat{}, err
	}
	if c.LastMessageID != "" && !validIdentifier(c.LastMessageID) {
		return messenger.Chat{}, fmt.Errorf("%w: chat last message", ErrUpstreamChanged)
	}
	return messenger.Chat{ID: c.ID, Name: c.Name, Kind: c.Kind, FirstMessageAt: first, LastMessageID: c.LastMessageID}, nil
}

type wireCapabilities struct {
	HistoryDepth      string  `json:"historyDepth"`
	CursorKind        string  `json:"cursorKind"`
	Edits             bool    `json:"edits"`
	Deletions         bool    `json:"deletions"`
	LiveEvents        bool    `json:"liveEvents"`
	IntervalSeconds   float64 `json:"intervalSeconds"`
	ReadWindowSeconds float64 `json:"readWindowSeconds"`
}

// capabilities checks the platform driver's descriptor. The add-on has no write
// verb, so a descriptor that names one is drift.
func (c wireCapabilities) capabilities() (messenger.Capabilities, error) {
	if c.HistoryDepth == "" || c.CursorKind == "" || len(c.HistoryDepth) > 128 || len(c.CursorKind) > 128 {
		return messenger.Capabilities{}, fmt.Errorf("%w: capability text", ErrUpstreamChanged)
	}
	if c.IntervalSeconds < 0 || c.ReadWindowSeconds < 0 || c.IntervalSeconds > 365*24*3600 || c.ReadWindowSeconds > 365*24*3600 {
		return messenger.Capabilities{}, fmt.Errorf("%w: capability time", ErrUpstreamChanged)
	}
	return messenger.Capabilities{
		HistoryDepth: c.HistoryDepth, CursorKind: c.CursorKind, Edits: c.Edits, Deletions: c.Deletions,
		LiveEvents: c.LiveEvents, Interval: time.Duration(c.IntervalSeconds * float64(time.Second)),
		ReadWindow: time.Duration(c.ReadWindowSeconds * float64(time.Second)),
	}, nil
}

type wirePage struct {
	Messages  []wireMessage  `json:"messages"`
	Deletions []wireDeletion `json:"deletions"`
	Next      string         `json:"next"`
	HasMore   bool           `json:"hasMore"`
	Through   string         `json:"through"`
	Coverage  *wireCoverage  `json:"coverage"`
}

// wireCoverage is what a read covers when the platform serves no position that
// outlives the process or a bounded range.
type wireCoverage struct {
	HistoryDepth          string `json:"historyDepth"`
	CursorSurvivesRestart bool   `json:"cursorSurvivesRestart"`
}

func (p wirePage) page() (messenger.Page, error) {
	if len(p.Messages) > 500 {
		return messenger.Page{}, fmt.Errorf("%w: page larger than the limit", ErrUpstreamChanged)
	}
	page := messenger.Page{Next: messenger.Cursor(p.Next), HasMore: p.HasMore}
	for _, value := range p.Messages {
		message, err := value.message()
		if err != nil {
			return messenger.Page{}, err
		}
		page.Messages = append(page.Messages, message)
	}
	for _, value := range p.Deletions {
		deletion, err := value.deletion()
		if err != nil {
			return messenger.Page{}, err
		}
		page.Deletions = append(page.Deletions, deletion)
	}
	through, err := wireTime(p.Through)
	if err != nil {
		return messenger.Page{}, err
	}
	page.Through = through
	if p.Coverage != nil {
		page.Coverage = &messenger.ReadCoverage{HistoryDepth: p.Coverage.HistoryDepth, CursorSurvivesRestart: p.Coverage.CursorSurvivesRestart}
	}
	return page, nil
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > 512 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// wireTime accepts the RFC 3339 form the add-on writes. An absent time is zero.
func wireTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: time %q", ErrUpstreamChanged, value)
	}
	return parsed.UTC(), nil
}

// decodeStrict refuses unknown fields, so a field a newer add-on adds cannot be
// silently dropped.
func decodeStrict(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("%w: %v", ErrUpstreamChanged, err)
	}
	if decoder.More() {
		return fmt.Errorf("%w: trailing data", ErrUpstreamChanged)
	}
	return nil
}

// requiredResult distinguishes omitted/null fields from valid empty values.
func requiredResult(op string, raw []byte) error {
	fields := map[string][]string{
		"page":      {"messages", "deletions", "next", "hasMore"},
		"listChats": {"chats"}, "members": {"members"}, "signIn": {"state"},
		"beginSignIn": {"state"}, "answer": {"state"}, "whoami": {"author"},
		"capabilities": {"historyDepth", "cursorKind", "edits", "deletions", "liveEvents", "intervalSeconds", "readWindowSeconds"},
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return ErrUpstreamChanged
	}
	for _, key := range fields[op] {
		if v, ok := value[key]; !ok || string(v) == "null" {
			return ErrUpstreamChanged
		}
	}
	return nil
}
