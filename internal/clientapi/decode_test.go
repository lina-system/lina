// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestRequestDecoderRejectsUnknownFields(t *testing.T) {
	data, err := os.ReadFile("../../protocol/fixtures/client/1.0/hello.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"envelope", "payload", "sender", "protocol", "kind"} {
		t.Run(path, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			switch path {
			case "envelope":
				value["extra"] = true
			case "kind":
				value["kind"] = "future_request"
			default:
				value[path].(map[string]any)["extra"] = true
			}
			invalid, _ := json.Marshal(value)
			var server RequestDecoder
			got, refusal := server.Decode(invalid)
			if got != nil || refusal == nil || refusal.Code != protocol.ErrorMalformed {
				t.Fatalf("got=%v refusal=%v", got, refusal)
			}
			got, refusal = server.Decode(data)
			if refusal != nil || got == nil || got.Kind != protocol.KindHello {
				t.Fatalf("known request after refusal: got=%v refusal=%v", got, refusal)
			}
			if server.Stats() != (protocol.DecodeStats{Accepted: 1, Malformed: 1}) {
				t.Fatalf("counts: %+v", server.Stats())
			}
		})
	}
}
