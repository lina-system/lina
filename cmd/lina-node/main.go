// SPDX-License-Identifier: Apache-2.0
package main

import (
	"fmt"
	"os"

	"github.com/thisisjun786/lina/kit/proc"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "version" {
		version()
		return
	}
	fmt.Fprintln(os.Stderr, "usage: lina-node <version|proc-supervise>")
	os.Exit(64)
}
