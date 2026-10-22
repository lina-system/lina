// SPDX-License-Identifier: Apache-2.0

package pin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Build returns provenance only after the source corpus passes both the byte
// pin and LINA's record validator. root is the build's source checkout; installed
// binaries read the resulting manifest without requiring this checkout.
func Build(root string) (Record, error) {
	data, err := os.ReadFile(filepath.Join(root, "internal/core/work/pin/pin.json"))
	if err != nil {
		return Record{}, fmt.Errorf("read CRW pin: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("decode CRW pin: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Record{}, fmt.Errorf("trailing CRW pin data")
	}
	corpus := os.DirFS(filepath.Join(root, "testdata/crw"))
	validator, err := NewValidator(record, corpus)
	if err != nil {
		return Record{}, err
	}
	if _, err := Run(record, corpus, func(in Input) (bool, error) {
		return validator.Validate(in.Schema, in.Instance)
	}); err != nil {
		return Record{}, err
	}
	return record, nil
}
