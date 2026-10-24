// SPDX-License-Identifier: Apache-2.0

package webex

import (
	"errors"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// Test seams: the cursor encoding is unexported, and a scenario needs to build
// one without reading a page first.

func FillCursorForTest(before, newest string) messenger.Cursor { return fillCursor(before, newest, "") }

func TailCursorForTest(newest string) messenger.Cursor { return tailCursor(newest) }

func EncodeForTest(id string) string { return encode(id) }

func ParseCursorForTest(cursor messenger.Cursor) (string, string, error) {
	at, err := parseCursor(cursor)
	if err != nil {
		return "", "", err
	}
	if at.Mode != modeFill {
		return "", "", errors.New("not a fill cursor")
	}
	return at.Before, at.Newest, nil
}
