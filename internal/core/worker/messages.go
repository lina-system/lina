// SPDX-License-Identifier: Apache-2.0
package worker

import (
	"bytes"
	"crypto/rand"
	"encoding/xml"
	"fmt"
)

// FrameMessage preserves provenance independently of the vendor's user role.
// The caller supplies identity from Core's bound route, never from body text.
func FrameMessage(sender, kind, work, id, body string) string {
	if id == "" {
		id = rand.Text()
	}
	if sender == "" {
		sender = "core"
	}
	if kind == "" {
		kind = "system"
	}
	escape := func(s string) string { var b bytes.Buffer; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
	return fmt.Sprintf(`<lina-message sender="%s" speaker_kind="%s" work_id="%s" id="%s"><text>%s</text></lina-message>`, escape(sender), escape(kind), escape(work), escape(id), escape(body))
}
