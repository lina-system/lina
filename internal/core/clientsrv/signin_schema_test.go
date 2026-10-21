// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestSignInPayloadContracts(t *testing.T) {
	for _, test := range []struct {
		kind, payload string
		valid         bool
	}{
		{"account_request", `{"operation":"list"}`, true},
		{"account_request", `{"operation":"sign_out","account_id":"synthetic"}`, true},
		{"account_request", `{"operation":"sign_out"}`, false},
		{"account_result", `{"operation":"list","accounts":[]}`, true},
		{"account_result", `{"operation":"list","accounts":[{"account_id":"synthetic","provider":"chatgpt","label":"ChatGPT","status":"ok","access_token":"forbidden"}]}`, false},
		{"sign_in_listen", `{}`, true},
		{"sign_in_listening", `{"port":32123}`, true},
		{"sign_in_listening", `{"cannot_listen":"remote_terminal"}`, true},
		{"sign_in_listening", `{"port":0}`, false},
		{"sign_in_listening", `{"port":65536}`, false},
		{"sign_in_listening", `{"cannot_listen":""}`, false},
		{"sign_in_listening", `{"port":32123,"cannot_listen":"remote_terminal"}`, false},
		{"sign_in_open", `{"url":"https://auth.openai.com/api/accounts/authorize?state=synthetic"}`, true},
		{"sign_in_open", `{}`, true},
		{"sign_in_redirect", `{"code":"synthetic-code","state":"synthetic-state","iss":"https://auth.openai.com"}`, true},
		{"sign_in_redirect", `{"error":"access_denied","state":"synthetic-state","error_description":"declined"}`, true},
		{"sign_in_redirect", `{"url":"http://127.0.0.1:32123/auth/callback?code=synthetic&state=synthetic&client_id=oaiapp_example"}`, true},
		{"sign_in_redirect", `{"url":"http://127.0.0.1:32123/auth/callback","code":"synthetic"}`, false},
		{"sign_in_redirect", `{"code":"synthetic","error":"access_denied","state":"synthetic"}`, false},
		{"sign_in_redirect", `{"code":"synthetic"}`, false},
		{"sign_in_redirect", `{"code":"synthetic","state":"synthetic","access_token":"forbidden"}`, false},
		{"account_request", `{"operation":"sign_in","provider":"chatgpt","purpose":"login"}`, true},
		{"account_request", `{"operation":"sign_in","provider":"chatgpt","purpose":"provider","account_id":"synthetic-account"}`, true},
		{"account_request", `{"operation":"sign_in","provider":"chatgpt","purpose":"unknown"}`, false},
		{"account_result", `{"operation":"sign_in","account_id":"synthetic-account","status":"ok"}`, true},
		{"account_result", `{"operation":"sign_in","account_id":"synthetic-account","status":"ok","refresh_token":"forbidden"}`, false},
	} {
		t.Run(test.kind+test.payload, func(t *testing.T) {
			raw, err := os.ReadFile("../../../protocol/fixtures/client/1.0/normal/" + test.kind + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			envelope["payload"] = json.RawMessage(test.payload)
			raw, err = json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			var decoded protocol.Envelope
			err = json.Unmarshal(raw, &decoded)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v, want %v: %v", err == nil, test.valid, err)
			}
		})
	}
}
