// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestWorkPanelOrderKeepsProgressHierarchy(t *testing.T) {
	for _, test := range []struct {
		name, order string
		want        []string
	}{
		{"core order", `,"card_order":["b-needs","a-running","b-waiting"]`, []string{"b-needs", "a-running", "b-waiting"}},
		{"older Core", "", []string{"a-running", "b-needs", "b-waiting"}},
		{"partial order", `,"card_order":["missing","b-needs","b-needs","a-goal"]`, []string{"b-needs", "a-running", "b-waiting"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var p protocol.ProgressUpdate
			raw := `{"rows":[
				{"ref":"a-goal","line":{"name":"Goal A","kind":"goal","waiting":0,"depth":0}},
				{"ref":"a-running","line":{"name":"Child A","kind":"project","waiting":0,"depth":1,"state":"in_progress"}},
				{"ref":"b-goal","line":{"name":"Goal B","kind":"goal","waiting":1,"depth":0}},
				{"ref":"b-needs","line":{"name":"Child B1","kind":"project","waiting":1,"depth":1,"state":"needs_input"}},
				{"ref":"b-waiting","line":{"name":"Child B2","kind":"project","waiting":0,"depth":1,"state":"waiting"}}
			]` + test.order + `}`
			if err := json.Unmarshal([]byte(raw), &p); err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(p)
			work := projectWork(p)
			got := []string{}
			for _, card := range work.Cards {
				got = append(got, card.ID)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("card order %v, want %v", got, test.want)
			}
			after, _ := json.Marshal(p)
			if string(before) != string(after) {
				t.Fatal("card projection mutated shared overview")
			}
			for _, language := range []design.Language{design.English, design.Korean} {
				text := progressText(p, language, nil)
				last := -1
				for _, name := range []string{"Goal A", "Child A", "Goal B", "Child B1", "Child B2"} {
					index := strings.Index(*text, name)
					if index <= last {
						t.Fatalf("text hierarchy lost %s: %s", name, *text)
					}
					last = index
				}
			}
		})
	}
}
