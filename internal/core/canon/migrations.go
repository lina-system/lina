// SPDX-License-Identifier: Apache-2.0
package canon

// Migrations returns a private copy of Core's ordered forward-only schema list.
// Once a version reaches dev it is never renumbered or rewritten.
func Migrations() []Migration {
	return []Migration{{Version: 1, SQL: authorityEffectsSchema}}
}
