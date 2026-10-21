// SPDX-License-Identifier: Apache-2.0

// sqliteinfo emits the storage metadata consumed by scripts/build.mjs.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/thisisjun786/lina/internal/core/manifest"
)

func main() {
	info, err := manifest.SQLiteBuild()
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(info)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
