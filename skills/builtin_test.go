// SPDX-License-Identifier: Apache-2.0
package skills_test

import (
	"crypto/sha256"
	"fmt"
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

func TestBuiltinRevisionCoversEmbeddedBytes(t *testing.T) {
	files := skills.Builtins()
	hash := sha256.New()
	err := fs.WalkDir(files, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(files, path)
		if err != nil {
			return err
		}
		hash.Write([]byte(path + "\x00"))
		hash.Write(data)
		hash.Write([]byte{0})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", hash.Sum(nil)); files.SkillRevision() != got {
		t.Fatalf("update the built-in revision to %s", got)
	}
}
