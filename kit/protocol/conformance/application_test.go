// SPDX-License-Identifier: Apache-2.0

package conformance_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/protocol/conformance"
)

func TestApplicationRunnerChecksRepliesAndRecordedState(t *testing.T) {
	files := fstest.MapFS{"example.json": {Data: []byte(`{"service":"example","setup":{},"input":{"request":1},"output":{"reply":2},"outcome":{"writes":0}}`)}}
	cases, err := conformance.LoadApplications(files)
	if err != nil || len(cases) != 1 {
		t.Fatalf("load: %v, %v", cases, err)
	}
	for _, failure := range []string{"", "reply", "outcome"} {
		t.Run(failure, func(t *testing.T) {
			observed := false
			err := conformance.RunApplication(cases[0], conformance.Application{
				Execute: func(input json.RawMessage) (json.RawMessage, error) {
					if string(input) != `{"request":1}` {
						t.Fatalf("request changed: %s", input)
					}
					if failure == "reply" {
						return json.RawMessage(`{"reply":3}`), nil
					}
					return json.RawMessage(`{"reply":2}`), nil
				},
				Observe: func() (json.RawMessage, error) {
					observed = true
					if failure == "outcome" {
						return json.RawMessage(`{"writes":1}`), nil
					}
					return json.RawMessage(`{"writes":0}`), nil
				},
			})
			if (err == nil) != (failure == "") {
				t.Fatalf("failure=%q: %v", failure, err)
			}
			if failure == "" && !observed {
				t.Fatal("recorded state was not read")
			}
		})
	}
}

func TestApplicationCorpusHasConsumersAndEveryResultType(t *testing.T) {
	cases, err := conformance.LoadApplications(os.DirFS("../../../protocol/fixtures/client/1.0/application"))
	if err != nil {
		t.Fatal(err)
	}
	results := map[protocol.ResultType]bool{}
	for _, c := range cases {
		switch c.Service {
		case "client":
			if !strings.HasPrefix(c.Name, "agents/") {
				t.Fatalf("%s has no Core socket consumer", c.Name)
			}
			// Requests remain real schema-valid envelopes even though the
			// expected reply projection omits allocated transport metadata.
			if err := (&conformance.Suite{Envelopes: map[string]json.RawMessage{c.Name: c.Input}}).RoundTrip(codec); err != nil {
				t.Fatal(err)
			}
		case "results":
			if !strings.HasPrefix(c.Name, "results/") {
				t.Fatalf("%s has no Core result consumer", c.Name)
			}
			var output struct {
				Error  string `json:"error"`
				Result *struct {
					Type protocol.ResultType `json:"type"`
				} `json:"result"`
			}
			if err := json.Unmarshal(c.Output, &output); err != nil {
				t.Fatal(err)
			}
			if output.Error == "" && output.Result != nil {
				results[output.Result.Type] = true
			}
		default:
			t.Fatalf("%s has no application consumer for %q", c.Name, c.Service)
		}
	}
	// The wire card inventory is canonical even while full wire bodies remain
	// reserved. Every type needs a successful native Core body fixture.
	wire, err := conformance.Load(os.DirFS("../../../protocol/fixtures/client/1.0"))
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range wire.Envelopes {
		var e protocol.Envelope
		if err := json.Unmarshal(data, &e); err != nil {
			t.Fatal(err)
		}
		if r, ok := e.Payload.(protocol.Result); ok && !results[r.Type] {
			t.Errorf("no Core body fixture for %s", r.Type)
		}
	}
}

func TestApplicationLoaderFailsClosed(t *testing.T) {
	for _, raw := range []string{
		`{}`, `null`,
		`{"service":"example","setup":{},"input":{},"output":{},"outcome":{},"typo":true}`,
		`{"service":"example","setup":{},"input":{},"output":{}}`,
		`{"service":"example","setup":{},"input":{},"output":{},"outcome":null}`,
		`{"service":"example","setup":{},"input":{},"output":{},"outcome":{}} {}`,
	} {
		if _, err := conformance.LoadApplications(fstest.MapFS{"bad.json": {Data: []byte(raw)}}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := conformance.LoadApplications(fstest.MapFS{}); err == nil {
		t.Fatal("accepted empty corpus")
	}
}
