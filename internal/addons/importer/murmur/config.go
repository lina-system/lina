// SPDX-License-Identifier: Apache-2.0
package murmur

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// configRoom is one room's override in the person's config. Only the name and
// the business flag are read; everything else is the collector's own state.
type configRoom struct {
	Source       string          `json:"source"`
	RoomID       json.RawMessage `json:"room_id"`
	Name         string          `json:"name"`
	Business     bool            `json:"business"`
	FirstMessage *string         `json:"first_message_ts"`
}

type config struct {
	Version json.RawMessage `json:"version"`
	Rooms   []configRoom    `json:"rooms"`
}

// parseConfig gates the config on its registered version and applies the room
// names it holds. A config the adapter does not know refuses the run.
func parseConfig(raw []byte) (map[string]configRoom, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("%w: config: %v", ErrRefused, err)
	}
	for name := range document {
		switch name {
		case "version", "embedding", "integrity", "rooms", "sources":
		default:
			return nil, fmt.Errorf("%w: config: unknown field %q", ErrRefused, name)
		}
	}
	versionRaw, ok := document["version"]
	if !ok {
		return nil, fmt.Errorf("%w: config: version is required", ErrRefused)
	}
	var version int
	if err := json.Unmarshal(versionRaw, &version); err != nil || version != 1 {
		return nil, fmt.Errorf("%w: config: version %s, want 1", ErrRefused, versionRaw)
	}
	rooms := map[string]configRoom{}
	if roomsRaw, ok := document["rooms"]; ok {
		var list []configRoom
		if err := json.Unmarshal(roomsRaw, &list); err != nil {
			return nil, fmt.Errorf("%w: config: rooms: %v", ErrRefused, err)
		}
		for _, entry := range list {
			if _, known := Platforms[entry.Source]; !known {
				return nil, fmt.Errorf("%w: config: room source %q is not a murmur source", ErrRefused, entry.Source)
			}
			room, err := rawID(entry.RoomID)
			if err != nil || room == "" {
				return nil, fmt.Errorf("%w: config: room id is required", ErrRefused)
			}
			rooms[entry.Source+"\x00"+room] = entry
		}
	}
	if sourcesRaw, ok := document["sources"]; ok {
		var list []map[string]json.RawMessage
		if err := json.Unmarshal(sourcesRaw, &list); err != nil {
			return nil, fmt.Errorf("%w: config: sources: %v", ErrRefused, err)
		}
		for _, entry := range list {
			if _, ok := entry["type"]; !ok {
				return nil, fmt.Errorf("%w: config: a source has no type", ErrRefused)
			}
		}
	}
	return rooms, nil
}

// rawID accepts murmur's numeric or string identifiers and keeps their text.
func rawID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String(), nil
	}
	return "", errors.New("room id must be a string or a number")
}

// sortedRoomKeys gives deterministic room order for reports and imports.
func sortedRoomKeys(rooms map[string]*room) []string {
	keys := make([]string, 0, len(rooms))
	for key := range rooms {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
