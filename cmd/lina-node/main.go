// SPDX-License-Identifier: Apache-2.0
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		version()
		return
	}
	fmt.Fprintln(os.Stderr, "usage: lina-node <version>")
	os.Exit(64)
}
