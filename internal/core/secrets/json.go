// SPDX-License-Identifier: Apache-2.0
package secrets

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/thisisjun786/lina/kit/secret"
)

// Mask structured payloads at decoded string boundaries. Applying plaintext
// assignment patterns to JSON syntax can retain escaped secrets or lose quotes.
func maskJSON(text string, flagged []Span) (Masked, error) {
	if len(flagged) > 0 {
		spans := append([]Span(nil), flagged...)
		sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
		var out strings.Builder
		end := 0
		for _, span := range spans {
			if !validSpan(text, span) || span.Sensitivity != "" && span.Sensitivity != "secret" || span.Start < end {
				return Masked{}, ErrSpan
			}
			out.WriteString(text[end:span.Start])
			out.WriteString(secret.Placeholder)
			end = span.End
		}
		out.WriteString(text[end:])
		text = out.String()
	}
	decode := func(raw string) (any, error) {
		if !json.Valid([]byte(raw)) {
			return nil, ErrSpan
		}
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		var value any
		err := decoder.Decode(&value)
		return value, err
	}
	value, err := decode(text)
	if err != nil {
		return Masked{}, err
	}
	var visit func(any, string, int) (any, bool, error)
	visit = func(value any, key string, depth int) (any, bool, error) {
		if depth > 64 {
			return nil, false, ErrSpan
		}
		switch v := value.(type) {
		case string:
			quoted, _ := json.Marshal(v)
			pair := key + "=" + string(quoted)
			checked, err := Mask(pair)
			if err != nil {
				return nil, false, err
			}
			if key != "" && checked.Text() == key+"="+secret.Placeholder {
				return secret.Placeholder, v != secret.Placeholder, nil
			}
			trimmed := strings.TrimSpace(v)
			if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
				if nested, err := decode(v); err == nil {
					masked, changed, err := visit(nested, "", depth+1)
					if err != nil || !changed {
						return v, false, err
					}
					encoded, err := json.Marshal(masked)
					return string(encoded), true, err
				}
			}
			masked, err := Mask(v)
			return masked.Text(), masked.Text() != v, err
		case []any:
			changed := false
			for i, child := range v {
				masked, edited, err := visit(child, "", depth+1)
				if err != nil {
					return nil, false, err
				}
				v[i], changed = masked, changed || edited
			}
			return v, changed, nil
		case map[string]any:
			changed := false
			for name, child := range v {
				masked, edited, err := visit(child, name, depth+1)
				if err != nil {
					return nil, false, err
				}
				checked, err := Mask(name)
				if err != nil {
					return nil, false, err
				}
				if checked.Text() != name {
					delete(v, name)
					changed = true
				}
				v[checked.Text()], changed = masked, changed || edited
			}
			return v, changed, nil
		default:
			return value, false, nil
		}
	}
	value, changed, err := visit(value, "", 0)
	if err != nil {
		return Masked{}, err
	}
	if changed {
		encoded, err := json.Marshal(value)
		if err != nil {
			return Masked{}, err
		}
		text = string(encoded)
	}
	result := Masked{text: text}
	// These placeholders lie inside JSON strings. Removing their contents for
	// derived history retains the structural quotes and valid provider JSON.
	for offset := 0; ; {
		at := strings.Index(text[offset:], secret.Placeholder)
		if at < 0 {
			break
		}
		start := offset + at
		offset = start + len(secret.Placeholder)
		result.spans = append(result.spans, Span{Start: start, End: offset, Sensitivity: "secret"})
	}
	return result, nil
}
