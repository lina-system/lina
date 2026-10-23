// SPDX-License-Identifier: Apache-2.0
package claudecode

import (
	"github.com/thisisjun786/lina/internal/core/worker"
	"strings"
	"testing"
)

func TestExistingClaudeRecordsSupplyDoing(t *testing.T) {
	body := `printf '{"type":"system","subtype":"init","session_id":"fixture","cwd":"%s"}\n' "$PWD"
cat <<'RECORDS'
{"type":"ai-title","aiTitle":"Login"}
{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"tool1","name":"TodoWrite","input":{"todos":[{"content":"Run tests","activeForm":"Running tests","status":"in_progress"}]}}]}}
{"type":"result","subtype":"success","session_id":"fixture","is_error":false,"result":"Done"}
RECORDS
`
	a, ctx, l, dir := fixture(t, body)
	if _, err := a.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: dir, TaskPacket: "Repair login"}); err != nil {
		t.Fatal(err)
	}
	seen := false
	for {
		select {
		case e := <-a.Events():
			if e.Progress != nil && e.Doing != nil && e.Doing.Line == "Login · Running tests" {
				seen = e.Doing.Busy
			}
			if e.Finish != nil {
				if !seen || e.Doing == nil || e.Doing.Busy || !strings.Contains(e.Doing.Line, "Running tests") {
					t.Fatalf("record or lifecycle lost: %+v", e)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
