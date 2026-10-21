// SPDX-License-Identifier: Apache-2.0
package canon

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
	}
}
