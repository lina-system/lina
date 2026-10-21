// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"github.com/thisisjun786/lina/internal/core/accounts"
	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/jobs"
	"github.com/thisisjun786/lina/internal/core/turn"
)

// ApplicationMigrations is the schema installed by Core and recognized by its
// read-only diagnostics. Return a fresh slice so callers cannot change it.
func ApplicationMigrations() []canon.Migration {
	migrations := canon.Migrations()
	for _, schema := range []string{turn.Schema, turn.ControlSchema, turn.OutcomeSchema, accounts.SignInSchema, canon.GrowthSchema, jobs.Schema + budget.Schema} {
		migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: schema})
	}
	return migrations
}
