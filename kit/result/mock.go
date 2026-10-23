// SPDX-License-Identifier: Apache-2.0
package result

import "regexp"

// Mock contains only pinned assets and the executable checklist. Page bytes are
// never part of a result envelope. Only the Core mock pipeline may present it.
type Mock struct {
	Request    string      `json:"request"`
	Checklist  []MockItem  `json:"checklist"`
	Page       AssetRef    `json:"page"`
	PNG        AssetRef    `json:"png"`
	Previous   *ResultRef  `json:"previous"`
	StopReason string      `json:"stop_reason"`
	Checks     []MockCheck `json:"checks"`
}
type MockItem struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Target      string `json:"target"`
}
type MockCheck struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
}

func (*Mock) resultBody() {}

var mockID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func ValidateMockChecklist(items []MockItem) error {
	if len(items) == 0 || len(items) > 64 {
		return bad()
	}
	seen := map[string]bool{}
	for _, item := range items {
		if !mockID.MatchString(item.ID) || seen[item.ID] || !nonempty(item.Description) || len(item.Description) > 1000 || !one(item.Action, "none", "toggle", "select", "input") {
			return bad()
		}
		if item.Action == "toggle" && !mockID.MatchString(item.Target) || item.Action != "toggle" && item.Target != "" {
			return bad()
		}
		seen[item.ID] = true
	}
	return nil
}
func (v *Mock) validate() error {
	if !nonempty(v.Request) || !asset(&v.Page) || !asset(&v.PNG) || v.Page.ID == v.PNG.ID || v.StopReason != "all_passed" {
		return bad()
	}
	if err := ValidateMockChecklist(v.Checklist); err != nil {
		return err
	}
	if v.Previous != nil && (!nonempty(v.Previous.ID) || v.Previous.Revision <= 0) {
		return bad()
	}
	required := map[string]bool{"elements": false, "controls": false, "tokens": false, "network": false, "errors": false, "layout_390": false, "layout_1280": false}
	for _, c := range v.Checks {
		seen, ok := required[c.ID]
		if !ok || seen || !c.Passed {
			return bad()
		}
		required[c.ID] = true
	}
	for _, pass := range required {
		if !pass {
			return bad()
		}
	}
	return nil
}
