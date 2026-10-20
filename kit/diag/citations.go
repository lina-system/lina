// SPDX-License-Identifier: Apache-2.0
package diag

import (
	"bytes"
	"encoding/json"
	"io"
)

// exactObject rejects missing, unknown and duplicate keys, including fields
// that a permissive decoder would silently discard before returning the event.
func exactObject(data []byte, names ...string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrInvalid
	}
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	values := make(map[string]json.RawMessage, len(names))
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, ErrInvalid
		}
		name, ok := token.(string)
		if !ok || !allowed[name] {
			return nil, ErrInvalid
		}
		delete(allowed, name)
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, ErrInvalid
		}
		values[name] = value
	}
	if _, err := d.Token(); err != nil || len(allowed) != 0 {
		return nil, ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	return values, nil
}

// Citation is the machine-checkable part of a diagnosis. Prose is deliberately
// outside this oracle. expectedRoot comes from the injection, not the summary.
type Citation struct {
	EventID     ID         `json:"event_id"`
	RootEventID ID         `json:"root_event_id"`
	ErrorClass  ErrorClass `json:"error_class"`
	CauseCode   CauseCode  `json:"cause_code"`
}

func VerifyCitations(events []Event, expectedRoot ID, citations []Citation) error {
	if expectedRoot.IsZero() || len(citations) == 0 {
		return ErrInvalid
	}
	byID := make(map[ID]Event, len(events))
	for _, event := range events {
		if err := event.Validate(); err != nil {
			return err
		}
		if _, exists := byID[event.EventID]; exists {
			return ErrInvalid
		}
		byID[event.EventID] = event
	}
	for _, citation := range citations {
		event, ok := byID[citation.EventID]
		if !ok || citation.RootEventID != expectedRoot || event.ErrorClass == NoError || event.ErrorClass != citation.ErrorClass || event.Cause.Code != citation.CauseCode {
			return ErrInvalid
		}
		if event.Result != Failure && event.Result != Cancelled && event.Result != Denied && event.Result != Unknown {
			return ErrInvalid
		}
		seen := make(map[ID]bool)
		for {
			if seen[event.EventID] {
				return ErrInvalid
			}
			seen[event.EventID] = true
			if event.Cause.ParentEventID.IsZero() {
				if event.EventID != expectedRoot {
					return ErrInvalid
				}
				break
			}
			event, ok = byID[event.Cause.ParentEventID]
			if !ok {
				return ErrInvalid
			}
		}
	}
	return nil
}
