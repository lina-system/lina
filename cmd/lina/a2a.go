// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/buildinfo"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Settings arrive on stdin, never argv or conversation history. The same
// authenticated client connection carries the existing browser sign-in relay.
func runA2A(args []string) int {
	flags := flag.NewFlagSet("lina a2a", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	socket := flags.String("socket", "", "Core client socket")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: lina a2a --socket <path> < request.json\nOperations: configure, list, revoke, credential, sign_in, calls, answer, verify_result.")
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
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 128<<10))
	decoder.DisallowUnknownFields()
	var request protocol.A2aRequest
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		fmt.Fprintln(os.Stderr, "lina a2a: invalid settings request")
		return 64
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	c, err := clientapi.DialWithCapabilities(ctx, *socket, buildinfo.Version, []protocol.Capability{{Name: "a2a", Version: "1.0"}, {Name: "sign_in_relay", Version: "1.0"}})
	if err != nil {
		return developmentError("a2a", err)
	}
	defer c.Close()
	if _, err = c.A2A(ctx, request); err != nil {
		return developmentError("a2a", err)
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
			return developmentError("a2a", err)
		}
		if frame.Error != nil {
			return developmentError("a2a", &clientapi.Refusal{Cause: *frame.Error})
		}
		switch p := frame.Payload.(type) {
		case protocol.A2aResult:
			if json.NewEncoder(os.Stdout).Encode(p) != nil {
				return 74
			}
			return 0
		case protocol.SignInListen:
			listener, err = clientapi.ListenSignIn(ctx)
			if err != nil {
				return developmentError("a2a", err)
			}
			port := json.Number(strconv.Itoa(listener.Port()))
			if _, err = c.SignInListening(ctx, protocol.SignInListening{Port: &port}); err != nil {
				return developmentError("a2a", err)
			}
		case protocol.SignInOpen:
			if listener == nil || p.URL == nil {
				return 70
			}
			if err = clientapi.OpenSignInBrowser(ctx, *p.URL); err != nil {
				return developmentError("a2a", err)
			}
			select {
			case redirect := <-listener.Redirects():
				if _, err = c.SignInRedirect(ctx, redirect); err != nil {
					return developmentError("a2a", err)
				}
			case <-ctx.Done():
				return 70
			}
		default:
			return 70
		}
	}
}
