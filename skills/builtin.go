// SPDX-License-Identifier: Apache-2.0

// Package skills embeds the product's built-in skill folders, without user data.
package skills

import "embed"

//go:embed meta/SKILL.md
var files embed.FS

// Builtins exposes immutable files. Core indexes front matter and reads bodies
// only when a preparation or the skill reading tool requests them.
func Builtins() CatalogFS { return CatalogFS{files} }

// CatalogFS identifies one immutable compiled generation without loading bodies.
type CatalogFS struct{ embed.FS }

// SkillRevision covers every embedded path and its content. The regression test
// requires this identity to change whenever an embedded skill changes.
func (CatalogFS) SkillRevision() string {
	return "be6df01212af7205494dfdb49843731751d6dc1a0c1f971284eb4ac5ec57ccd8"
}
