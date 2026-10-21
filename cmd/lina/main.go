// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/thisisjun786/lina/kit/tools"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "backup":
			os.Exit(runBackup(os.Args[2:]))
		case "core":
			os.Exit(runCore(os.Args[2:]))
		case "install":
			os.Exit(runInstall(os.Args[2:]))
		case "version":
			version()
			return
		case "tui":
			os.Exit(runTUI(os.Args[2:]))
		case "file-tool":
			os.Exit(tools.Main(context.Background(), os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
		}
	}
	fmt.Fprintln(os.Stderr, "usage: lina <core|tui|install|backup|version|file-tool>")
	os.Exit(64)
}
