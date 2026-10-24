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
	// written to the request and dropped; a secret is read without echo. One
	// reader spans every step, so input piped for several prompts is not lost
	// between them.
	prompts := newAnswers(os.Stdin, term.IsTerminal(os.Stdin))
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
			if !prompts.wait(fmt.Sprintf("Confirm this code on your phone: %s (then press Enter)", step.Show.Value)) {
				return 74
			}
			// A repeated step shows the same code while the phone confirms.
			request = poll
			continue
		}
		value, ok := prompts.read(step.Prompt.Kind == "secret", step.Prompt.Text)
		if !ok {
			return 1
		}
		request = protocol.KakaotalkRequest{Operation: "answer", Prompt: &step.Prompt.Key, Value: &value}
	}
}

// maxAnswer bounds one typed line.
const maxAnswer = 4096

// answers reads the add-on's own prompts from one stream. The buffered reader
// is shared by every step, so a pipe that supplies several answers at once
// keeps the lines it has already read.
type answers struct {
	file     *os.File
	reader   *bufio.Reader
	terminal bool
}

func newAnswers(file *os.File, terminal bool) *answers {
	var in io.Reader = file
	return &answers{file: file, reader: bufio.NewReader(in), terminal: terminal}
}

// line reads one line of at most maxAnswer bytes and leaves the rest of the
// stream for the next step.
func (a *answers) line() ([]byte, error) {
	var out []byte
	for len(out) < maxAnswer {
		chunk, err := a.reader.ReadSlice('\n')
		out = append(out, chunk...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return out, err
	}
	return out[:maxAnswer], nil
}

// read prints the add-on's own prompt and returns one answer. A secret is read
// without echo and kept as typed: only the line ending is removed, because a
// leading or trailing space can be part of the credential. Other answers are
// trimmed.
func (a *answers) read(secret bool, prompt string) (string, bool) {
	fmt.Fprint(os.Stderr, prompt+": ")
	var line []byte
	var err error
	if secret && a.terminal && a.file != nil {
		line, err = term.ReadSecret(a.file)
		fmt.Fprintln(os.Stderr)
	} else {
		line, err = a.line()
	}
	if err != nil && err != io.EOF {
		return "", false
	}
	if secret {
		value := strings.TrimRight(string(line), "\r\n")
		return value, value != ""
	}
	value := strings.TrimSpace(string(line))
	return value, value != ""
}

// wait shows a line and consumes one line, such as the Enter after the person
// confirms a code on their phone.
func (a *answers) wait(text string) bool {
	fmt.Fprintln(os.Stderr, text)
	_, err := a.line()
	return err == nil || err == io.EOF
}
