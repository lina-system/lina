// SPDX-License-Identifier: Apache-2.0
package work

// WorkerDefinition fixes the permitted task before a worker is selected or run.
type WorkerDefinition struct {
	Worker     string   `json:"worker,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Skill      string   `json:"skill,omitempty"`
	Repository string   `json:"repository"`
	Base       string   `json:"base"`
	Branch     string   `json:"branch"`
	What       string   `json:"what"`
	Why        string   `json:"why"`
	Scope      []string `json:"scope"`
	NotTouch   []string `json:"not_touch"`
	Done       string   `json:"done"`
	DesignRefs []string `json:"design_refs"`
	WriteArea  []string `json:"write_area"`
}
