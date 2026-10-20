// SPDX-License-Identifier: Apache-2.0

// Package diag records only closed codes and opaque identifiers. It never
// accepts an error message, prompt, provider payload, path or other free text.
package diag

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

var ErrInvalid = errors.New("invalid diagnosis record")

// ID is a random 128-bit identifier. Its zero value means absent, encoded null.
// ParseID is a wire boundary, not a free-text constructor: only canonical,
// nonzero, lowercase 32-hex identifiers are accepted.
type ID struct{ value [16]byte }

func NewID() (ID, error) {
	var id ID
	if _, err := rand.Read(id.value[:]); err != nil {
		return ID{}, err
	}
	if id.IsZero() {
		return NewID()
	}
	return id, nil
}
func ParseID(text string) (ID, error) {
	var id ID
	if len(text) != 32 {
		return id, ErrInvalid
	}
	if _, err := hex.Decode(id.value[:], []byte(text)); err != nil || id.IsZero() || id.String() != text {
		return ID{}, ErrInvalid
	}
	return id, nil
}
func (id ID) IsZero() bool   { return id == ID{} }
func (id ID) String() string { return hex.EncodeToString(id.value[:]) }
func (id ID) MarshalJSON() ([]byte, error) {
	if id.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(id.String())
}
func (id *ID) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*id = ID{}
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return ErrInvalid
	}
	parsed, err := ParseID(text)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// Epoch is a durable, positive Core generation; zero is pre-epoch (null).
type Epoch uint64

func (e Epoch) MarshalJSON() ([]byte, error) {
	if e == 0 {
		return []byte("null"), nil
	}
	return json.Marshal(strconv.FormatUint(uint64(e), 10))
}
func (e *Epoch) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*e = 0
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return ErrInvalid
	}
	n, err := strconv.ParseUint(text, 10, 64)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != text {
		return ErrInvalid
	}
	*e = Epoch(n)
	return nil
}

type References struct {
	ConversationID ID    `json:"conversation_id"`
	TaskID         ID    `json:"task_id"`
	AgentID        ID    `json:"agent_id"`
	Epoch          Epoch `json:"epoch"`
	NodeID         ID    `json:"node_id"`
}
type Cause struct {
	Code          CauseCode `json:"code"`
	ParentEventID ID        `json:"parent_event_id"`
}
type Fields struct {
	Component Component `json:"component"`
	References
	Cause      Cause      `json:"cause"`
	Result     Result     `json:"result"`
	NextAction Action     `json:"next_action"`
	ErrorClass ErrorClass `json:"error_class"`
}
type Event struct {
	EventID ID        `json:"event_id"`
	Time    time.Time `json:"time"`
	Fields
}

func (f Fields) validate() error {
	if !f.Component.valid() || !f.Cause.Code.valid() || !f.Result.valid() || !f.NextAction.valid() || !f.ErrorClass.valid() {
		return ErrInvalid
	}
	root := f.Cause.Code == BootStarted || f.Cause.Code == TurnStarted || f.Cause.Code == TaskStarted
	if root != f.Cause.ParentEventID.IsZero() {
		return ErrInvalid
	}
	if root && (f.Result != Pending || f.ErrorClass != NoError || f.NextAction != Continue) {
		return ErrInvalid
	}
	if f.Cause.Code == BootStarted && f.Component != OS {
		return ErrInvalid
	}
	if f.Cause.Code == TurnStarted && (f.Component != Conversation || f.ConversationID.IsZero()) {
		return ErrInvalid
	}
	if f.Cause.Code == TaskStarted && (f.Component != Task || f.TaskID.IsZero()) {
		return ErrInvalid
	}
	return nil
}
func (e Event) Validate() error {
	if e.EventID.IsZero() || e.Time.IsZero() || e.Time.Location() != time.UTC || e.Time.Year() < 1 || e.Time.Year() > 9999 || e.Cause.ParentEventID == e.EventID {
		return ErrInvalid
	}
	return e.Fields.validate()
}
func (e Event) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	type wire Event
	return json.Marshal(wire(e))
}
func (e *Event) UnmarshalJSON(data []byte) error {
	fields, err := exactObject(data, "event_id", "time", "component", "conversation_id", "task_id", "agent_id", "epoch", "node_id", "cause", "result", "next_action", "error_class")
	if err != nil {
		return err
	}
	if _, err := exactObject(fields["cause"], "code", "parent_event_id"); err != nil {
		return err
	}
	type wire Event
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return ErrInvalid
	}
	result := Event(decoded)
	if err := result.Validate(); err != nil {
		return err
	}
	*e = result
	return nil
}

// Writer commits a validated event. Implementations must reject duplicate ids
// and missing parents, and acknowledge terminal results only after durability.
type Writer interface {
	Append(context.Context, Event) error
}
type Emitter struct{ writer Writer }

func NewEmitter(writer Writer) *Emitter { return &Emitter{writer: writer} }
func (e *Emitter) Emit(ctx context.Context, fields Fields) (Event, error) {
	if err := fields.validate(); err != nil {
		return Event{}, err
	}
	ctx, cancel := CommitContext(ctx, fields.Result)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if e == nil || e.writer == nil {
		return Event{}, ErrInvalid
	}
	id, err := NewID()
	if err != nil {
		return Event{}, err
	}
	event := Event{EventID: id, Time: time.Now().UTC().Round(0), Fields: fields}
	if err := e.writer.Append(ctx, event); err != nil {
		return Event{}, err
	}
	return event, nil
}

type RootKind uint8

const (
	BootRoot RootKind = iota + 1
	TurnRoot
	TaskRoot
)

func (e *Emitter) Root(ctx context.Context, kind RootKind, refs References) (Event, error) {
	f := Fields{References: refs, Result: Pending, NextAction: Continue}
	switch kind {
	case BootRoot:
		f.Component, f.Cause.Code = OS, BootStarted
	case TurnRoot:
		f.Component, f.Cause.Code = Conversation, TurnStarted
	case TaskRoot:
		f.Component, f.Cause.Code = Task, TaskStarted
	default:
		return Event{}, ErrInvalid
	}
	return e.Emit(ctx, f)
}
