// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/thisisjun786/lina/internal/buildinfo"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

type repeated []string

func (r *repeated) String() string         { return fmt.Sprint([]string(*r)) }
func (r *repeated) Set(value string) error { *r = append(*r, value); return nil }
func developmentClient(ctx context.Context, socket string) (*clientapi.Client, error) {
	return clientapi.DialWithCapabilities(ctx, socket, buildinfo.Version, []protocol.Capability{{Name: "replay", Version: "1.0"}})
}
func developmentError(command string, err error) int {
	if errors.Is(err, clientapi.ErrUnavailable) {
		fmt.Fprintf(os.Stderr, "lina %s: Core unavailable; check --socket\n", command)
		return 69
	}
	var refused *clientapi.Refusal
	if errors.As(err, &refused) {
		fmt.Fprintf(os.Stderr, "lina %s: %s\n", command, refused)
		return 70
	}
	fmt.Fprintf(os.Stderr, "lina %s: request or file operation failed\n", command)
	return 70
}
func runExport(args []string) int {
	flags := flag.NewFlagSet("lina export", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	socket := flags.String("socket", "", "Core client socket")
	agent := flags.String("agent", "", "explicit agent id")
	since := flags.String("since", "", "inclusive RFC3339 time")
	until := flags.String("until", "", "exclusive RFC3339 time")
	var conversations repeated
	flags.Var(&conversations, "conversation", "explicit conversation id (repeatable)")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: lina export --socket <path> [--agent <id> --conversation <id> --since <time> --until <time>]\nNothing is selected by default. Files are written under staging/export/.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if *socket == "" || flags.NArg() != 0 {
		flags.Usage()
		return 64
	}
	p := protocol.ExportRequest{}
	if *agent != "" || len(conversations) > 0 || *since != "" || *until != "" {
		from, err := time.Parse(time.RFC3339Nano, *since)
		to, e := time.Parse(time.RFC3339Nano, *until)
		if *agent == "" || len(conversations) == 0 || err != nil || e != nil || !to.After(from) {
			flags.Usage()
			return 64
		}
		list := []string(conversations)
		p = protocol.ExportRequest{AgentID: agent, Conversations: &list, Since: since, Until: until}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := developmentClient(ctx, *socket)
	if err != nil {
		return developmentError("export", err)
	}
	defer c.Close()
	if _, err = c.Export(ctx, p); err != nil {
		return developmentError("export", err)
	}
	frame, err := c.Receive(ctx)
	if err != nil {
		return developmentError("export", err)
	}
	if frame.Error != nil {
		return developmentError("export", &clientapi.Refusal{Cause: *frame.Error})
	}
	result, ok := frame.Payload.(protocol.ExportResult)
	if !ok {
		return developmentError("export", errors.New("invalid response"))
	}
	if err = json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return developmentError("export", err)
	}
	fmt.Fprintln(os.Stderr, "Exported files are outside LINA; later deletion in LINA will not remove them.")
	return 0
}
