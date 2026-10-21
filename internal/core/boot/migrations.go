// SPDX-License-Identifier: Apache-2.0

package boot

import "github.com/thisisjun786/lina/internal/core/canon"

// ApplicationMigrations is the fixed canon catalog shared by Core, backup,
// restore and read-only diagnostics. Canon returns a fresh slice for each caller.
func ApplicationMigrations() []canon.Migration {
	return canon.Migrations()
}
