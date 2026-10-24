// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/thisisjun786/lina/internal/core/textcall"
)

func runTextToolReplay(args []string) int {
	return textToolReplay(args, os.Stdout, os.Stderr)
}
func textToolReplay(args []string, out, diagnostics io.Writer) int {
	if len(args) != 1 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(diagnostics, "usage: lina text-tool-replay <cases.jsonl>")
		if len(args) == 1 {
			return 0
		}
		return 64
	}
	report := textcall.ReplayFile(args[0])
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return 74
	}
	if !report.Passed() {
		return 1
	}
	return 0
}
