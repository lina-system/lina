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
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/buildinfo"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

// KakaoTalk sign-in uses the owner client route and the add-on's own flow. The
// person's answers travel to Core and the add-on and are never written here.
func runKakaoTalk(args []string) int {
	flags := flag.NewFlagSet("lina kakaotalk", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	socket := flags.String("socket", "", "Core client socket")
	requestFile := flags.String("request", "", "JSON request file; defaults to stdin")
	accept := flags.Bool("accept", false, "Accept the cited Kakao terms and begin the add-on's sign-in (connect only)")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: lina kakaotalk --socket <path> [--request <file>] [--accept]\nOperations: list, card, connect, sign_in, answer, disconnect.")
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
	input := os.Stdin
	if *requestFile != "" {
		f, err := os.Open(*requestFile)
		if err != nil {
			return developmentError("kakaotalk", err)
		}
		defer f.Close()
		input = f
	}
	decoder := json.NewDecoder(io.LimitReader(input, 128<<10))
	decoder.DisallowUnknownFields()
	var request protocol.KakaotalkRequest
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		fmt.Fprintln(os.Stderr, "lina kakaotalk: invalid settings request")
		return 64
	}
	if *accept {
		if request.Operation != "connect" {
			fmt.Fprintln(os.Stderr, "lina kakaotalk: --accept belongs to connect")
			return 64
		}
		request.Accept = accept
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	c, err := clientapi.DialWithCapabilities(ctx, *socket, buildinfo.Version, []protocol.Capability{{Name: "kakaotalk", Version: "1.0"}})
	if err != nil {
		return developmentError("kakaotalk", err)
	}
	defer c.Close()
	// The terminal supplies answers to the add-on's own prompts. Values are
	// written to the request and dropped; a secret is read without echo.
	terminal := term.IsTerminal(os.Stdin)
	// poll repeats the operation that started the relay while the phone
	// confirms the tablet; an answer is never resent.
	poll := request
	account := ""
	if request.Account != nil {
		account = *request.Account
	}
	if request.Operation == "answer" {
		poll = protocol.KakaotalkRequest{Operation: "sign_in"}
		if account != "" {
			poll.Account = &account
		}
	}
	for {
		if _, err = c.KakaoTalk(ctx, request); err != nil {
			return developmentError("kakaotalk", err)
		}
		frame, err := c.Receive(ctx)
		if err != nil {
			return developmentError("kakaotalk", err)
		}
		if frame.Error != nil {
			if request.Operation == "answer" {
				fmt.Fprintln(os.Stderr, "lina kakaotalk: the code was not accepted; run sign_in again")
			}
			return developmentError("kakaotalk", &clientapi.Refusal{Cause: *frame.Error})
		}
		result, ok := frame.Payload.(protocol.KakaotalkResult)
		if !ok {
			return 70
		}
		if result.Step == nil {
			if json.NewEncoder(os.Stdout).Encode(result) != nil {
				return 74
			}
			return 0
		}
		step := *result.Step
		if step.State != "needs_sign_in" || step.Prompt == nil && step.Show == nil {
			if json.NewEncoder(os.Stdout).Encode(result) != nil {
				return 74
			}
			if step.State == "signed_in" {
				return 0
			}
			return 1
		}
		if step.Show != nil {
			fmt.Fprintf(os.Stderr, "Confirm this code on your phone: %s (then press Enter)\n", step.Show.Value)
			if _, err = bufio.NewReader(io.LimitReader(os.Stdin, 2)).ReadString('\n'); err != nil && err != io.EOF {
				return 74
			}
			// A repeated step shows the same code while the phone confirms.
			request = poll
			continue
		}
		value, ok := readAnswer(terminal, step.Prompt.Kind == "secret", step.Prompt.Text)
		if !ok {
			return 1
		}
		request = protocol.KakaotalkRequest{Operation: "answer", Prompt: &step.Prompt.Key, Value: &value}
	}
}

// readAnswer prints the add-on's own prompt and reads one line. A secret is
// read without echo, so a password never shows on the terminal.
func readAnswer(terminal, secret bool, prompt string) (string, bool) {
	fmt.Fprint(os.Stderr, prompt+": ")
	var line []byte
	var err error
	if secret && terminal {
		line, err = term.ReadSecret(os.Stdin)
		fmt.Fprintln(os.Stderr)
	} else {
		line, err = bufio.NewReader(io.LimitReader(os.Stdin, 4097)).ReadBytes('\n')
	}
	if err != nil && err != io.EOF {
		return "", false
	}
	value := strings.TrimSpace(string(line))
	return value, value != ""
}
