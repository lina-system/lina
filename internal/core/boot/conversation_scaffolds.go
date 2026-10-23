// SPDX-License-Identifier: Apache-2.0

package boot

import "strings"

// Model context and restored display intentionally retain different notice
// sets. Changing membership is separate from keeping their SQL in sync.
var modelContextScaffolds = []string{
	"interruption",
	"failure",
	"persona_change",
	"run_handoff",
	"background_notice",
	"coding_handoff",
	"work_completion",
	"timer",
	"timer_work",
	"handoff_check",
	"worker_session",
	"worker_completion",
	"question_default",
	"question_answer",
}

var displayScaffolds = []string{
	"interruption",
	"failure",
	"skill_change",
	"persona_change",
	"worker_session",
	"run_handoff",
	"background_notice",
	"worker_result",
	"coding_handoff",
	"timer",
	"plan_rejection",
	"question_default",
	"question_answer",
}

// scaffoldSQLList quotes the fixed scaffold names for a SQL IN expression.
func scaffoldSQLList(kinds []string) string {
	quoted := make([]string, len(kinds))
	for i, kind := range kinds {
		quoted[i] = "'" + strings.ReplaceAll(kind, "'", "''") + "'"
	}
	return "(" + strings.Join(quoted, ",") + ")"
}
