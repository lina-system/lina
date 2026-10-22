// SPDX-License-Identifier: Apache-2.0

// crwinfo emits verified work-ledger contract provenance for scripts/build.mjs.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/thisisjun786/lina/internal/core/work/pin"
)

func main() {
	record, err := pin.Build(".")
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(record)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
