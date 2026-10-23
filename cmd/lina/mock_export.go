// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/buildinfo"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

func runMockExport(args []string) int {
	flags := flag.NewFlagSet("lina mock-export", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	socket := flags.String("socket", "", "Core client socket")
	conversation := flags.String("conversation", "", "conversation id")
	id := flags.String("result", "", "mock result id")
	revision := flags.Int("revision", 0, "retained revision")
	asset := flags.String("asset", "png", "png or page")
	output := flags.String("output", "", "new destination file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if flags.NArg() != 0 || *socket == "" || *conversation == "" || *id == "" || *revision < 1 || *output == "" || (*asset != "png" && *asset != "page") {
		fmt.Fprintln(os.Stderr, "usage: lina mock-export --socket <path> --conversation <id> --result <id> --revision <n> --asset png|page --output <new file>")
		return 64
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c, err := clientapi.DialWithCapabilities(ctx, *socket, buildinfo.Version, []protocol.Capability{{Name: "mocks", Version: "1.0"}})
	if err != nil {
		return developmentError("mock-export", err)
	}
	defer c.Close()
	file, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return developmentError("mock-export", err)
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			_ = os.Remove(*output)
		}
	}()
	request := protocol.MockRequest{Action: "asset", ResultID: *id, Revision: json.Number(strconv.Itoa(*revision)), ConversationID: *conversation, Asset: asset}
	var offset int64
	var pinned *protocol.MockAssetRef
	for {
		key, err := c.Mock(ctx, request, "")
		if err != nil {
			return developmentError("mock-export", err)
		}
		frame, err := c.Receive(ctx)
		if err != nil {
			return developmentError("mock-export", err)
		}
		if frame.ReplyTo == nil || *frame.ReplyTo != key {
			return developmentError("mock-export", errors.New("uncorrelated asset"))
		}
		if frame.Error != nil {
			return developmentError("mock-export", &clientapi.Refusal{Cause: *frame.Error})
		}
		part, ok := frame.Payload.(protocol.MockResponse)
		if !ok || part.Asset == nil || part.Data == nil || part.MediaType == nil {
			return developmentError("mock-export", errors.New("invalid asset page"))
		}
		expectedMedia := "image/png"
		if *asset == "page" {
			expectedMedia = "application/zip"
		}
		if *part.MediaType != expectedMedia || pinned != nil && *pinned != *part.Asset {
			return developmentError("mock-export", errors.New("asset changed"))
		}
		pinned = part.Asset
		data, err := base64.StdEncoding.DecodeString(*part.Data)
		if err != nil || len(data) > 48<<10 || offset+int64(len(data)) > 8<<20 {
			return developmentError("mock-export", errors.New("invalid asset size"))
		}
		if _, err = file.Write(data); err != nil {
			return developmentError("mock-export", err)
		}
		offset += int64(len(data))
		if part.Next == nil {
			break
		}
		next, err := part.Next.Int64()
		if err != nil || next != offset || len(data) == 0 {
			return developmentError("mock-export", errors.New("invalid asset cursor"))
		}
		request.Offset = part.Next
	}
	if err = file.Sync(); err != nil {
		return developmentError("mock-export", err)
	}
	if err = file.Close(); err != nil {
		return developmentError("mock-export", err)
	}
	complete = true
	fmt.Fprintln(os.Stdout, "Exported.")
	return 0
}
