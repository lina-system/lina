// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/coder/websocket"
)

func TestM2DiscordGatewayRecovery(t *testing.T) {
	var scenario struct {
		CloseCodes []websocket.StatusCode `json:"close_codes"`
	}
	data, err := os.ReadFile("../m2/discord-recovery.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	if len(scenario.CloseCodes) == 0 {
		t.Fatal("recovery scenario has no close codes")
	}
	runM2DiscordCore(t, scenario.CloseCodes)
}
