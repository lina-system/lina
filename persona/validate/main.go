// SPDX-License-Identifier: Apache-2.0

// Command validate gates release packaging on all eight shipped source pairs.
package main

import (
	"fmt"
	"github.com/thisisjun786/lina/persona"
	"os"
)

func main() {
	root, err := os.OpenRoot("persona")
	if err == nil {
		defer root.Close()
		err = persona.Validate(root.FS())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "persona validation failed")
		os.Exit(1)
	}
}
