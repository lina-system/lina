// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"os"
	"testing"
)

func TestM3ScheduledOwnerStop(t *testing.T) {
	raw, err := os.ReadFile("../m3/dependency-stop.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct{ Modes []string }
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, mode := range scenario.Modes {
		t.Run(mode, func(t *testing.T) { testM3DependencyRelease(t, nil, nil, "", mode) })
	}
}
