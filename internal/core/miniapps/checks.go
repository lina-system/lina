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
	dataCheck  = regexp.MustCompile(`data-check\s*=\s*"([^"]+)"`)
	remoteAddr = regexp.MustCompile(`(?i)https?://|url\(\s*['"]?(https?:)?//|@import\b|\bsrc\s*=\s*['"]?(https?:)?//|\bhref\s*=\s*['"]?(https?:)?//`)
)

func staticChecks(files map[string][]byte, checklist []result.MockItem) map[string]bool {
	checks := map[string]bool{"size": true, "tokens": true, "network": true, "elements": true, "controls": true, "errors": true}
	if err := ValidateFiles(files); err != nil {
		checks["size"] = false
		return checks
	}
	index := string(files["index.html"])
	found := map[string]bool{}
	for _, match := range dataCheck.FindAllStringSubmatch(index, -1) {
		found[match[1]] = true
	}
	for _, item := range checklist {
		if !found[item.ID] {
			checks["elements"] = false
		}
	}
	if len(found) > len(checklist) {
		// An extra data-check is a control the person did not ask for.
		checks["controls"] = false
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
