// SPDX-License-Identifier: Apache-2.0

package webex

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// needsOlderParent reports a reply, in this fetch or carried from an earlier
// page, whose parent this walk has not seen and has not already committed.
func needsOlderParent(batches [][]wireMessage, at anchor) bool {
	ids := map[string]bool{}
	for _, batch := range batches {
		for _, item := range batch {
			if item.ID != "" {
				ids[item.ID] = true
			}
		}
	}
	resolved := at.Pending.resolvedSet()
	open := func(parent, self string) bool {
		if parent == "" || parent == self || parent == at.Stop || resolved[parent] || ids[parent] {
			return false
		}
		return true
	}
	for _, batch := range batches {
		for _, item := range batch {
			if open(item.ParentID, item.ID) {
				return true
			}
		}
	}
	for _, hold := range at.Pending.Hold {
		if !ids[hold.ID] && open(hold.Parent, hold.ID) {
			return true
		}
	}
	return false
}

// assemblePage chooses the messages this admission can commit. A reply whose
// parent is still ahead of the walk stays held. Fetched messages are oldest
// first; carried replies are released only after that parent is selected or
// already committed, and never past the admission limit.
func assemblePage(fetched []wireMessage, held []holdRef, resolved map[string]bool, stop string, continuing bool, limit int) (emit []wireMessage, release []holdRef, nextHold []holdRef, nextResolved []string) {
	if limit < 1 {
		limit = 1
	}
	ids := map[string]bool{}
	for _, message := range fetched {
		if message.ID != "" {
			ids[message.ID] = true
		}
	}
	waiting := map[string]bool{}
	for _, hold := range held {
		if hold.ID != "" {
			waiting[hold.ID] = true
		}
	}
	blocked := map[string]bool{}
	for {
		moved := false
		for _, message := range fetched {
			if message.ID == "" || blocked[message.ID] {
				continue
			}
			if !parentAdmissible(message.ParentID, message.ID, ids, blocked, resolved, nil, waiting, stop, continuing) {
				blocked[message.ID] = true
				moved = true
			}
		}
		if !moved {
			break
		}
	}
	selected := map[string]bool{}
	for _, message := range fetched {
		if message.ID == "" {
			continue
		}
		if blocked[message.ID] || len(emit) >= limit || !parentAdmissible(message.ParentID, message.ID, ids, blocked, resolved, selected, waiting, stop, continuing) {
			nextHold = append(nextHold, holdRef{ID: message.ID, Parent: message.ParentID})
			continue
		}
		emit = append(emit, message)
		selected[message.ID] = true
	}
	pending := append([]holdRef(nil), held...)
	for len(emit)+len(release) < limit && len(pending) > 0 {
		pick := -1
		for i, hold := range pending {
			if hold.ID == "" || selected[hold.ID] || ids[hold.ID] {
				pick = i
				break
			}
			if parentHeld(hold.Parent, pending) {
				continue
			}
			if parentAdmissible(hold.Parent, hold.ID, ids, blocked, resolved, selected, nil, stop, continuing) {
				pick = i
				break
			}
		}
		if pick < 0 {
			break
		}
		hold := pending[pick]
		pending = append(pending[:pick], pending[pick+1:]...)
		if hold.ID == "" || selected[hold.ID] || ids[hold.ID] {
			continue
		}
		release = append(release, hold)
		selected[hold.ID] = true
	}
	nextHold = append(nextHold, pending...)

	need := map[string]bool{}
	for _, hold := range nextHold {
		if hold.Parent != "" {
			need[hold.Parent] = true
		}
	}
	seen := map[string]bool{}
	for id := range selected {
		if need[id] {
			nextResolved = append(nextResolved, id)
			seen[id] = true
		}
	}
	for id := range resolved {
		if need[id] && !seen[id] {
			nextResolved = append(nextResolved, id)
		}
	}
	sort.Strings(nextResolved)
	return emit, release, nextHold, nextResolved
}

// parentAdmissible reports whether a reply may be committed on this page.
// A parent still waiting in this walk, or still ahead of it, is not ready.
func parentAdmissible(parent, self string, fetched, blocked, resolved, selected, waiting map[string]bool, stop string, continuing bool) bool {
	if parent == "" || parent == self || parent == stop {
		return true
	}
	if resolved[parent] || selected[parent] {
		return true
	}
	if waiting[parent] {
		return false
	}
	if fetched[parent] {
		return !blocked[parent]
	}
	if continuing {
		return false
	}
	return true
}

func parentHeld(parent string, pending []holdRef) bool {
	if parent == "" {
		return false
	}
	for _, hold := range pending {
		if hold.ID == parent {
			return true
		}
	}
	return false
}

func holdsSame(a, b []holdRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameIDs(list []string, other []string) bool {
	if len(list) != len(other) {
		return false
	}
	have := map[string]int{}
	for _, id := range list {
		have[id]++
	}
	for _, id := range other {
		if have[id] == 0 {
			return false
		}
		have[id]--
	}
	return true
}

// loadHeld re-reads replies carried on the cursor. A message the platform no
// longer has is dropped; any other failure leaves the cursor where it was.
func (a *Adapter) loadHeld(ctx context.Context, chat string, release []holdRef) ([]wireMessage, error) {
	out := make([]wireMessage, 0, len(release))
	for _, hold := range release {
		msg, ok, err := a.messageByID(ctx, hold.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if msg.ID == "" || (msg.RoomID != "" && msg.RoomID != chat) {
			return nil, ErrMisconfigured
		}
		out = append(out, msg)
	}
	return out, nil
}

func (a *Adapter) messageByID(ctx context.Context, id string) (wireMessage, bool, error) {
	escaped := url.PathEscape(id)
	if id == "" || escaped == "." || escaped == ".." || strings.Contains(escaped, "/") {
		return wireMessage{}, false, ErrMisconfigured
	}
	var msg wireMessage
	_, err := a.get(ctx, a.base+"/v1/messages/"+escaped, &msg)
	if err != nil {
		var status *StatusError
		if errors.As(err, &status) && status.Status == http.StatusNotFound {
			return wireMessage{}, false, nil
		}
		return wireMessage{}, false, err
	}
	return msg, true, nil
}
