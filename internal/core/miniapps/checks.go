// SPDX-License-Identifier: Apache-2.0
package miniapps

import (
	"regexp"
	"slices"
	"strings"

	"github.com/thisisjun786/lina/kit/result"
)

// CheckDimensions is the fixed order shared with drawn mocks. A revision goes
// live only when every dimension passes.
var CheckDimensions = []string{"size", "tokens", "network", "elements", "controls", "errors", "layout_390", "layout_1280"}

var (
	rawColor   = regexp.MustCompile(`(?i)#[0-9a-f]{3,8}\b|\b(rgba?|hsla?)\s*\(`)
	fontDecl   = regexp.MustCompile(`(?i)(font-family|font)\s*:\s*([^;}"']+)`)
	tagPattern = regexp.MustCompile(`(?is)<\s*([a-z][a-z0-9-]*)\b([^>]*)>`)
	checkAttr  = regexp.MustCompile(`(?i)\bdata-check\s*=\s*"([^"]+)"`)
	idAttr     = regexp.MustCompile(`(?i)\bid\s*=\s*"([^"]+)"`)
	roleAttr   = regexp.MustCompile(`(?i)\brole\s*=\s*"([^"]+)"`)
	remoteAddr = regexp.MustCompile(`(?i)https?://|url\(\s*['"]?(https?:)?//|@import\b|\bsrc\s*=\s*['"]?(https?:)?//|\bhref\s*=\s*['"]?(https?:)?//`)
)

// controlTags and controlRoles are what a checklist item may point at when it
// asks the person to act. A plain element cannot carry an action.
var controlTags = []string{"button", "input", "select", "textarea", "summary"}

var controlRoles = []string{"button", "checkbox", "switch", "radio", "textbox", "combobox"}

// element is the part of a parsed tag the checks need.
type element struct {
	tag  string
	id   string
	role string
	mark string
}

func elements(index string) []element {
	var out []element
	for _, match := range tagPattern.FindAllStringSubmatch(index, -1) {
		e := element{tag: strings.ToLower(match[1])}
		if m := checkAttr.FindStringSubmatch(match[2]); m != nil {
			e.mark = m[1]
		}
		if m := idAttr.FindStringSubmatch(match[2]); m != nil {
			e.id = m[1]
		}
		if m := roleAttr.FindStringSubmatch(match[2]); m != nil {
			e.role = strings.ToLower(m[1])
		}
		out = append(out, e)
	}
	return out
}

func (e element) control() bool {
	return slices.Contains(controlTags, e.tag) || slices.Contains(controlRoles, e.role)
}

func staticChecks(files map[string][]byte, checklist []result.MockItem) map[string]bool {
	checks := map[string]bool{"size": true, "tokens": true, "network": true, "elements": true, "controls": true, "errors": true}
	if err := ValidateFiles(files); err != nil {
		checks["size"] = false
		return checks
	}
	index := string(files["index.html"])
	markers := map[string]element{}
	ids := map[string]bool{}
	duplicate := false
	for _, e := range elements(index) {
		if e.id != "" {
			ids[e.id] = true
		}
		if e.mark == "" {
			continue
		}
		if _, seen := markers[e.mark]; seen {
			duplicate = true
		}
		markers[e.mark] = e
	}
	if duplicate {
		checks["elements"] = false
	}
	for _, item := range checklist {
		e, ok := markers[item.ID]
		if !ok {
			// The item has no element at all; when it asks for an action the
			// control is missing too.
			checks["elements"] = false
			if item.Action != "none" {
				checks["controls"] = false
			}
			continue
		}
		if item.Action == "none" {
			continue
		}
		if !e.control() {
			checks["controls"] = false
		}
		if item.Action == "toggle" && !ids[item.Target] {
			checks["controls"] = false
		}
	}
	declared := map[string]bool{}
	for _, item := range checklist {
		declared[item.ID] = true
	}
	for mark := range markers {
		if !declared[mark] {
			// An extra data-check is a control the person did not ask for.
			checks["controls"] = false
			break
		}
	}
	for name, data := range files {
		text := string(data)
		if remoteAddr.MatchString(text) {
			checks["network"] = false
		}
		if name == "index.html" || name == "app.js" || name == "app.css" {
			if rawColor.MatchString(text) {
				checks["tokens"] = false
			}
			for _, match := range fontDecl.FindAllStringSubmatch(text, -1) {
				if !strings.Contains(match[2], "var(") && !strings.Contains(match[2], "inherit") {
					checks["tokens"] = false
				}
			}
		}
	}
	return checks
}

// Static runs the rules that do not need a browser. Callers still run the
// browser dimensions before a revision goes live.
func Static(files map[string][]byte, checklist []result.MockItem) ([]result.MockCheck, error) {
	if result.ValidateMockChecklist(checklist) != nil || ValidateFiles(files) != nil {
		return nil, ErrInvalid
	}
	passed := staticChecks(files, checklist)
	checks := make([]result.MockCheck, 0, len(CheckDimensions))
	for _, id := range CheckDimensions {
		value, ok := passed[id]
		checks = append(checks, result.MockCheck{ID: id, Passed: ok && value})
	}
	return checks, nil
}

func mergeChecks(base []result.MockCheck, extra map[string]bool) []result.MockCheck {
	out := make([]result.MockCheck, 0, len(CheckDimensions))
	values := map[string]bool{}
	for _, check := range base {
		values[check.ID] = check.Passed
	}
	for id, passed := range extra {
		if current, ok := values[id]; ok {
			values[id] = current && passed
		} else {
			values[id] = passed
		}
	}
	for _, id := range CheckDimensions {
		out = append(out, result.MockCheck{ID: id, Passed: values[id]})
	}
	return out
}

func allPassed(checks []result.MockCheck) bool {
	if len(checks) != len(CheckDimensions) {
		return false
	}
	for _, check := range checks {
		if !slices.Contains(CheckDimensions, check.ID) || !check.Passed {
			return false
		}
	}
	return true
}
