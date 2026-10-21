// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/tools"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "import-hermes":
			os.Exit(runImportHermes(os.Args[2:]))
		case "restore":
			os.Exit(runRestore(os.Args[2:]))
		case "backup":
			os.Exit(runBackup(os.Args[2:]))
		case "core":
			os.Exit(runCore(os.Args[2:]))
		case "install":
			os.Exit(runInstall(os.Args[2:]))
		case "doctor":
			os.Exit(runDoctor(os.Args[2:]))
		case "replay":
			os.Exit(runReplay(os.Args[2:]))
		case "export":
			os.Exit(runExport(os.Args[2:]))
		case "replay-worker":
			if err := replay.Worker(context.Background(), os.Stdin, os.Stdout); err != nil {
				os.Exit(70)
			}
			return
		case "version":
			version()
			return
		case "tui":
			os.Exit(runTUI(os.Args[2:]))
		case "file-tool":
			os.Exit(tools.Main(context.Background(), os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
		}
	}
	fmt.Fprintln(os.Stderr, "usage: lina <core|tui|install|backup|restore|import-hermes|doctor|replay|export|version|file-tool>")
	os.Exit(64)
}
