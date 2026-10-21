// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
)

// Main implements the explicit internal file-tool subcommand. The executor
// launches it through sandbox.Run with argv, never through a hidden re-exec mode.
// It has no state-layout knowledge and can also be registered by a Node binary.
func Main(ctx context.Context, args []string, input io.Reader, output, diagnostic io.Writer) int {
	flags := flag.NewFlagSet("file-tool", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "")
	versions := flags.String("versions", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !filepath.IsAbs(*root) || !filepath.IsAbs(*versions) {
		fmt.Fprintln(diagnostic, "usage: lina file-tool --root <directory> --versions <private-directory>")
		return 64
	}
	if err := ServeFiles(ctx, *root, *versions, input, output); err != nil {
		fmt.Fprintln(diagnostic, "file_tool_transport_failed")
		return 74
	}
	return 0
}
