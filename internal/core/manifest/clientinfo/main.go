// SPDX-License-Identifier: Apache-2.0

// Command clientinfo emits the selected build target's client support table.
package main

import (
	"encoding/json"
	"os"

	"github.com/thisisjun786/lina/internal/core/manifest"
)

func main() {
	if len(os.Args) != 2 {
		panic("usage: clientinfo GOOS/GOARCH")
	}
	rows := manifest.ClientCombinationsFor(os.Args[1])
	if err := manifest.ValidateCombinations(rows); err != nil {
		panic(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(rows); err != nil {
		panic(err)
	}
}
