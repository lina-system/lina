// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"errors"
)

var ErrDevelopmentSchema = errors.New("development schema does not match the published migration catalog")

// Migrations returns the ordered application schema. Published versions stay
// fixed; new table groups append a migration in integration order.
func Migrations() []Migration {
	return []Migration{
		{Version: 1, SQL: schemaAssets},
		{Version: 2, SQL: InboxSchema},
		{Version: 3, SQL: authorityEffectsSchema},
		{Version: 4, SQL: authorityApprovalsSchema},
		{Version: 5, SQL: agentsSchema},
		{Version: 6, SQL: accountsSchema},
		{Version: 7, SQL: ledgerSchema},
		{Version: 8, SQL: memorySchema},
		{Version: 9, SQL: ledgerSearchSchema},
		{Version: 10, SQL: memorySearchSchema},
		{Version: 11, SQL: retractSchema + retractLineageSchema + retractViewsSchema + retractSearchSchema},
		{Version: 12, SQL: TurnSchema},
		{Version: 13, SQL: TurnControlSchema},
		{Version: 14, SQL: TurnOutcomeSchema},
		{Version: 15, SQL: compactionSchema},
		{Version: 16, SQL: AccountSignInSchema},
		{Version: 17, SQL: ChannelsSchema},
		{Version: 18, SQL: TelegramSchema},
		{Version: 19, SQL: ExchangeSchema},
		{Version: 20, SQL: conversationOrderSchema},
		{Version: 21, SQL: SkillsSchema},
	}
}

// Reject incompatible pre-release layouts before opening the writer. There is
// no renumbering or repair path for development databases with a different
// catalog. Generic canon users with their own migration lists are unaffected.
func validatePublishedCatalog(ctx context.Context, path string, version int, migrations []Migration) error {
	if version == 0 || len(migrations) < 14 {
		return nil
	}
	catalog := Migrations()
	for i := 0; i < min(len(catalog), len(migrations)); i++ {
		if migrations[i] != catalog[i] {
			return nil
		}
	}
	db, close, err := openInspection(ctx, path)
	if err != nil {
		return err
	}
	defer close()
	for _, entry := range []struct {
		version int
		table   string
	}{
		{11, "retract_meta"}, {12, "turn_runs"}, {12, "turn_items"}, {12, "turn_calls"}, {12, "turn_partials"}, {13, "turn_control"}, {14, "turn_outcome"}, {15, "sum_node"}, {15, "sum_edge"}, {15, "sum_cover"},
		{16, "owner_binding"}, {16, "sign_in_host"}, {16, "sign_in_identities"},
		{17, "channel_connections"}, {17, "channel_bindings"}, {17, "channel_actions"}, {17, "channel_receipts"}, {17, "channel_outbound"},
		{18, "telegram_buttons"}, {18, "telegram_presentations"},
		{19, "exchanges"}, {19, "exch_msg"}, {19, "exch_event"}, {19, "exch_waiting"},
		{20, "conversation_order"},
		{21, "skill_versions"}, {21, "skill_heads"}, {21, "skill_undone"}, {21, "skill_repo"},
	} {
		var count int
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?", entry.table).Scan(&count); err != nil {
			return err
		}
		if (count == 1) != (version >= entry.version) {
			return ErrDevelopmentSchema
		}
	}
	return nil
}
