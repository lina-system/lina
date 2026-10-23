// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	wire "github.com/thisisjun786/lina/kit/acp"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Form projects ACP's primitive fields onto Core's existing waiting card. An
// array uses a text field containing a JSON array, preserving the ACP value type.
type Form struct {
	Message string
	Fields  []protocol.FormField
	Targets []protocol.DecisionTarget
	arrays  map[string]bool
	schema  *jsonschema.Schema
}

type noFormResources struct{}

func (noFormResources) Load(string) (any, error) {
	return nil, errors.New("external form schema resources are unsupported")
}

func ParseForm(params json.RawMessage) (*Form, error) {
	var p struct {
		Mode            string
		Message         string
		RequestedSchema json.RawMessage
	}
	if json.Unmarshal(params, &p) != nil || p.Mode != "form" {
		return nil, errors.New("unsupported ACP form")
	}
	// Reject secret annotations before decoding the generated baseline, which
	// deliberately preserves only known schema fields.
	var original any
	if json.Unmarshal(params, &original) != nil || secretForm(original) {
		return nil, errors.New("secret or invalid ACP form")
	}
	var schema wire.ElicitationSchema
	if json.Unmarshal(p.RequestedSchema, &schema) != nil || schema.Type == nil || *schema.Type != wire.ElicitationSchemaTypeObject || schema.Properties == nil || len(*schema.Properties) == 0 {
		return nil, errors.New("invalid ACP form schema")
	}
	f := &Form{Message: p.Message, arrays: map[string]bool{}}
	if strings.TrimSpace(f.Message) == "" {
		f.Message = "The worker has a question"
	}
	names := make([]string, 0, len(*schema.Properties))
	for name := range *schema.Properties {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if protocol.RefusedFormFieldName(name) {
			return nil, errors.New("refused ACP form field")
		}
		property := (*schema.Properties)[name]
		field := protocol.FormField{Name: name}
		if schema.Required == nil || !slices.Contains(*schema.Required, name) {
			field.Optional = optionalFormField()
		}
		var title, description *string
		switch {
		case property.String != nil:
			v := property.String
			title, description = v.Title, v.Description
			field.Type = protocol.FormFieldTypeText
			if v.Enum != nil {
				field.Choices = v.Enum
			}
			if v.OneOf != nil {
				choices := []string{}
				for _, o := range *v.OneOf {
					choices = append(choices, o.Const)
				}
				field.Choices = &choices
			}
			if field.Choices != nil {
				field.Type = protocol.FormFieldTypeChoice
			}
		case property.Number != nil:
			field.Type = protocol.FormFieldTypeNumber
			title, description = property.Number.Title, property.Number.Description
		case property.Integer != nil:
			field.Type = protocol.FormFieldTypeNumber
			title, description = property.Integer.Title, property.Integer.Description
		case property.Boolean != nil:
			field.Type = protocol.FormFieldTypeBoolean
			title, description = property.Boolean.Title, property.Boolean.Description
		case property.Array != nil:
			field.Type = protocol.FormFieldTypeText
			title, description = property.Array.Title, property.Array.Description
			f.arrays[name] = true
		default:
			return nil, errors.New("unsupported ACP form field")
		}
		text := name
		if title != nil {
			text = *title
		}
		if description != nil {
			text += "\n" + *description
		}
		if f.arrays[name] {
			text += "\nEnter choices as a JSON array."
			if choices := multiSelectChoices(property.Array.Items); choices != "" {
				text += "\nAllowed: " + choices
			}
		}
		f.Fields = append(f.Fields, field)
		f.Targets = append(f.Targets, protocol.DecisionTarget{Name: name, Value: text})
	}
	// Compile the generated, primitive-only schema, never arbitrary references
	// supplied by the worker. Additional answer fields cannot smuggle credentials.
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	doc.(map[string]any)["additionalProperties"] = false
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(noFormResources{})
	compiler.AssertFormat()
	const resource = "https://lina.invalid/acp-form"
	if err = compiler.AddResource(resource, doc); err != nil {
		return nil, err
	}
	f.schema, err = compiler.Compile(resource)
	if err != nil {
		return nil, errors.New("invalid ACP form schema")
	}
	return f, nil
}
func optionalFormField() *bool {
	value := true
	return &value
}

func multiSelectChoices(items json.RawMessage) string {
	var enumerated struct {
		Enum []string `json:"enum"`
	}
	if json.Unmarshal(items, &enumerated) == nil && len(enumerated.Enum) > 0 {
		return strings.Join(enumerated.Enum, ", ")
	}
	var titled struct {
		AnyOf []wire.EnumOption `json:"anyOf"`
	}
	if json.Unmarshal(items, &titled) != nil || len(titled.AnyOf) == 0 {
		return ""
	}
	parts := make([]string, 0, len(titled.AnyOf))
	for _, option := range titled.AnyOf {
		part := option.Const
		if option.Title != "" && option.Title != option.Const {
			part += " (" + option.Title + ")"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

func secretForm(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if (key == "isSecret" || key == "secret" || key == "writeOnly") && item == true {
				return true
			}
			if key == "format" && item == "password" {
				return true
			}
			if secretForm(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if secretForm(item) {
				return true
			}
		}
	}
	return false
}
func (f *Form) validate(content json.RawMessage) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
	if err != nil || f.schema.Validate(doc) != nil {
		return errors.New("invalid ACP form values")
	}
	return nil
}
func (f *Form) Reply(answer protocol.QuestionAnswer) (json.RawMessage, error) {
	switch answer.Answer {
	case protocol.QuestionAnswerActionCancel:
		return json.RawMessage(`{"action":"cancel"}`), nil
	case protocol.QuestionAnswerActionDecline:
		return json.RawMessage(`{"action":"decline"}`), nil
	case protocol.QuestionAnswerActionAccept:
	default:
		return nil, errors.New("invalid ACP form action")
	}
	content := map[string]json.RawMessage{}
	if answer.Values != nil {
		for name, value := range *answer.Values {
			raw, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			if f.arrays[name] {
				if value.String == nil {
					return nil, errors.New("invalid ACP array value")
				}
				raw = json.RawMessage(*value.String)
			}
			content[name] = raw
		}
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	if err = f.validate(raw); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"action": "accept", "content": content})
}
