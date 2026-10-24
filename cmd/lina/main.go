// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/tools"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "proc-supervise":
			os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
		case "proc-job":
			os.Exit(proc.JobMain(os.Args[2:], os.Stderr))
		case "import-hermes":
			os.Exit(runImportHermes(os.Args[2:]))
		case "import-murmur":
			os.Exit(runImportMurmur(os.Args[2:]))
		case "restore":
			os.Exit(runRestore(os.Args[2:]))
		case "backup":
			os.Exit(runBackup(os.Args[2:]))
		case "worker-tools":
			os.Exit(runWorkerTools(os.Args[2:]))
		case "core":
			os.Exit(runCore(os.Args[2:]))
		case "install":
			os.Exit(runInstall(os.Args[2:]))
		case "update":
			os.Exit(runUpdate(os.Args[2:]))
		case "doctor":
			os.Exit(runDoctor(os.Args[2:]))
		case "provider-replay":
			os.Exit(runProviderReplay(os.Args[2:]))
		case "replay":
			os.Exit(runReplay(os.Args[2:]))
		case "intake-replay":
			os.Exit(runIntakeReplay(os.Args[2:]))
		case "method-replay":
			os.Exit(runMethodReplay(os.Args[2:]))
		case "a2a":
			os.Exit(runA2A(os.Args[2:]))
		case "mock-export":
			os.Exit(runMockExport(os.Args[2:]))
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
	fmt.Fprintln(os.Stderr, "usage: lina <core|tui|install|update|backup|restore|import-hermes|import-murmur|doctor|replay|provider-replay|intake-replay|method-replay|export|a2a|version|file-tool|worker-tools|proc-supervise|proc-job>")
	os.Exit(64)
}
