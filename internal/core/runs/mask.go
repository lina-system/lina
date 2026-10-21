// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"sync"

	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

// Redact decoded strings, including provider JSON embedded inside strings.
// Masking an encoded JSON document directly can remove its structural quotes.
// Unchanged provider items keep their exact bytes and number representations.
// Only digests and masked outputs are cached; raw credentials are never cache
// keys or values. Repeated checkpoints need not rescan unchanged strings.
type masker struct {
	mu    sync.Mutex
	cache map[[32]byte]string
}

func (m *masker) json(data []byte) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.cache) > 4096 || m.cache == nil {
		m.cache = map[[32]byte]string{}
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	changed, err := m.node(&value, "", 0)
	if err != nil {
		return nil, err
	}
	if !changed {
		return data, nil
	}
	return json.Marshal(value)
}
func (m *masker) node(value *any, key string, depth int) (bool, error) {
	if depth > 64 {
		return false, ErrInvalid
	}
	changed := false
	switch v := (*value).(type) {
	case string:
		encodedKey, _ := json.Marshal([2]string{key, v})
		digest := sha256.Sum256(encodedKey)
		if cached, ok := m.cache[digest]; ok {
			*value = cached
			return cached != v, nil
		}
		original := v
		trimmed := strings.TrimSpace(v)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			d := json.NewDecoder(strings.NewReader(v))
			d.UseNumber()
			var nested any
			if json.Valid([]byte(v)) && d.Decode(&nested) == nil {
				edited, err := m.node(&nested, "", depth+1)
				if err != nil {
					return false, err
				}
				if edited {
					b, err := json.Marshal(nested)
					if err != nil {
						return false, err
					}
					v = string(b)
					changed = true
				}
			}
		}
		// Sensitive object keys also flag credentials without a recognizable shape.
		encoded, _ := json.Marshal(v)
		pair := key + "=" + string(encoded)
		checked, err := secrets.Mask(pair)
		if err != nil {
			return false, err
		}
		if key != "" && checked.Text() != pair && strings.TrimPrefix(checked.Text(), key+"=") == secret.Placeholder {
			*value = secret.Placeholder
			m.cache[digest] = secret.Placeholder
			return true, nil
		}
		checked, err = secrets.Mask(v)
		if err != nil {
			return false, err
		}
		*value = checked.Text()
		m.cache[digest] = checked.Text()
		return checked.Text() != original, nil
	case []any:
		for i := range v {
			edited, err := m.node(&v[i], "", depth+1)
			if err != nil {
				return false, err
			}
			changed = changed || edited
		}
	case map[string]any:
		for k, child := range v {
			edited, err := m.node(&child, k, depth+1)
			if err != nil {
				return false, err
			}
			var name any = k
			renamed, err := m.node(&name, "", depth+1)
			if err != nil {
				return false, err
			}
			if renamed {
				delete(v, k)
				changed = true
			}
			v[name.(string)] = child
			changed = changed || edited
		}
	}
	return changed, nil
}
func masked[T any](m *masker, v T) (T, error) {
	b, err := json.Marshal(v)
	if err == nil {
		b, err = m.json(b)
	}
	if err != nil {
		return v, err
	}
	var out T
	err = json.Unmarshal(b, &out)
	return out, err
}
