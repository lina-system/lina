// SPDX-License-Identifier: Apache-2.0

package pin

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func adoptedCorpus(t *testing.T) (Record, fstest.MapFS) {
	t.Helper()
	raw, err := os.ReadFile("pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	corpus := os.DirFS("../../../../testdata/crw")
	f := make(fstest.MapFS)
	if err := fs.WalkDir(corpus, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, err := fs.ReadFile(corpus, path)
		if err != nil {
			return err
		}
		f[path] = &fstest.MapFile{Data: raw}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return r, f
}

func TestAllAdoptedJudgmentsAndConstantMutants(t *testing.T) {
	r, f := adoptedCorpus(t)
	v, err := NewValidator(r, f)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Run(r, f, func(in Input) (bool, error) { return v.Validate(in.Schema, in.Instance) })
	if err != nil || result.Cases != 91 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	for _, constant := range []bool{true, false} {
		result, err := Run(r, f, func(Input) (bool, error) { return constant, nil })
		if err == nil || result.Cases != 91 {
			t.Fatalf("constant %t escaped the oracle: %+v %v", constant, result, err)
		}
	}
}

func TestEveryAdoptedFileIsHashChecked(t *testing.T) {
	r, original := adoptedCorpus(t)
	files := append(append([]File{}, r.Schemas...), r.Fixtures...)
	files = append(files, File{Path: r.Replay.Path})
	for _, file := range files {
		t.Run(file.Path, func(t *testing.T) {
			f := make(fstest.MapFS)
			for k, v := range original {
				f[k] = v
			}
			f[file.Path] = &fstest.MapFile{Data: append(append([]byte{}, f[file.Path].Data...), '\n')}
			_, err := Run(r, f, func(Input) (bool, error) { t.Fatal("executed a changed corpus"); return true, nil })
			if err == nil || !strings.Contains(err.Error(), file.Path) {
				t.Fatalf("changed file was not identified: %v", err)
			}
		})
	}
}

func TestReplayCannotDropCasesOrRewriteUnmaskedInputs(t *testing.T) {
	for _, change := range []func(map[string]any){
		func(d map[string]any) { d["inputs"] = d["inputs"].([]any)[1:] },
		func(d map[string]any) { a := d["inputs"].([]any); a[0] = a[1] },
		func(d map[string]any) {
			d["inputs"].([]any)[0].(map[string]any)["instance"].(map[string]any)["status"] = "paused"
		},
		func(d map[string]any) {
			d["inputs"].([]any)[0].(map[string]any)["normalizedSHA256"] = strings.Repeat("0", 64)
		},
	} {
		r, f := adoptedCorpus(t)
		var document map[string]any
		if err := json.Unmarshal(f[r.Replay.Path].Data, &document); err != nil {
			t.Fatal(err)
		}
		change(document)
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		f[r.Replay.Path].Data = raw
		r.Replay.SHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
		if _, err := Run(r, f, func(Input) (bool, error) { t.Fatal("executed inconsistent replay"); return true, nil }); err == nil {
			t.Fatal("inconsistent replay accepted")
		}
	}
}
