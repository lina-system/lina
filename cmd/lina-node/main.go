// SPDX-License-Identifier: Apache-2.0
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Fprintln(os.Stderr, "lina-node version: not implemented")
		os.Exit(70)
	}
	fmt.Fprintln(os.Stderr, "usage: lina-node <version>")
	os.Exit(64)
}
