// SPDX-License-Identifier: Apache-2.0
package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPreflightAdvertisesDurableReadyProtocol(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	os.MkdirAll(filepath.Join(root, "system/core"), 0700)
	os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"update-test"}`), 0600)
	result, err := Preflight(t.Context(), root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	var wire map[string]any
	json.Unmarshal(data, &wire)
	if wire["update_protocol"] != float64(1) {
		t.Fatal("candidate has no durable READY protocol evidence")
	}
}
