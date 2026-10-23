// SPDX-License-Identifier: Apache-2.0

package result

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/thisisjun786/lina/kit/protocol"
)

type SurfaceBatch struct {
	Messages []json.RawMessage `json:"messages"`
}

// FoldSurface applies a bounded A2UI batch to a private copy. Only a validated
// whole revision is returned; callers must never publish individual messages.
func FoldSurface(previous *Surface, batch SurfaceBatch) (*Surface, error) {
	if len(batch.Messages) == 0 || len(batch.Messages) > 256 {
		return nil, surfaceError("message count")
	}
	var s *Surface
	if previous != nil {
		raw, _ := json.Marshal(previous)
		s = &Surface{}
		_ = json.Unmarshal(raw, s)
	}
	total := 0
	for _, raw := range batch.Messages {
		total += len(raw)
		if total > 64<<10 {
			return nil, surfaceError("batch size")
		}
		var m struct {
			Version    string          `json:"version"`
			Create     json.RawMessage `json:"createSurface,omitempty"`
			Components json.RawMessage `json:"updateComponents,omitempty"`
			Data       json.RawMessage `json:"updateDataModel,omitempty"`
			Delete     json.RawMessage `json:"deleteSurface,omitempty"`
		}
		if Decode(raw, &m) != nil || m.Version != "v0.9" {
			return nil, surfaceError("message version or fields")
		}
		count := 0
		for _, v := range []json.RawMessage{m.Create, m.Components, m.Data, m.Delete} {
			if v != nil {
				count++
			}
		}
		if count != 1 {
			return nil, surfaceError("message operation")
		}
		if m.Create != nil {
			var p struct {
				ID      string `json:"surfaceId"`
				Catalog string `json:"catalogId"`
			}
			if Decode(m.Create, &p) != nil || p.ID == "" || p.Catalog != SurfaceCatalog || s != nil && !s.Deleted {
				return nil, surfaceError("create surface")
			}
			if s != nil && s.ID != p.ID {
				return nil, surfaceError("surface identity")
			}
			s = &Surface{Version: "v0.9", CatalogID: SurfaceCatalog, CatalogVersion: SurfaceCatalogVersion, ID: p.ID, Components: []SurfaceComponent{}, Data: map[string]json.RawMessage{}}
			continue
		}
		if s == nil || s.Deleted {
			return nil, surfaceError("surface missing")
		}
		if m.Components != nil {
			var p struct {
				ID         string            `json:"surfaceId"`
				Components []json.RawMessage `json:"components"`
			}
			if Decode(m.Components, &p) != nil || p.ID != s.ID || len(p.Components) == 0 || len(p.Components) > 256 {
				return nil, surfaceError("component message")
			}
			seen := map[string]bool{}
			for _, raw := range p.Components {
				if protocol.ValidateSurfaceComponent(raw) != nil {
					return nil, surfaceError("component schema")
				}
				var c SurfaceComponent
				if Decode(raw, &c) != nil || seen[c.ID] {
					return nil, surfaceError("component shape")
				}
				seen[c.ID] = true
				i := slices.IndexFunc(s.Components, func(old SurfaceComponent) bool { return old.ID == c.ID })
				if i < 0 {
					s.Components = append(s.Components, c)
				} else {
					s.Components[i] = c
				}
			}
		}
		if m.Data != nil {
			var p struct {
				ID    string          `json:"surfaceId"`
				Path  string          `json:"path,omitempty"`
				Value json.RawMessage `json:"value,omitempty"`
			}
			if Decode(m.Data, &p) != nil || p.ID != s.ID {
				return nil, surfaceError("data message")
			}
			parts, err := pointerParts(p.Path)
			if err != nil {
				return nil, err
			}
			root, _ := json.Marshal(s.Data)
			updated, err := setSurfaceData(root, parts, p.Value)
			if err != nil {
				return nil, err
			}
			if json.Unmarshal(updated, &s.Data) != nil || s.Data == nil {
				return nil, surfaceError("data root")
			}
		}
		if m.Delete != nil {
			var p struct {
				ID string `json:"surfaceId"`
			}
			if Decode(m.Delete, &p) != nil || p.ID != s.ID {
				return nil, surfaceError("delete surface")
			}
			s.Deleted = true
			s.Components = []SurfaceComponent{}
			s.Data = map[string]json.RawMessage{}
		}
	}
	if !s.Deleted {
		ordered, err := s.ordered()
		if err != nil {
			return nil, err
		}
		s.Components = ordered // Discard old components detached by a tree replacement.
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return validateSurface(raw)
}
func setSurfaceData(raw json.RawMessage, parts []string, value json.RawMessage) (json.RawMessage, error) {
	if len(parts) == 0 {
		if value == nil {
			return json.RawMessage(`{}`), nil
		}
		return value, nil
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		var array []json.RawMessage
		n, err := strconv.Atoi(parts[0])
		if json.Unmarshal(raw, &array) != nil || err != nil || n < 0 || n > len(array) || strconv.Itoa(n) != parts[0] {
			return nil, surfaceError("array data path")
		}
		if len(parts) == 1 && value == nil {
			if n >= len(array) {
				return nil, surfaceError("array data path")
			}
			array = append(array[:n], array[n+1:]...)
		} else {
			var previous json.RawMessage
			if n < len(array) {
				previous = array[n]
			}
			next, err := setSurfaceData(previous, parts[1:], value)
			if err != nil {
				return nil, err
			}
			if n == len(array) {
				array = append(array, next)
			} else {
				array[n] = next
			}
		}
		return json.Marshal(array)
	}
	object := map[string]json.RawMessage{}
	if raw != nil && json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, surfaceError("data parent")
	}
	if len(parts) == 1 && value == nil {
		delete(object, parts[0])
	} else {
		next, err := setSurfaceData(object[parts[0]], parts[1:], value)
		if err != nil {
			return nil, err
		}
		object[parts[0]] = next
	}
	out, err := json.Marshal(object)
	return out, err
}
