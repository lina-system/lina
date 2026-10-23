// SPDX-License-Identifier: Apache-2.0
package mocks

import (
	"encoding/xml"
	"html"
	"io"
	"regexp"
	"strings"

	"github.com/thisisjun786/lina/kit/result"
)

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var tags = strings.Fields("main section article header footer nav aside div span h1 h2 h3 p strong em small ul ol li table thead tbody tr th td label button select option input textarea br hr")
var classes = strings.Fields("stack row grid card muted accent compact")

func contains(list []string, s string) bool {
	for _, v := range list {
		if s == v {
			return true
		}
	}
	return false
}

type element struct {
	tag   string
	attrs map[string]string
	text  strings.Builder
}

// CheckFragment accepts a bounded XHTML fragment, rebuilding it from the closed
// tag/attribute set. Any rejection discards the whole page; it never sanitizes a
// failed page into a passing revision. Scripts and CSS belong to the template.
func CheckFragment(fragment string, checklist []result.MockItem) (string, map[string]bool) {
	checks := map[string]bool{"elements": true, "controls": true, "tokens": true, "network": true}
	if len(fragment) == 0 || len(fragment) > 128<<10 || result.ValidateMockChecklist(checklist) != nil {
		return "", map[string]bool{}
	}
	d := xml.NewDecoder(strings.NewReader("<root>" + fragment + "</root>"))
	var stack []*element
	var out strings.Builder
	found := map[string]*element{}
	ids := map[string]bool{}
	var controls []*element
	valid := true
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			valid = false
			break
		}
		switch v := tok.(type) {
		case xml.StartElement:
			if v.Name.Local == "root" && len(stack) == 0 {
				stack = append(stack, &element{tag: "root"})
				continue
			}
			if len(stack) > 32 || v.Name.Space != "" || !contains(tags, v.Name.Local) {
				valid = false
				checks["network"] = false
				continue
			}
			el := &element{tag: v.Name.Local, attrs: map[string]string{}}
			out.WriteString("<" + el.tag)
			for _, a := range v.Attr {
				name := a.Name.Local
				if _, ok := el.attrs[name]; ok || a.Name.Space != "" {
					valid = false
				}
				el.attrs[name] = a.Value
				switch name {
				case "id", "data-check", "data-target", "for":
					if !idPattern.MatchString(a.Value) {
						valid = false
					}
				case "class":
					for _, c := range strings.Fields(a.Value) {
						if !contains(classes, c) {
							checks["tokens"] = false
						}
					}
				case "data-action":
					if !contains([]string{"toggle", "select", "input"}, a.Value) {
						checks["controls"] = false
					}
				case "type":
					if el.tag != "input" || !contains([]string{"text", "checkbox", "radio", "number", "date"}, a.Value) {
						checks["controls"] = false
					}
				case "value", "placeholder", "aria-label":
				case "checked", "selected", "hidden":
					if a.Value != "" && a.Value != name {
						valid = false
					}
				case "colspan", "rowspan":
					if !contains([]string{"1", "2", "3", "4", "5", "6"}, a.Value) {
						valid = false
					}
				case "style":
					checks["tokens"] = false
				default:
					valid = false
					checks["network"] = false
				}
				out.WriteString(" " + name + "=\"" + html.EscapeString(a.Value) + "\"")
			}
			out.WriteString(">")
			if id := el.attrs["id"]; id != "" {
				if ids[id] {
					valid = false
				}
				ids[id] = true
			}
			if key := el.attrs["data-check"]; key != "" {
				if found[key] != nil {
					checks["elements"] = false
				}
				found[key] = el
			}
			if contains([]string{"button", "select", "input", "textarea"}, el.tag) {
				controls = append(controls, el)
			} else if el.attrs["data-action"] != "" {
				checks["controls"] = false
			}
			stack = append(stack, el)
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1].tag != v.Name.Local {
				valid = false
				continue
			}
			stack = stack[:len(stack)-1]
			if v.Name.Local != "root" && !contains([]string{"input", "br", "hr"}, v.Name.Local) {
				out.WriteString("</" + v.Name.Local + ">")
			}
		case xml.CharData:
			out.WriteString(html.EscapeString(string(v)))
			for _, el := range stack {
				el.text.Write(v)
			}
		default:
			valid = false
			checks["network"] = false
		}
	}
	expected := map[string]result.MockItem{}
	for _, item := range checklist {
		expected[item.ID] = item
		el := found[item.ID]
		if el == nil {
			checks["elements"] = false
			continue
		}
		label := strings.TrimSpace(el.text.String() + " " + el.attrs["aria-label"] + " " + el.attrs["placeholder"])
		if !strings.Contains(label, item.Description) {
			checks["elements"] = false
		}
		if item.Action == "none" {
			if el.attrs["data-action"] != "" {
				checks["controls"] = false
			}
		} else if el.attrs["data-action"] != item.Action || el.attrs["data-target"] != item.Target {
			checks["controls"] = false
		}
	}
	for _, el := range controls {
		item, ok := expected[el.attrs["data-check"]]
		if !ok || item.Action == "none" || el.attrs["data-action"] != item.Action {
			checks["controls"] = false
			continue
		}
		switch item.Action {
		case "toggle":
			if el.tag != "button" || !ids[item.Target] {
				checks["controls"] = false
			}
		case "select":
			if el.tag != "select" && !(el.tag == "input" && contains([]string{"checkbox", "radio"}, el.attrs["type"])) {
				checks["controls"] = false
			}
		case "input":
			if el.tag != "textarea" && !(el.tag == "input" && contains([]string{"text", "number", "date"}, el.attrs["type"])) {
				checks["controls"] = false
			}
		}
	}
	if !valid {
		checks["network"] = false
	}
	for _, v := range checks {
		if !v {
			return "", checks
		}
	}
	return out.String(), checks
}
