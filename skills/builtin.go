// SPDX-License-Identifier: Apache-2.0

// Package skills embeds the product's built-in skill folders, without user data.
package skills

import (
	"embed"
	"io/fs"
)

//go:embed meta/SKILL.md
var files embed.FS

// Builtins exposes immutable files. Core indexes front matter and reads bodies
// only when a preparation or the skill reading tool requests them.
func Builtins() fs.FS { return files }
