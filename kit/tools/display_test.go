// SPDX-License-Identifier: Apache-2.0
package tools_test

import (
	"testing"

	"github.com/thisisjun786/lina/kit/tools"
)

func TestDisplayNameUsesTrustedCatalog(t *testing.T) {
	for _, name := range []string{"skill_index", "read_skill", "write_skill", "undo_skill", "read_file", "write_file", "delete_file", "restore_file", "apply_patch", "exec_command", "memory_write", "recall_person", "expand", "present_result", "read_result", "save_result", "change_persona", "undo_persona", "fill_persona_setting", "record_relationship", "record_emotion", "set_emotion_expression", "growth_notices", "undo_growth", "start_run", "dispatch_work"} {
		if got := tools.DisplayName(name); got == "" || got == name || got == "Using tool" {
			t.Errorf("missing trusted label for %q: %q", name, got)
		}
	}
	for _, name := range []string{"", "arbitrary model text", "read_skill\nprivate", "plugin__read_skill", "READ_SKILL", " read_skill", "<script>alert('secret')</script>", "read_skill\x1b[2J"} {
		if got := tools.DisplayName(name); got != "Using tool" {
			t.Errorf("untrusted name %q escaped catalog: %q", name, got)
		}
	}
}
