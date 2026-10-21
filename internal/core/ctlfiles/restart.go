// SPDX-License-Identifier: Apache-2.0

package ctlfiles

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
)

// RestartRequest lives only in system/core, outside backup generations. The
// terminal receipt remains here so restoring canon cannot revive a request.
// Epochs are canonical decimal strings, like the other control records.
type RestartRequest struct {
	Schema          int    `json:"schema"`
	ID              string `json:"id"`
	Mode            string `json:"mode"`
	Epoch           string `json:"epoch"`
	CommittedEpoch  string `json:"committed_epoch,omitempty"`
	DischargedEpoch string `json:"discharged_epoch,omitempty"`
}

func (r RestartRequest) Owed() bool { return r.ID != "" && r.DischargedEpoch == "" }

func (r RestartRequest) validate() error {
	id, err := hex.DecodeString(r.ID)
	if err != nil || len(id) != 16 || hex.EncodeToString(id) != r.ID || r.Schema != 1 ||
		(r.Mode != "when_idle" && r.Mode != "drain") {
		return errors.New("invalid restart request")
	}
	parse := func(text string) (uint64, bool) {
		n, err := strconv.ParseUint(text, 10, 64)
		return n, err == nil && n != 0 && strconv.FormatUint(n, 10) == text
	}
	epoch, valid := parse(r.Epoch)
	if !valid {
		return errors.New("invalid restart epoch")
	}
	if r.CommittedEpoch == "" {
		if r.DischargedEpoch != "" {
			return errors.New("restart discharged without commitment")
		}
		return nil
	}
	committed, valid := parse(r.CommittedEpoch)
	if !valid || committed < epoch {
		return errors.New("invalid restart commitment epoch")
	}
	if r.DischargedEpoch != "" {
		discharged, valid := parse(r.DischargedEpoch)
		if !valid || discharged <= committed {
			return errors.New("invalid restart discharge epoch")
		}
	}
	return nil
}

// ReadRestart distinguishes absence from unreadable or malformed state. It
// never repairs an invalid request or interprets it as an idle lifecycle.
func (s *Store) ReadRestart() (*RestartRequest, error) {
	data, err := s.Read("lifecycle.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Duplicate keys and null optional epochs can erase a terminal receipt in
	// ordinary JSON decoding. Refuse them instead of resurrecting a restart.
	fields := json.NewDecoder(bytes.NewReader(data))
	if token, err := fields.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("restart record must be an object")
	}
	seen := make(map[string]bool)
	for fields.More() {
		token, err := fields.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok || seen[name] {
			return nil, errors.New("duplicate restart field")
		}
		seen[name] = true
		var value json.RawMessage
		if err := fields.Decode(&value); err != nil {
			return nil, err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, errors.New("null restart field")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request RestartRequest
	if err := decoder.Decode(&request); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing restart record")
	}
	if err := request.validate(); err != nil {
		return nil, err
	}
	return &request, nil
}

// WriteRestart uses the locked atomic-write/fsync path and never mints an epoch.
func (s *Store) WriteRestart(request RestartRequest) error {
	if err := request.validate(); err != nil {
		return err
	}
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	return s.Write("lifecycle.json", data)
}
