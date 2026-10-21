// SPDX-License-Identifier: Apache-2.0
package canon

// Migrations returns the ordered application schema. Published versions stay
// fixed; new table groups append a migration in integration order.
func Migrations() []Migration {
	return []Migration{
		{Version: 1, SQL: schemaAssets},
		{Version: 2, SQL: InboxSchema},
	}
}
