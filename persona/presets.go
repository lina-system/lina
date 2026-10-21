// SPDX-License-Identifier: Apache-2.0

// Package persona embeds the project's preset material; it contains no user data.
package persona

import _ "embed"

//go:embed lina/core.md
var LinaCore string

//go:embed lina/default-voice.md
var LinaDefaultVoice string
