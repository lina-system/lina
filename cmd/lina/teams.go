// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/buildinfo"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Teams settings use the owner client route, never conversation or model input.
func runTeams(args []string) int {
	flags := flag.NewFlagSet("lina teams", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	socket := flags.String("socket", "", "Core client socket")
	requestFile := flags.String("request", "", "JSON request file; defaults to stdin")
	paste := flags.Bool("paste", false, "Print the sign-in URL and read the callback URL from stdin (requires --request)")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: lina teams --socket <path> [--request <file>] [--paste]\nOperations: configure, list, sign_in, disconnect, chats, switches.")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if *socket == "" || flags.NArg() != 0 || *paste && *requestFile == "" {
		flags.Usage()
		return 64
	}
	input := os.Stdin
	if *requestFile != "" {
		f, err := os.Open(*requestFile)
		if err != nil {
			return developmentError("teams", err)
		}
		defer f.Close()
		input = f
	}
	decoder := json.NewDecoder(io.LimitReader(input, 128<<10))
	decoder.DisallowUnknownFields()
	var request protocol.TeamsRequest
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		fmt.Fprintln(os.Stderr, "lina teams: invalid settings request")
		return 64
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	c, err := clientapi.DialWithCapabilities(ctx, *socket, buildinfo.Version, []protocol.Capability{{Name: "teams", Version: "1.0"}, {Name: "sign_in_relay", Version: "1.0"}})
	if err != nil {
		return developmentError("teams", err)
	}
	defer c.Close()
	if _, err = c.Teams(ctx, request); err != nil {
		return developmentError("teams", err)
	}
	var listener *clientapi.SignInListener
	defer func() {
		if listener != nil {
			listener.Close()
		}
	}()
	for {
		frame, err := c.Receive(ctx)
		if err != nil {
			return developmentError("teams", err)
		}
		if frame.Error != nil {
			return developmentError("teams", &clientapi.Refusal{Cause: *frame.Error})
		}
		switch p := frame.Payload.(type) {
		case protocol.TeamsResult:
			if json.NewEncoder(os.Stdout).Encode(p) != nil {
				return 74
			}
			return 0
		case protocol.SignInListen:
			listening := protocol.SignInListening{}
			if *paste {
				reason := "remote terminal"
				listening.CannotListen = &reason
			} else {
				listener, err = clientapi.ListenSignIn(ctx)
				if err != nil {
					return developmentError("teams", err)
				}
				port := json.Number(strconv.Itoa(listener.Port()))
				listening.Port = &port
			}
			if _, err = c.SignInListening(ctx, listening); err != nil {
				return developmentError("teams", err)
			}
		case protocol.SignInOpen:
			if p.URL == nil || !clientapi.SignInURLAllowed(*p.URL) {
				return 70
			}
			var redirect protocol.SignInRedirect
			if *paste {
				fmt.Fprintln(os.Stderr, "Open this URL in your browser:", *p.URL, "\nPaste the redirected URL:")
				// Stdin is only the local person's terminal in this explicit mode.
				line, err := bufio.NewReader(io.LimitReader(os.Stdin, 16385)).ReadString('\n')
				if err != nil && err != io.EOF {
					return 74
				}
				if len(line) > 16384 {
					return 64
				}
				line = strings.TrimSpace(line)
				redirect.URL = &line
			} else {
				if listener == nil {
					return 70
				}
				if err = clientapi.OpenSignInBrowser(ctx, *p.URL); err != nil {
					return developmentError("teams", err)
				}
				select {
				case redirect = <-listener.Redirects():
				case <-ctx.Done():
					return 70
				}
			}
			if _, err = c.SignInRedirect(ctx, redirect); err != nil {
				return developmentError("teams", err)
			}
		default:
			return 70
		}
	}
}
