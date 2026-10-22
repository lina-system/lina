// SPDX-License-Identifier: Apache-2.0

package pin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Validator checks the adopted ledger record shapes. Cross-record invariants,
// authorization and evidence of effects belong to the work engine, not JSON Schema.
type Validator struct{ schemas map[string]*jsonschema.Schema }

type pinnedOnly struct{}

func (pinnedOnly) Load(url string) (any, error) {
	return nil, fmt.Errorf("unpinned schema reference %q", url)
}

func NewValidator(r Record, corpus fs.FS) (*Validator, error) {
	if err := r.Verify(corpus); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft7)
	compiler.UseLoader(pinnedOnly{})
	names := make(map[string]string)
	for _, f := range r.Schemas {
		name := strings.TrimSuffix(path.Base(f.Path), ".json")
		if _, exists := names[name]; exists {
			return nil, fmt.Errorf("duplicate schema name %q", name)
		}
		raw, err := fs.ReadFile(corpus, f.Path)
		if err != nil {
			return nil, err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("schema %s: %w", name, err)
		}
		url := "https://crw.invalid/" + f.Path
		if err := compiler.AddResource(url, doc); err != nil {
			return nil, err
		}
		names[name] = url
	}
	v := &Validator{schemas: make(map[string]*jsonschema.Schema)}
	for name, url := range names {
		schema, err := compiler.Compile(url)
		if err != nil {
			return nil, fmt.Errorf("schema %s: %w", name, err)
		}
		v.schemas[name] = schema
	}
	return v, nil
}

// Validate distinguishes a schema rejection from malformed input or a broken
// validator. The latter cannot satisfy a fixture expecting an invalid record.
func (v *Validator) Validate(name string, instance json.RawMessage) (bool, error) {
	schema, ok := v.schemas[name]
	if !ok {
		return false, fmt.Errorf("unknown pinned schema %q", name)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(instance))
	if err != nil {
		return false, err
	}
	err = schema.Validate(doc)
	var invalid *jsonschema.ValidationError
	if errors.As(err, &invalid) {
		return false, nil
	}
	return err == nil, err
}
