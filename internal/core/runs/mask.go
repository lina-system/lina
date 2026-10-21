// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/secret"
)

// Mask JSON values rather than its serialized text: replacing a quoted secret
// in encoded JSON can otherwise break the record. UseNumber preserves exact
// counters and revisions. Unchanged provider items keep their original bytes.
func maskedJSON(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	value, changed, err := maskValue(value)
	if err != nil {
		return nil, err
	}
	if !changed {
		return data, nil
	}
	return json.Marshal(value)
}
func maskValue(value any) (any, bool, error) {
	switch v := value.(type) {
	case string:
		text, err := maskedText(v)
		return text, text != v, err
	case []any:
		changed := false
		for i, entry := range v {
			next, c, err := maskValue(entry)
			if err != nil {
				return nil, false, err
			}
			v[i] = next
			changed = changed || c
		}
		return v, changed, nil
	case map[string]any:
		changed := false
		out := make(map[string]any, len(v))
		for key, entry := range v {
			maskedKey, err := secrets.Mask(key)
			if err != nil {
				return nil, false, err
			}
			outputKey := maskedKey.Text()
			if _, exists := out[outputKey]; exists {
				return nil, false, ErrInvalid
			}
			changed = changed || outputKey != key
			// Reuse the central detector for sensitive field names, including
			// unshaped values such as {"api_key":"synthetic-value"}.
			probe := key + `="synthetic-field-value"`
			field, err := secrets.Mask(probe)
			if err != nil {
				return nil, false, err
			}
			if field.Text() != probe && entry != nil {
				out[outputKey] = secret.Placeholder
				if text, ok := entry.(string); !ok || text != secret.Placeholder {
					changed = true
				}
				continue
			}
			next, c, err := maskValue(entry)
			if err != nil {
				return nil, false, err
			}
			out[outputKey] = next
			changed = changed || c
		}
		return out, changed, nil
	default:
		return value, false, nil
	}
}
func maskedText(text string) (string, error) {
	trimmed := strings.TrimSpace(text)
	// Tool outputs and result bodies can contain JSON encoded inside a string.
	if (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Valid([]byte(text)) {
		data, err := maskedJSON([]byte(text))
		return string(data), err
	}
	masked, err := secrets.Mask(text)
	return masked.Text(), err
}
func maskedItems(items []provider.Item) ([]provider.Item, error) {
	out := make([]provider.Item, len(items))
	for i, item := range items {
		data, err := maskedJSON(item)
		if err != nil {
			return nil, err
		}
		out[i] = data
	}
	return out, nil
}
