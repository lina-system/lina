// SPDX-License-Identifier: Apache-2.0
package result

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrMalformed = errors.New("malformed")

// Limits may tighten the defaults; a zero field uses its default. Limits cannot
// widen the closed contract. Points counts all chart values, not each series.
type Limits struct{ Bytes, Items, Points int }

func (l Limits) bounded() Limits {
	if l.Bytes <= 0 || l.Bytes > 1<<20 {
		l.Bytes = 1 << 20
	}
	if l.Items <= 0 || l.Items > 1000 {
		l.Items = 1000
	}
	if l.Points <= 0 || l.Points > 10000 {
		l.Points = 10000
	}
	return l
}

func Validate(kind Type, raw json.RawMessage, lim Limits) (Body, error) {
	lim = lim.bounded()
	if len(raw) > lim.Bytes || !utf8.Valid(raw) {
		return nil, fmt.Errorf("%w: body size or encoding", ErrMalformed)
	}
	var b Body
	switch kind {
	case SurfaceType:
		return validateSurface(raw)
	case SummaryType:
		b = &Summary{}
	case TableType:
		b = &Table{}
	case DiagramType:
		b = &Diagram{}
	case ChartType:
		b = &Chart{}
	case DraftType:
		b = &Draft{}
	case ComparisonType:
		b = &Comparison{}
	case PlanType:
		b = &Plan{}
	case FileType:
		b = &File{}
	default:
		return nil, fmt.Errorf("%w: result type", ErrMalformed)
	}
	// The shape pass checks exact field names, required/non-null fields, and
	// duplicates before Go's decoder can silently discard or normalize them.
	if err := Decode(raw, b); err != nil {
		return nil, err
	}
	if kind == DraftType {
		if err := blockShapes(raw); err != nil {
			return nil, fmt.Errorf("%w: block fields", ErrMalformed)
		}
	}
	if err := check(b, lim); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrMalformed, err)
	}
	return b, nil
}

// Decode accepts one strict JSON value matching dst, including required fields.
// An omitempty tag marks an optional field, and a pointer permits explicit null.
func Decode(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := tokens(d, 0); err != nil {
		return fmt.Errorf("%w: JSON shape", ErrMalformed)
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("%w: trailing JSON", ErrMalformed)
	}
	if err := shape(raw, reflect.TypeOf(dst).Elem()); err != nil {
		return fmt.Errorf("%w: %s", ErrMalformed, err)
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return fmt.Errorf("%w: field value", ErrMalformed)
	}
	return nil
}
func tokens(d *json.Decoder, depth int) error {
	if depth > 64 {
		return ErrMalformed
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return ErrMalformed
			}
			seen[s] = true
			if err := tokens(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := tokens(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrMalformed
	}
	_, err = d.Token()
	return err
}

var rawType = reflect.TypeFor[json.RawMessage]()

func shape(raw []byte, t reflect.Type) error {
	raw = bytes.TrimSpace(raw)
	if t.Kind() == reflect.Pointer {
		if bytes.Equal(raw, []byte("null")) {
			return nil
		}
		return shape(raw, t.Elem())
	}
	if bytes.Equal(raw, []byte("null")) {
		return errors.New("null field")
	}
	if t == rawType {
		return nil
	}
	switch {
	case t.Kind() == reflect.Struct:
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return errors.New("expected object")
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, opt, _ := strings.Cut(f.Tag.Get("json"), ",")
			v, ok := obj[name]
			if !ok {
				if opt == "omitempty" {
					continue
				}
				return fmt.Errorf("missing %s", name)
			}
			if err := shape(v, f.Type); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			delete(obj, name)
		}
		if len(obj) != 0 {
			return errors.New("unknown field")
		}
	case t.Kind() == reflect.Slice:
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return errors.New("expected array")
		}
		for _, v := range arr {
			if err := shape(v, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
func one(s string, allowed ...string) bool { return slices.Contains(allowed, s) }
func nonempty(s string) bool               { return strings.TrimSpace(s) != "" }
func asset(a *AssetRef) bool               { return a != nil && nonempty(a.ID) && a.Revision > 0 }
func bad() error                           { return errors.New("invalid body fields") }
func check(b Body, l Limits) error {
	// Bound every collection, except chart points which have their own total.
	if !collections(reflect.ValueOf(b), l.Items, l.Points) {
		return errors.New("collection limit")
	}
	switch v := b.(type) {
	case *Summary:
		if v.Length <= 0 || len(v.Outline) == 0 {
			return bad()
		}
		for _, s := range v.Outline {
			if !nonempty(s.Heading) {
				return bad()
			}
		}
		for _, q := range v.Questions {
			if !nonempty(q.Question) {
				return bad()
			}
		}
	case *Table:
		return table(v)
	case *Diagram:
		if !one(v.Kind, "flow", "sequence", "tree") || !one(v.Direction, "right", "left", "down", "up") || len(v.Nodes) == 0 {
			return bad()
		}
		nodes := map[string]bool{}
		parents := map[string]string{}
		for _, n := range v.Nodes {
			if !nonempty(n.ID) || !nonempty(n.Label) || nodes[n.ID] {
				return bad()
			}
			nodes[n.ID] = true
		}
		for _, e := range v.Edges {
			if !nodes[e.From] || !nodes[e.To] {
				return bad()
			}
			if v.Kind == "tree" {
				if _, ok := parents[e.To]; ok {
					return bad()
				}
				parents[e.To] = e.From
			}
		}
		if v.Kind == "tree" {
			if len(v.Edges) != len(v.Nodes)-1 {
				return bad()
			}
			for id := range nodes {
				seen := map[string]bool{}
				for p := id; p != ""; p = parents[p] {
					if seen[p] {
						return bad()
					}
					seen[p] = true
				}
			}
		}
		for _, g := range v.Groups {
			if !nonempty(g.Label) {
				return bad()
			}
			seen := map[string]bool{}
			for _, id := range g.Nodes {
				if !nodes[id] || seen[id] {
					return bad()
				}
				seen[id] = true
			}
		}
	case *Chart:
		if !one(v.Kind, "bar", "line", "area", "pie", "scatter") || len(v.Series) == 0 {
			return bad()
		}
		count := 0
		for _, s := range v.Series {
			if !nonempty(s.Name) || len(s.Values) == 0 {
				return bad()
			}
			count += len(s.Values)
			for _, p := range s.Values {
				var x any
				d := json.NewDecoder(bytes.NewReader(p.X))
				d.UseNumber()
				if d.Decode(&x) != nil {
					return bad()
				}
				switch x.(type) {
				case string:
					if v.Kind == "scatter" {
						return bad()
					}
				case json.Number:
				default:
					return bad()
				}
				if v.Kind == "pie" && p.Y < 0 {
					return bad()
				}
			}
		}
		if count > l.Points {
			return bad()
		}
	case *Draft:
		if len(v.Blocks) == 0 {
			return bad()
		}
		if v.FromSummary != nil && (!nonempty(v.FromSummary.ID) || v.FromSummary.Revision <= 0) {
			return bad()
		}
		for _, b := range v.Blocks {
			if err := block(b); err != nil {
				return err
			}
		}
	case *Comparison:
		if len(v.Targets) == 0 || v.Apply != nil && !nonempty(*v.Apply) {
			return bad()
		}
		for _, t := range v.Targets {
			if !nonempty(t.Label) {
				return bad()
			}
			for _, h := range t.Hunks {
				if h.OldStart < 0 || h.NewStart < 0 || h.OldCount < 0 || h.NewCount < 0 || h.OldStart > len(strings.Split(t.Old, "\n"))-h.OldCount || h.NewStart > len(strings.Split(t.New, "\n"))-h.NewCount {
					return bad()
				}
			}
		}
	case *Plan:
		if len(v.Entries) == 0 || v.Confirm != nil && !nonempty(*v.Confirm) {
			return bad()
		}
		seen := map[string]bool{}
		for _, e := range v.Entries {
			if !nonempty(e.ID) || seen[e.ID] || !nonempty(e.Title) || !one(e.Kind, "goal", "project", "todo", "area", "memo", "resource") || !one(e.State, "planned", "active", "blocked", "done", "cancelled") {
				return bad()
			}
			if e.Parent != nil && !seen[*e.Parent] {
				return bad()
			}
			seen[e.ID] = true
		}
	case *File:
		if !asset(&v.Asset) || !nonempty(v.Name) || !fileMediaType(v.MediaType) || v.Size < 0 {
			return bad()
		}
	default:
		return bad()
	}
	return nil
}
func collections(v reflect.Value, items, points int) bool {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return true
		}
		return collections(v.Elem(), items, points)
	}
	if v.Type() == rawType {
		return true
	}
	switch {
	case v.Kind() == reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !collections(v.Field(i), items, points) {
				return false
			}
		}
	case v.Kind() == reflect.Slice:
		max := items
		if v.Type().Elem() == reflect.TypeFor[Point]() {
			max = points
		}
		if v.Len() > max {
			return false
		}
		for i := 0; i < v.Len(); i++ {
			if !collections(v.Index(i), items, points) {
				return false
			}
		}
	}
	return true
}
func table(t *Table) error {
	if len(t.Columns) == 0 || len(t.Columns) > 100 {
		return bad()
	}
	for _, c := range t.Columns {
		if !nonempty(c.Name) || !one(c.Type, "text", "number", "date", "boolean") {
			return bad()
		}
	}
	rows := t.Rows
	if t.Totals != nil {
		rows = append(append([][]json.RawMessage(nil), rows...), t.Totals)
	}
	for _, row := range rows {
		if len(row) != len(t.Columns) {
			return bad()
		}
		for i, cell := range row {
			var val any
			d := json.NewDecoder(bytes.NewReader(cell))
			d.UseNumber()
			if d.Decode(&val) != nil {
				return bad()
			}
			switch t.Columns[i].Type {
			case "text":
				if _, ok := val.(string); !ok {
					return bad()
				}
			case "number":
				if _, ok := val.(json.Number); !ok {
					return bad()
				}
			case "boolean":
				if _, ok := val.(bool); !ok {
					return bad()
				}
			case "date":
				s, ok := val.(string)
				if !ok {
					return bad()
				}
				if _, err := time.Parse("2006-01-02", s); err != nil {
					return bad()
				}
			}
		}
	}
	return nil
}
func block(b Block) error {
	if !one(b.Audience, "", "recipient", "person") {
		return bad()
	}
	allowed := map[string]bool{"kind": true, "audience": true}
	var fields []string
	switch b.Kind {
	case "heading":
		fields = []string{"text", "level"}
		if b.Level < 1 || b.Level > 6 || !nonempty(b.Text) {
			return bad()
		}
	case "paragraph", "quote":
		fields = []string{"text"}
		if !nonempty(b.Text) {
			return bad()
		}
	case "list":
		fields = []string{"items"}
		if len(b.Items) == 0 {
			return bad()
		}
	case "table":
		fields = []string{"table"}
		if b.Table == nil {
			return bad()
		}
		if err := table(b.Table); err != nil {
			return err
		}
	case "code":
		fields = []string{"text", "language"}
		if b.Text == "" {
			return bad()
		}
	case "image":
		fields = []string{"asset", "text"}
		if !asset(b.Asset) || !nonempty(b.Text) {
			return bad()
		}
	case "citation":
		fields = []string{"anchor", "text"}
		a := b.Anchor
		if a == nil || !nonempty(b.Text) || a.Start < 0 || a.End <= a.Start || (a.EventID == "") == (a.Asset == nil) || a.Asset != nil && !asset(a.Asset) {
			return bad()
		}
	default:
		return bad()
	}
	for _, f := range fields {
		allowed[f] = true
	}
	raw, _ := json.Marshal(b)
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(raw, &obj)
	for f := range obj {
		if !allowed[f] {
			return bad()
		}
	}
	return nil
}

func blockShapes(raw []byte) error {
	var draft struct {
		Blocks []map[string]json.RawMessage `json:"blocks"`
	}
	if err := json.Unmarshal(raw, &draft); err != nil {
		return err
	}
	for _, b := range draft.Blocks {
		var kind string
		if err := json.Unmarshal(b["kind"], &kind); err != nil {
			return err
		}
		allowed := map[string]bool{"kind": true, "audience": true}
		required := []string{}
		switch kind {
		case "heading":
			required = []string{"text", "level"}
		case "paragraph", "quote":
			required = []string{"text"}
		case "list":
			required = []string{"items"}
		case "table":
			required = []string{"table"}
		case "code":
			required = []string{"text"}
			allowed["language"] = true
		case "image":
			required = []string{"asset", "text"}
		case "citation":
			required = []string{"anchor", "text"}
		default:
			return ErrMalformed
		}
		for _, f := range required {
			v, ok := b[f]
			if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				return ErrMalformed
			}
			allowed[f] = true
		}
		for f := range b {
			if !allowed[f] {
				return ErrMalformed
			}
		}
	}
	return nil
}

// A file result is an opaque material reference. MIME metadata selects a viewer
// or download; it never authorizes execution or remote URL loading.
func fileMediaType(value string) bool {
	media, params, err := mime.ParseMediaType(value)
	return err == nil && len(params) == 0 && strings.Contains(media, "/") && media == value
}
