// SPDX-License-Identifier: Apache-2.0

// Package protocol implements the client 1.0 handshake skeleton. These types
// are handwritten pending the schema generator and full client protocol in G1-17.
// It handles envelopes, not JSON-RPC framing or handshake negotiation.
package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"time"
)

type Kind string

const (
	KindHello   Kind = "hello"
	KindWelcome Kind = "welcome"
	KindError   Kind = "error"
)

type Party string

const (
	PartyCore   Party = "core"
	PartyClient Party = "client"
	PartyNode   Party = "node"
)

type EffectState string

const (
	EffectNone      EffectState = "none"
	EffectRefused   EffectState = "refused"
	EffectFailed    EffectState = "failed"
	EffectApplied   EffectState = "applied"
	EffectCancelled EffectState = "cancelled"
	EffectUnknown   EffectState = "unknown"
)

type ErrorCode string

const (
	ErrorUnsupportedCombination  ErrorCode = "unsupported_combination"
	ErrorCapabilityNotNegotiated ErrorCode = "capability_not_negotiated"
	ErrorMalformed               ErrorCode = "malformed"
	ErrorStaleEpoch              ErrorCode = "stale_epoch"
	ErrorStaleRevision           ErrorCode = "stale_revision"
	ErrorNotAuthorized           ErrorCode = "not_authorized"
	ErrorOutOfScope              ErrorCode = "out_of_scope"
	ErrorDuplicate               ErrorCode = "duplicate"
	ErrorUnavailable             ErrorCode = "unavailable"
)

type Protocol struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Sender struct {
	Party Party  `json:"party"`
	ID    string `json:"id"`
}

type ReceiptRef struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

type SourceRef struct {
	Origin     string  `json:"origin"`
	Namespace  string  `json:"namespace"`
	ID         string  `json:"id"`
	Revision   string  `json:"revision"`
	Locator    string  `json:"locator"`
	ObservedAt string  `json:"observed_at"`
	ObservedBy *string `json:"observed_by"`
}

type Error struct {
	Code   ErrorCode `json:"code"`
	Detail string    `json:"detail"`
}

// Payload is one of Hello, Welcome or ErrorPayload (or a pointer to one).
type Payload interface{ isPayload() }

type Capability struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Hello struct {
	Party            Party        `json:"party"`
	ID               string       `json:"id"`
	ProductVersion   string       `json:"product_version"`
	ProtocolVersions []string     `json:"protocol_versions"`
	Capabilities     []Capability `json:"capabilities"`
}

func (Hello) isPayload() {}

type Agent struct {
	AgentID string  `json:"agent_id"`
	Name    string  `json:"name"`
	Preset  *string `json:"preset"`
}

type Welcome struct {
	ProtocolVersion string       `json:"protocol_version"`
	Capabilities    []Capability `json:"capabilities"`
	MainID          string       `json:"main_id"`
	Epoch           string       `json:"epoch"`
	PersonID        string       `json:"person_id"`
	Agents          []Agent      `json:"agents"`
}

func (Welcome) isPayload() {}

// ErrorPayload is empty; refusal and failure information lives in Envelope.Error.
type ErrorPayload struct{}

func (ErrorPayload) isPayload() {}

type message[P any] struct {
	Protocol       Protocol    `json:"protocol"`
	Kind           Kind        `json:"kind"`
	MessageID      string      `json:"message_id"`
	AttemptID      string      `json:"attempt_id"`
	ReplyTo        *string     `json:"reply_to"`
	Sender         Sender      `json:"sender"`
	Epoch          string      `json:"epoch"`
	SentAt         string      `json:"sent_at"`
	IdempotencyKey *string     `json:"idempotency_key"`
	EffectState    EffectState `json:"effect_state"`
	ReceiptRef     *ReceiptRef `json:"receipt_ref"`
	SourceRefs     []SourceRef `json:"source_refs"`
	Error          *Error      `json:"error"`
	Payload        P           `json:"payload"`
}

// Envelope carries every field, including explicit nulls and empty arrays.
// UnmarshalJSON rejects unknown or missing fields and invalid wire values.
// MarshalJSON applies the same checks to envelopes constructed by callers.
type Envelope message[Payload]

func (e *Envelope) UnmarshalJSON(data []byte) error {
	var wire message[json.RawMessage]
	if err := decodeObject(data, &wire); err != nil {
		return err
	}
	if err := validateEnvelope(wire); err != nil {
		return err
	}
	var payload Payload
	switch wire.Kind {
	case KindHello:
		var p Hello
		if err := decodeObject(wire.Payload, &p); err != nil {
			return fmt.Errorf("hello: %w", err)
		}
		if p.Party != PartyClient {
			return fmt.Errorf("hello party must be client")
		}
		if len(p.ProtocolVersions) == 0 {
			return fmt.Errorf("hello needs a protocol version")
		}
		for _, version := range p.ProtocolVersions {
			if !versionPattern.MatchString(version) {
				return fmt.Errorf("invalid protocol version %q", version)
			}
		}
		if err := validateCapabilities(p.Capabilities); err != nil {
			return err
		}
		payload = p
	case KindWelcome:
		var p Welcome
		if err := decodeObject(wire.Payload, &p); err != nil {
			return fmt.Errorf("welcome: %w", err)
		}
		if p.ProtocolVersion != "1.0" {
			return fmt.Errorf("welcome protocol version must be 1.0")
		}
		if err := validateEpoch(p.Epoch); err != nil {
			return err
		}
		if err := validateCapabilities(p.Capabilities); err != nil {
			return err
		}
		payload = p
	case KindError:
		var p ErrorPayload
		if err := decodeObject(wire.Payload, &p); err != nil {
			return fmt.Errorf("error payload: %w", err)
		}
		payload = p
	default:
		return fmt.Errorf("unsupported kind %q", wire.Kind)
	}
	*e = Envelope{
		Protocol: wire.Protocol, Kind: wire.Kind, MessageID: wire.MessageID,
		AttemptID: wire.AttemptID, ReplyTo: wire.ReplyTo, Sender: wire.Sender,
		Epoch: wire.Epoch, SentAt: wire.SentAt, IdempotencyKey: wire.IdempotencyKey,
		EffectState: wire.EffectState, ReceiptRef: wire.ReceiptRef,
		SourceRefs: wire.SourceRefs, Error: wire.Error, Payload: payload,
	}
	return nil
}

func (e Envelope) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal(message[Payload](e))
	if err != nil {
		return nil, err
	}
	var checked Envelope
	if err := json.Unmarshal(data, &checked); err != nil {
		return nil, err
	}
	return data, nil
}

var (
	versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	epochPattern   = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	utcPattern     = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|\+00:00)$`)
)

func validateEpoch(epoch string) error {
	if !epochPattern.MatchString(epoch) {
		return fmt.Errorf("invalid epoch %q", epoch)
	}
	if _, err := strconv.ParseUint(epoch, 10, 64); err != nil {
		return fmt.Errorf("invalid epoch: %w", err)
	}
	return nil
}

func validateTime(value string) error {
	if !utcPattern.MatchString(value) {
		return fmt.Errorf("timestamp must be RFC 3339 in UTC: %q", value)
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return err
	}
	return nil
}

func validateCapabilities(capabilities []Capability) error {
	for _, capability := range capabilities {
		if !versionPattern.MatchString(capability.Version) {
			return fmt.Errorf("invalid capability version %q", capability.Version)
		}
	}
	return nil
}

func validateEnvelope(e message[json.RawMessage]) error {
	if e.Protocol.Name != "client" || e.Protocol.Version != "1.0" {
		return fmt.Errorf("expected client protocol 1.0")
	}
	if (e.Kind == KindError || e.EffectState == EffectRefused || e.EffectState == EffectFailed) && e.Error == nil {
		return fmt.Errorf("an error or refusal must carry an error code and detail")
	}
	switch e.Sender.Party {
	case PartyCore, PartyClient, PartyNode:
	default:
		return fmt.Errorf("invalid sender party %q", e.Sender.Party)
	}
	switch e.EffectState {
	case EffectNone, EffectRefused, EffectFailed, EffectApplied, EffectCancelled, EffectUnknown:
	default:
		return fmt.Errorf("invalid effect state %q", e.EffectState)
	}
	if e.Error != nil {
		switch e.Error.Code {
		case ErrorUnsupportedCombination, ErrorCapabilityNotNegotiated, ErrorMalformed, ErrorStaleEpoch, ErrorStaleRevision, ErrorNotAuthorized, ErrorOutOfScope, ErrorDuplicate, ErrorUnavailable:
		default:
			return fmt.Errorf("invalid error code %q", e.Error.Code)
		}
	}
	if err := validateEpoch(e.Epoch); err != nil {
		return err
	}
	if err := validateTime(e.SentAt); err != nil {
		return err
	}
	if e.ReceiptRef != nil && e.ReceiptRef.Kind != "receipt" {
		return fmt.Errorf("invalid receipt kind %q", e.ReceiptRef.Kind)
	}
	for _, source := range e.SourceRefs {
		if err := validateTime(source.ObservedAt); err != nil {
			return err
		}
	}
	return nil
}

// encoding/json alone allows missing fields, null for scalars, and case-folded
// keys. Check the exact object shape before decoding into the handwritten types.
func decodeObject(data []byte, target any) error {
	if err := checkShape(data, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func checkShape(data []byte, typ reflect.Type) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		if typ.Kind() == reflect.Pointer {
			return nil
		}
		return fmt.Errorf("%s cannot be null", typ)
	}
	if typ.Kind() == reflect.Pointer {
		return checkShape(data, typ.Elem())
	}
	if typ == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	switch {
	case typ.Kind() == reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return err
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := field.Tag.Get("json")
			raw, ok := fields[name]
			if !ok {
				return fmt.Errorf("missing field %q", name)
			}
			if err := checkShape(raw, field.Type); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			delete(fields, name)
		}
		for name := range fields {
			return fmt.Errorf("unknown field %q", name)
		}
	case typ.Kind() == reflect.Slice:
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return err
		}
		for _, item := range items {
			if err := checkShape(item, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
