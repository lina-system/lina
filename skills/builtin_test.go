// SPDX-License-Identifier: Apache-2.0
package skills_test

import (
	"io/fs"
	"testing"

	"github.com/thisisjun786/lina/skills"
)

func TestBuiltinMetaSkillIsEmbedded(t *testing.T) {
	file, err := skills.Builtins().Open("meta/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() == 0 || info.Mode()&fs.ModeType != 0 {
		t.Fatalf("embedded skill: %+v %v", info, err)
	}
}
