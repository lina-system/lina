// SPDX-License-Identifier: Apache-2.0
package textcall

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"regexp"
	"strings"
)

type parsed struct {
	Call
	named map[string]string
}

var attribute = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z_0-9]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>` + "`" + `]+))`)

func parse(s string) (*parsed, bool, error) {
	if strings.HasPrefix(s, "[tool_use") {
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return nil, false, errSyntax
		}
		attrs, err := attributes(strings.TrimSpace(s[len("[tool_use"):end]))
		if err != nil || attrs["name"] == "" {
			return nil, false, errSyntax
		}
		raw, tail, err := jsonPrefix(strings.TrimSpace(s[end+1:]))
		if err != nil {
			return nil, false, err
		}
		if tail != "" {
			return trailing(tail)
		}
		return &parsed{Call: Call{Name: attrs["name"], Arguments: raw}}, false, nil
	}
	// JSON bodies in tool_use/tool_call wrappers are JSON, not XML character
	// data: shell operators and literal < characters must survive unchanged.
	for _, tag := range []string{"tool_use", "tool_call"} {
		if strings.HasPrefix(s, "<"+tag) {
			end := strings.IndexByte(s, '>')
			if end < 0 {
				return nil, false, errSyntax
			}
			body := strings.TrimSpace(s[end+1:])
			if strings.HasPrefix(body, "{") {
				raw, tail, err := jsonPrefix(body)
				if err != nil {
					return nil, false, err
				}
				close := "</" + tag + ">"
				if !strings.HasPrefix(tail, close) {
					return nil, false, errSyntax
				}
				if rest := strings.TrimSpace(strings.TrimPrefix(tail, close)); rest != "" {
					return trailing(rest)
				}
				header, err := readXML(s[:end+1] + close)
				if err != nil {
					return nil, false, err
				}
				if tag == "tool_use" {
					if header.attrs["name"] == "" {
						return nil, false, errSyntax
					}
					return &parsed{Call: Call{Name: header.attrs["name"], Arguments: raw}}, false, nil
				}
				value, err := decodeJSON(raw)
				if err != nil {
					return nil, false, err
				}
				obj, ok := value.(map[string]any)
				if !ok || len(obj) != 2 {
					return nil, false, errSyntax
				}
				name, ok := obj["name"].(string)
				if !ok || name == "" {
					return nil, false, errSyntax
				}
				args, ok := obj["arguments"].(map[string]any)
				if !ok {
					return nil, false, errSyntax
				}
				encoded, _ := json.Marshal(args)
				return &parsed{Call: Call{Name: name, Arguments: encoded}}, false, nil
			}
		}
	}
	root, err := readXML(s)
	if err != nil {
		// A completed XML call followed by an explanation is quoted prose.
		for _, tag := range []string{"tool_use", "tool_call", "function_calls", "invoke", "antml:function_calls", "antml:invoke"} {
			close := "</" + tag + ">"
			if i := strings.LastIndex(s, close); i >= 0 && strings.TrimSpace(s[i+len(close):]) != "" {
				if _, e := readXML(s[:i+len(close)]); e == nil {
					return trailing(strings.TrimSpace(s[i+len(close):]))
				}
			}
		}
		return nil, false, err
	}
	if root.name == "function_calls" {
		if len(root.children) != 1 || strings.TrimSpace(root.text) != "" || len(root.attrs) != 0 {
			return nil, false, errSyntax
		}
		root = root.children[0]
		if root.name != "invoke" {
			return nil, false, errSyntax
		}
	}
	if (root.name != "invoke" && root.name != "tool_use") || root.attrs["name"] == "" || strings.TrimSpace(root.text) != "" {
		return nil, false, errSyntax
	}
	out := &parsed{Call: Call{Name: root.attrs["name"]}, named: map[string]string{}}
	for _, child := range root.children {
		name := child.name
		if child.name == "parameter" {
			name = child.attrs["name"]
			if len(child.attrs) != 1 {
				return nil, false, errSyntax
			}
		} else if root.name != "tool_use" || len(child.attrs) != 0 {
			return nil, false, errSyntax
		}
		if name == "" || len(child.children) != 0 {
			return nil, false, errSyntax
		}
		if _, exists := out.named[name]; exists {
			return nil, false, errSyntax
		}
		out.named[name] = child.text
	}
	return out, false, nil
}
func trailing(tail string) (*parsed, bool, error) {
	// Multiple calls and trailing JSON are ambiguous, never partial dispatch.
	if candidate(tail) != "" || strings.HasPrefix(tail, "{") || strings.HasPrefix(tail, "[") || strings.HasPrefix(tail, "<") {
		return nil, false, errSyntax
	}
	return nil, true, nil
}
func jsonPrefix(s string) (json.RawMessage, string, error) {
	d := json.NewDecoder(strings.NewReader(s))
	var raw json.RawMessage
	if err := d.Decode(&raw); err != nil {
		return nil, "", err
	}
	if _, err := decodeJSON(raw); err != nil {
		return nil, "", err
	}
	return raw, strings.TrimSpace(s[d.InputOffset():]), nil
}
func attributes(s string) (map[string]string, error) {
	out := map[string]string{}
	for s != "" {
		m := attribute.FindStringSubmatch(s)
		if m == nil || (m[1] != "id" && m[1] != "name") {
			return nil, errSyntax
		}
		if _, ok := out[m[1]]; ok {
			return nil, errSyntax
		}
		value := m[2]
		if m[3] != "" {
			value = m[3]
		}
		if m[4] != "" {
			value = m[4]
		}
		out[m[1]] = value
		rest := s[len(m[0]):]
		if rest != "" && !strings.ContainsRune(" \t\r\n", rune(rest[0])) {
			return nil, errSyntax
		}
		s = strings.TrimSpace(rest)
	}
	return out, nil
}

type element struct {
	name     string
	attrs    map[string]string
	text     string
	children []*element
}

func readXML(s string) (*element, error) {
	d := xml.NewDecoder(strings.NewReader(s))
	var root *element
	var stack []*element
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			if len(stack) > 64 || (token.Name.Space != "" && token.Name.Space != "antml") {
				return nil, errSyntax
			}
			node := &element{name: token.Name.Local, attrs: map[string]string{}}
			for _, attr := range token.Attr {
				if attr.Name.Space != "" || (attr.Name.Local != "id" && attr.Name.Local != "name") {
					return nil, errSyntax
				}
				if _, ok := node.attrs[attr.Name.Local]; ok {
					return nil, errSyntax
				}
				node.attrs[attr.Name.Local] = attr.Value
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, errSyntax
				}
				root = node
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, node)
			}
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errSyntax
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 {
				if strings.TrimSpace(string(token)) != "" {
					return nil, errSyntax
				}
			} else {
				stack[len(stack)-1].text += string(token)
			}
		default:
			return nil, errSyntax
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, errSyntax
	}
	return root, nil
}
func (p *parsed) arguments(schema json.RawMessage) error {
	if p.named == nil {
		return nil
	}
	value, err := decodeJSON(schema)
	if err != nil {
		return err
	}
	obj, _ := value.(map[string]any)
	props, _ := obj["properties"].(map[string]any)
	args := map[string]any{}
	for name, raw := range p.named {
		prop, _ := props[name].(map[string]any)
		// Strings retain whitespace and JSON-looking content. Non-string named
		// parameters must themselves contain complete JSON; never guess a value.
		if typ, ok := prop["type"].(string); ok && typ != "string" {
			v, err := decodeJSON([]byte(raw))
			if err != nil {
				return err
			}
			args[name] = v
		} else {
			args[name] = raw
		}
	}
	p.Arguments, err = json.Marshal(args)
	return err
}
