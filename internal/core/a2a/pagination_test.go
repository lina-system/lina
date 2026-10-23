// SPDX-License-Identifier: Apache-2.0
package a2a

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPaginationBindsCallerQueryAndOutputLimits(t *testing.T) {
	s, _ := httpFixture(t)
	for i := range 3 {
		params := map[string]any{"message": map[string]any{"messageId": fmt.Sprintf("message-%d", i), "role": "ROLE_USER", "parts": []any{map[string]any{"data": map[string]any{"task": "write", "input": "input"}}}}, "configuration": map[string]any{"returnImmediately": true}}
		w := rpc(t, s, "one", "1.0", "SendMessage", params)
		var out struct{ Result struct{ Task Task } }
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Result.Task.ID == "" {
			t.Fatal(w.Body)
		}
		if _, err := s.Store.Update(t.Context(), out.Result.Task.ID, func(r *Record) error {
			r.Task.Artifacts = []Artifact{{ArtifactID: "result", Parts: []Part{{Raw: []byte("artifact")}}}}
			SetState(r, Completed, time.Now())
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	query := map[string]any{"pageSize": 1, "historyLength": 0}
	seen := map[string]bool{}
	cursor := ""
	for i := range 3 {
		if cursor != "" {
			query["pageToken"] = cursor
		}
		w := rpc(t, s, "one", "1.0", "ListTasks", query)
		var out struct{ Result ListResponse }
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Result.Tasks) != 1 || out.Result.TotalSize != 3 {
			t.Fatal(w.Body)
		}
		task := out.Result.Tasks[0]
		if seen[task.ID] || len(task.History) != 0 || len(task.Artifacts) != 0 {
			t.Fatalf("pagination duplicated or ignored projection: %+v", task)
		}
		seen[task.ID] = true
		cursor = out.Result.NextPageToken
		if i == 0 {
			altered := map[string]any{"pageSize": 1, "historyLength": 0, "pageToken": cursor}
			if w = rpc(t, s, "two", "1.0", "ListTasks", altered); !strings.Contains(w.Body.String(), `"code":-32602`) {
				t.Fatalf("cross-caller cursor accepted: %s", w.Body)
			}
			altered["includeArtifacts"] = true
			if w = rpc(t, s, "one", "1.0", "ListTasks", altered); !strings.Contains(w.Body.String(), `"code":-32602`) {
				t.Fatalf("changed query accepted: %s", w.Body)
			}
		}
	}
	if cursor != "" {
		t.Fatal("last page retained a cursor")
	}
	w := rpc(t, s, "one", "1.0", "ListTasks", map[string]any{"includeArtifacts": true, "status": Completed})
	var out struct{ Result ListResponse }
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Result.Tasks) != 3 || len(out.Result.Tasks[0].Artifacts) != 1 {
		t.Fatal(w.Body)
	}
}
