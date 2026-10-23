// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/kit/protocol"
)

func recordValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func (r *conversationRuntime) Record(ctx context.Context, s clientsrv.Session, p protocol.RecordRequest) (result protocol.RecordResult, err error) {
	result = protocol.RecordResult{Operation: p.Operation, Rows: []protocol.RecordRow{}}
	if err = r.authorize(s, nil); err != nil {
		return
	}
	if s.Epoch != r.epoch {
		return result, &clientsrv.OperationError{Code: protocol.ErrorStaleEpoch}
	}
	if r.records == nil {
		return result, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
	}
	defer func() {
		if err == nil {
			return
		}
		if errors.Is(err, records.ErrUncertain) {
			code := "record_operation_unknown"
			result.Code = &code
			err = nil
			return
		}
		for _, known := range []error{records.ErrRecordsChanged, records.ErrStaleRevision, records.ErrNotFound, records.ErrInvalid, records.ErrAmbiguousDate, records.ErrDependencyCycle} {
			if errors.Is(err, known) {
				code := known.Error()
				result.Code = &code
				err = nil
				return
			}
		}
	}()
	switch p.Operation {
	case "query":
		if p.Ref != nil || p.Text != nil || p.Token != nil || p.Revision != nil {
			return result, malformedManagement()
		}
		rows, e := r.records.Query(ctx, records.Params{Person: s.PersonID, View: recordValue(p.View), Search: recordValue(p.Search), After: recordValue(p.After), Day: recordValue(p.Day), Zone: recordValue(p.Zone), Limit: 51})
		if e != nil {
			return result, e
		}
		if len(rows) > 50 {
			last := rows[49].ID
			result.Next = &last
			rows = rows[:50]
		}
		for _, row := range rows {
			v, e := r.recordRow(ctx, row, false)
			if e != nil {
				return result, e
			}
			result.Rows = append(result.Rows, v)
		}
	case "read":
		if p.Ref == nil || p.View != nil || p.Search != nil || p.Text != nil || p.Token != nil || p.Day != nil || p.Zone != nil {
			return result, malformedManagement()
		}
		row, e := r.records.ViewRecord(ctx, s.PersonID, *p.Ref)
		if e != nil {
			return result, e
		}
		if p.Revision != nil && *p.Revision != row.Revision {
			return result, records.ErrStaleRevision
		}
		v, e := r.recordRow(ctx, row, true)
		if e != nil {
			return result, e
		}
		v, result.Next, e = recordPage(v, p.After)
		if e != nil {
			return result, e
		}
		result.Rows = append(result.Rows, v)
	default:
		return r.recordCapture(ctx, s, p)
	}
	return
}
func (r *conversationRuntime) recordRow(ctx context.Context, row records.Row, body bool) (protocol.RecordRow, error) {
	out := protocol.RecordRow{Ref: row.ID, Revision: row.Revision, Kind: row.Kind, State: row.State, Title: row.Title, Chips: []protocol.RecordChip{}, Overdue: row.Overdue, Blocked: row.Blocked}
	if len(row.ID) > 256 || len(row.Revision) > 64 || len(row.Kind) > 64 || len(row.State) > 64 {
		return out, records.ErrInvalid
	}
	title := []rune(row.Title)
	if len(title) > 512 {
		out.Title = string(title[:509]) + "..."
	}
	if !body {
		return out, nil
	}
	out.Note = row.Note
	add := func(kind, value string) {
		if value != "" {
			out.Chips = append(out.Chips, protocol.RecordChip{Kind: kind, Value: value})
		}
	}
	if v := row.Todo; v != nil {
		if v.Completion != nil {
			add("completion", v.Completion.Label)
		}
		add("when", v.When)
		add("deadline", v.Deadline)
		if v.Important {
			add("important", "true")
		}
		for _, tag := range v.Tags {
			add("tag", tag)
		}
		if v.Repeat != nil {
			text := v.Repeat.Text
			if text == "" {
				text = v.Repeat.Rule
			}
			add("repeat", text)
		}
		for _, id := range v.BelongsTo {
			parent, err := r.records.ViewRecord(ctx, r.person, id)
			if err != nil {
				return out, err
			}
			add("project", parent.Title)
		}
	}
	return out, nil
}

// A continuation advances note and chip offsets together. Escaped JSON sizes
// bound large metadata as well as note text; no client computes page membership.
func recordPage(row protocol.RecordRow, after *string) (protocol.RecordRow, *string, error) {
	noteOffset, chipOffset := 0, 0
	if after != nil {
		a, b, ok := strings.Cut(*after, ":")
		var err error
		noteOffset, err = strconv.Atoi(a)
		if err != nil || !ok {
			return row, nil, records.ErrInvalid
		}
		chipOffset, err = strconv.Atoi(b)
		if err != nil {
			return row, nil, records.ErrInvalid
		}
	}
	if noteOffset < 0 || noteOffset > len(row.Note) || chipOffset < 0 || chipOffset > len(row.Chips) || noteOffset < len(row.Note) && !utf8.RuneStart(row.Note[noteOffset]) {
		return row, nil, records.ErrInvalid
	}
	noteEnd := min(len(row.Note), noteOffset+16*1024)
	for noteEnd < len(row.Note) && !utf8.RuneStart(row.Note[noteEnd]) {
		noteEnd--
	}
	chipEnd, size := chipOffset, 0
	for chipEnd < len(row.Chips) {
		chip := row.Chips[chipEnd]
		if len(chip.Value) > 4096 {
			return row, nil, records.ErrInvalid
		}
		raw, err := json.Marshal(chip)
		if err != nil {
			return row, nil, err
		}
		if size+len(raw) > 32*1024 {
			break
		}
		size += len(raw)
		chipEnd++
	}
	var next *string
	if noteEnd < len(row.Note) || chipEnd < len(row.Chips) {
		value := strconv.Itoa(noteEnd) + ":" + strconv.Itoa(chipEnd)
		next = &value
	}
	row.Note = row.Note[noteOffset:noteEnd]
	row.Chips = row.Chips[chipOffset:chipEnd]
	return row, next, nil
}
