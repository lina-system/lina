// SPDX-License-Identifier: Apache-2.0

// Package telegram reads the person's own Telegram account through the
// official client API (gotd/td) with the api_id the person created.
//
// The upstream reference is agent-messenger 2.39.1 at commit
// 9d2de96a43fe3e0e172e8e399454c475146a2697 (2026-10-20), as read for
// docs/research/messengers.md. Its Telegram platform is a TDLib client with the
// person's own api_id; this port keeps that shape on gotd/td. LINA never
// scripts my.telegram.org and never creates an api_id, an account or a session
// on someone's behalf.
//
// Reads never mark a message read and never show presence or typing: the
// adapter calls messages.getDialogs, messages.getHistory, members and
// attachment methods only. See REFERENCE.md for the port spec.
package telegram

import (
	"strconv"
	"strings"
	"time"
)

const cursorVersion = "tgc1"

// phase is where a chat's read is: filling history backwards, or following new
// messages after the first fill.
type phase string

const (
	phaseFill   phase = "f"
	phaseSteady phase = "s"
)

// position is one chat's opaque cursor. newest and oldest are message ids seen
// by the first fill; newestAt is the newest message time read completely.
// before is the id below which the next page is requested, so a catch-up with
// more new messages than one page continues where the last page stopped.
type position struct {
	newest, oldest int
	newestAt       time.Time
	before         int
	phase          phase
}

// encode renders the cursor. The platform's message ids and the read time are
// the whole state; no peer or account data is stored.
func (p position) encode() string {
	at := int64(0)
	if !p.newestAt.IsZero() {
		at = p.newestAt.Unix()
	}
	phase := p.phase
	if phase == "" {
		phase = phaseFill
	}
	return strings.Join([]string{cursorVersion, strconv.Itoa(p.newest), strconv.Itoa(p.oldest), string(phase), strconv.FormatInt(at, 10), strconv.Itoa(p.before)}, ":")
}

// decode reads a cursor. An absent or unrecognised cursor starts a first fill;
// a cursor from an older adapter cannot be misread as a position.
func decode(cursor string) position {
	if cursor == "" {
		return position{phase: phaseFill}
	}
	parts := strings.Split(cursor, ":")
	if len(parts) != 6 || parts[0] != cursorVersion {
		return position{phase: phaseFill}
	}
	newest, err := strconv.Atoi(parts[1])
	if err != nil || newest < 0 {
		return position{phase: phaseFill}
	}
	oldest, err := strconv.Atoi(parts[2])
	if err != nil || oldest < 0 {
		return position{phase: phaseFill}
	}
	p := position{newest: newest, oldest: oldest}
	switch phase(parts[3]) {
	case phaseFill:
		p.phase = phaseFill
	case phaseSteady:
		p.phase = phaseSteady
	default:
		return position{phase: phaseFill}
	}
	if at, err := strconv.ParseInt(parts[4], 10, 64); err == nil && at > 0 {
		p.newestAt = time.Unix(at, 0).UTC()
	}
	if before, err := strconv.Atoi(parts[5]); err == nil && before > 0 && p.phase == phaseSteady {
		p.before = before
	}
	return p
}
