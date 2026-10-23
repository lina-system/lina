// SPDX-License-Identifier: Apache-2.0
package protocol

import "testing"

func TestResultGetPageOffset(t *testing.T) {
	p := concretePayloads()["result_get"].(map[string]any)
	p["revision"] = float64(1)
	p["offset"] = float64(65536)
	roundTrip(t, clientMessage(t, "result_get", p))
}
