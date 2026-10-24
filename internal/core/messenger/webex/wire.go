// SPDX-License-Identifier: Apache-2.0

package webex

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// The wire types keep only the fields this adapter reads. A response that names
// fields LINA does not use is not a schema change, so unknown fields are
// ignored; a field this adapter needs and the response omits is a shape error
// where it matters.

type wireToken struct {
	Access     string `json:"access_token"`
	Refresh    string `json:"refresh_token"`
	ExpiresIn  int    `json:"expires_in"`
	TokenType  string `json:"token_type"`
	RefreshExp int    `json:"refresh_token_expires_in"`
}

type wireError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
	Message     string `json:"message"`
}

type wirePerson struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"displayName"`
	Emails      []string `json:"emails"`
}

type wireRoom struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

type roomsPage struct {
	Items []wireRoom `json:"items"`
}

type wireMembership struct {
	PersonID          string `json:"personId"`
	PersonEmail       string `json:"personEmail"`
	PersonDisplayName string `json:"personDisplayName"`
}

type membershipsPage struct {
	Items []wireMembership `json:"items"`
}

type listPage struct {
	Items []wireMessage `json:"items"`
}

type wireMessage struct {
	ID          string   `json:"id"`
	RoomID      string   `json:"roomId"`
	Text        string   `json:"text"`
	Markdown    string   `json:"markdown"`
	PersonID    string   `json:"personId"`
	PersonEmail string   `json:"personEmail"`
	Created     string   `json:"created"`
	Updated     string   `json:"updated"`
	ParentID    string   `json:"parentId"`
	Files       []string `json:"files"`
}

// Body is the message's text as the person wrote it. Webex clients show the
// plain text when it is present and the markdown otherwise.
func (m wireMessage) Body() string {
	if m.Text != "" {
		return m.Text
	}
	return m.Markdown
}

// Name is the author's display name as the room listed it, then the email.
func (m wireMembership) Name() string {
	if m.PersonDisplayName != "" {
		return m.PersonDisplayName
	}
	return m.PersonEmail
}

// Kind maps the platform's room type to a messenger chat kind.
func (r wireRoom) Kind() string {
	switch r.Type {
	case "direct", "group":
		return r.Type
	}
	return "group"
}

// Cursor layout. A cursor names one of two reads and the anchors the platform
// needs for it:
//
//	webex/1/fill/<newest>/<before>                         older messages, backwards
//	webex/1/fill/<newest>/<before>/<stop>                  catching up backwards to <stop>
//	webex/1/fill/<newest>/<before>/<stop>/<pending>        same, carrying replies
//	webex/1/tail/<newest>                                  the newest page
//
// A fill walks back through a room's first read. When the room has no older
// messages left it hands over to the tail, anchored on the newest message the
// fill saw. A tail with an empty newest is a room that was empty; the next
// burst fills to the beginning before that anchor moves. A tail read that
// overflows one page holds more messages than one page since the last read; it
// then walks backwards (a fill with a stop) until it reaches the message the
// last read ended on, so nothing between them is skipped. A reply whose parent
// is still on an older page of that walk is returned in the same page as the
// parent when both fit in one admission-sized page. A wider gap stores the
// reply's id in <pending> and returns it with the page that reaches the
// parent, or on the next page after that parent was committed. ids are
// base64url-encoded because a Webex id is base64 itself and may contain '/'.
const cursorPrefix = "webex/1/"

const (
	modeFill = "fill"
	modeTail = "tail"
)

// holdRef is a reply withheld until its parent can be admitted with it.
type holdRef struct {
	ID     string `json:"id"`
	Parent string `json:"parent,omitempty"`
}

// walkPending is the part of a fill cursor that survives between pages: replies
// still waiting on a parent, parents already committed for those replies, and
// whether the platform walk itself has finished.
type walkPending struct {
	Hold     []holdRef `json:"hold,omitempty"`
	Resolved []string  `json:"resolved,omitempty"`
	Drain    bool      `json:"drain,omitempty"`
}

func (p walkPending) resolvedSet() map[string]bool {
	set := make(map[string]bool, len(p.Resolved))
	for _, id := range p.Resolved {
		if id != "" {
			set[id] = true
		}
	}
	return set
}

func (p walkPending) empty() bool {
	return len(p.Hold) == 0 && len(p.Resolved) == 0 && !p.Drain
}

type anchor struct {
	Mode    string
	Before  string
	Newest  string
	Stop    string
	Pending walkPending
}

func parseCursor(cursor messenger.Cursor) (anchor, error) {
	if cursor == "" {
		return anchor{Mode: modeFill}, nil
	}
	text := string(cursor)
	if !strings.HasPrefix(text, cursorPrefix) {
		return anchor{}, messenger.ErrInvalid
	}
	parts := strings.Split(strings.TrimPrefix(text, cursorPrefix), "/")
	if len(parts) < 2 {
		return anchor{}, messenger.ErrInvalid
	}
	at := anchor{Mode: parts[0], Newest: decode(parts[1])}
	switch at.Mode {
	case modeFill:
		if len(parts) < 3 || len(parts) > 5 {
			return anchor{}, messenger.ErrInvalid
		}
		at.Before = decode(parts[2])
		if len(parts) >= 4 {
			at.Stop = decode(parts[3])
		}
		if len(parts) == 5 {
			pending, err := decodePending(parts[4])
			if err != nil {
				return anchor{}, err
			}
			at.Pending = pending
		}
	case modeTail:
		if len(parts) != 2 {
			return anchor{}, messenger.ErrInvalid
		}
	default:
		return anchor{}, messenger.ErrInvalid
	}
	return at, nil
}

func fillCursor(before, newest, stop string) messenger.Cursor {
	return fillCursorPending(before, newest, stop, walkPending{})
}

func fillCursorPending(before, newest, stop string, pending walkPending) messenger.Cursor {
	cursor := cursorPrefix + modeFill + "/" + encode(newest) + "/" + encode(before)
	blob := encodePending(pending)
	if stop == "" && blob == "" {
		return messenger.Cursor(cursor)
	}
	cursor += "/" + encode(stop)
	if blob != "" {
		cursor += "/" + blob
	}
	return messenger.Cursor(cursor)
}

func encodePending(pending walkPending) string {
	if pending.empty() {
		return ""
	}
	raw, err := json.Marshal(pending)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodePending(text string) (walkPending, error) {
	if text == "" || text == "-" {
		return walkPending{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return walkPending{}, messenger.ErrInvalid
	}
	var pending walkPending
	if err := json.Unmarshal(raw, &pending); err != nil {
		return walkPending{}, messenger.ErrInvalid
	}
	return pending, nil
}

func tailCursor(newest string) messenger.Cursor {
	return messenger.Cursor(cursorPrefix + modeTail + "/" + encode(newest))
}

// encode and decode keep an opaque platform id inside one cursor field.
func encode(id string) string {
	if id == "" {
		return "-"
	}
	return base64.RawURLEncoding.EncodeToString([]byte(id))
}

func decode(text string) string {
	if text == "-" {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return text
	}
	return string(raw)
}
