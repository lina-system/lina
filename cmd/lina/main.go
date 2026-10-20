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
		case "tui", "version":
			fmt.Fprintf(os.Stderr, "lina %s: not implemented\n", os.Args[1])
			os.Exit(70)
		}
	}
	fmt.Fprintln(os.Stderr, "usage: lina <core|tui|version>")
	os.Exit(64)
}
