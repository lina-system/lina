// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/protocol/conformance"
)

func TestServerConformance(t *testing.T) {
	suite, err := conformance.Load(os.DirFS("../../protocol/fixtures/client/1.0"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range suite.Cases {
		if c.Side != "server" {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			var server RequestDecoder
			if err := suite.Run(c, conformance.Receiver{
				Decode: func(data []byte) (json.RawMessage, protocol.ErrorCode, error) {
					message, refusal := server.Decode(data)
					if refusal != nil {
						return nil, refusal.Code, nil
					}
					encoded, err := json.Marshal(message)
					return encoded, "", err
				},
				Stats: server.Stats,
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
