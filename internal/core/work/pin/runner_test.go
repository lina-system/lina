// SPDX-License-Identifier: Apache-2.0

package pin

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

func runnerCorpus(t *testing.T, data string) (Record, fstest.MapFS) {
	t.Helper()
	r, files := testRecord()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := &r.Fixtures[0]
	files[f.Path].Data = buf.Bytes()
	f.SHA256 = fmt.Sprintf("%x", sha256.Sum256(buf.Bytes()))
	return r, files
}

const judgments = `[
 {"schema":"relationship","label":"object","instance":"{}","errors":[]},
 {"schema":"relationship","label":"not object","instance":"false","errors":["must be an object"]}
]`

func TestRunnerUsesOnlyFixtureExpectations(t *testing.T) {
	r, f := runnerCorpus(t, judgments)
	seen := 0
	result, err := Run(r, f, func(in Input) (bool, error) {
		seen++
		if in.Schema != "relationship" {
			t.Fatalf("unexpected input: %+v", in)
		}
		var object map[string]any
		return json.Unmarshal(in.Instance, &object) == nil, nil
	})
	if err != nil || result.Cases != 2 || seen != 2 {
		t.Fatalf("result=%+v calls=%d error=%v", result, seen, err)
	}
	for _, constant := range []bool{true, false} {
		_, err := Run(r, f, func(Input) (bool, error) { return constant, nil })
		if err == nil || !strings.Contains(err.Error(), r.Fixtures[0].Path) {
			t.Fatalf("constant %t escaped fixture oracle: %v", constant, err)
		}
	}
}

func TestRunnerVerifiesAllHashesBeforeExecution(t *testing.T) {
	r, f := runnerCorpus(t, judgments)
	f[r.Fixtures[0].Path].Data[0]++
	_, err := Run(r, f, func(Input) (bool, error) { t.Fatal("executed corrupt corpus"); return true, nil })
	if err == nil {
		t.Fatal("corrupt corpus accepted")
	}
}

func TestRunnerRejectsIncompleteFixtureOrExecutorFailure(t *testing.T) {
	for _, data := range []string{
		`[]`, `null`, `{}`, `[{}]`,
		`[{"schema":"relationship","label":"x","instance":"{}"}]`,
		`[{"schema":"relationship","label":"x","instance":"{}","errors":null}]`,
		`[{"schema":"relationship","label":"x","instance":"bad JSON","errors":[]}]`,
		`[{"schema":"unknown","label":"x","instance":"{}","errors":[]}]`,
	} {
		r, f := runnerCorpus(t, data)
		if _, err := Run(r, f, func(Input) (bool, error) { t.Fatal("executed malformed fixture"); return true, nil }); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
	r, f := runnerCorpus(t, judgments)
	_, err := Run(r, f, func(Input) (bool, error) { return false, errors.New("executor unavailable") })
	if err == nil || !strings.Contains(err.Error(), "executor unavailable") {
		t.Fatalf("lost executor failure: %v", err)
	}
}
