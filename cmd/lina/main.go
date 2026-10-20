// SPDX-License-Identifier: Apache-2.0
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "core":
			os.Exit(runCore(os.Args[2:]))
		case "version":
			version()
			return
		case "tui":
			os.Exit(runTUI(os.Args[2:]))
		}
	}
	fmt.Fprintln(os.Stderr, "usage: lina <core|tui|version>")
	os.Exit(64)
}
