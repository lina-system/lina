// SPDX-License-Identifier: Apache-2.0
package e2e_test

import "testing"

// TestM3PlanDependencyTargetReleasesQueue rejects project dependency targets
// through installed Core and Moirai, then confirms a valid cross-project todo
// dependency. It also checks restart, duplicate application and usage accounting.
func TestM3PlanDependencyTargetReleasesQueue(t *testing.T) {
	runPlanInvalidResponseScenario(t, "../m3/plan-dependency-target.json")
}
