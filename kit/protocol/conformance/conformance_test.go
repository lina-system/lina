// SPDX-License-Identifier: Apache-2.0

package conformance_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"

	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/protocol/conformance"
)

func codec(data []byte) ([]byte, error) {
	var envelope protocol.Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	return json.Marshal(envelope)
}

func TestCorpusRoundTrips(t *testing.T) {
	suite, err := conformance.Load(os.DirFS("../../../protocol/fixtures/client/1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if err := suite.RoundTrip(codec); err != nil {
		t.Fatal(err)
	}
	newer, err := conformance.Load(os.DirFS("../../../protocol/fixtures/client/1.1"))
	if err != nil {
		t.Fatal(err)
	}
	if err = newer.RoundTrip(codec); err != nil {
		t.Fatal(err)
	}
	for name, data := range newer.Envelopes {
		suite.Envelopes["1.1/"+name] = data
	}
	// Follow the canonical inventory, so adding a kind without a fixture fails.
	data, err := os.ReadFile("../../../protocol/schema/client/1.0/envelope.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
		Defs map[string]struct {
			Enum []string `json:"enum"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	kinds, effects, results, codes := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, data := range suite.Envelopes {
		var envelope protocol.Envelope
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		kinds[string(envelope.Kind)] = true
		effects[string(envelope.EffectState)] = true
		if envelope.Error != nil {
			codes[string(envelope.Error.Code)] = true
		}
		if result, ok := envelope.Payload.(protocol.Result); ok {
			results[string(result.Type)] = true
		}
	}
	for family, inventory := range map[string]struct {
		want []string
		got  map[string]bool
	}{
		"kind":        {schema.Properties["kind"].Enum, kinds},
		"effect":      {schema.Defs["effect_state"].Enum, effects},
		"error code":  {schema.Defs["error_code"].Enum, codes},
		"result type": {schema.Defs["result_type"].Enum, results},
	} {
		for _, value := range inventory.want {
			if !inventory.got[value] {
				t.Errorf("missing %s fixture: %s", family, value)
			}
		}
	}
}

func TestClientCases(t *testing.T) {
	for _, version := range []string{"1.0", "1.1"} {
		t.Run(version, func(t *testing.T) { testClientCases(t, version) })
	}
}
func testClientCases(t *testing.T, version string) {
	suite, err := conformance.Load(os.DirFS("../../../protocol/fixtures/client/" + version))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range suite.Cases {
		if c.Side != "client" {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			var client protocol.ClientDecoder
			receiver := conformance.Receiver{
				Decode: func(data []byte) (json.RawMessage, protocol.ErrorCode, error) {
					message, err := client.Decode(data)
					if err != nil {
						return nil, protocol.ErrorMalformed, nil
					}
					if message == nil {
						return nil, "", nil
					}
					encoded, err := json.Marshal(message)
					return encoded, "", err
				},
				Stats: client.Stats,
			}
			if err := suite.Run(c, receiver); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunnerDetectsLossAndWrongOutcomes(t *testing.T) {
	suite, err := conformance.Load(os.DirFS("../../../protocol/fixtures/client/1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if err := suite.RoundTrip(func(data []byte) ([]byte, error) { return []byte(`{}`), nil }); err == nil {
		t.Fatal("accepted a lossy codec")
	}
	var c conformance.Case
	for _, candidate := range suite.Cases {
		if candidate.Name == "cases/normal_client.json" {
			c = candidate
		}
	}
	if c.Name == "" {
		t.Fatal("missing normal client case")
	}
	for _, failure := range []string{"counts", "output", "code"} {
		t.Run(failure, func(t *testing.T) {
			var count uint64
			if err := suite.Run(c, conformance.Receiver{
				Decode: func(data []byte) (json.RawMessage, protocol.ErrorCode, error) {
					count++
					if failure == "output" {
						return nil, "", nil
					}
					if failure == "code" {
						return nil, protocol.ErrorMalformed, nil
					}
					return data, "", nil
				},
				Stats: func() protocol.DecodeStats {
					if failure == "counts" {
						return protocol.DecodeStats{}
					}
					return protocol.DecodeStats{Accepted: count}
				},
			}); err == nil {
				t.Fatalf("accepted incorrect %s", failure)
			}
		})
	}
}

func TestRoundTripComparesNumbersWithoutRounding(t *testing.T) {
	for _, tc := range []struct {
		input, output string
		equal         bool
	}{
		{`{"n":9007199254740993.125}`, `{"n":9007199254740993.1250}`, true},
		{`{"n":9007199254740993}`, `{"n":9007199254740992}`, false},
		{`{"n":1e400}`, `{"n":10e399}`, true},
		{`{"n":1}`, `{"n":"1"}`, false},
		{`{"n":[null]}`, `{"n":[]}`, false},
		{`{"n":1}`, `{"n":1} {}`, false},
	} {
		suite := conformance.Suite{Envelopes: map[string]json.RawMessage{"number.json": []byte(tc.input)}}
		err := suite.RoundTrip(func([]byte) ([]byte, error) { return []byte(tc.output), nil })
		if (err == nil) != tc.equal {
			t.Errorf("%s -> %s: %v", tc.input, tc.output, err)
		}
	}
}

func TestLoaderFailsClosed(t *testing.T) {
	for name, files := range map[string]fstest.MapFS{
		"empty suite":      {},
		"unknown field":    {"cases/example.json": {Data: []byte(`{"side":"client","typo":true}`)}},
		"missing envelope": {"cases/example.json": {Data: []byte(`{"side":"client","input":["missing.json"],"output":["missing.json"],"errors":[""],"outcome":{"accepted":1,"malformed":0,"unknown_kinds":0}}`)}},
		"path traversal":   {"cases/example.json": {Data: []byte(`{"side":"client","input":["../hello.json"],"output":[],"errors":[""],"outcome":{"accepted":0,"malformed":0,"unknown_kinds":1}}`)}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := conformance.Load(files); err == nil {
				t.Fatal("accepted invalid suite")
			}
		})
	}
	corpus := fstest.MapFS{}
	root := os.DirFS("../../../protocol/fixtures/client/1.0")
	if err := fs.WalkDir(root, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(root, name)
		if err != nil {
			return err
		}
		corpus[name] = &fstest.MapFile{Data: data}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := conformance.Load(corpus); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"reference", "orphan", "side", "counts", "invalid output"} {
		t.Run(failure, func(t *testing.T) {
			files := fstest.MapFS{}
			for name, file := range corpus {
				files[name] = file
			}
			name := "cases/normal_client.json"
			var c conformance.Case
			if err := json.Unmarshal(files[name].Data, &c); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "reference":
				c.Input[0] = "missing.json"
			case "orphan":
				files["orphan.json"] = files["hello.json"]
			case "side":
				c.Side = "other"
			case "counts":
				c.Outcome.Accepted++
			case "invalid output":
				c.Output[0] = "invalid/unknown_kind.json"
			}
			data, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			files[name] = &fstest.MapFile{Data: data}
			if _, err := conformance.Load(files); err == nil {
				t.Fatal("accepted broken corpus")
			}
		})
	}
}
