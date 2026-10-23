// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestMockMinorVersionFixtures(t *testing.T) {
	for _, name := range []string{"mock", "mock-read", "mock-asset", "mock-build"} {
		raw, err := os.ReadFile("../../protocol/fixtures/client/1.1/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var e Envelope
		if err = json.Unmarshal(raw, &e); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err = json.Marshal(e); err != nil {
			t.Fatal(err)
		}
		if name == "mock" {
			var decoder ClientDecoder
			got, decodeErr := decoder.Decode(raw)
			if decodeErr != nil || got == nil {
				t.Fatalf("receive boundary lost mock: %v", decodeErr)
			}
			body := got.Payload.(Result).Body
			if body == nil || !strings.Contains(string(*body), `"request"`) {
				t.Fatal("receive boundary pruned the mock body")
			}
		}
		e.Protocol.Version = "1.0"
		if _, err = json.Marshal(e); err == nil {
			t.Fatal("mock crossed the old protocol boundary")
		}
	}
}
