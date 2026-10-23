// SPDX-License-Identifier: Apache-2.0
package state

import (
	"github.com/thisisjun786/lina/kit/protocol"
	"strings"
	"testing"
)

func TestRecordListPreservesCoreOrderSelectionAndSanitizesText(t *testing.T) {
	rows := []protocol.RecordRow{{Ref: "z", Title: "Zebra\x1b]52;c;ZXZpbA==\a", Chips: []protocol.RecordChip{{Kind: "tag", Value: "tag"}}}, {Ref: "a", Title: "Apple"}}
	v := (RecordList{}).Replace(rows)
	if v.Rows[0].Ref != "z" || v.Rows[1].Ref != "a" {
		t.Fatal("Core order changed")
	}
	if strings.Contains(v.Rows[0].Title, "ZXZpbA") || strings.Contains(v.Rows[0].Title, "\x1b") {
		t.Fatal("terminal control survived")
	}
	v = v.Move(1)
	v = v.Replace([]protocol.RecordRow{rows[1], rows[0]})
	if v.Selected != 0 || v.Rows[v.Selected].Ref != "a" {
		t.Fatal("refresh changed selected identity")
	}
	rows[0].Chips[0].Value = "changed"
	if v.Rows[1].Chips[0].Value != "tag" {
		t.Fatal("aliased peer chips")
	}
	v = v.Replace(nil).Move(1)
	if v.Selected != 0 || len(v.Rows) != 0 {
		t.Fatal("empty list has a selected row")
	}
}
