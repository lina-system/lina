// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/kit/protocol"
)

type recordCapture struct {
	connection string
	expires    time.Time
	write      coreexec.RecordWrite
	row        protocol.RecordRow
	saved      *protocol.RecordRow
	uncertain  bool
}

func (r *conversationRuntime) recordCapture(ctx context.Context, s clientsrv.Session, p protocol.RecordRequest) (protocol.RecordResult, error) {
	result := protocol.RecordResult{Operation: p.Operation, Rows: []protocol.RecordRow{}}
	if !s.CanDecide {
		return result, &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	}
	if p.View != nil || p.Search != nil || p.After != nil || p.Day != nil || p.Zone != nil || p.Ref != nil || p.Revision != nil {
		return result, malformedManagement()
	}
	r.recordCaptureMu.Lock()
	defer r.recordCaptureMu.Unlock()
	now := time.Now()
	if r.recordCaptures == nil {
		r.recordCaptures = map[string]*recordCapture{}
	}
	for token, preview := range r.recordCaptures {
		if !preview.expires.After(now) || p.Operation == "preview" && preview.connection == s.ConnectionID {
			delete(r.recordCaptures, token)
		}
	}
	switch p.Operation {
	case "preview":
		if p.Text == nil || p.Token != nil {
			return result, malformedManagement()
		}
		if len(r.recordCaptures) >= 128 {
			return result, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
		}
		capture, err := records.ParseCapture(*p.Text)
		if err != nil {
			return result, err
		}
		for _, name := range capture.Projects {
			var matches []records.Row
			for _, kind := range []string{"project", "area"} {
				after := ""
				for {
					rows, err := r.records.Query(ctx, records.Params{Person: s.PersonID, Kind: kind, After: after, Limit: 100})
					if err != nil {
						return result, err
					}
					for _, row := range rows {
						if row.Title == name {
							matches = append(matches, row)
						}
					}
					if len(rows) < 100 {
						break
					}
					after = rows[len(rows)-1].ID
				}
			}
			if len(matches) != 1 {
				return result, records.ErrInvalid
			}
			capture.Todo.BelongsTo = append(capture.Todo.BelongsTo, matches[0].ID)
		}
		row, err := r.recordRow(ctx, records.Row{Kind: "todo", State: "open", Title: capture.Title, Todo: &capture.Todo}, true)
		if err != nil {
			return result, err
		}
		// Empty fields are explicit too, so a plain sentence is still a preview.
		for _, kind := range []string{"when", "project", "tag", "repeat"} {
			found := false
			for _, chip := range row.Chips {
				found = found || chip.Kind == kind
			}
			if !found {
				row.Chips = append(row.Chips, protocol.RecordChip{Kind: kind})
			}
		}
		token := records.NewID()
		r.recordCaptures[token] = &recordCapture{row: row, connection: s.ConnectionID, expires: now.Add(15 * time.Minute), write: coreexec.RecordWrite{Kind: "todo", Title: capture.Title, Todo: &capture.Todo}}
		result.Token = &token
		result.Rows = append(result.Rows, row)
	case "save":
		if p.Token == nil || p.Text != nil {
			return result, malformedManagement()
		}
		capture := r.recordCaptures[*p.Token]
		if capture == nil || capture.connection != s.ConnectionID {
			code := "preview_expired"
			result.Code = &code
			return result, nil
		}
		if capture.saved != nil {
			result.Rows = append(result.Rows, *capture.saved)
			return result, nil
		}
		if capture.uncertain {
			code := "record_operation_unknown"
			result.Code = &code
			return result, nil
		}
		receipt, err := coreexec.WriteRecord(ctx, r.records, records.Actor{Person: s.PersonID, Agent: r.agent}, capture.write)
		if err != nil {
			capture.uncertain = errors.Is(err, records.ErrUncertain)
			return result, err
		}
		// Retain the exact preview even if a belonging record is renamed
		// immediately after the commit. A repeated confirm returns this receipt.
		row := capture.row
		row.Ref, row.Revision = receipt.ID, receipt.Revision
		capture.saved = &row
		result.Rows = append(result.Rows, row)
	default:
		return result, malformedManagement()
	}
	return result, nil
}
