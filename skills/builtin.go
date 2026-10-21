// SPDX-License-Identifier: Apache-2.0

// Package skills embeds the product's built-in skill folders, without user data.
package skills

import "embed"

//go:embed meta/SKILL.md persona-change/SKILL.md
var files embed.FS

// Builtins exposes immutable files. Core indexes front matter and reads bodies
// only when a preparation or the skill reading tool requests them.
func Builtins() CatalogFS { return CatalogFS{files} }

// CatalogFS identifies one immutable compiled generation without loading bodies.
type CatalogFS struct{ embed.FS }

// SkillRevision covers every embedded path and its content. The regression test
// requires this identity to change whenever an embedded skill changes.
func (CatalogFS) SkillRevision() string {
	return "83d164824c02878f646fda04aa895711721e2d9ec8ac551589efca6d1e26cc30"
}
