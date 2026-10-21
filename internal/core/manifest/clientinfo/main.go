// SPDX-License-Identifier: Apache-2.0

// Command clientinfo emits the native build's compiled client support table.
package main

import (
	"encoding/json"
	"os"

	"github.com/thisisjun786/lina/internal/core/manifest"
)

func main() {
	if err := json.NewEncoder(os.Stdout).Encode(manifest.ClientCombinations()); err != nil {
		panic(err)
	}
}
