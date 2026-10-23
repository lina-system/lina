// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"encoding/json"
	"strings"

	"github.com/thisisjun786/lina/kit/secret"
)

// Non-string credential values must be masked before the native JSON walker,
// which handles string values. Arguments can themselves contain serialized
// JSON. Decode those boundaries instead of applying byte spans to JSON syntax.
func maskScalarCallCredentials(body string) (string, error) {
	decode := func(text string) (any, error) {
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		var value any
		err := decoder.Decode(&value)
		return value, err
	}
	var visit func(any, string, int) (any, bool, error)
	visit = func(value any, key string, depth int) (any, bool, error) {
		if depth > 64 {
			return nil, false, ErrSnapshot
		}
		switch value := value.(type) {
		case json.Number, bool, nil:
			raw, err := json.Marshal(value)
			if err != nil {
				return nil, false, err
			}
			if key != "" && len(ScanSecrets(append([]byte(key+"="), raw...))) > 0 {
				return secret.Placeholder, true, nil
			}
		case string:
			trimmed := strings.TrimSpace(value)
			if (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Valid([]byte(value)) {
				nested, err := decode(value)
				if err != nil {
					return nil, false, err
				}
				masked, changed, err := visit(nested, "", depth+1)
				if err != nil {
					return nil, false, err
				}
				if changed {
					raw, err := json.Marshal(masked)
					return string(raw), true, err
				}
			}
		case []any:
			changed := false
			for i, child := range value {
				masked, edited, err := visit(child, "", depth+1)
				if err != nil {
					return nil, false, err
				}
				value[i], changed = masked, changed || edited
			}
			return value, changed, nil
		case map[string]any:
			changed := false
			for name, child := range value {
				masked, edited, err := visit(child, name, depth+1)
				if err != nil {
					return nil, false, err
				}
				value[name], changed = masked, changed || edited
			}
			return value, changed, nil
		}
		return value, false, nil
	}
	value, err := decode(body)
	if err != nil {
		return "", err
	}
	masked, changed, err := visit(value, "", 0)
	if err != nil {
		return "", err
	}
	if !changed {
		return body, nil
	}
	raw, err := json.Marshal(masked)
	return string(raw), err
}
