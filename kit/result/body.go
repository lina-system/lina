// SPDX-License-Identifier: Apache-2.0

// Package result validates closed, data-only result bodies and renders text.
// It performs no IO and never interprets a body as instructions or executable code.
package result

import "encoding/json"

type Type string

const (
	SummaryType    Type = "summary"
	TableType      Type = "table"
	DiagramType    Type = "diagram"
	ChartType      Type = "chart"
	DraftType      Type = "document_draft"
	ComparisonType Type = "comparison"
	PlanType       Type = "plan_view"
	FileType       Type = "file"
)

type Body interface{ resultBody() }
type Summary struct {
	Outline   []Section  `json:"outline"`
	KeyPoints []string   `json:"key_points"`
	Decisions []string   `json:"decisions"`
	Questions []Question `json:"open_questions"`
	Scope     Coverage   `json:"scope"`
	Length    float64    `json:"length"`
}
type Section struct {
	Heading string `json:"heading"`
	Notes   string `json:"notes,omitempty"`
}
type Question struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}
type Coverage struct {
	Includes []string `json:"includes"`
	Excludes []string `json:"excludes"`
}
type Table struct {
	Columns []Column            `json:"columns"`
	Rows    [][]json.RawMessage `json:"rows"`
	Totals  []json.RawMessage   `json:"totals,omitempty"`
}
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Unit string `json:"unit"`
}
type Diagram struct {
	Kind      string  `json:"kind"`
	Direction string  `json:"direction"`
	Nodes     []Node  `json:"nodes"`
	Edges     []Edge  `json:"edges"`
	Groups    []Group `json:"groups"`
}
type Node struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type Edge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
}
type Group struct {
	Label string   `json:"label"`
	Nodes []string `json:"nodes"`
}
type Chart struct {
	Kind   string   `json:"kind"`
	Series []Series `json:"series"`
	X      Axis     `json:"x_axis"`
	Y      Axis     `json:"y_axis"`
}
type Series struct {
	Name   string  `json:"name"`
	Values []Point `json:"values"`
}
type Point struct {
	X json.RawMessage `json:"x"`
	Y float64         `json:"y"`
}
type Axis struct {
	Label string `json:"label"`
	Unit  string `json:"unit"`
}
type Draft struct {
	Recipient   string     `json:"recipient,omitempty"`
	Blocks      []Block    `json:"blocks"`
	FromSummary *ResultRef `json:"from_summary"`
}

// Block is a tagged union; validation refuses fields from any other variant.
type Block struct {
	Audience string    `json:"audience,omitempty"`
	Kind     string    `json:"kind"`
	Text     string    `json:"text,omitempty"`
	Level    int       `json:"level,omitempty"`
	Items    []string  `json:"items,omitempty"`
	Table    *Table    `json:"table,omitempty"`
	Language string    `json:"language,omitempty"`
	Asset    *AssetRef `json:"asset,omitempty"`
	Anchor   *Anchor   `json:"anchor,omitempty"`
}
type ResultRef struct {
	ID       string `json:"result_id"`
	Revision int    `json:"revision"`
}
type AssetRef struct {
	ID       string `json:"asset_id"`
	Revision int    `json:"revision"`
}

// Anchor identifies a passage in either a ledger event or one asset revision.
type Anchor struct {
	EventID string    `json:"event_id,omitempty"`
	Asset   *AssetRef `json:"asset,omitempty"`
	Start   int       `json:"start"`
	End     int       `json:"end"`
}
type Comparison struct {
	Targets []Target `json:"targets"`
	Apply   *string  `json:"apply"`
}
type Target struct {
	Label string `json:"label"`
	Old   string `json:"old"`
	New   string `json:"new"`
	Hunks []Hunk `json:"hunks"`
}
type Hunk struct {
	OldStart int `json:"old_start"`
	OldCount int `json:"old_count"`
	NewStart int `json:"new_start"`
	NewCount int `json:"new_count"`
}
type Plan struct {
	Entries []Entry `json:"entries"`
	Confirm *string `json:"confirm"`
}
type Entry struct {
	Done      string   `json:"done,omitempty"`
	Stage     string   `json:"stage,omitempty"`
	DependsOn []string `json:"depends_on,omitempty"`
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	Parent    *string  `json:"parent"`
}
type File struct {
	Asset     AssetRef `json:"asset"`
	Name      string   `json:"name"`
	MediaType string   `json:"media_type"`
	Size      int64    `json:"size"`
}

func (*Summary) resultBody()    {}
func (*Table) resultBody()      {}
func (*Diagram) resultBody()    {}
func (*Chart) resultBody()      {}
func (*Draft) resultBody()      {}
func (*Comparison) resultBody() {}
func (*Plan) resultBody()       {}
func (*File) resultBody()       {}
